//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// selectedDriver mirrors internal/infra/database's helper of the same name. It
// cannot be imported — a _test.go file is not importable — so it is duplicated
// here rather than shared, kept identical in shape on purpose.
func selectedDriver() string {
	if driver := os.Getenv("AEGIS_TEST_DRIVER"); driver != "" {
		return driver
	}

	return "sqlite"
}

// databaseEnvironment mirrors engineOptions in internal/infra/database: it
// starts a container when the selected driver needs one and returns the
// DATABASE_* variables the binary reads, plus the topology every prod-profile
// boot has to declare.
//
// sqlite is a development engine and is refused under the production profile
// (internal/configs/database.go), so it boots under the dev profile instead.
// TLS_TERMINATION stays explicit either way: Normalize only fills in what a
// source left blank, so declaring it here keeps every driver on the same
// plain-HTTP listener the other tests in this package expect.
//
// The second return value lets a caller stop the database for real, mid test,
// without waiting for t.Cleanup: see the comment on
// TestBootAgainstTheConfiguredEngine for what that proves that a plain
// "/readyz answered 200" cannot. It is nil for sqlite, which has no server to
// stop -- see the same comment for why that leg cannot be strengthened the
// same way.
func databaseEnvironment(t *testing.T) ([]string, func()) {
	t.Helper()

	driver := selectedDriver()

	env := []string{
		"PUBLIC_URL=http://127.0.0.1:7500",
		"TLS_TERMINATION=none",
		"DATABASE_DRIVER=" + driver,
	}

	if driver == "sqlite" {
		return append(env,
			"AEGIS_PROFILE=dev",
			"DATABASE_PATH="+filepath.Join(t.TempDir(), "aegis.db"),
		), nil
	}

	env = append(env,
		"AEGIS_PROFILE=prod",
		"DATABASE_NAME=aegis",
		"DATABASE_USER=aegis",
		"DATABASE_PASSWORD=aegis",
		"DATABASE_SSL_MODE=disable",
	)

	var host, port string

	var stop func()

	switch driver {
	case "postgres":
		host, port, stop = startPostgres(t)
	case "mysql":
		host, port, stop = startMySQL(t, "mysql:8.0")
	case "mariadb":
		host, port, stop = startMySQL(t, "mariadb:10.6")
	default:
		t.Fatalf("unknown test driver %q", driver)
	}

	return append(env, "DATABASE_HOST="+host, "DATABASE_PORT="+port), stop
}

// startPostgres returns the endpoint plus a function that stops the container
// immediately, ahead of the t.Cleanup registered here: Terminate tolerates
// being called twice (the second call's error is discarded, exactly like the
// cleanup below already does), which is what makes handing out an early stop
// safe.
func startPostgres(t *testing.T) (string, string, func()) {
	t.Helper()

	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("aegis"),
		postgres.WithUsername("aegis"),
		postgres.WithPassword("aegis"),
		testcontainers.WithWaitStrategy(
			// Postgres restarts once during initialisation, so waiting for the
			// port alone connects to a server that is about to go away.
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		),
	)
	if err != nil {
		t.Fatalf("starting postgres: %v", err)
	}

	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	host, port := endpoint(t, container, "5432/tcp")

	return host, port, func() { _ = container.Terminate(context.Background()) }
}

// startMySQL serves both engines: the module drives MariaDB images too, and the
// two differ in the migration lineage they use, not in how they start.
//
// The module's own default wait strategy waits for the log line "port: 3306
// MySQL Community Server", which MariaDB's entrypoint never prints - its own
// startup log ends with "mariadbd: ready for connections." instead. Left
// unoverridden, the module considers a MariaDB container never ready and keeps
// starting a fresh one roughly once a minute until the test's own timeout
// gives up, rather than failing fast.
//
// MariaDB also restarts once during initialisation, the same way Postgres
// does (see startPostgres): the line appears once for the temporary bootstrap
// server the entrypoint uses to create the database and user, and again for
// the real one. WithOccurrence(2) is what keeps this from connecting to the
// server that is about to be torn down.
func startMySQL(t *testing.T, image string) (string, string, func()) {
	t.Helper()

	ctx := context.Background()

	opts := []testcontainers.ContainerCustomizer{
		mysql.WithDatabase("aegis"),
		mysql.WithUsername("aegis"),
		mysql.WithPassword("aegis"),
	}

	if strings.Contains(image, "mariadb") {
		opts = append(opts, testcontainers.WithWaitStrategy(
			wait.ForLog("mariadbd: ready for connections.").WithOccurrence(2),
		))
	}

	container, err := mysql.Run(ctx, image, opts...)
	if err != nil {
		t.Fatalf("starting %s: %v", image, err)
	}

	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	host, port := endpoint(t, container, "3306/tcp")

	return host, port, func() { _ = container.Terminate(context.Background()) }
}

// endpoint reads back where the container actually landed. MappedPort takes
// the exposed port spec directly in this version of testcontainers-go — there
// is no separate constructor to turn the string into a port type first.
//
// MappedPort re-inspects the container through the Docker API on every call
// rather than reading a value cached at creation time, and that inspect has
// occasionally raced the port binding it asks about, reporting the port as not
// found a moment after the container's own readiness log line already fired.
// Retried a handful of times rather than failed outright, since the container
// is already known to exist by this point - only Docker's own bookkeeping
// about it is still catching up.
func endpoint(t *testing.T, container testcontainers.Container, port string) (string, string) {
	t.Helper()

	ctx := context.Background()

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("reading the container host: %v", err)
	}

	var mapped network.Port

	for attempt := 0; ; attempt++ {
		mapped, err = container.MappedPort(ctx, port)
		if err == nil {
			break
		}

		if attempt >= 4 {
			t.Fatalf("reading the mapped port: %v", err)
		}

		time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
	}

	return host, mapped.Port()
}

// The binary is what proves the boot, which the runner's own suite cannot: it
// never goes through main - and, on every engine but Postgres, it is the only
// thing that proves the database readiness check wiring.go registers even
// works: waitUntilReady only polls /livez, which never depends on it. Without
// asserting /readyz here too, TestBootsOnDefaultsAndShutsDownCleanly in
// boot_test.go was the sole place that ever checked it, and that test only
// ever runs against Postgres.
//
// A 200 from /readyz alone proves less than it looks like it does: the plain
// (non-detailed) report has the same shape, "ready" with no per-check detail,
// whether one check passed or none were ever registered at all, so it cannot
// by itself tell "the database check is wired and healthy" apart from "no
// check runs here". The database is stopped for real below and /readyz is
// polled again to prove the difference: a check that observes a real failure
// is the only way to show it exists without a detailed report, and a detailed
// one exists in internal/infra/health (ReadyDetailed) but is mounted on no
// router -- its own doc comment restricts it to "the authenticated
// administration surface", and no such surface exists yet in this codebase.
// Wiring one is a real design decision -- which auth guards it -- that
// belongs to whichever task adds it, not to an integration test written
// against a route that does not exist.
//
// sqlite is the one leg this cannot cover: it runs in process, has no server
// to stop, and its driver (ncruces/go-sqlite3) implements no driver.Pinger,
// so database/sql's default Ping only borrows a connection from the pool
// without executing anything against the file -- there is no way from outside
// the process to make it observe a failure. That leg stops at the plain 200,
// same as every leg used to; the other three prove the check is both wired
// and actually observed.
func TestBootAgainstTheConfiguredEngine(t *testing.T) {
	env, breakConnectivity := databaseEnvironment(t)

	process := startBinary(t, env)
	t.Cleanup(func() { stopBinary(t, process) })

	waitUntilReady(t, process)

	status, body := process.get(t, "/readyz")
	if status != http.StatusOK || !strings.Contains(body, `"ready"`) {
		t.Fatalf("readyz: want 200 ready, got %d %s", status, body)
	}

	if breakConnectivity == nil {
		return
	}

	breakConnectivity()

	assertReadyzTurnsUnhealthy(t, process)
}

// assertReadyzTurnsUnhealthy polls /readyz until it reports not_ready or
// bootTimeout elapses. Polled rather than checked once: the check has to run
// (and, for a server engine, the pooled connection has to notice the peer is
// gone) before the failure is observed.
func assertReadyzTurnsUnhealthy(t *testing.T, process *instance) {
	t.Helper()

	deadline := time.Now().Add(bootTimeout)

	var status int

	var body string

	for time.Now().Before(deadline) {
		status, body = process.get(t, "/readyz")
		if status == http.StatusServiceUnavailable && strings.Contains(body, `"not_ready"`) {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("readyz did not report not_ready after the database was stopped within %s: last got %d %s", bootTimeout, status, body)
}

// An unreachable database must fail the boot with a message, not start a
// process that answers requests it cannot serve. Always postgres and always
// unreachable, regardless of AEGIS_TEST_DRIVER: this proves the failure path
// itself, not engine coverage.
func TestTheBootFailsWhenTheDatabaseIsUnreachable(t *testing.T) {
	env := []string{
		"AEGIS_PROFILE=prod",
		"PUBLIC_URL=http://localhost:7500",
		"TLS_TERMINATION=none",
		"DATABASE_DRIVER=postgres",
		"DATABASE_HOST=127.0.0.1",
		"DATABASE_PORT=1",
		"DATABASE_NAME=aegis",
		"DATABASE_USER=aegis",
		"DATABASE_PASSWORD=aegis",
		"DATABASE_SSL_MODE=disable",
		"DATABASE_CONNECT_TIMEOUT=2s",
	}

	output, err := runBinaryToCompletion(t, nil, env)
	if err == nil {
		t.Fatalf("expected the boot to fail, got a clean exit:\n%s", output)
	}

	if !strings.Contains(output, "not reachable") {
		t.Fatalf("expected a message naming the unreachable database, got:\n%s", output)
	}
}
