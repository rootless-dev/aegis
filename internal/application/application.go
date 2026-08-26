package application

import (
	"context"
	"errors"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/banner"
	"github.com/rootless-dev/aegis/internal/buildinfo"
	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/handler/page"
	"github.com/rootless-dev/aegis/internal/http/assets"
	"github.com/rootless-dev/aegis/internal/http/server"
	"github.com/rootless-dev/aegis/internal/infra/certs"
	"github.com/rootless-dev/aegis/internal/infra/database"
	"github.com/rootless-dev/aegis/internal/infra/graceful"
	"github.com/rootless-dev/aegis/internal/infra/health"
	"github.com/rootless-dev/aegis/internal/service"
)

var ErrConfigurationIsNil = errors.New("application: configuration is nil")

type Application struct {
	cfg      *configs.Application
	logger   *log.Logger
	graceful *graceful.Graceful
	health   *health.Health
	database *database.DB

	realms *service.RealmService

	// router is what the server serves; surfaces is the group carrying the full
	// middleware chain, and is where each area mounts its own routes so that
	// adding an endpoint never means editing the assembly.
	router   chi.Router
	surfaces chi.Router

	// pages is the surface a browser reaches: the same base chain as the API
	// group, plus the security headers, and a recoverer that answers HTML.
	pages chi.Router

	assets *assets.Server
	page   *page.Handler

	// certificates is nil whenever TLS ends somewhere else, which is what tells
	// the server to listen in plain HTTP. Assigning a typed nil pointer to it
	// would defeat that check, so it is only ever set from a value that was
	// verified first. The reloader exists alongside it only when the pair comes
	// from files: a generated one has nothing to rotate.
	certificates        CertificateSource
	certificateReloader *certs.Reloader

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
		instance.setDatabase,
		instance.setSchema,
		instance.setServices,
		instance.setCertificates,
		instance.setWeb,
		instance.setRouter,
		instance.setHttpServer,
	}

	for _, step := range steps {
		if err := step(); err != nil {
			// A step failing after setDatabase leaves an open pool nobody can
			// reach: New returns no Application, so the caller has nothing left
			// to call Shutdown on. Shutdown is a no-op when the database was
			// never opened, which is every step before it.
			_ = instance.Shutdown(context.Background())

			return nil, err
		}
	}

	return instance, nil
}

// Shutdown releases what New acquired. Run goes through graceful instead, which
// orders every resource.
func (app *Application) Shutdown(ctx context.Context) error {
	if app.database == nil {
		return nil
	}

	return app.database.Shutdown(ctx)
}

// Database exposes the pool for tests and for nothing else: no production code
// reaches past the services for it.
func (app *Application) Database() *database.DB { return app.database }

// Run starts the application resources and blocks until shutdown is requested,
// either by a system signal or by a fatal failure of one of the resources.
func (app *Application) Run() error {
	// Written straight to stderr, alongside the logs, and before the first log
	// line so the identity of the binary heads the output.
	banner.Print(os.Stderr, app.cfg.Banner.Enabled)

	build := buildinfo.Read()

	// The identity of the process and nothing else: every resource announces
	// what it resolved on its own, at the point it resolved it.
	app.logger.Info().
		Str("name", app.cfg.AppName).
		Str("profile", app.cfg.Profile.String()).
		Str("public_url", app.cfg.PublicURL).
		Str("version", build.Version).
		Str("revision", build.ShortRevision()).
		Str("built_at", build.Time).
		Bool("dirty", build.Modified).
		Msg("application started")

	// Registered before the resources and therefore resolved after them:
	// pendings run in reverse, and nothing may be closed while a request can
	// still reach it.
	app.graceful.Register("database", app.database.Shutdown)

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
