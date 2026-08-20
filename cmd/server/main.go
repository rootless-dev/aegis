package main

import (
	"fmt"
	"os"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/application"
	"github.com/rootless-dev/aegis/internal/infra/configbuilder"

	// Reads .env into the environment on init, before any configuration is
	// resolved. A missing file is ignored, and variables already set win.
	_ "github.com/joho/godotenv/autoload"
)

// main keeps the single exit point of the process. Everything else lives in run
// so that deferred calls still happen: os.Exit, which Fatal ends up calling,
// skips them.
func main() {
	if err := run(); err != nil {
		log.Fatal().Err(err).Msg("aegis exited with error")
	}
}

func run() error {
	// Layered on purpose, each one overwriting only what it declares: defaults
	// are the base, the file is what ships with the image, the environment is
	// how a single instance is adjusted without rebuilding anything, and the
	// command line is whoever is looking at the process right now.
	cfg, err := configbuilder.New().
		WithDefaults().
		WithYAML().
		WithEnv().
		WithFlags(os.Args[1:]).
		Normalize().
		Validate().
		Build()
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	app, err := application.New(cfg)
	if err != nil {
		return fmt.Errorf("initialization failed: %w", err)
	}

	return app.Run()
}
