//go:build integration

package database

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// columnExists speaks each engine's dialect directly: there is no portable
// "does this column exist" query. The names are always this package's own
// fixtures, never external input.
func columnExists(t *testing.T, db *DB, table, column string) bool {
	t.Helper()

	var query string

	switch db.Driver {
	case DriverPostgres, DriverMySQL, DriverMariaDB:
		query = "SELECT COUNT(*) FROM information_schema.columns WHERE table_name = '" + table + "' AND column_name = '" + column + "'"
	case DriverSQLite:
		query = "SELECT COUNT(*) FROM pragma_table_info('" + table + "') WHERE name = '" + column + "'"
	default:
		t.Fatalf("columnExists: unhandled driver %q", db.Driver)
	}

	var count int
	if err := db.SQL.QueryRowContext(context.Background(), query).Scan(&count); err != nil {
		t.Fatalf("checking whether %s.%s exists: %v", table, column, err)
	}

	return count > 0
}

func TestMigrationsApplyOnEveryEngine(t *testing.T) {
	db, err := Open(engineOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if err := db.Migrate(context.Background(), migrationFixtures(selectedDriver()), migrateOptions()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	version, dirty, err := db.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("reading the schema version: %v", err)
	}

	if dirty || version != 2 {
		t.Fatalf("expected a clean schema at version 2, got %d dirty=%v", version, dirty)
	}
}

// Down migrations never run in production, which is why the public surface
// has no way to run one and why this reaches into db.migrator. ForceVersion
// would not do: it rewrites the bookkeeping row and runs no SQL, so migrating
// up again would replay against a schema that already has the columns.
func TestUpDownUpLeavesTheSchemaWhereItStarted(t *testing.T) {
	db, err := Open(engineOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	source := migrationFixtures(selectedDriver())

	if err := db.Migrate(context.Background(), source, migrateOptions()); err != nil {
		t.Fatalf("first up: %v", err)
	}

	sourceDriver, err := iofs.New(source, ".")
	if err != nil {
		t.Fatalf("reading the migration source: %v", err)
	}

	migrator, closeMigrator, err := db.migrator(sourceDriver, 0)
	if err != nil {
		t.Fatalf("opening the migrator: %v", err)
	}

	downErr := migrator.Down()
	closeMigrator()

	if downErr != nil {
		t.Fatalf("down: %v", downErr)
	}

	if err := db.Migrate(context.Background(), source, migrateOptions()); err != nil {
		t.Fatalf("second up: %v", err)
	}
}

// The lock is the reason the runner opens a connection of its own. Two racing
// migrations must not both proceed.
func TestConcurrentMigrationsDoNotBothProceed(t *testing.T) {
	opts := engineOptions(t)

	if opts.Driver == DriverSQLite {
		t.Skip("sqlite is a single process engine, there is no lock to contend for")
	}

	first, err := Open(opts)
	if err != nil {
		t.Fatalf("opening the first connection: %v", err)
	}

	t.Cleanup(func() { _ = first.Shutdown(context.Background()) })

	second, err := Open(opts)
	if err != nil {
		t.Fatalf("opening the second connection: %v", err)
	}

	t.Cleanup(func() { _ = second.Shutdown(context.Background()) })

	source := migrationFixtures(selectedDriver())

	var wg sync.WaitGroup

	errs := make([]error, 2)

	for index, db := range []*DB{first, second} {
		wg.Add(1)

		go func() {
			defer wg.Done()

			errs[index] = db.Migrate(context.Background(), source, migrateOptions())
		}()
	}

	wg.Wait()

	// Both succeeding is the correct outcome: one migrates, the other waits for
	// the lock and then finds nothing to do. What must never happen is a
	// corrupt schema, which the version check below catches.
	for _, err := range errs {
		if err != nil {
			t.Fatalf("a concurrent migration failed: %v", err)
		}
	}

	version, dirty, err := first.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("reading the schema version: %v", err)
	}

	if dirty || version != 2 {
		t.Fatalf("expected a clean schema at version 2, got %d dirty=%v", version, dirty)
	}
}

// golang-migrate commits the dirty flag before running a migration's body, so
// a failure marks the schema dirty on every engine, not only the two without
// transactional DDL. Whether a real partial change is left behind is a
// different question, which the fixture below is what observes.
func TestAFailedMigrationLeavesTheSchemaDirty(t *testing.T) {
	db, err := Open(engineOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	err = db.Migrate(context.Background(), brokenFixtures(selectedDriver()), migrateOptions())
	if err == nil {
		t.Fatal("expected the broken fixture to fail")
	}

	_, dirty, versionErr := db.SchemaVersion(context.Background())
	if versionErr != nil {
		t.Fatalf("reading the schema version: %v", versionErr)
	}

	if !dirty {
		t.Fatalf("expected %s to be marked dirty after the failed migration", db.Driver)
	}

	// Dirty: the next attempt must refuse with the actionable error rather than
	// try again and fail differently.
	var schemaDirty *SchemaDirtyError
	if err := db.Migrate(context.Background(), brokenFixtures(selectedDriver()), migrateOptions()); !errors.As(err, &schemaDirty) {
		t.Fatalf("expected SchemaDirtyError on the second attempt, got %v", err)
	}
}

// The second migration here carries two statements: the first would succeed
// on its own, the second fails. No engine leaves extra_column behind, for two
// different reasons: Postgres and SQLite run the file as one transaction and
// roll it back, while MySQL and MariaDB never run either statement, since
// mysqlDSN forces MultiStatements off and the driver refuses the query.
//
// So what SchemaDirtyError protects against is narrower than a half-applied
// schema: a migration that did not finish, on a tool that cannot say by itself
// whether anything changed.
func TestATwoStatementMigrationLeavesNoPartialEffectOnAnyEngine(t *testing.T) {
	db, err := Open(engineOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	err = db.Migrate(context.Background(), brokenMultiFixtures(selectedDriver()), migrateOptions())
	if err == nil {
		t.Fatal("expected the broken fixture to fail")
	}

	if columnExists(t, db, "migration_probe", "extra_column") {
		t.Fatalf(
			"%s: expected the first statement's column to be absent after the second failed, but it is present",
			db.Driver,
		)
	}

	_, dirty, versionErr := db.SchemaVersion(context.Background())
	if versionErr != nil {
		t.Fatalf("reading the schema version: %v", versionErr)
	}

	if !dirty {
		t.Fatalf("expected %s to be marked dirty after the failed migration", db.Driver)
	}
}
