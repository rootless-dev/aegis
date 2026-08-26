package database

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// LockTimeout is how long the second connection waits for the first, in the
// concurrent-migration test. 30s matches the ConnectTimeout used elsewhere.
func migrateOptions() MigrateOptions {
	return MigrateOptions{Timeout: time.Minute, LockTimeout: 30 * time.Second}
}

func sqliteFixtures() fs.FS {
	return migrationFixtures(DriverSQLite)
}

// blockingDriver puts a migration deterministically in flight - inside Run,
// not merely started - without depending on wall-clock timing.
type blockingDriver struct {
	version int
	dirty   bool
	release chan struct{}
	running chan struct{}

	// runErr is returned by Run once release fires, so a test can prove a real
	// failure from an in-flight migration survives being wrapped alongside the
	// timeout/cancellation that stopped runUp from waiting on it forever.
	runErr error
}

func newBlockingDriver() *blockingDriver {
	return &blockingDriver{
		version: migratedb.NilVersion,
		release: make(chan struct{}),
		running: make(chan struct{}),
	}
}

func (d *blockingDriver) Open(_ string) (migratedb.Driver, error) { return d, nil }
func (d *blockingDriver) Close() error                            { return nil }
func (d *blockingDriver) Lock() error                             { return nil }
func (d *blockingDriver) Unlock() error                           { return nil }
func (d *blockingDriver) Drop() error                             { return nil }

func (d *blockingDriver) Run(migration io.Reader) error {
	// Synchronises with the library's own buffering goroutine; without it
	// there is nothing to order the two, and -race says so.
	if _, err := io.ReadAll(migration); err != nil {
		return err
	}

	close(d.running)
	<-d.release

	return d.runErr
}

func (d *blockingDriver) SetVersion(version int, dirty bool) error {
	d.version = version
	d.dirty = dirty

	return nil
}

func (d *blockingDriver) Version() (int, bool, error) {
	return d.version, d.dirty, nil
}

// migratorWithBlockingDriver drives runUp against a real *migrate.Migrate
// while controlling when the migration is allowed to finish.
func migratorWithBlockingDriver(t *testing.T) (*migrate.Migrate, *blockingDriver) {
	t.Helper()

	src := fstest.MapFS{"000001_x.up.sql": &fstest.MapFile{Data: []byte("noop")}}

	sourceDriver, err := iofs.New(src, ".")
	if err != nil {
		t.Fatalf("building the source: %v", err)
	}

	driver := newBlockingDriver()

	migrator, err := migrate.NewWithInstance("iofs", sourceDriver, "fake", driver)
	if err != nil {
		t.Fatalf("building the migrator: %v", err)
	}

	return migrator, driver
}

// waitForRunning fails instead of hanging if GracefulStop ever wins the race
// against Run being entered: a hang names nothing and poisons the whole run.
func waitForRunning(t *testing.T, driver *blockingDriver) {
	t.Helper()

	select {
	case <-driver.running:
	case <-time.After(5 * time.Second):
		t.Fatal("the migration never reached Run; GracefulStop likely won the race against it starting")
	}
}

func TestMigrateBringsTheSchemaUp(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if err := db.Migrate(context.Background(), sqliteFixtures(), migrateOptions()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	version, dirty, err := db.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("reading the schema version: %v", err)
	}

	if dirty {
		t.Fatal("expected a clean schema after a successful migration")
	}

	if version != 2 {
		t.Fatalf("expected both fixtures to be applied, got version %d", version)
	}
}

// Every replica of a deployment runs this path, so the second one through must
// find nothing to do rather than fail.
func TestMigratingTwiceIsHarmless(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if err := db.Migrate(context.Background(), sqliteFixtures(), migrateOptions()); err != nil {
		t.Fatalf("first migration: %v", err)
	}

	if err := db.Migrate(context.Background(), sqliteFixtures(), migrateOptions()); err != nil {
		t.Fatalf("second migration should be a no-op, got %v", err)
	}
}

// An empty source is what phase 1 will never hand over, but a caller mistake
// must be a clear error rather than a silent success.
func TestMigrateRejectsAnEmptySource(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	err = db.Migrate(context.Background(), fstest.MapFS{}, migrateOptions())
	if err == nil || !strings.Contains(err.Error(), "no migrations") {
		t.Fatalf("expected an error naming the empty source, got %v", err)
	}
}

// migrator() treats a nil fs.FS as legitimate for the internal callers that
// never touch a migration file (SchemaVersion, ForceVersion). Migrate is a
// public entry point that takes a caller-supplied fs.FS directly, so the same
// nil must not reach fs.ReadDir/iofs.New unguarded and panic.
func TestMigrateRejectsANilSource(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	err = db.Migrate(context.Background(), nil, migrateOptions())
	if err == nil {
		t.Fatal("expected a nil source to be refused rather than panic")
	}
}

// iofs silently skips any filename source.DefaultParse does not recognise, so
// a directory of misnamed migrations - the likeliest real caller mistake -
// is not empty by directory-entry count. Only asking the built source for its
// first migration catches it.
func TestMigrateRejectsASourceWithNoRecognisedMigrations(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	misnamed := fstest.MapFS{
		"001_init.sql": &fstest.MapFile{Data: []byte("CREATE TABLE nope (id INTEGER);")},
	}

	err = db.Migrate(context.Background(), misnamed, migrateOptions())
	if err == nil || !strings.Contains(err.Error(), "no migrations") {
		t.Fatalf("expected an error naming the absence of migrations, got %v", err)
	}
}

// source.Driver.First does not care which direction a version has, so a
// down-only source passes it, applies nothing, and would report success.
func TestMigrateRejectsADownOnlySource(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	downOnly := fstest.MapFS{
		"000001_probe.down.sql": &fstest.MapFile{Data: []byte("DROP TABLE migration_probe;")},
	}

	err = db.Migrate(context.Background(), downOnly, migrateOptions())
	if err == nil || !strings.Contains(err.Error(), "no migrations") {
		t.Fatalf("expected an error naming the absence of up migrations, got %v", err)
	}
}

// permissionDeniedFS fails to open any *.up.sql, which is how a real read
// failure - as opposed to a missing file - is reached through the public
// Migrate API rather than against a fake source.Driver.
type permissionDeniedFS struct {
	fs.FS
}

func (f permissionDeniedFS) Open(name string) (fs.File, error) {
	if strings.HasSuffix(name, ".up.sql") {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	}

	return f.FS.Open(name)
}

// A source that cannot be read is a different problem than one with no
// migrations, and must not be reported as the latter - that would send an
// operator looking for a caller mistake instead of a real I/O
// problem.
func TestMigrateDistinguishesAReadFailureFromNoMigrations(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	source := permissionDeniedFS{FS: fstest.MapFS{
		"000001_x.up.sql": &fstest.MapFile{Data: []byte("noop")},
	}}

	err = db.Migrate(context.Background(), source, migrateOptions())
	if err == nil {
		t.Fatal("expected an error")
	}

	if strings.Contains(err.Error(), "no migrations") {
		t.Fatalf("expected a read-failure message, not the no-migrations one: %v", err)
	}

	if !strings.Contains(err.Error(), "reading the migration source") {
		t.Fatalf("expected the read-failure wording, got %v", err)
	}
}

// Timeout has no sensible library default: unlike LockTimeout, a guessed
// value would silently pick how long a boot is willing to wait, so a caller
// mistake here has to be a clear error instead.
func TestMigrateRequiresAPositiveTimeout(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	err = db.Migrate(context.Background(), sqliteFixtures(), MigrateOptions{})
	if err == nil || !strings.Contains(err.Error(), "Timeout must be positive") {
		t.Fatalf("expected the zero-value Timeout to be refused, got %v", err)
	}
}

// Returning as soon as the timeout fires would let Migrate's deferred
// closeMigrator close the pool under the running migration, whose next write -
// the one clearing the dirty flag - would then fail and leave the schema dirty
// for good. The timeout still has to be reported, only later.
func TestRunUpWaitsForAnInFlightMigrationOnTimeout(t *testing.T) {
	migrator, driver := migratorWithBlockingDriver(t)

	// Long enough that the in-process work ahead of Run finishes first even on
	// a loaded runner: too short and GracefulStop fires before Run is ever
	// reached, which is a different scenario than the one under test.
	const timeout = 250 * time.Millisecond

	runUpDone := make(chan error, 1)
	go func() { runUpDone <- runUp(context.Background(), migrator, timeout) }()

	waitForRunning(t, driver)

	// Comfortably longer than the timeout, so that if runUp were to return
	// early (the bug), it would already have done so by now.
	time.Sleep(timeout + 100*time.Millisecond)

	select {
	case err := <-runUpDone:
		t.Fatalf("runUp returned (err=%v) while the migration was still in flight", err)
	default:
	}

	close(driver.release)

	select {
	case err := <-runUpDone:
		if !strings.Contains(err.Error(), "did not finish within") {
			t.Fatalf("expected a timeout error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runUp never returned after the migration finished")
	}

	if driver.dirty {
		t.Fatal("expected the in-flight migration to finish cleanly instead of being cut off by the timeout")
	}

	if driver.version != 1 {
		t.Fatalf("expected the in-flight migration to have been allowed to complete, got version %d", driver.version)
	}
}

// The wait can surface a real failure, not just ErrNoChange. Reporting only
// "timed out" would hide what actually went wrong with the schema.
func TestRunUpSurfacesTheRealErrorWhenAMigrationFailsDuringTheStop(t *testing.T) {
	migrator, driver := migratorWithBlockingDriver(t)
	driver.runErr = errors.New("disk is full")

	const timeout = 250 * time.Millisecond

	runUpDone := make(chan error, 1)
	go func() { runUpDone <- runUp(context.Background(), migrator, timeout) }()

	waitForRunning(t, driver)
	time.Sleep(timeout + 100*time.Millisecond)
	close(driver.release)

	select {
	case err := <-runUpDone:
		if !strings.Contains(err.Error(), "disk is full") {
			t.Fatalf("expected the underlying failure to survive, got %v", err)
		}

		if !strings.Contains(err.Error(), "did not finish within") {
			t.Fatalf("expected the error to still mention the timeout, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runUp never returned after the migration finished")
	}
}

// Same property as above, forced through context cancellation instead of the
// timer: cancelling while a migration is genuinely in flight must not abandon
// it either.
func TestRunUpWaitsForAnInFlightMigrationWhenTheContextIsCancelled(t *testing.T) {
	migrator, driver := migratorWithBlockingDriver(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runUpDone := make(chan error, 1)
	go func() { runUpDone <- runUp(ctx, migrator, time.Minute) }()

	// Nothing here can signal GracefulStop before Run is reached - cancel is
	// only called a few lines below, after this wait completes - so unlike the
	// timeout test above there is no race to lose. Still bounded, for the same
	// reason: a hang is worse than a failure.
	waitForRunning(t, driver)

	cancel()
	time.Sleep(100 * time.Millisecond)

	select {
	case err := <-runUpDone:
		t.Fatalf("runUp returned (err=%v) while the migration was still in flight", err)
	default:
	}

	close(driver.release)

	select {
	case err := <-runUpDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runUp never returned after the migration finished")
	}

	if driver.dirty {
		t.Fatal("expected the in-flight migration to finish cleanly instead of being cut off by the cancellation")
	}

	if driver.version != 1 {
		t.Fatalf("expected the in-flight migration to have been allowed to complete, got version %d", driver.version)
	}
}

// With runErr unset, as in the test above, <-done is always nil and folding
// the error into the message text would leave errors.Is(err, Canceled) true
// anyway. Setting it is what exercises the two-error path.
func TestRunUpSurfacesTheRealErrorWhenCancelledDuringAFailingMigration(t *testing.T) {
	migrator, driver := migratorWithBlockingDriver(t)
	driver.runErr = errors.New("disk is full")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runUpDone := make(chan error, 1)
	go func() { runUpDone <- runUp(ctx, migrator, time.Minute) }()

	waitForRunning(t, driver)

	cancel()
	time.Sleep(100 * time.Millisecond)
	close(driver.release)

	select {
	case err := <-runUpDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled to still be reachable via errors.Is, got %v", err)
		}

		if !strings.Contains(err.Error(), "disk is full") {
			t.Fatalf("expected the underlying failure to survive, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runUp never returned after the migration finished")
	}
}

// A dirty schema is where a MySQL installation ends up when a migration fails
// halfway. It must produce a message an operator can act on, never a driver
// error leaking into a support ticket.
func TestADirtySchemaIsRefusedWithAnActionableError(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if err := db.Migrate(context.Background(), sqliteFixtures(), migrateOptions()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	if _, err := db.SQL.ExecContext(context.Background(), "UPDATE aegis_schema_migrations SET dirty = 1"); err != nil {
		t.Fatalf("marking the schema dirty: %v", err)
	}

	err = db.Migrate(context.Background(), sqliteFixtures(), migrateOptions())

	var dirty *SchemaDirtyError
	if !errors.As(err, &dirty) {
		t.Fatalf("expected SchemaDirtyError, got %v", err)
	}

	// What the message has to carry is the version an operator repairs by hand
	// and the command that clears the flag afterwards.
	for _, want := range []string{"dirty at version 2", "by hand", "aegisd migrate force"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected the error to be actionable and mention %q, got %v", want, err)
		}
	}
}

func TestForceVersionClearsTheDirtyFlag(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if err := db.Migrate(context.Background(), sqliteFixtures(), migrateOptions()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	version, _, err := db.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("reading the schema version: %v", err)
	}

	if _, err := db.SQL.ExecContext(context.Background(), "UPDATE aegis_schema_migrations SET dirty = 1"); err != nil {
		t.Fatalf("marking the schema dirty: %v", err)
	}

	if err := db.ForceVersion(context.Background(), int(version)); err != nil {
		t.Fatalf("forcing the version: %v", err)
	}

	_, dirty, err := db.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("reading the schema version: %v", err)
	}

	if dirty {
		t.Fatal("expected force to clear the dirty flag")
	}
}

// migrator() looks db.Driver up in dialects() directly; a *DB not built
// through Open (or one that has drifted from it) must get a clear error back
// instead of a nil-pointer panic on the dsn/dialector funcs.
func TestSchemaVersionRefusesAnUnknownDriver(t *testing.T) {
	db := &DB{Driver: "oracle"}

	_, _, err := db.SchemaVersion(context.Background())
	if !errors.Is(err, ErrUnsupportedDriver) {
		t.Fatalf("expected ErrUnsupportedDriver, got %v", err)
	}
}

func TestVerifySchemaAcceptsAnEqualVersion(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if err := db.Migrate(context.Background(), sqliteFixtures(), migrateOptions()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	// sqliteFixtures carries two migrations.
	if err := db.VerifySchema(context.Background(), 2); err != nil {
		t.Errorf("an equal version must be accepted, got %v", err)
	}
}

// Refusing here is the whole point: without it, a binary whose migration was
// skipped serves requests until the first query touches a column that is not
// there.
func TestVerifySchemaRefusesASchemaBehind(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	err = db.VerifySchema(context.Background(), 3)
	if err == nil {
		t.Fatal("an empty database is behind version 3 and must be refused")
	}

	if !errors.Is(err, ErrSchemaBehind) {
		t.Errorf("want ErrSchemaBehind, got %v", err)
	}

	// The operator has to learn both numbers and the way out, or the message
	// sends them reading source mid-incident.
	for _, want := range []string{"3", "aegisd migrate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should mention %q, got: %v", want, err)
		}
	}
}

// During a rolling update the old binary runs beside the new one, which has
// already migrated. An old binary that refuses a version it does not know
// makes rollback impossible exactly when it is needed.
func TestVerifySchemaToleratesASchemaAhead(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if err := db.Migrate(context.Background(), sqliteFixtures(), migrateOptions()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	if err := db.VerifySchema(context.Background(), 1); err != nil {
		t.Errorf("a schema ahead must be tolerated, got %v", err)
	}
}

// A dirty flag outranks the version comparison: a half-applied migration is
// not something a version number can describe.
func TestVerifySchemaRefusesADirtySchema(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if err := db.Migrate(context.Background(), sqliteFixtures(), migrateOptions()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	// ForceVersion is the only supported way to clear the flag, so it cannot
	// be used to set one. Writing the control row directly is what a
	// half-applied migration leaves behind.
	if _, err := db.SQL.ExecContext(context.Background(),
		`UPDATE aegis_schema_migrations SET dirty = 1`); err != nil {
		t.Fatalf("marking the schema dirty: %v", err)
	}

	err = db.VerifySchema(context.Background(), 2)

	dirty := &SchemaDirtyError{}
	if !errors.As(err, &dirty) {
		t.Fatalf("want SchemaDirtyError, got %v", err)
	}
}

// schema_migrations is the golang-migrate default and is shared with Rails and
// with every other golang-migrate project. In a customer database aegis shares
// with another application, a neighbour using the same table is not an error —
// aegis reads their version, decides it is up to date, and creates nothing.
func TestTheControlTableIsNamespaced(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if err := db.Migrate(context.Background(), sqliteFixtures(), migrateOptions()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	var name string

	err = db.SQL.QueryRowContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'aegis_schema_migrations'`,
	).Scan(&name)
	if err != nil {
		t.Fatalf("the control table must be aegis_schema_migrations: %v", err)
	}

	var stray string

	err = db.SQL.QueryRowContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`,
	).Scan(&stray)
	if err == nil {
		t.Error("the library default table must not be created")
	}
}
