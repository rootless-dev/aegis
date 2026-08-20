package application

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/infra/health"
)

func newTestApplication(t *testing.T, logs *bytes.Buffer) *Application {
	t.Helper()

	app := &Application{
		cfg: &configs.Application{
			AppName:   "aegis-test",
			Profile:   configs.ProfileProd,
			PublicURL: "https://aegis.test",
			HttpServer: &configs.HttpServer{
				Port: "7500", Host: "127.0.0.1",
				RequestTimeout: 10 * time.Second,
			},
			Graceful: &configs.Graceful{Timeout: 20 * time.Second},
			Health:   &configs.Health{CheckTimeout: time.Second, DrainDelay: time.Second},
			TLS:      &configs.TLS{Termination: configs.TerminationNone},
			Proxy:    &configs.Proxy{},
			HSTS:     &configs.HSTS{},
		},
		logger: &log.Logger{Writer: log.IOWriter{Writer: logs}},
	}

	if err := app.setGraceful(); err != nil {
		t.Fatalf("graceful: %v", err)
	}

	if err := app.setHealth(); err != nil {
		t.Fatalf("health: %v", err)
	}

	if err := app.setRouter(); err != nil {
		t.Fatalf("router: %v", err)
	}

	return app
}

func TestProbesAnswerAndStayOutOfTheRequestLog(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	for _, path := range []string{health.LivenessPath, health.ReadinessPath} {
		recorder := httptest.NewRecorder()
		app.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		if recorder.Code != http.StatusOK {
			t.Errorf("%s: want 200, got %d", path, recorder.Code)
		}
	}

	// The orchestrator polls these every few seconds per replica; logging them
	// would bury the real traffic.
	if strings.Contains(logs.String(), "http request") {
		t.Errorf("probes must not reach the request logger, got: %s", logs.String())
	}
}

func TestGroupedRoutesGoThroughTheFullChain(t *testing.T) {
	var logs bytes.Buffer

	app := newTestApplication(t, &logs)

	// Mounted the same way a domain surface would be, to exercise the chain the
	// group installs.
	app.surfaces.Get("/grouped", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	app.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/grouped", nil))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", recorder.Code)
	}

	if recorder.Header().Get("X-Request-Id") == "" {
		t.Error("grouped routes should carry a request id")
	}

	if !strings.Contains(logs.String(), "http request") {
		t.Errorf("grouped routes should be logged, got: %s", logs.String())
	}
}

func TestNewRejectsNilConfiguration(t *testing.T) {
	if _, err := New(nil); err != ErrConfigurationIsNil {
		t.Errorf("want ErrConfigurationIsNil, got %v", err)
	}
}
