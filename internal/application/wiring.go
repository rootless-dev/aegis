package application

import (
	"github.com/rootless-dev/aegis/internal/http/server"
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
	})

	return nil
}

func (app *Application) setHttpServer() error {
	httpCfg := app.cfg.HttpServer

	app.httpServer = server.New(server.Options{
		Address:           httpCfg.Address(),
		Handler:           app.router,
		Logger:            app.logger,
		ReadHeaderTimeout: httpCfg.ReadHeaderTimeout,
		ReadTimeout:       httpCfg.ReadTimeout,
		WriteTimeout:      httpCfg.WriteTimeout,
		IdleTimeout:       httpCfg.IdleTimeout,
		MaxHeaderBytes:    httpCfg.MaxHeaderBytes,
	})

	return nil
}
