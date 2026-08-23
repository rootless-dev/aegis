package database

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestSQLiteDSNForcesThePragmas(t *testing.T) {
	dsn, err := sqliteDSN(Options{Driver: DriverSQLite, Path: "./aegis.dev.db"})
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	if !strings.HasPrefix(dsn, "file:./aegis.dev.db?") {
		t.Fatalf("expected the path to lead the dsn, got %q", dsn)
	}

	query, err := url.ParseQuery(strings.SplitN(dsn, "?", 2)[1])
	if err != nil {
		t.Fatalf("the dsn query does not parse: %v", err)
	}

	pragmas := strings.Join(query["_pragma"], " ")

	// Foreign keys are off by default in SQLite, so a constraint the other
	// engines enforce would be silently ignored here.
	for _, wanted := range []string{"foreign_keys(1)", "busy_timeout(5000)", "journal_mode(WAL)"} {
		if !strings.Contains(pragmas, wanted) {
			t.Fatalf("expected pragma %q, got %q", wanted, pragmas)
		}
	}
}

// _pragma statements run in query order, so an unfiltered caller-supplied
// _pragma naming one of the forced pragmas would run after it and win.
func TestSQLiteOptionsCannotShadowTheForcedPragmas(t *testing.T) {
	opts := Options{
		Driver: DriverSQLite,
		Path:   "./aegis.dev.db",
		Options: map[string]string{
			"_pragma": "foreign_keys(0)",
		},
	}

	dsn, err := sqliteDSN(opts)
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	query, err := url.ParseQuery(strings.SplitN(dsn, "?", 2)[1])
	if err != nil {
		t.Fatalf("the dsn query does not parse: %v", err)
	}

	want := []string{"foreign_keys(1)", "busy_timeout(5000)", "journal_mode(WAL)"}
	if got := query["_pragma"]; !slices.Equal(want, got) {
		t.Fatalf("expected only the forced pragmas to survive, got %v", got)
	}
}

// A PRAGMA name is case-insensitive in SQLite itself, so a caller-supplied
// "_pragma" spelled "FOREIGN_KEYS(0)" has to be caught exactly like
// "foreign_keys(0)" would be, or it survives unfiltered and runs after the
// forced pragma of the same name.
func TestSQLiteOptionsCannotShadowTheForcedPragmasRegardlessOfCase(t *testing.T) {
	opts := Options{
		Driver: DriverSQLite,
		Path:   "./aegis.dev.db",
		Options: map[string]string{
			"_pragma": "FOREIGN_KEYS(0)",
		},
	}

	dsn, err := sqliteDSN(opts)
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	query, err := url.ParseQuery(strings.SplitN(dsn, "?", 2)[1])
	if err != nil {
		t.Fatalf("the dsn query does not parse: %v", err)
	}

	want := []string{"foreign_keys(1)", "busy_timeout(5000)", "journal_mode(WAL)"}
	if got := query["_pragma"]; !slices.Equal(want, got) {
		t.Fatalf("expected only the forced pragmas to survive, got %v", got)
	}
}

// This is the regression the ncruces/glebarez driver swap risked: the dsn
// carrying the right text proves nothing if the driver behind it ignores
// _pragma. Opens a real database and reads the three settings back.
func TestSQLiteDialectorAppliesThePragmas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aegis.db")

	dsn, err := sqliteDSN(Options{Driver: DriverSQLite, Path: path})
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	gdb, err := gorm.Open(sqliteDialector(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("opening gorm: %v", err)
	}

	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("getting the underlying *sql.DB: %v", err)
	}

	t.Cleanup(func() { _ = sqlDB.Close() })

	var foreignKeys int
	if err := sqlDB.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("reading foreign_keys back: %v", err)
	}

	if foreignKeys != 1 {
		t.Fatalf("expected foreign_keys to be on, got %d", foreignKeys)
	}

	var busyTimeout int
	if err := sqlDB.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("reading busy_timeout back: %v", err)
	}

	if busyTimeout != 5000 {
		t.Fatalf("expected busy_timeout to be 5000, got %d", busyTimeout)
	}

	var journalMode string
	if err := sqlDB.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("reading journal_mode back: %v", err)
	}

	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("expected journal_mode to be wal, got %q", journalMode)
	}
}

// Writes serialise over the whole file: more connections produce "database is
// locked", never more throughput.
func TestSQLiteIsASingleWriter(t *testing.T) {
	pool := Pool{MaxOpen: 25, MaxIdle: 25}

	sqliteSingleWriter(&pool)

	if pool.MaxOpen != 1 || pool.MaxIdle != 1 {
		t.Fatalf("expected a single connection, got open=%d idle=%d", pool.MaxOpen, pool.MaxIdle)
	}
}

// Unescaped, "?" would start the query early and the driver would silently
// open a file named after the truncation.
func TestSQLiteOpensAPathWithUriSyntaxInIt(t *testing.T) {
	for _, name := range []string{"aegis?db.sqlite", "aegis#1.sqlite", "aegis 100%.sqlite"} {
		t.Run(name, func(t *testing.T) {
			opts := sqliteOptions(t)
			opts.Path = filepath.Join(t.TempDir(), name)

			db, err := Open(opts)
			if err != nil {
				t.Fatalf("opening %q: %v", opts.Path, err)
			}

			t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

			if _, err := os.Stat(opts.Path); err != nil {
				t.Fatalf("expected the database to be created at %q: %v", opts.Path, err)
			}
		})
	}
}
