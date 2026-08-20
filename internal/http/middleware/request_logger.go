package middleware

import (
	"net/http"
	"time"

	"github.com/phuslu/log"
)

func RequestLogger(logger *log.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			recorder := &responseRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(recorder, r)

			// Falls back to the peer so a chain assembled without the proxy
			// middleware still logs an address instead of an empty field.
			clientIP := ClientIPFrom(r.Context())
			if clientIP == "" {
				clientIP = r.RemoteAddr
			}

			logger.Info().
				Str("request_id", RequestIDFrom(r.Context())).
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Str("scheme", SchemeFrom(r.Context())).
				Int("status", recorder.status).
				Int("bytes", recorder.bytes).
				Dur("duration", time.Since(start)).
				Str("client_ip", clientIP).
				Msg("http request")
		})
	}
}
