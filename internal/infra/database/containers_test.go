//go:build integration

package database

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/network"
	"github.com/phuslu/log"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// selectedDriver is how the CI matrix picks one engine per job.
func selectedDriver() Driver {
	if driver := os.Getenv("AEGIS_TEST_DRIVER"); driver != "" {
		return Driver(driver)
	}

	return DriverSQLite
}

// engineOptions returns Options pointing at a running engine, starting a
// container when the driver needs one.
func engineOptions(t *testing.T) Options {
	t.Helper()

	base := Options{
		Driver:         selectedDriver(),
		Name:           "aegis",
		User:           "aegis",
		Password:       "aegis",
		SSLMode:        "disable",
		ConnectTimeout: 30 * time.Second,
		Pool:           Pool{MaxOpen: 5, MaxIdle: 5, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute},
		Logger:         &log.DefaultLogger,
	}

	switch base.Driver {
	case DriverSQLite:
		base.Name, base.User, base.Password, base.SSLMode = "", "", "", ""
		base.Path = filepath.Join(t.TempDir(), "aegis.db")
	case DriverPostgres:
		base.Host, base.Port = startPostgres(t)
	case DriverMySQL:
		base.Host, base.Port = startMySQL(t, "mysql:8.0")
	case DriverMariaDB:
		base.Host, base.Port = startMySQL(t, "mariadb:10.6")
	default:
		t.Fatalf("unknown test driver %q", base.Driver)
	}

	return base
}

func startPostgres(t *testing.T) (string, string) {
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

	return endpoint(t, container, "5432/tcp")
}

// startMySQL serves both engines. The wait strategy has to be overridden for
// MariaDB: the module waits for a log line MariaDB never prints, and rather
// than failing it keeps restarting the container until the test times out.
// WithOccurrence(2) is for the bootstrap server the entrypoint tears down,
// the same as in startPostgres.
func startMySQL(t *testing.T, image string) (string, string) {
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

	return endpoint(t, container, "3306/tcp")
}

// endpoint reads back where the container landed. MappedPort re-inspects
// through the Docker API on every call, and that inspect has raced the port
// binding it asks about - hence the retry rather than an outright failure.
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
