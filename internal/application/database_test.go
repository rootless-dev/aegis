package application_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/application"
	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/infra/database"
	"github.com/rootless-dev/aegis/internal/migrations"
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

// New migrates and seeds, so the unit suite runs real migrations against the
// embedded engine. Asserted here rather than merely walked through.
func TestNewMigratesAndSeedsTheMasterRealm(t *testing.T) {
	app, err := application.New(developmentConfig(t))
	if err != nil {
		t.Fatalf("assembling: %v", err)
	}

	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	expected, err := migrations.Latest("sqlite")
	if err != nil {
		t.Fatalf("reading the expected version: %v", err)
	}

	if err := app.Database().VerifySchema(t.Context(), expected); err != nil {
		t.Errorf("the schema must be at the version the binary carries: %v", err)
	}

	var slug string

	err = app.Database().SQL.QueryRowContext(t.Context(),
		`SELECT slug FROM realms WHERE slug = 'master'`).Scan(&slug)
	if err != nil {
		t.Fatalf("the master realm must exist after a boot: %v", err)
	}
}

func TestNewIsIdempotentAcrossBoots(t *testing.T) {
	cfg := developmentConfig(t)

	first, err := application.New(cfg)
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}

	var firstID string

	if err := first.Database().SQL.QueryRowContext(t.Context(),
		`SELECT id FROM realms WHERE slug = 'master'`).Scan(&firstID); err != nil {
		t.Fatalf("reading the master realm: %v", err)
	}

	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutting down: %v", err)
	}

	second, err := application.New(cfg)
	if err != nil {
		t.Fatalf("second boot: %v", err)
	}

	t.Cleanup(func() { _ = second.Shutdown(context.Background()) })

	var secondID string

	if err := second.Database().SQL.QueryRowContext(t.Context(),
		`SELECT id FROM realms WHERE slug = 'master'`).Scan(&secondID); err != nil {
		t.Fatalf("reading the master realm: %v", err)
	}

	if firstID != secondID {
		t.Error("a second boot must not create a second master realm")
	}
}

// With migration off, the boot refuses rather than serves against a schema with
// none of its tables, and the message names the way out.
func TestNewRefusesAnEmptySchemaWhenMigrationIsOff(t *testing.T) {
	cfg := developmentConfig(t)
	cfg.Database.Migrate.OnBoot = false

	_, err := application.New(cfg)
	if err == nil {
		t.Fatal("an empty schema with migration off must refuse the boot")
	}

	if !errors.Is(err, database.ErrSchemaBehind) {
		t.Errorf("want ErrSchemaBehind, got %v", err)
	}

	if !strings.Contains(err.Error(), "aegisd migrate") {
		t.Errorf("the error must name the way out, got: %v", err)
	}
}

// Only the development half can live here: developmentConfig runs on SQLite,
// which the production profile refuses outright, so the production branch is
// proved in internal/service (against the fake) and again in the integration
// suite, by TestAMovedPublicURLRefusesTheBootInProduction against Postgres.
// Together those three fix the polarity — a single test asserting development
// rewrites would pass with the condition inverted.
func TestAChangedPublicURLRewritesTheMasterIssuerInDevelopment(t *testing.T) {
	cfg := developmentConfig(t)
	cfg.PublicURL = "http://localhost:7500"

	app, err := application.New(cfg)
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutting down: %v", err)
	}

	moved := developmentConfig(t)
	moved.Database.Path = cfg.Database.Path
	moved.PublicURL = "http://localhost:9999"

	rebooted, err := application.New(moved)
	if err != nil {
		t.Fatalf("development must rewrite the issuer rather than refuse: %v", err)
	}

	var issuer string

	if err := rebooted.Database().SQL.QueryRowContext(t.Context(),
		`SELECT issuer FROM realms WHERE slug = 'master'`).Scan(&issuer); err != nil {
		t.Fatalf("reading the issuer: %v", err)
	}

	if err := rebooted.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutting down: %v", err)
	}

	if want := "http://localhost:9999/realms/master"; issuer != want {
		t.Errorf("issuer: want %q, got %q", want, issuer)
	}
}
