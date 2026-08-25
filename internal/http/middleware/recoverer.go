package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/phuslu/log"
)

// ErrorWriter answers a request that failed, one per surface: JSON for the API
// and the probes, HTML for the pages. The choice comes from the route group and
// never from the caller-controlled Accept header.
type ErrorWriter func(w http.ResponseWriter, r *http.Request)

// Recoverer turns a panic into a controlled 500. Without it net/http drops the
// connection with no response, and the client sees a network error instead of
// a server error.
func Recoverer(logger *log.Logger, write ErrorWriter) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}

				// ErrAbortHandler is net/http asking to abort silently.
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}

				logger.Error().
					Str("request_id", RequestIDFrom(r.Context())).
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Interface("panic", recovered).
					Bytes("stack", debug.Stack()).
					Msg("panic recovered while serving request")

				if recorder, ok := w.(HeaderRecorder); ok && recorder.WroteHeader() {
					return
				}

				write(w, r)
			}()

			next.ServeHTTP(w, r)
		})
	}
}
