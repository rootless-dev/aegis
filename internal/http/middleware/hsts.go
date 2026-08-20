package middleware

import "net/http"

const StrictTransportSecurityHeader = "Strict-Transport-Security"

// HSTS announces that this host is only to be reached over HTTPS. It is sent
// exclusively on requests that already arrived over HTTPS: over plain HTTP the
// header asks the browser to trust the one message an attacker on the path
// could have written, and browsers ignore it there anyway.
func HSTS(value string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if SchemeFrom(r.Context()) == "https" {
				w.Header().Set(StrictTransportSecurityHeader, value)
			}

			next.ServeHTTP(w, r)
		})
	}
}
