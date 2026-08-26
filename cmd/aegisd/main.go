package main

import (
	"fmt"
	"os"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/application"
	"github.com/rootless-dev/aegis/internal/cli"
	"github.com/rootless-dev/aegis/internal/infra/configbuilder"

	// Reads .env into the environment on init, before any configuration is
	// resolved. A missing file is ignored, and variables already set win.
	_ "github.com/joho/godotenv/autoload"
)

// main keeps the single exit point of the process. Everything else lives in run
// so that deferred calls still happen — os.Exit skips them, which is why
// nothing below this line may call it.
func main() {
	os.Exit(run())
}

func run() int {
	handled, args, command, usageErr := cli.Dispatch(os.Args[1:])
	if usageErr != nil {
		fmt.Fprint(os.Stderr, usageErr)

		// An argument error is not a configuration error: naming an unknown
		// subcommand must not first require a reachable database.
		return 2
	}

	// Layered on purpose, each one overwriting only what it declares: defaults
	// are the base, the file is what ships with the image, the environment is
	// how a single instance is adjusted without rebuilding anything, and the
	// command line is whoever is looking at the process right now.
	cfg, err := configbuilder.New().
		WithDefaults().
		WithYAML().
		WithEnv().
		WithFlags(args).
		Normalize().
		Validate().
		Build()
	if err != nil {
		log.Error().Err(fmt.Errorf("invalid configuration: %w", err)).Msg("aegis exited with error")
		return 1
	}

	// Subcommands validate the whole configuration, not just the database
	// section: one that migrates but cannot boot is a trap.
	if handled {
		return command(cfg)
	}

	app, err := application.New(cfg)
	if err != nil {
		log.Error().Err(fmt.Errorf("initialization failed: %w", err)).Msg("aegis exited with error")
		return 1
	}

	if err := app.Run(); err != nil {
		log.Error().Err(err).Msg("aegis exited with error")
		return 1
	}

	return 0
}
