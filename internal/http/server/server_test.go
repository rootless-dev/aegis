package server_test

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/http/server"
)

func newServer(t *testing.T, address string, handler http.Handler) *server.Server {
	t.Helper()

	return server.New(server.Options{
		Address:           address,
		Handler:           handler,
		Logger:            &log.Logger{Writer: log.IOWriter{Writer: io.Discard}},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	})
}

func TestShutdownWaitsForInFlightRequests(t *testing.T) {
	const address = "127.0.0.1:7591"

	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte("finished"))
	})

	srv := newServer(t, address, mux)
	srv.Start()
	waitReady(t, "http://"+address+"/slow")

	body := make(chan string, 1)

	go func() {
		resp, err := http.Get("http://" + address + "/slow")
		if err != nil {
			body <- "request failed: " + err.Error()

			return
		}
		defer resp.Body.Close()

		read, _ := io.ReadAll(resp.Body)
		body <- string(read)
	}()

	time.Sleep(100 * time.Millisecond)

	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}

	if got := <-body; got != "finished" {
		t.Errorf("in-flight request was cut short: %q", got)
	}
}

func TestStartReportsListenFailure(t *testing.T) {
	const address = "127.0.0.1:7592"

	blocker := newServer(t, address, http.NewServeMux())
	blocker.Start()
	defer blocker.Shutdown(context.Background())
	waitReady(t, "http://"+address+"/")

	// The second server cannot bind the same port and must report it instead of
	// failing quietly in its goroutine.
	failure := newServer(t, address, http.NewServeMux()).Start()

	select {
	case err := <-failure:
		if err == nil {
			t.Fatal("binding an address already in use must report an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listen failure was never reported")
	}
}

func waitReady(t *testing.T, url string) {
	t.Helper()

	for range 100 {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()

			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("server did not start")
}
