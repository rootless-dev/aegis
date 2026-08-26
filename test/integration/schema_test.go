//go:build integration

// The boot and the CLI end to end, against a real Postgres server. Always
// Postgres, like start in boot_test.go: these cases are about the boot and the
// CLI, not engine coverage, which internal/repository covers across all four.
package integration_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rootless-dev/aegis/internal/infra/database"
)

// Kept so a test can open a second, direct connection: neither the CLI nor the
// server exposes what it changed.
type schemaEndpoint struct {
	host, port string
}

// One container, and the endpoint it landed on, so a single test can launch the
// binary more than once — as a server and as a subcommand — against it.
func schemaEnv(t *testing.T, publicPort string) ([]string, schemaEndpoint) {
	t.Helper()

	dbHost, dbPort, _ := startPostgres(t)

	env := []string{
		"TLS_TERMINATION=none",
		"PUBLIC_URL=http://127.0.0.1:" + publicPort,
		"DATABASE_DRIVER=postgres",
		"DATABASE_HOST=" + dbHost,
		"DATABASE_PORT=" + dbPort,
		"DATABASE_NAME=aegis",
		"DATABASE_USER=aegis",
		"DATABASE_PASSWORD=aegis",
		"DATABASE_SSL_MODE=disable",
	}

	return env, schemaEndpoint{host: dbHost, port: dbPort}
}

// Bypasses the binary: nothing in this slice exposes an admin surface to ask
// what the boot seeded.
func (e schemaEndpoint) open(t *testing.T) *database.DB {
	t.Helper()

	db, err := database.Open(database.Options{
		Driver:         database.DriverPostgres,
		Host:           e.host,
		Port:           e.port,
		Name:           "aegis",
		User:           "aegis",
		Password:       "aegis",
		SSLMode:        "disable",
		ConnectTimeout: 10 * time.Second,
		Pool:           database.Pool{MaxOpen: 2, MaxIdle: 2, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute},
	})
	if err != nil {
		t.Fatalf("connecting to verify the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	return db
}

func (e schemaEndpoint) masterRealmID(t *testing.T) string {
	t.Helper()

	var id string

	err := e.open(t).SQL.QueryRowContext(context.Background(),
		`SELECT id FROM realms WHERE slug = 'master'`).Scan(&id)
	if err != nil {
		t.Fatalf("reading the master realm: %v", err)
	}

	return id
}

func TestBootingOnAFreshDatabaseCreatesTheSchemaAndTheMasterRealm(t *testing.T) {
	appPort := freePort(t)
	env, endpoint := schemaEnv(t, appPort)

	server := launch(t, appPort, "http", http.DefaultClient, env, nil)
	waitUntilReady(t, server)

	status, body := server.get(t, "/readyz")
	if status != http.StatusOK || !strings.Contains(body, `"ready"`) {
		t.Fatalf("readyz: want 200 ready, got %d %s", status, body)
	}

	stopBinary(t, server)

	if id := endpoint.masterRealmID(t); id == "" {
		t.Error("the master realm must have a non-empty id")
	}
}

func TestBootingASecondTimeLeavesTheSameRealmID(t *testing.T) {
	firstPort := freePort(t)
	env, endpoint := schemaEnv(t, firstPort)

	first := launch(t, firstPort, "http", http.DefaultClient, env, nil)
	waitUntilReady(t, first)
	stopBinary(t, first)

	firstID := endpoint.masterRealmID(t)

	second := launch(t, freePort(t), "http", http.DefaultClient, env, nil)
	waitUntilReady(t, second)
	stopBinary(t, second)

	secondID := endpoint.masterRealmID(t)

	if firstID != secondID {
		t.Errorf("a second boot must not create a second master realm: first %q, second %q", firstID, secondID)
	}
}

// With migration off and an empty database, the boot has to refuse and say how
// an operator gets out.
func TestMigrateOnBootFalseRefusesAnEmptyDatabaseAndNamesTheCLI(t *testing.T) {
	env, _ := schemaEnv(t, freePort(t))
	env = append(env, "DATABASE_MIGRATE_ON_BOOT=false")

	out, err := runBinaryToCompletion(t, nil, env)
	if err == nil {
		t.Fatalf("expected the boot to refuse an empty schema, got a clean exit:\n%s", out)
	}

	if !strings.Contains(out, "aegisd migrate") {
		t.Errorf("expected the message to name `aegisd migrate`, got:\n%s", out)
	}
}

// The deployment-gate shape: migrate as a one-shot subcommand, then serve with
// migration on boot turned off.
func TestMigrateSubcommandAppliesThenTheServerBootsWithMigrationOff(t *testing.T) {
	appPort := freePort(t)
	env, _ := schemaEnv(t, appPort)

	code, out := runCommand(t, env, "migrate")
	if code != 0 {
		t.Fatalf("aegisd migrate: want exit 0 against an empty database, got %d:\n%s", code, out)
	}

	server := launch(t, appPort, "http", http.DefaultClient, append(env, "DATABASE_MIGRATE_ON_BOOT=false"), nil)
	waitUntilReady(t, server)

	status, body := server.get(t, "/readyz")
	if status != http.StatusOK || !strings.Contains(body, `"ready"`) {
		t.Errorf("readyz: want 200 ready, got %d %s", status, body)
	}

	stopBinary(t, server)
}

func TestMigrateStatusExitsOneBeforeMigratingAndZeroAfter(t *testing.T) {
	env, _ := schemaEnv(t, freePort(t))

	code, out := runCommand(t, env, "migrate", "status")
	if code != 1 {
		t.Fatalf("migrate status before migrating: want exit 1, got %d:\n%s", code, out)
	}

	code, out = runCommand(t, env, "migrate")
	if code != 0 {
		t.Fatalf("migrate: want exit 0, got %d:\n%s", code, out)
	}

	code, out = runCommand(t, env, "migrate", "status")
	if code != 0 {
		t.Fatalf("migrate status after migrating: want exit 0, got %d:\n%s", code, out)
	}
}

// Replaced rather than appended: which of two identical variables a process
// reads is not something a test should rest on.
func movedTo(env []string, publicURL string) []string {
	moved := make([]string, 0, len(env))

	for _, entry := range env {
		if strings.HasPrefix(entry, "PUBLIC_URL=") {
			continue
		}

		moved = append(moved, entry)
	}

	return append(moved, "PUBLIC_URL="+publicURL)
}

// The production half of the issuer polarity. internal/application can only
// prove the development half — its configuration runs on SQLite, which the
// production profile refuses — so without this, a test asserting development
// rewrites would pass just as well with the condition inverted.
func TestAMovedPublicURLRefusesTheBootInProduction(t *testing.T) {
	appPort := freePort(t)
	env, _ := schemaEnv(t, appPort)

	seeded := launch(t, appPort, "http", http.DefaultClient, env, nil)
	waitUntilReady(t, seeded)
	stopBinary(t, seeded)

	// The listener moves with it: the port is free, but it must not be why this
	// fails.
	moved := append(movedTo(env, "http://moved.example.com:9000"),
		"HTTP_SERVER_HOST=127.0.0.1",
		"HTTP_SERVER_PORT="+freePort(t),
	)

	code, out := runCommand(t, moved)
	if code == 0 {
		t.Fatalf("a moved public url must refuse the boot in production, got a clean exit:\n%s", out)
	}

	// Both issuers and the recovery, so an operator does not need the database
	// open to read the message.
	for _, want := range []string{
		"http://127.0.0.1:" + appPort + "/realms/master",
		"http://moved.example.com:9000/realms/master",
		"UPDATE realms SET issuer",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal must name %q, got:\n%s", want, out)
		}
	}
}

// Refused by Dispatch before the configuration builder runs, so no database is
// given.
func TestUnknownSubcommandExitsTwoAndPrintsUsage(t *testing.T) {
	code, out := runCommand(t, nil, "frobnicate")
	if code != 2 {
		t.Fatalf("aegisd frobnicate: want exit 2, got %d:\n%s", code, out)
	}

	if !strings.Contains(out, "Usage:") {
		t.Errorf("expected usage to be printed, got:\n%s", out)
	}
}
