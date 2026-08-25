// Package health answers the orchestrator probes.
//
// Liveness and readiness answer different questions and must not be confused:
// a failing liveness gets the container killed and restarted, while a failing
// readiness only takes it out of rotation. That is why liveness checks nothing
// external — a slow database would otherwise fail every replica at once and
// have all of them restarted, turning a degradation into an outage.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phuslu/log"
)

const (
	LivenessPath  = "/livez"
	ReadinessPath = "/readyz"

	statusAlive    = "alive"
	statusReady    = "ready"
	statusNotReady = "not_ready"

	resultHealthy = "ok"
	resultFailed  = "failed"
)

// Router is the slice of the HTTP router this package needs. Declaring it here
// keeps the infrastructure free of any dependency on the router in use.
type Router interface {
	Get(pattern string, handler http.HandlerFunc)
	Head(pattern string, handler http.HandlerFunc)
}

// Check reports whether a dependency is usable. It must honor the context,
// which carries the per check timeout.
type Check func(ctx context.Context) error

// DetailedCheck also describes what it reached — the server, the pool, how long
// the round trip took. The description is only rendered where RevealErrors
// allows it: it is diagnostic during development and topology in production.
type DetailedCheck func(ctx context.Context) (map[string]string, error)

type Options struct {
	Logger       *log.Logger
	CheckTimeout time.Duration
	DrainDelay   time.Duration

	// RevealErrors puts the failure itself in the public report instead of just
	// naming the check that failed. It is for development: an error carries the
	// address and the driver behind it.
	RevealErrors bool
}

type registeredCheck struct {
	name string
	fn   DetailedCheck
}

type Health struct {
	logger       *log.Logger
	checkTimeout time.Duration
	drainDelay   time.Duration
	revealErrors bool

	mu     sync.RWMutex
	checks []registeredCheck

	draining atomic.Bool
}

type report struct {
	Status string                       `json:"status"`
	Checks map[string]map[string]string `json:"checks,omitempty"`
}

func New(opts Options) *Health {
	return &Health{
		logger:       opts.Logger,
		checkTimeout: opts.CheckTimeout,
		drainDelay:   opts.DrainDelay,
		revealErrors: opts.RevealErrors,
	}
}

// Register adds a readiness dependency that only answers yes or no.
// Liveness never runs these.
func (h *Health) Register(name string, fn Check) *Health {
	return h.RegisterDetailed(name, func(ctx context.Context) (map[string]string, error) {
		return nil, fn(ctx)
	})
}

// RegisterDetailed adds one that also describes what it reached.
func (h *Health) RegisterDetailed(name string, fn DetailedCheck) *Health {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.checks = append(h.checks, registeredCheck{name: name, fn: fn})

	return h
}

// Mount registers the probes. They belong outside the shared middleware chain:
// the orchestrator hits them every few seconds per replica, which would bury
// the real traffic in the request log.
//
// HEAD sits next to each GET because the router matches by method: a probe
// asking for the headers alone would otherwise get a 405, which a load balancer
// reads as the instance being down.
func (h *Health) Mount(router Router) {
	live, ready := h.Live(), h.Ready()

	router.Get(LivenessPath, live)
	router.Head(LivenessPath, live)

	router.Get(ReadinessPath, ready)
	router.Head(ReadinessPath, ready)
}

// Live answers whether the process is able to serve at all. It deliberately
// checks nothing.
func (h *Health) Live() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeReport(w, http.StatusOK, report{Status: statusAlive})
	}
}

// Ready answers whether this instance should receive traffic, naming each
// check and whether it passed. What it withholds outside development is the
// failure itself: "failed" says which dependency is down, while the error
// behind it would also say where it lives.
func (h *Health) Ready() http.HandlerFunc {
	return h.readiness(h.revealErrors)
}

// ReadyDetailed reports the failures themselves whatever the profile. It must
// only be mounted on the authenticated administration surface.
func (h *Health) ReadyDetailed() http.HandlerFunc {
	return h.readiness(true)
}

func (h *Health) readiness(revealErrors bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// No checks are run while draining, so the absence of them in the report
		// is what tells an ordered shutdown apart from a dependency failing.
		if h.draining.Load() {
			writeReport(w, http.StatusServiceUnavailable, report{Status: statusNotReady})

			return
		}

		results, healthy := h.run(r.Context())

		status := http.StatusOK
		body := report{Status: statusReady, Checks: describe(results, revealErrors)}

		if !healthy {
			status = http.StatusServiceUnavailable
			body.Status = statusNotReady
		}

		writeReport(w, status, body)
	}
}

// describe renders what the report shows for each check: an object carrying
// the verdict, so the shape is the same whatever the profile, plus whatever
// the check described and the failure itself where those are safe to read.
func describe(results map[string]result, revealErrors bool) map[string]map[string]string {
	described := make(map[string]map[string]string, len(results))

	for name, outcome := range results {
		rendered := map[string]string{"status": resultHealthy}

		if outcome.err != nil {
			rendered["status"] = resultFailed
		}

		if revealErrors {
			for key, value := range outcome.details {
				rendered[key] = value
			}

			if outcome.err != nil {
				rendered["error"] = outcome.err.Error()
			}
		}

		described[name] = rendered
	}

	return described
}

// result is one check's outcome: whether it passed, and what it said about the
// dependency on the way. A check that failed can still have described it.
type result struct {
	details map[string]string
	err     error
}

// run evaluates the checks concurrently so the probe cost is the slowest check
// rather than their sum.
func (h *Health) run(ctx context.Context) (map[string]result, bool) {
	h.mu.RLock()
	checks := slices.Clone(h.checks)
	h.mu.RUnlock()

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results = make(map[string]result, len(checks))
		healthy = true
	)

	for _, check := range checks {
		wg.Add(1)

		go func() {
			defer wg.Done()

			checkCtx, cancel := context.WithTimeout(ctx, h.checkTimeout)
			defer cancel()

			details, err := check.fn(checkCtx)
			if err != nil {
				// The public report may withhold this, and the probe is a
				// moment rather than a history. Whoever is diagnosing needs
				// both, so the failure is recorded here regardless.
				h.logger.Warn().Str("check", check.name).Err(err).Msg("readiness check failed")
			}

			mu.Lock()
			defer mu.Unlock()

			results[check.name] = result{details: details, err: err}

			if err != nil {
				healthy = false
			}
		}()
	}

	wg.Wait()

	return results, healthy
}

// BeginDrain fails readiness and holds for the drain delay, giving the load
// balancer time to take this instance out of rotation before connections stop
// being accepted. Registered last with the graceful shutdown so that it is the
// first pending to resolve.
func (h *Health) BeginDrain(ctx context.Context) error {
	h.draining.Store(true)

	h.logger.Info().
		Dur("delay", h.drainDelay).
		Msg("readiness is now failing, draining before the server stops accepting")

	timer := time.NewTimer(h.drainDelay)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-ctx.Done():
		// An interrupted drain is not a resource failure, so the shutdown is
		// not reported as failed because of it.
		h.logger.Warn().Msg("drain was cut short by the shutdown budget")
	}

	return nil
}

func writeReport(w http.ResponseWriter, status int, body report) {
	encoded, err := json.Marshal(body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	// Probes must never be answered from a cache.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	_, _ = w.Write(encoded)
}
