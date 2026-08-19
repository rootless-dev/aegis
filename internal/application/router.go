package application

import (
	"github.com/go-chi/chi/v5"
	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/http/middleware"
)

// setRouter decides which chain each surface goes through. The probes stay
// outside the group so the orchestrator polling them does not drown the request
// log, while everything the outside world calls goes through the full chain.
func (app *Application) setRouter() error {
	trustedProxies, err := app.cfg.Proxy.Networks()
	if err != nil {
		return err
	}

	router := chi.NewRouter()

	// Global, so a panic answers a status even on a probe instead of dropping
	// the connection.
	router.Use(middleware.Recoverer(app.logger))

	app.health.Mount(router)

	router.Group(func(group chi.Router) {
		group.Use(
			middleware.RequestID(),
			// Ahead of everything that reads the client address or the scheme:
			// this is where the forwarded headers are either trusted or removed,
			// and no handler downstream should have to make that call again.
			middleware.Proxy(middleware.ProxyOptions{
				TrustForwardedHeaders: app.cfg.TLS.TrustsForwardedHeaders(),
				TrustedProxies:        trustedProxies,
				Headers:               forwardedHeaders(app.cfg.Proxy.Headers),
				Scheme:                app.cfg.PublicScheme(),
			}),
			middleware.RequestLogger(app.logger),
			// Repeated inside the logger so a panic also shows up as status 500
			// on the request line, which the outer one cannot do.
			middleware.Recoverer(app.logger),
			middleware.Timeout(app.cfg.HttpServer.RequestTimeout),
		)

		if app.cfg.HSTS.Enabled {
			group.Use(middleware.HSTS(app.cfg.HSTS.HeaderValue()))
		}

		app.surfaces = group
	})

	app.router = router

	return nil
}

// forwardedHeaders translates the configured family into what the middleware
// takes, so no package outside the assembly depends on the other's spelling.
func forwardedHeaders(headers configs.ForwardedHeaders) middleware.ForwardedHeaders {
	if headers == configs.HeadersForwarded {
		return middleware.HeadersForwarded
	}

	return middleware.HeadersXForwarded
}
