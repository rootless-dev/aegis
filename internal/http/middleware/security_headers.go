package middleware

import "net/http"

// ContentSecurityPolicy is what every HTML page is served under. default-src
// 'none' rather than 'self' so a fetch nobody designed fails loudly instead of
// inheriting a permission.
//
// script-src, connect-src and font-src are absent on purpose: nothing here ships
// JavaScript or a web font yet. The first two arrive with HTMX, which fails
// without connect-src.
const ContentSecurityPolicy = "default-src 'none'; " +
	"style-src 'self'; " +
	"img-src 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'"

const (
	ContentSecurityPolicyHeader = "Content-Security-Policy"
	ContentTypeOptionsHeader    = "X-Content-Type-Options"
	ReferrerPolicyHeader        = "Referrer-Policy"
	FrameOptionsHeader          = "X-Frame-Options"
)

// SecurityHeaders guards the page surface. An empty policy sends no CSP, which
// is what a disabled csp section produces; the other three have no switch.
func SecurityHeaders(policy string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if policy != "" {
				w.Header().Set(ContentSecurityPolicyHeader, policy)
			}

			w.Header().Set(ContentTypeOptionsHeader, "nosniff")

			// Login URLs carry state and redirect_uri in the query string, and
			// Referer would hand them to every host a page links to.
			w.Header().Set(ReferrerPolicyHeader, "no-referrer")

			// Redundant with frame-ancestors on current browsers.
			w.Header().Set(FrameOptionsHeader, "DENY")

			next.ServeHTTP(w, r)
		})
	}
}

// NoSniff is applied at the root of the router, so the JSON surfaces — the
// probes today, the API endpoints later — are covered as well. SecurityHeaders
// keeps sending it too: it guards the page surface on its own, and must not
// depend on a caller having installed this one.
func NoSniff() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(ContentTypeOptionsHeader, "nosniff")

			next.ServeHTTP(w, r)
		})
	}
}
