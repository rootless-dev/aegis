package middleware

import (
	"context"
	"net/http"
	"time"
)

// Timeout carries a deadline on the request context. It does not interrupt a
// handler that ignores the context, only what honors it.
func Timeout(timeout time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
