package server_test

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/http/server"
	"github.com/rootless-dev/aegis/internal/infra/certs"
)

func discardLogger() *log.Logger {
	return &log.Logger{Writer: log.IOWriter{Writer: io.Discard}}
}

func newServer(t *testing.T, address string, handler http.Handler) *server.Server {
	t.Helper()

	return newServerWithTLS(t, address, handler, nil)
}

func newServerWithTLS(t *testing.T, address string, handler http.Handler, tlsConfig *tls.Config) *server.Server {
	t.Helper()

	return server.New(server.Options{
		Address:           address,
		Handler:           handler,
		Logger:            discardLogger(),
		TLSConfig:         tlsConfig,
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

func TestServesTLSAndNegotiatesHTTP2(t *testing.T) {
	const address = "127.0.0.1:7593"

	keeper, err := certs.SelfSigned(discardLogger(), certs.DevelopmentHosts)
	if err != nil {
		t.Fatalf("generating the certificate: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secure"))
	})

	srv := newServerWithTLS(t, address, mux, &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: keeper.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
	})

	srv.Start()

	defer srv.Shutdown(context.Background())

	client := &http.Client{Transport: &http.Transport{
		// The certificate is self-signed and generated for this run, so there is
		// no chain to verify against.
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}}

	var resp *http.Response

	for range 100 {
		resp, err = client.Get("https://" + address + "/")
		if err == nil {
			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	if err != nil {
		t.Fatalf("requesting over TLS: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "secure" {
		t.Errorf("body: want secure, got %q", body)
	}

	// A hand built TLS configuration replaces the one net/http would have
	// assembled, and HTTP/2 disappears with no error when h2 is not advertised.
	if resp.Proto != "HTTP/2.0" {
		t.Errorf("protocol: want HTTP/2.0, got %s", resp.Proto)
	}
}
