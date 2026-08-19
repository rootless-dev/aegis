package middleware_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/http/middleware"
)

func discardLogger() *log.Logger {
	return &log.Logger{Writer: log.IOWriter{Writer: io.Discard}}
}

func TestRequestIDKeepsSafeInboundValue(t *testing.T) {
	var seen string

	handler := middleware.RequestID()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = middleware.RequestIDFrom(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(middleware.RequestIDHeader, "trace-123")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if seen != "trace-123" {
		t.Errorf("context: want trace-123, got %q", seen)
	}

	if got := recorder.Header().Get(middleware.RequestIDHeader); got != "trace-123" {
		t.Errorf("response header: want trace-123, got %q", got)
	}
}

func TestRequestIDRejectsUnsafeInboundValue(t *testing.T) {
	cases := map[string]string{
		"line break":  "abc\ndef",
		"white space": "abc def",
		"too long":    strings.Repeat("a", 65),
		"absent":      "",
	}

	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			var seen string

			handler := middleware.RequestID()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seen = middleware.RequestIDFrom(r.Context())
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if value != "" {
				req.Header.Set(middleware.RequestIDHeader, value)
			}

			handler.ServeHTTP(httptest.NewRecorder(), req)

			if seen == value {
				t.Errorf("unsafe value %q was accepted", value)
			}

			if len(seen) != 32 {
				t.Errorf("want a generated 32 char id, got %q", seen)
			}
		})
	}
}

func TestRecovererAnswersOAuthServerError(t *testing.T) {
	handler := middleware.Recoverer(discardLogger())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status: want 500, got %d", recorder.Code)
	}

	if !strings.Contains(recorder.Body.String(), `"server_error"`) {
		t.Errorf("body: want an OAuth server_error, got %s", recorder.Body.String())
	}

	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("cache control: want no-store, got %q", got)
	}
}

func TestRecovererKeepsCommittedResponse(t *testing.T) {
	handler := middleware.RequestLogger(discardLogger())(
		middleware.Recoverer(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("partial"))
			panic("boom after committing")
		})),
	)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	// The status is already on the wire, so overwriting it would only corrupt
	// the response.
	if recorder.Code != http.StatusCreated {
		t.Errorf("status: want 201 to be preserved, got %d", recorder.Code)
	}
}

func TestTimeoutCarriesDeadlineOnContext(t *testing.T) {
	var hasDeadline bool

	handler := middleware.Timeout(time.Second)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, hasDeadline = r.Context().Deadline()
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if !hasDeadline {
		t.Error("request context should carry a deadline")
	}
}

func TestRequestLoggerKeepsFlusherReachable(t *testing.T) {
	var flushable bool

	handler := middleware.RequestLogger(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A direct type assertion, which is still how much of the ecosystem
		// reaches for streaming.
		_, flushable = w.(http.Flusher)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if !flushable {
		t.Error("the response recorder must not hide http.Flusher")
	}
}
