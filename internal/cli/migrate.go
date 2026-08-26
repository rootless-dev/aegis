package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/application"
	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/infra/database"
	"github.com/rootless-dev/aegis/internal/migrations"
)

func dispatchMigrate(args []string) ([]string, Runner, error) {
	if len(args) == 0 {
		return nil, runMigrateUp, nil
	}

	switch args[0] {
	case "status":
		return withoutStrayTokens(args[1:], runMigrateStatus, "migrate status")
	case "force":
		if len(args) < 2 {
			return nil, nil, fmt.Errorf("aegisd: migrate force needs a version\n\n%s", usage)
		}

		version, err := parseVersion(args[1])
		if err != nil {
			return nil, nil, err
		}

		run := func(cfg *configs.Application) int { return runMigrateForce(cfg, version) }

		return withoutStrayTokens(args[2:], run, "migrate force")
	default:
		return withoutStrayTokens(args, runMigrateUp, "migrate")
	}
}

// withoutStrayTokens refuses a leading argument that is not a flag: flag.Parse
// stops at the first non-flag argument, so `aegisd migrate frobnicate` would
// otherwise apply migrations. Only the first token is checked; past the first
// dash is the builder's business.
func withoutStrayTokens(rest []string, run Runner, command string) ([]string, Runner, error) {
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		return nil, nil, fmt.Errorf("aegisd: %s does not take %q\n\n%s", command, rest[0], usage)
	}

	return rest, run, nil
}

// open uses the same translation the server uses — a second copy would drift
// and migrations would run under different TLS or pool settings. That is why
// this package imports internal/application for one pure function; nothing here
// calls application.New.
func open(cfg *configs.Application) (*database.DB, string, error) {
	logger := log.DefaultLogger

	db, err := database.Open(application.DatabaseOptions(cfg, &logger))
	if err != nil {
		return nil, "", err
	}

	return db, cfg.Database.Driver.String(), nil
}

func runMigrateUp(cfg *configs.Application) int {
	db, driver, err := open(cfg)
	if err != nil {
		return fail(err)
	}

	defer func() { _ = db.Shutdown(context.Background()) }()

	source, err := migrations.For(driver)
	if err != nil {
		return fail(err)
	}

	expected, err := migrations.Latest(driver)
	if err != nil {
		return fail(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Database.Migrate.Timeout)
	defer cancel()

	if err := db.Migrate(ctx, source, database.MigrateOptions{
		Timeout:     cfg.Database.Migrate.Timeout,
		LockTimeout: cfg.Database.Migrate.LockTimeout,
	}); err != nil {
		return fail(err)
	}

	if err := db.VerifySchema(ctx, expected); err != nil {
		return fail(err)
	}

	fmt.Printf("schema is at version %d\n", expected)

	return 0
}

// Exits non-zero when the schema is behind, so it works as a deployment gate.
func runMigrateStatus(cfg *configs.Application) int {
	db, driver, err := open(cfg)
	if err != nil {
		return fail(err)
	}

	defer func() { _ = db.Shutdown(context.Background()) }()

	expected, err := migrations.Latest(driver)
	if err != nil {
		return fail(err)
	}

	// Bounded, or an init container gating a deploy on this waits forever on a
	// replica mid-migration holding the control table. connect_timeout is the
	// budget: this is one round trip on a connection that is already open.
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Database.ConnectTimeout)
	defer cancel()

	version, dirty, err := db.SchemaVersion(ctx)
	if err != nil {
		return fail(err)
	}

	fmt.Printf("driver=%s version=%d expected=%d dirty=%t\n", driver, version, expected, dirty)

	switch {
	case dirty:
		return 2
	case version < expected:
		return 1
	default:
		return 0
	}
}

func runMigrateForce(cfg *configs.Application, version int) int {
	db, _, err := open(cfg)
	if err != nil {
		return fail(err)
	}

	defer func() { _ = db.Shutdown(context.Background()) }()

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Database.ConnectTimeout)
	defer cancel()

	if err := db.ForceVersion(ctx, version); err != nil {
		return fail(err)
	}

	fmt.Printf("recorded version %d and cleared the dirty flag\n", version)

	return 0
}

func fail(err error) int {
	fmt.Fprintf(os.Stderr, "aegisd: %v\n", err)

	// A dirty schema needs an operator rather than a retry, so it gets its own
	// code.
	dirty := &database.SchemaDirtyError{}
	if errors.As(err, &dirty) {
		return 2
	}

	return 1
}
