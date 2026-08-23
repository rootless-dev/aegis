package application

import (
	"crypto/tls"

	"github.com/rootless-dev/aegis/internal/http/server"
	"github.com/rootless-dev/aegis/internal/infra/certs"
	"github.com/rootless-dev/aegis/internal/infra/database"
	"github.com/rootless-dev/aegis/internal/infra/graceful"
	"github.com/rootless-dev/aegis/internal/infra/health"
	"github.com/rootless-dev/aegis/internal/infra/logging"
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

// setDatabase opens the pool and registers the readiness check. Liveness never
// runs it: a slow database would otherwise fail every replica at once and have
// all of them restarted, turning a degradation into an outage.
func (app *Application) setDatabase() error {
	cfg := app.cfg.Database

	db, err := database.Open(database.Options{
		Driver:         database.Driver(cfg.Driver),
		Host:           cfg.Host,
		Port:           cfg.Port,
		Name:           cfg.Name,
		User:           cfg.User,
		Password:       cfg.Password,
		Path:           cfg.Path,
		SSLMode:        cfg.SSLMode,
		SSLRootCert:    cfg.SSLRootCert,
		Options:        cfg.Options,
		ConnectTimeout: cfg.ConnectTimeout,
		Pool: database.Pool{
			MaxOpen:         cfg.Pool.MaxOpen,
			MaxIdle:         cfg.Pool.MaxIdle,
			ConnMaxLifetime: cfg.Pool.ConnMaxLifetime,
			ConnMaxIdleTime: cfg.Pool.ConnMaxIdleTime,
		},
		Logger: app.logger,
		// A query slower than the request timeout has already lost the request
		// it belonged to, which makes it the natural threshold.
		SlowThreshold: app.cfg.HttpServer.RequestTimeout,
		// Query arguments are credentials, tokens and personal data. They are
		// only rendered outside production.
		LogParameters: app.cfg.Profile.IsDev(),
	})
	if err != nil {
		return err
	}

	app.database = db

	app.health.RegisterDetailed("database", db.Probe)

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
