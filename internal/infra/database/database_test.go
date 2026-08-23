package database

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phuslu/log"
)

func sqliteOptions(t *testing.T) Options {
	t.Helper()

	return Options{
		Driver:         DriverSQLite,
		Path:           filepath.Join(t.TempDir(), "aegis.db"),
		ConnectTimeout: 5 * time.Second,
		Pool:           Pool{MaxOpen: 25, MaxIdle: 25, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute},
		Logger:         &log.DefaultLogger,
	}
}

func TestOpenConnectsAndReports(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if db.Driver != DriverSQLite {
		t.Fatalf("expected the driver to be carried on the handle, got %q", db.Driver)
	}

	if db.Gorm == nil || db.SQL == nil {
		t.Fatal("expected both handles to be set")
	}

	if err := db.Ping(context.Background()); err != nil {
		t.Fatalf("pinging: %v", err)
	}
}

// The pool override belongs to the dialect, not to the caller.
func TestOpenAppliesTheDialectPoolOverride(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if got := db.SQL.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("expected sqlite to be collapsed to a single connection, got %d", got)
	}
}

func TestOpenRefusesAnUnknownDriver(t *testing.T) {
	opts := sqliteOptions(t)
	opts.Driver = "oracle"

	_, err := Open(opts)
	if !errors.Is(err, ErrUnsupportedDriver) {
		t.Fatalf("expected ErrUnsupportedDriver, got %v", err)
	}
}

// An unreachable database has to fail Open, not the first request.
func TestOpenFailsWhenTheDatabaseCannotBeReached(t *testing.T) {
	opts := sqliteOptions(t)
	opts.Path = filepath.Join(t.TempDir(), "missing-directory", "aegis.db")

	if _, err := Open(opts); err == nil {
		t.Fatal("expected an unreachable database to fail Open")
	}
}

// The end-to-end version of what logger_test.go proves unit by unit: every
// test there would stay green if Open's "!opts.LogParameters" were inverted.
// This is the one that goes through the path production takes.
func TestOpenNeverLogsQueryParametersWhenLogParametersIsOff(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := &log.Logger{Level: log.DebugLevel, Writer: log.IOWriter{Writer: buf}}

	opts := sqliteOptions(t)
	opts.Logger = logger
	opts.LogParameters = false

	db, err := Open(opts)
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	const secretValue = "hunter2-open-e2e-should-never-appear"

	if err := db.Gorm.Exec("SELECT ? AS probe", secretValue).Error; err != nil {
		t.Fatalf("running the probe query: %v", err)
	}

	output := buf.String()

	if strings.Contains(output, secretValue) {
		t.Fatalf("expected the secret to be redacted end to end through Open, got %q", output)
	}

	if !strings.Contains(output, "SELECT ? AS probe") {
		t.Fatalf("expected the statement to still be logged with its placeholder, got %q", output)
	}
}

// Zero would expire the context before the first packet, and the failure would
// read as an unreachable server.
func TestOpenRefusesANonPositiveConnectTimeout(t *testing.T) {
	opts := sqliteOptions(t)
	opts.ConnectTimeout = 0

	_, err := Open(opts)
	if err == nil || !strings.Contains(err.Error(), "ConnectTimeout must be positive") {
		t.Fatalf("expected a zero connect timeout to be refused by name, got %v", err)
	}
}

// Probe answers the readiness check and describes what it reached, so a
// developer looking at /readyz sees the connection rather than just a verdict.
func TestProbeDescribesTheConnection(t *testing.T) {
	opts := sqliteOptions(t)

	db, err := Open(opts)
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	details, err := db.Probe(context.Background())
	if err != nil {
		t.Fatalf("probing: %v", err)
	}

	if details["driver"] != string(DriverSQLite) || details["path"] != opts.Path {
		t.Fatalf("expected the probe to name the file it opened, got %v", details)
	}

	// The collapsed pool is the one number a sqlite report should show.
	if details["pool_max_open"] != "1" {
		t.Fatalf("expected the dialect pool limit in the probe, got %v", details)
	}

	for _, key := range []string{"latency", "pool_open", "pool_in_use", "pool_idle", "pool_wait_count"} {
		if details[key] == "" {
			t.Fatalf("expected %q in the probe, got %v", key, details)
		}
	}

	// The engine ships inside the binary and has no floor to check, so there is
	// no version to report either.
	if _, ok := details["version"]; ok {
		t.Fatalf("expected no server version for sqlite, got %v", details)
	}

	if _, ok := details["host"]; ok {
		t.Fatalf("expected no host for a file based engine, got %v", details)
	}
}

// A probe against a closed pool still describes the connection: what it was
// talking to is most of what makes the failure diagnosable.
func TestProbeDescribesTheConnectionWhenItFails(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	if err := db.Shutdown(context.Background()); err != nil {
		t.Fatalf("closing: %v", err)
	}

	details, err := db.Probe(context.Background())
	if err == nil {
		t.Fatal("expected a probe against a closed pool to fail")
	}

	if details["driver"] != string(DriverSQLite) {
		t.Fatalf("expected the failing probe to still describe the connection, got %v", details)
	}
}
