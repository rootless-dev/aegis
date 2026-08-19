package graceful_test

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/infra/graceful"
)

func newGraceful(timeout time.Duration) *graceful.Graceful {
	return graceful.New(graceful.Options{
		Logger:  &log.Logger{Writer: log.IOWriter{Writer: io.Discard}},
		Timeout: timeout,
	})
}

func TestPendingsResolveInReverseOrder(t *testing.T) {
	var (
		mu       sync.Mutex
		resolved []string
	)

	record := func(name string) graceful.ShutdownFunc {
		return func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			resolved = append(resolved, name)

			return nil
		}
	}

	instance := newGraceful(time.Second)
	instance.
		Register("database", record("database")).
		Register("http server", record("http server"))

	instance.Fail(errors.New("boom"))

	_ = instance.Wait()

	// Startup order is database then http, so shutdown must stop accepting
	// requests before the database goes away.
	if want := []string{"http server", "database"}; !slices.Equal(resolved, want) {
		t.Errorf("want %v, got %v", want, resolved)
	}
}

func TestFailingPendingDoesNotStopTheOthers(t *testing.T) {
	var (
		mu     sync.Mutex
		ranAll bool
	)

	instance := newGraceful(time.Second)
	instance.
		Register("last to resolve", func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			ranAll = true

			return nil
		}).
		Register("first to resolve", func(context.Context) error {
			return errors.New("could not close")
		})

	instance.Fail(errors.New("boom"))

	err := instance.Wait()

	if !ranAll {
		t.Error("a failing pending must not skip the remaining ones")
	}

	if err == nil {
		t.Error("the pending failure must surface")
	}
}

func TestWaitReportsTheFatalCause(t *testing.T) {
	cause := errors.New("listener died")

	instance := newGraceful(time.Second)
	instance.Fail(cause)

	if err := instance.Wait(); !errors.Is(err, cause) {
		t.Errorf("want the fatal cause, got %v", err)
	}
}

func TestWatchTriggersShutdownOnChannelError(t *testing.T) {
	failure := make(chan error, 1)
	failure <- errors.New("resource died")

	instance := newGraceful(time.Second)
	instance.Watch(failure)

	done := make(chan error, 1)

	go func() { done <- instance.Wait() }()

	select {
	case err := <-done:
		if err == nil {
			t.Error("a watched failure must end the wait with an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not trigger the shutdown")
	}
}
