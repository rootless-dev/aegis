package health_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/infra/health"
)

func newHealth(checkTimeout, drainDelay time.Duration) *health.Health {
	return health.New(health.Options{
		Logger:       &log.Logger{Writer: log.IOWriter{Writer: io.Discard}},
		CheckTimeout: checkTimeout,
		DrainDelay:   drainDelay,
	})
}

// revealingHealth is the development profile, where the failure itself is part
// of the report.
func revealingHealth() *health.Health {
	return health.New(health.Options{
		Logger:       &log.Logger{Writer: log.IOWriter{Writer: io.Discard}},
		CheckTimeout: time.Second,
		RevealErrors: true,
	})
}

func call(t *testing.T, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	return recorder
}

func TestLivenessIgnoresFailingDependencies(t *testing.T) {
	instance := newHealth(time.Second, 0)
	instance.Register("database", func(context.Context) error {
		return errors.New("connection refused")
	})

	recorder := call(t, instance.Live())

	// A failing liveness gets the container restarted, which would never fix a
	// database outage and would take every replica down at once.
	if recorder.Code != http.StatusOK {
		t.Errorf("liveness must not depend on checks, got %d", recorder.Code)
	}
}

func TestReadinessFailsWhenACheckFails(t *testing.T) {
	instance := newHealth(time.Second, 0)
	instance.Register("healthy", func(context.Context) error { return nil })
	instance.Register("database", func(context.Context) error {
		return errors.New("connection refused")
	})

	recorder := call(t, instance.Ready())

	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503, got %d", recorder.Code)
	}
}

// Outside development the report says which dependency is down and nothing
// about where it lives: the endpoint is public.
func TestPublicReadinessNamesTheCheckButNotTheFailure(t *testing.T) {
	instance := newHealth(time.Second, 0)
	instance.Register("database", func(context.Context) error {
		return errors.New("dial tcp 10.0.3.4:5432: connection refused")
	})

	body := call(t, instance.Ready()).Body.String()

	for _, want := range []string{`"database":{"status":"failed"}`, `"status":"not_ready"`} {
		if !strings.Contains(body, want) {
			t.Errorf("public readiness should report %s: %s", want, body)
		}
	}

	for _, leaked := range []string{"10.0.3.4", "connection refused"} {
		if strings.Contains(body, leaked) {
			t.Errorf("public readiness leaked %q: %s", leaked, body)
		}
	}
}

// Development is where the failure itself is worth more than the topology it
// describes.
func TestDevelopmentReadinessRevealsTheFailure(t *testing.T) {
	instance := revealingHealth()
	instance.Register("database", func(context.Context) error {
		return errors.New("dial tcp 10.0.3.4:5432: connection refused")
	})

	body := call(t, instance.Ready()).Body.String()

	for _, want := range []string{"database", "10.0.3.4", "connection refused"} {
		if !strings.Contains(body, want) {
			t.Errorf("development readiness should report %q: %s", want, body)
		}
	}
}

// A healthy dependency is named too, or a 200 could not be told apart from one
// answered by an instance with no checks registered at all.
func TestReadinessNamesAHealthyCheck(t *testing.T) {
	instance := newHealth(time.Second, 0)
	instance.Register("database", func(context.Context) error { return nil })

	body := call(t, instance.Ready()).Body.String()

	if !strings.Contains(body, `"database":{"status":"ok"}`) {
		t.Errorf("expected the healthy check to be named: %s", body)
	}
}

// A detailed check describes what it reached, and that description is for
// development only: it names the server the same way an error would.
func TestDetailsAreRenderedOnlyWhereErrorsAre(t *testing.T) {
	details := func(context.Context) (map[string]string, error) {
		return map[string]string{"host": "db.internal", "pool_open": "3"}, nil
	}

	public := call(t, newHealth(time.Second, 0).RegisterDetailed("database", details).Ready()).Body.String()

	if strings.Contains(public, "db.internal") {
		t.Errorf("public readiness leaked the server it reached: %s", public)
	}

	if !strings.Contains(public, `"database":{"status":"ok"}`) {
		t.Errorf("public readiness should still report the verdict: %s", public)
	}

	development := call(t, revealingHealth().RegisterDetailed("database", details).Ready()).Body.String()

	for _, want := range []string{`"host":"db.internal"`, `"pool_open":"3"`, `"status":"ok"`} {
		if !strings.Contains(development, want) {
			t.Errorf("development readiness should report %s: %s", want, development)
		}
	}
}

// A check that fails can still say what it was talking to, which is most of
// what makes the failure diagnosable.
func TestAFailingDetailedCheckStillDescribesTheDependency(t *testing.T) {
	instance := revealingHealth()
	instance.RegisterDetailed("database", func(context.Context) (map[string]string, error) {
		return map[string]string{"host": "db.internal"}, errors.New("connection refused")
	})

	body := call(t, instance.Ready()).Body.String()

	for _, want := range []string{`"status":"failed"`, `"host":"db.internal"`, `"error":"connection refused"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %s in the report: %s", want, body)
		}
	}
}

// Draining runs no checks, so an ordered shutdown is the one not_ready with no
// checks in it.
func TestDrainingReportsNoChecks(t *testing.T) {
	instance := newHealth(time.Second, 0)
	instance.Register("database", func(context.Context) error { return nil })

	if err := instance.BeginDrain(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}

	body := call(t, instance.Ready()).Body.String()

	if strings.Contains(body, "database") {
		t.Errorf("expected no checks while draining: %s", body)
	}
}

func TestDetailedReadinessNamesTheFailure(t *testing.T) {
	instance := newHealth(time.Second, 0)
	instance.Register("database", func(context.Context) error {
		return errors.New("connection refused")
	})

	body := call(t, instance.ReadyDetailed()).Body.String()

	for _, want := range []string{"database", "connection refused"} {
		if !strings.Contains(body, want) {
			t.Errorf("detailed readiness should report %q: %s", want, body)
		}
	}
}

func TestReadinessWithoutChecksIsReady(t *testing.T) {
	if code := call(t, newHealth(time.Second, 0).Ready()).Code; code != http.StatusOK {
		t.Errorf("want 200 with no dependencies registered, got %d", code)
	}
}

func TestCheckTimeoutIsEnforced(t *testing.T) {
	instance := newHealth(50*time.Millisecond, 0)
	instance.Register("slow", func(ctx context.Context) error {
		select {
		case <-time.After(2 * time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	start := time.Now()
	recorder := call(t, instance.Ready())
	elapsed := time.Since(start)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("a check that times out must fail readiness, got %d", recorder.Code)
	}

	if elapsed > time.Second {
		t.Errorf("the probe should be bounded by the check timeout, took %s", elapsed)
	}
}

func TestChecksRunConcurrently(t *testing.T) {
	instance := newHealth(time.Second, 0)

	for range 4 {
		instance.Register("slow", func(context.Context) error {
			time.Sleep(100 * time.Millisecond)

			return nil
		})
	}

	start := time.Now()
	call(t, instance.Ready())

	// Sequentially this would take 400ms.
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Errorf("checks should run concurrently, took %s", elapsed)
	}
}

func TestDrainFailsReadinessBeforeShutdown(t *testing.T) {
	instance := newHealth(time.Second, 20*time.Millisecond)

	if code := call(t, instance.Ready()).Code; code != http.StatusOK {
		t.Fatalf("want a ready instance before draining, got %d", code)
	}

	if err := instance.BeginDrain(context.Background()); err != nil {
		t.Fatalf("drain failed: %v", err)
	}

	if code := call(t, instance.Ready()).Code; code != http.StatusServiceUnavailable {
		t.Errorf("readiness must fail while draining, got %d", code)
	}

	// Liveness stays up: the process is healthy, it is just leaving rotation.
	if code := call(t, instance.Live()).Code; code != http.StatusOK {
		t.Errorf("liveness must stay up while draining, got %d", code)
	}
}

func TestDrainStopsWhenTheBudgetRunsOut(t *testing.T) {
	instance := newHealth(time.Second, 5*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()

	// An interrupted drain is not a resource failure and must not be reported
	// as one.
	if err := instance.BeginDrain(ctx); err != nil {
		t.Errorf("a cut short drain should not fail the shutdown, got %v", err)
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("drain ignored the shutdown budget, took %s", elapsed)
	}
}
