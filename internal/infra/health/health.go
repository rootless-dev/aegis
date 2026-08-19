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
)

// Router is the slice of the HTTP router this package needs. Declaring it here
// keeps the infrastructure free of any dependency on the router in use.
type Router interface {
	Get(pattern string, handler http.HandlerFunc)
}

// Check reports whether a dependency is usable. It must honor the context,
// which carries the per check timeout.
type Check func(ctx context.Context) error

type Options struct {
	Logger       *log.Logger
	CheckTimeout time.Duration
	DrainDelay   time.Duration
}

type registeredCheck struct {
	name string
	fn   Check
}

type Health struct {
	logger       *log.Logger
	checkTimeout time.Duration
	drainDelay   time.Duration

	mu     sync.RWMutex
	checks []registeredCheck

	draining atomic.Bool
}

type report struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

func New(opts Options) *Health {
	return &Health{
		logger:       opts.Logger,
		checkTimeout: opts.CheckTimeout,
		drainDelay:   opts.DrainDelay,
	}
}

// Register adds a readiness dependency. Liveness never runs these.
func (h *Health) Register(name string, fn Check) *Health {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.checks = append(h.checks, registeredCheck{name: name, fn: fn})

	return h
}

// Mount registers the probes. They belong outside the shared middleware chain:
// the orchestrator hits them every few seconds per replica, which would bury
// the real traffic in the request log.
func (h *Health) Mount(router Router) {
	router.Get(LivenessPath, h.Live())
	router.Get(ReadinessPath, h.Ready())
}

// Live answers whether the process is able to serve at all. It deliberately
// checks nothing.
func (h *Health) Live() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeReport(w, http.StatusOK, report{Status: statusAlive})
	}
}

// Ready answers whether this instance should receive traffic, without naming
// the dependency that failed: the endpoint is public, and the names and errors
// describe internal topology.
func (h *Health) Ready() http.HandlerFunc {
	return h.readiness(false)
}

// ReadyDetailed answers the same question naming each check and its failure.
// It must only be mounted on the authenticated administration surface.
func (h *Health) ReadyDetailed() http.HandlerFunc {
	return h.readiness(true)
}

func (h *Health) readiness(detailed bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.draining.Load() {
			writeReport(w, http.StatusServiceUnavailable, report{Status: statusNotReady})

			return
		}

		results, healthy := h.run(r.Context())

		status := http.StatusOK
		body := report{Status: statusReady}

		if !healthy {
			status = http.StatusServiceUnavailable
			body.Status = statusNotReady
		}

		if detailed {
			body.Checks = results
		}

		writeReport(w, status, body)
	}
}

// run evaluates the checks concurrently so the probe cost is the slowest check
// rather than their sum.
func (h *Health) run(ctx context.Context) (map[string]string, bool) {
	h.mu.RLock()
	checks := slices.Clone(h.checks)
	h.mu.RUnlock()

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results = make(map[string]string, len(checks))
		healthy = true
	)

	for _, check := range checks {
		wg.Add(1)

		go func() {
			defer wg.Done()

			checkCtx, cancel := context.WithTimeout(ctx, h.checkTimeout)
			defer cancel()

			err := check.fn(checkCtx)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				results[check.name] = err.Error()
				healthy = false

				return
			}

			results[check.name] = resultHealthy
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
