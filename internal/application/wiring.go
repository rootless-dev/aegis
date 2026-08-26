package application

import (
	"context"
	"crypto/tls"
	"fmt"
	"html/template"
	"net/url"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/handler/page"
	"github.com/rootless-dev/aegis/internal/http/assets"
	"github.com/rootless-dev/aegis/internal/http/render"
	"github.com/rootless-dev/aegis/internal/http/server"
	"github.com/rootless-dev/aegis/internal/infra/certs"
	"github.com/rootless-dev/aegis/internal/infra/database"
	"github.com/rootless-dev/aegis/internal/infra/graceful"
	"github.com/rootless-dev/aegis/internal/infra/health"
	"github.com/rootless-dev/aegis/internal/infra/logging"
	"github.com/rootless-dev/aegis/internal/migrations"
	"github.com/rootless-dev/aegis/internal/repository"
	"github.com/rootless-dev/aegis/internal/service"
	"github.com/rootless-dev/aegis/internal/templates"
)

// The steps below share the error returning signature even where nothing can
// fail yet, so that adding a dependency that talks to the outside world does
// not force the assembly to be rewritten.

func (app *Application) setLogger() error {
	app.logger = logging.New(logging.Options{
		Level:         app.cfg.Logging.Level,
		Caller:        app.cfg.Logging.Caller,
		TimeField:     app.cfg.Logging.TimeField,
		TimeFormat:    app.cfg.Logging.TimeFormat,
		PrettyEnabled: app.cfg.Logging.PrettyEnabled,
	})

	logging.SetDefault(app.logger)

	return nil
}

func (app *Application) setGraceful() error {
	app.graceful = graceful.New(graceful.Options{
		Logger:  app.logger,
		Timeout: app.cfg.Graceful.Timeout,
	})

	return nil
}

func (app *Application) setHealth() error {
	app.health = health.New(health.Options{
		Logger:       app.logger,
		CheckTimeout: app.cfg.Health.CheckTimeout,
		DrainDelay:   app.cfg.Health.DrainDelay,
		// A readiness failure carries the address and the driver behind it,
		// which is help during development and topology on a public endpoint.
		RevealErrors: app.cfg.Profile.IsDev(),
	})

	return nil
}

// DatabaseOptions is exported because the aegisd subcommands open the same
// database without assembling an Application, and a second copy of this mapping
// would drift the first time a field is added.
func DatabaseOptions(cfg *configs.Application, logger *log.Logger) database.Options {
	db := cfg.Database

	return database.Options{
		Driver:         database.Driver(db.Driver),
		Host:           db.Host,
		Port:           db.Port,
		Name:           db.Name,
		User:           db.User,
		Password:       db.Password,
		Path:           db.Path,
		SSLMode:        db.SSLMode,
		SSLRootCert:    db.SSLRootCert,
		Options:        db.Options,
		ConnectTimeout: db.ConnectTimeout,
		Pool: database.Pool{
			MaxOpen:         db.Pool.MaxOpen,
			MaxIdle:         db.Pool.MaxIdle,
			ConnMaxLifetime: db.Pool.ConnMaxLifetime,
			ConnMaxIdleTime: db.Pool.ConnMaxIdleTime,
		},
		Logger: logger,
		// A query slower than the request timeout has already lost the request
		// it belonged to, which makes it the natural threshold.
		SlowThreshold: cfg.HttpServer.RequestTimeout,
		// Query arguments are credentials, tokens and personal data. They are
		// only rendered outside production.
		LogParameters: cfg.Profile.IsDev(),
	}
}

// Readiness only: a slow database checked by liveness would restart every
// replica at once, turning a degradation into an outage.
func (app *Application) setDatabase() error {
	db, err := database.Open(DatabaseOptions(app.cfg, app.logger))
	if err != nil {
		return err
	}

	app.database = db

	app.health.RegisterDetailed("database", db.Probe)

	return nil
}

// setSchema migrates when asked, and verifies either way: with migration off,
// an operator who forgot `aegisd migrate` would otherwise serve requests until
// the first query touched a missing column.
func (app *Application) setSchema() error {
	driver := app.cfg.Database.Driver.String()

	expected, err := migrations.Latest(driver)
	if err != nil {
		return err
	}

	cfg := app.cfg.Database.Migrate

	// New takes no context, so the deadline comes from configuration. Keep the
	// startupProbe budget above it: a probe firing mid-DDL leaves a dirty
	// schema needing a manual force.
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	if cfg.OnBoot {
		source, err := migrations.For(driver)
		if err != nil {
			return err
		}

		app.logger.Info().Str("driver", driver).Uint64("target", uint64(expected)).
			Msg("applying database migrations")

		if err := app.database.Migrate(ctx, source, database.MigrateOptions{
			Timeout:     cfg.Timeout,
			LockTimeout: cfg.LockTimeout,
		}); err != nil {
			return err
		}
	}

	if err := app.database.VerifySchema(ctx, expected); err != nil {
		return err
	}

	app.logger.Info().Str("driver", driver).Uint64("version", uint64(expected)).
		Msg("database schema verified")

	return nil
}

// setServices builds what the use cases need and seeds the master realm. The
// service is held on the Application rather than built as a local, so the admin
// API does not later construct a second one.
func (app *Application) setServices() error {
	publicURL, err := url.Parse(app.cfg.PublicURL)
	if err != nil {
		return fmt.Errorf("application: reading the public url: %w", err)
	}

	store := repository.NewStore(app.database.Gorm)
	app.realms = service.NewRealmService(store, publicURL)

	// Bounded, or a FindBySlug waiting on a row lock hangs the boot. It borrows
	// connect_timeout: the seed is a few single-row round trips on an open
	// connection, so the budget matches and there is no second setting to keep
	// in step.
	ctx, cancel := context.WithTimeout(context.Background(), app.cfg.Database.ConnectTimeout)
	defer cancel()

	master, err := app.realms.EnsureMaster(ctx, app.cfg.Profile.IsDev())
	if err != nil {
		return err
	}

	app.logger.Info().
		Str("realm", master.Slug()).
		Str("issuer", master.Issuer()).
		Msg("master realm ready")

	return nil
}

// setCertificates only produces a keeper when this process is the one
// terminating TLS. Behind a gateway there is nothing to load, and asking for a
// certificate there would be asking for one nobody has.
func (app *Application) setCertificates() error {
	tlsCfg := app.cfg.TLS

	if !tlsCfg.ServesTLS() {
		return nil
	}

	if tlsCfg.GeneratesCertificate(app.cfg.Profile) {
		keeper, err := certs.SelfSigned(app.logger, certs.DevelopmentHosts)
		if err != nil {
			return err
		}

		app.certificates = keeper

		return nil
	}

	keeper, reloader, err := certs.FromFiles(certs.Options{
		Logger:         app.logger,
		CertFile:       tlsCfg.CertFile,
		KeyFile:        tlsCfg.KeyFile,
		ReloadInterval: tlsCfg.ReloadInterval,
		Hostname:       app.cfg.PublicHost(),
	})
	if err != nil {
		return err
	}

	app.certificates = keeper
	app.certificateReloader = reloader

	return nil
}

// setWeb builds the HTML surface in the only order that works: the asset server
// first, since the renderer's asset function comes from it.
func (app *Application) setWeb() error {
	assetFS, err := templates.Assets()
	if err != nil {
		return err
	}

	server, err := assets.New(assetFS)
	if err != nil {
		return err
	}

	if err := app.verifyAssets(server); err != nil {
		return err
	}

	renderer, err := render.New(render.Options{
		Templates: templates.Templates(),
		Funcs:     template.FuncMap{"asset": server.URL},
	})
	if err != nil {
		return err
	}

	app.assets = server
	app.page = page.New(renderer, app.logger)

	return nil
}

// verifyAssets refuses the boot in every profile, development included: the
// layout resolves both assets through the asset function, which fails on an
// unknown logical path, so a boot without either one renders the fallback error
// document on every route. It is separate from setWeb to stay testable against
// a bare *assets.Server.
func (app *Application) verifyAssets(server *assets.Server) error {
	err := server.Verify(assets.Stylesheet, assets.Favicon)
	if err == nil {
		return nil
	}

	// Only the stylesheet is generated. The favicon is committed, and sending
	// whoever reads this to `make assets` over it would send them nowhere.
	if server.Verify(assets.Stylesheet) != nil {
		return fmt.Errorf("%w: run `make assets` before building", err)
	}

	return err
}

func (app *Application) setHttpServer() error {
	httpCfg := app.cfg.HttpServer

	app.httpServer = server.New(server.Options{
		Address:           httpCfg.Address(),
		Handler:           app.router,
		Logger:            app.logger,
		TLSConfig:         app.tlsConfig(),
		ReadHeaderTimeout: httpCfg.ReadHeaderTimeout,
		ReadTimeout:       httpCfg.ReadTimeout,
		WriteTimeout:      httpCfg.WriteTimeout,
		IdleTimeout:       httpCfg.IdleTimeout,
		MaxHeaderBytes:    httpCfg.MaxHeaderBytes,
	})

	return nil
}

// tlsConfig resolves the certificate per handshake rather than pinning one at
// startup, which is what allows a rotation to land under a running server.
func (app *Application) tlsConfig() *tls.Config {
	if app.certificates == nil {
		return nil
	}

	return &tls.Config{
		// Fixed rather than configurable, for the same reason the cipher suites
		// are: the only answer that stays right over time is the one the
		// standard library keeps current, and a floor pinned in configuration
		// ages into the weakest thing this service still accepts.
		MinVersion:     tls.VersionTLS12,
		GetCertificate: app.certificates.GetCertificate,
		// Spelled out because a hand built configuration replaces the one
		// net/http would have assembled, and HTTP/2 disappears with no error
		// when the protocol is not advertised.
		NextProtos: []string{"h2", "http/1.1"},
		// Cipher suites are left to the standard library on purpose: a list
		// pinned here ages into the weakest thing this service still accepts.
	}
}
