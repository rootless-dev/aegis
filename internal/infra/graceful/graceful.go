// Package graceful coordinates the ordered shutdown of the application.
package graceful

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/phuslu/log"
)

type ShutdownFunc func(ctx context.Context) error

type Options struct {
	Logger  *log.Logger
	Timeout time.Duration
}

type pending struct {
	name string
	fn   ShutdownFunc
}

type Graceful struct {
	logger  *log.Logger
	timeout time.Duration
	signals []os.Signal

	mu      sync.Mutex
	pending []pending

	failure chan error
}

func New(opts Options) *Graceful {
	return &Graceful{
		logger:  opts.Logger,
		timeout: opts.Timeout,
		signals: []os.Signal{os.Interrupt, syscall.SIGTERM},
		failure: make(chan error, 1),
	}
}

// Register adds a shutdown pending. Pendings are resolved from the last
// registration to the first, like defer, so registering in startup order makes
// the HTTP server stop accepting requests before the database closes.
func (g *Graceful) Register(name string, fn ShutdownFunc) *Graceful {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.pending = append(g.pending, pending{name: name, fn: fn})

	return g
}

// Watch triggers the shutdown when a fatal error arrives on errs.
func (g *Graceful) Watch(errs <-chan error) *Graceful {
	go func() {
		for err := range errs {
			if err != nil {
				g.Fail(err)

				return
			}
		}
	}()

	return g
}

// Fail triggers the shutdown. Calls after the first are dropped, since the
// shutdown is already underway.
func (g *Graceful) Fail(err error) {
	select {
	case g.failure <- err:
	default:
	}
}

// Wait blocks until a termination signal or a fatal error, resolves the
// registered pendings and returns both the shutdown cause and any pending
// failure.
func (g *Graceful) Wait() error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, g.signals...)
	defer signal.Stop(signals)

	var cause error

	select {
	case received := <-signals:
		g.logger.Info().Str("signal", received.String()).Msg("shutdown signal received")
	case err := <-g.failure:
		cause = err
		g.logger.Error().Err(err).Msg("fatal error received, shutting down")
	}

	// A second signal means the caller is no longer willing to wait.
	go func() {
		received := <-signals
		g.logger.Warn().Str("signal", received.String()).Msg("second signal received, exiting immediately")
		os.Exit(1)
	}()

	return errors.Join(cause, g.resolve())
}

func (g *Graceful) resolve() error {
	g.mu.Lock()
	tasks := slices.Clone(g.pending)
	g.mu.Unlock()

	if len(tasks) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	g.logger.Info().
		Int("pending", len(tasks)).
		Dur("timeout", g.timeout).
		Msg("resolving shutdown pendings")

	var errs []error

	for _, task := range slices.Backward(tasks) {
		start := time.Now()

		// A failing pending must not stop the remaining ones: the goal is to
		// close as much as possible before the process dies.
		if err := task.fn(ctx); err != nil {
			g.logger.Error().Str("pending", task.name).Err(err).Msg("shutdown pending failed")
			errs = append(errs, fmt.Errorf("graceful: %s: %w", task.name, err))

			continue
		}

		g.logger.Info().
			Str("pending", task.name).
			Dur("duration", time.Since(start)).
			Msg("shutdown pending resolved")
	}

	return errors.Join(errs...)
}
