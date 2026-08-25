package application

import (
	"net/http"
	"net/netip"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/http/middleware"
	"github.com/rootless-dev/aegis/internal/http/response"
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
	// the connection. JSON, because what is mounted bare answers JSON.
	router.Use(middleware.Recoverer(app.logger, response.ServerError))

	// Global too: the probes and every future JSON endpoint answer a sniffable
	// content type, and only the page surface goes through SecurityHeaders.
	router.Use(middleware.NoSniff())

	app.health.Mount(router)

	// Assets carry nosniff of their own and want none of the rest: no request
	// timeout tuned for handlers, and no log line per stylesheet.
	app.assets.Mount(router)

	base := app.baseChain(trustedProxies)

	router.Group(func(group chi.Router) {
		group.Use(base...)
		// Repeated inside the logger so a panic also shows up as status 500 on
		// the request line, which the outer one cannot do.
		group.Use(middleware.Recoverer(app.logger, response.ServerError))

		app.surfaces = group
	})

	router.Group(func(group chi.Router) {
		group.Use(base...)
		// Repeated inside the logger so a panic also shows up as status 500 on
		// the request line, which the outer one cannot do.
		group.Use(middleware.Recoverer(app.logger, app.page.ServerError))
		group.Use(middleware.SecurityHeaders(app.contentSecurityPolicy()))

		group.Get("/", app.page.Landing)
		// Every GET gets a HEAD beside it: chi routes by method, so the
		// alternative is a 405 answered with a whole HTML page. A rewriting
		// middleware would hide the method from the request logger, which is
		// where someone diagnosing a probe looks.
		group.Head("/", app.page.Landing)

		// On the group rather than the bare router, so a 404 still carries the
		// request id, the CSP and the log line. It answers HTML because an
		// unregistered path is more likely a typo than a missing endpoint.
		//
		// These are service wide rather than the group's. On an inline group
		// chi assigns them to the parent mux, wrapped in the group's chain
		// (mux.go, `if mx.inline && mx.parent != nil`), so the JSON surfaces
		// answer the HTML 405 too, and app.surfaces.NotFound would scope
		// nothing — it would overwrite this one for the whole service. Only
		// chi.Mount builds a mux carrying handlers of its own, so an API branch
		// has to be mounted and register its own there.
		group.NotFound(app.page.NotFound)
		group.MethodNotAllowed(withAllow(router, app.page.MethodNotAllowed))

		app.pages = group
	})

	app.router = router

	return nil
}

// routableMethods is every method chi can route, asked one at a time below.
var routableMethods = []string{
	http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead,
	http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut,
	http.MethodTrace,
}

// withAllow puts back the Allow header RFC 9110 requires on a 405. chi builds it
// from the methods the route matched, but only inside its own handler: replacing
// that handler leaves an http.HandlerFunc, and the matched methods sit in an
// unexported field of the route context. Match is the public way back to the
// same list. It runs before next, because the body handler commits the response.
func withAllow(router *chi.Mux, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		allowed := make([]string, 0, len(routableMethods))
		rctx := chi.NewRouteContext()

		for _, method := range routableMethods {
			rctx.Reset()

			if router.Match(rctx, method, r.URL.Path) {
				allowed = append(allowed, method)
			}
		}

		// An empty Allow would claim the resource takes no method at all.
		if len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
		}

		next(w, r)
	}
}

// baseChain is what both surfaces share. Extracted so the next middleware added
// to one of them cannot go silently missing from the other.
func (app *Application) baseChain(trustedProxies []netip.Prefix) []func(http.Handler) http.Handler {
	chain := []func(http.Handler) http.Handler{
		middleware.RequestID(),
		// Ahead of everything that reads the client address or the scheme: this
		// is where the forwarded headers are either trusted or removed, and no
		// handler downstream should have to make that call again.
		middleware.Proxy(middleware.ProxyOptions{
			TrustForwardedHeaders: app.cfg.TLS.TrustsForwardedHeaders(),
			TrustedProxies:        trustedProxies,
			Headers:               forwardedHeaders(app.cfg.Proxy.Headers),
			Scheme:                app.cfg.PublicScheme(),
		}),
		middleware.RequestLogger(app.logger),
	}

	if app.cfg.HSTS.Enabled {
		chain = append(chain, middleware.HSTS(app.cfg.HSTS.HeaderValue()))
	}

	return append(chain, middleware.Timeout(app.cfg.HttpServer.RequestTimeout))
}

// contentSecurityPolicy fails closed: only an operator saying enabled=false
// takes the header off. A nil section — which validation rejects today, but a
// future caller might not — still gets the policy.
func (app *Application) contentSecurityPolicy() string {
	if app.cfg.CSP != nil && !app.cfg.CSP.Enabled {
		return ""
	}

	return middleware.ContentSecurityPolicy
}

// forwardedHeaders translates the configured family into what the middleware
// takes, so no package outside the assembly depends on the other's spelling.
func forwardedHeaders(headers configs.ForwardedHeaders) middleware.ForwardedHeaders {
	if headers == configs.HeadersForwarded {
		return middleware.HeadersForwarded
	}

	return middleware.HeadersXForwarded
}
