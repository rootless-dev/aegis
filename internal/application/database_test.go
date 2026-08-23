package application_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/application"
	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/infra/database"
)

func developmentConfig(t *testing.T) *configs.Application {
	t.Helper()

	cfg := configs.Default()
	cfg.Profile = configs.ProfileDev
	cfg.Normalize()
	cfg.Database.Path = filepath.Join(t.TempDir(), "aegis.db")

	return cfg
}

func TestNewOpensTheDatabase(t *testing.T) {
	app, err := application.New(developmentConfig(t))
	if err != nil {
		t.Fatalf("assembling the application: %v", err)
	}

	if err := app.Shutdown(t.Context()); err != nil {
		t.Fatalf("shutting down: %v", err)
	}
}

// An unreachable database has to fail the assembly, not the first request.
func TestNewFailsWhenTheDatabaseIsUnreachable(t *testing.T) {
	cfg := developmentConfig(t)
	cfg.Database.Path = filepath.Join(t.TempDir(), "missing", "aegis.db")

	if _, err := application.New(cfg); err == nil {
		t.Fatal("expected an unreachable database to fail the assembly")
	}
}

// seedDatabase leaves content in the sqlite file, which is what makes it grow
// a write-ahead log while a pool is open. An empty one never does, and the
// assertion below would have nothing to see.
func seedDatabase(t *testing.T, path string) {
	t.Helper()

	db, err := database.Open(database.Options{
		Driver:         database.DriverSQLite,
		Path:           path,
		ConnectTimeout: 5 * time.Second,
		Pool:           database.Pool{MaxOpen: 1, MaxIdle: 1, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute},
		Logger:         &log.DefaultLogger,
	})
	if err != nil {
		t.Fatalf("seeding the database: %v", err)
	}

	if _, err := db.SQL.ExecContext(t.Context(), "CREATE TABLE probe (id INTEGER)"); err != nil {
		t.Fatalf("seeding the database: %v", err)
	}

	if err := db.Shutdown(t.Context()); err != nil {
		t.Fatalf("closing the seeded database: %v", err)
	}
}

// A step failing after the database is open leaves a pool nobody can reach:
// New returns no Application to call Shutdown on.
func TestNewClosesTheDatabaseWhenALaterStepFails(t *testing.T) {
	cfg := developmentConfig(t)

	seedDatabase(t, cfg.Database.Path)

	// Terminating TLS from a key pair that is not there fails setCertificates,
	// which runs after setDatabase.
	cfg.TLS.Termination = configs.TerminationApp
	cfg.TLS.CertFile = filepath.Join(t.TempDir(), "missing.pem")
	cfg.TLS.KeyFile = filepath.Join(t.TempDir(), "missing-key.pem")

	if _, err := application.New(cfg); err == nil {
		t.Fatal("expected the assembly to fail on the missing certificate")
	}

	if _, err := os.Stat(cfg.Database.Path + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected the pool to have been closed, but its write-ahead log is still there: %v", err)
	}
}
