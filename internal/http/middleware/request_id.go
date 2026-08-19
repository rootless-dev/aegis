package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

const RequestIDHeader = "X-Request-Id"

const maxInboundRequestIDLength = 64

func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if !isSafeRequestID(id) {
				id = newRequestID()
			}

			w.Header().Set(RequestIDHeader, id)

			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDContextKey, id)))
		})
	}
}

func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDContextKey).(string)

	return id
}

// isSafeRequestID guards the inbound header, which ends up in the logs: a
// client sending line breaks could forge log entries for another request.
func isSafeRequestID(id string) bool {
	if id == "" || len(id) > maxInboundRequestIDLength {
		return false
	}

	for _, c := range []byte(id) {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return false
		}
	}

	return true
}

func newRequestID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}

	return hex.EncodeToString(buf)
}
