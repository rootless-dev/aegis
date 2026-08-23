package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"testing/fstest"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

type MigrateOptions struct {
	// Timeout bounds how long new migrations keep being started, not how long
	// Migrate takes to return: one already running is always allowed to finish
	// (see runUp). Zero is refused rather than read as "no timeout".
	Timeout time.Duration

	// LockTimeout is how long to wait for another replica to finish migrating.
	// Zero leaves golang-migrate's own default.
	LockTimeout time.Duration
}

// errNoUpMigrations separates an empty source from a source that failed to be
// read, which must not be reported as if it were empty.
var errNoUpMigrations = errors.New("no migration in the source has an up direction")

// SchemaDirtyError reports the flag golang-migrate sets before running a
// migration and clears after it. Typed because it is the one failure here that
// needs an operator rather than a retry.
type SchemaDirtyError struct {
	Version uint
	Driver  Driver
}

// Names no command: the subcommand that would run the recovery does not exist
// yet, and pointing an operator at one mid-incident is worse than not naming it.
func (e *SchemaDirtyError) Error() string {
	return fmt.Sprintf(
		"database: the %s schema is dirty at version %d: a migration failed and this package cannot tell "+
			"from here whether it left a partial change behind. "+
			"Inspect the schema and finish or undo version %d by hand; the recorded version has to be forced "+
			"back to a clean state before this instance will start",
		e.Driver, e.Version, e.Version,
	)
}

// Migrate applies every pending migration in migrations, under a lock. The
// source is a parameter because where migrations live is the caller's
// knowledge, not this package's.
func (db *DB) Migrate(ctx context.Context, migrations fs.FS, opts MigrateOptions) error {
	if opts.Timeout <= 0 {
		return errors.New("database: MigrateOptions.Timeout must be positive")
	}

	// Otherwise iofs.New panics on it.
	if migrations == nil {
		migrations = fstest.MapFS{}
	}

	sourceDriver, err := iofs.New(migrations, ".")
	if err != nil {
		return fmt.Errorf("database: reading the migration source: %w", err)
	}

	if err := requireUpMigrations(sourceDriver); err != nil {
		_ = sourceDriver.Close()

		if errors.Is(err, errNoUpMigrations) {
			return fmt.Errorf("database: the migration source has no migrations: %w", err)
		}

		return fmt.Errorf("database: reading the migration source: %w", err)
	}

	migrator, closeMigrator, err := db.migrator(sourceDriver, opts.LockTimeout)
	if err != nil {
		// Nothing else owns sourceDriver yet.
		_ = sourceDriver.Close()

		return err
	}
	defer closeMigrator()

	version, dirty, err := readVersion(migrator)
	if err != nil {
		return err
	}

	if dirty {
		return &SchemaDirtyError{Version: version, Driver: db.Driver}
	}

	return runUp(ctx, migrator, opts.Timeout)
}

func (db *DB) SchemaVersion(_ context.Context) (uint, bool, error) {
	migrator, closeMigrator, err := db.migrator(emptySource(), 0)
	if err != nil {
		return 0, false, err
	}
	defer closeMigrator()

	return readVersion(migrator)
}

// ForceVersion clears the dirty flag after a manual repair. It rewrites the
// recorded version and touches no schema, so it is the last step of a recovery.
func (db *DB) ForceVersion(_ context.Context, version int) error {
	migrator, closeMigrator, err := db.migrator(emptySource(), 0)
	if err != nil {
		return err
	}
	defer closeMigrator()

	if err := migrator.Force(version); err != nil {
		return fmt.Errorf("database: forcing version %d: %w", version, err)
	}

	return nil
}

// emptySource serves the operations that only read or force the recorded
// version. iofs.New cannot fail on an empty MapFS, hence the discarded error.
func emptySource() source.Driver {
	sourceDriver, _ := iofs.New(fstest.MapFS{}, ".")

	return sourceDriver
}

// requireUpMigrations rejects a source with nothing to apply upwards: empty,
// entirely misnamed, or down-only. All three reach migrate.Up as ErrNoChange,
// where a caller mistake would be reported as success.
func requireUpMigrations(sourceDriver source.Driver) error {
	version, err := sourceDriver.First()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: the source has no migrations at all", errNoUpMigrations)
		}

		return err
	}

	for {
		body, _, err := sourceDriver.ReadUp(version)
		if err == nil {
			_ = body.Close()

			return nil
		}

		if !errors.Is(err, os.ErrNotExist) {
			return err
		}

		next, err := sourceDriver.Next(version)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: every migration present is down-only", errNoUpMigrations)
		}

		if err != nil {
			return err
		}

		version = next
	}
}

// migrator builds a migrate.Migrate over a connection of its own. MySQL holds
// GET_LOCK on the session, so a migration outliving the application pool's
// connection lifetime would have the lock released mid-DDL and let a second
// replica in.
func (db *DB) migrator(sourceDriver source.Driver, lockTimeout time.Duration) (*migrate.Migrate, func(), error) {
	selected, ok := dialects()[db.Driver]
	if !ok {
		return nil, nil, fmt.Errorf("%w: %q", ErrUnsupportedDriver, db.Driver)
	}

	dsn, err := selected.dsn(db.opts)
	if err != nil {
		return nil, nil, err
	}

	pool, err := sql.Open(selected.driverName, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("database: opening a migration connection: %w", err)
	}

	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	// Deliberately unset: a recycled connection drops the lock it holds.
	pool.SetConnMaxLifetime(0)

	closePool := func() { _ = pool.Close() }

	databaseDriver, err := selected.migrator(pool)
	if err != nil {
		closePool()

		return nil, nil, fmt.Errorf("database: preparing the migrator: %w", err)
	}

	migrator, err := migrate.NewWithInstance("iofs", sourceDriver, db.Driver.String(), databaseDriver)
	if err != nil {
		// selected.migrator checked out a *sql.Conn that pool.Close cannot
		// reach; only this releases it.
		_ = databaseDriver.Close()
		closePool()

		return nil, nil, fmt.Errorf("database: preparing the migrator: %w", err)
	}

	if lockTimeout > 0 {
		migrator.LockTimeout = lockTimeout
	}

	release := func() {
		// Closes the database driver, releasing the checked-out connection,
		// which has to happen before the pool closes under it.
		_, _ = migrator.Close()
		closePool()
	}

	return migrator, release, nil
}

func readVersion(migrator *migrate.Migrate) (uint, bool, error) {
	version, dirty, err := migrator.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}

	if err != nil {
		return 0, false, fmt.Errorf("database: reading the schema version: %w", err)
	}

	return version, dirty, nil
}

// runUp stops new migrations from starting, on the timeout and on
// cancellation. Both paths still wait for <-done: migrate.Up is not context
// aware, and returning early would close the pool under a migration still
// writing, which is itself how a schema ends up dirty.
func runUp(ctx context.Context, migrator *migrate.Migrate, timeout time.Duration) error {
	done := make(chan error, 1)

	go func() {
		done <- migrator.Up()
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("database: applying migrations: %w", err)
		}

		return nil
	case <-timer.C:
		migrator.GracefulStop <- true

		timeoutErr := fmt.Errorf("database: migrations did not finish within %s", timeout)

		// Both wrapped with %w: the wait can surface a real failure from the
		// migration that was already running.
		if err := <-done; err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("%w: %w", timeoutErr, err)
		}

		return timeoutErr
	case <-ctx.Done():
		migrator.GracefulStop <- true

		// ctx.Err() stays a %w so errors.Is(err, context.Canceled) holds.
		if err := <-done; err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("database: migration cancelled: %w: %w", ctx.Err(), err)
		}

		return ctx.Err()
	}
}
