package application

import (
	"errors"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/banner"
	"github.com/rootless-dev/aegis/internal/buildinfo"
	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/http/server"
	"github.com/rootless-dev/aegis/internal/infra/graceful"
	"github.com/rootless-dev/aegis/internal/infra/health"
)

var ErrConfigurationIsNil = errors.New("application: configuration is nil")

type Application struct {
	cfg      *configs.Application
	logger   *log.Logger
	graceful *graceful.Graceful
	health   *health.Health

	// router is what the server serves; surfaces is the group carrying the full
	// middleware chain, and is where each area mounts its own routes so that
	// adding an endpoint never means editing the assembly.
	router   chi.Router
	surfaces chi.Router

	httpServer *server.Server
}

// New assembles the application. Every step may fail because the ones still to
// come — the database above all — depend on the outside world, and a
// constructor is the worst place to discover that with a panic.
func New(cfg *configs.Application) (*Application, error) {
	if cfg == nil {
		return nil, ErrConfigurationIsNil
	}

	instance := &Application{cfg: cfg}

	steps := []func() error{
		instance.setLogger,
		instance.setGraceful,
		instance.setHealth,
		instance.setRouter,
		instance.setHttpServer,
	}

	for _, step := range steps {
		if err := step(); err != nil {
			return nil, err
		}
	}

	return instance, nil
}

// Run starts the application resources and blocks until shutdown is requested,
// either by a system signal or by a fatal failure of one of the resources.
func (app *Application) Run() error {
	// Written straight to stderr, alongside the logs, and before the first log
	// line so the identity of the binary heads the output.
	banner.Print(os.Stderr, app.cfg.Banner.Enabled)

	build := buildinfo.Read()

	app.logger.Info().
		Str("name", app.cfg.AppName).
		Str("version", build.Version).
		Str("revision", build.ShortRevision()).
		Str("built_at", build.Time).
		Bool("dirty", build.Modified).
		Msg("application started")

	for _, resource := range app.resources() {
		app.registerResource(resource)
	}

	// Registered after the resources and resolved before them, since pendings
	// run in reverse: readiness has to start failing while connections are still
	// being accepted, or the load balancer keeps routing here after the door
	// closes.
	app.graceful.Register("readiness drain", app.health.BeginDrain)

	err := app.graceful.Wait()

	app.logger.Info().Msg("application stopped")

	return err
}
