package application

import (
	"github.com/go-chi/chi/v5"
	"github.com/rootless-dev/aegis/internal/http/middleware"
)

// setRouter decides which chain each surface goes through. The probes stay
// outside the group so the orchestrator polling them does not drown the request
// log, while everything the outside world calls goes through the full chain.
func (app *Application) setRouter() error {
	router := chi.NewRouter()

	// Global, so a panic answers a status even on a probe instead of dropping
	// the connection.
	router.Use(middleware.Recoverer(app.logger))

	app.health.Mount(router)

	router.Group(func(group chi.Router) {
		group.Use(
			middleware.RequestID(),
			middleware.RequestLogger(app.logger),
			// Repeated inside the logger so a panic also shows up as status 500
			// on the request line, which the outer one cannot do.
			middleware.Recoverer(app.logger),
			middleware.Timeout(app.cfg.HttpServer.RequestTimeout),
		)

		app.surfaces = group
	})

	app.router = router

	return nil
}
