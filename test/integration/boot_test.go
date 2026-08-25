//go:build integration

// Package integration exercises the compiled binary, which the unit tests
// cannot reach: they build the configuration by hand and never go through the
// builder defaults, the assembly in New, or main itself. A default that stopped
// being valid fails only at boot, with every other test still green.
package integration_test

import (
	"crypto/tls"
	"net/http"
	"strings"
	"testing"
	"time"
)

// start launches the binary from a production-shaped environment: the
// topology and the database driver are declared, because production refuses
// to guess either one. It runs against a real Postgres container — Task 8 made
// the assembly connect for real, so nothing shy of a real server proves the
// boot still works.
func start(t *testing.T, extraEnv ...string) *instance {
	t.Helper()

	dbHost, dbPort, _ := startPostgres(t)
	appPort := freePort(t)

	env := append([]string{
		"TLS_TERMINATION=none",
		// Matches the port the server is actually told to listen on below,
		// same as before this suite ran against a real container: nothing
		// asserts on PUBLIC_URL, but a value that could not possibly be this
		// process's own address is a needless way to invite confusion later.
		"PUBLIC_URL=http://127.0.0.1:" + appPort,
		"DATABASE_DRIVER=postgres",
		"DATABASE_HOST=" + dbHost,
		"DATABASE_PORT=" + dbPort,
		"DATABASE_NAME=aegis",
		"DATABASE_USER=aegis",
		"DATABASE_PASSWORD=aegis",
		"DATABASE_SSL_MODE=disable",
	}, extraEnv...)

	return launch(t, appPort, "http", http.DefaultClient, env, nil)
}

// startDevelopment exercises the TLS opt-in: dev serves plain HTTP, and
// TLS_TERMINATION=app turns HTTPS back on without a key pair on the machine.
// It is the only place left where a generated certificate meets a real socket.
func startDevelopment(t *testing.T) *instance {
	t.Helper()

	client := &http.Client{Transport: &http.Transport{
		// The certificate was generated seconds ago for this run alone, so there
		// is no chain to verify it against.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
	}}

	return launch(t, freePort(t), "https", client, []string{"TLS_TERMINATION=app"}, []string{"--dev"})
}

// startDevelopmentDefaults declares nothing at all, as `make run` does.
func startDevelopmentDefaults(t *testing.T) *instance {
	t.Helper()

	return launch(t, freePort(t), "http", http.DefaultClient, nil, []string{"--dev"})
}

// TestBootsOnDefaultsAndShutsDownCleanly is the reason this package exists: it
// proves the binary comes up with nothing but a port, a topology and a
// database driver declared, and leaves on SIGTERM with a success status.
func TestBootsOnDefaultsAndShutsDownCleanly(t *testing.T) {
	server := start(t)
	waitUntilReady(t, server)

	status, body := server.get(t, "/livez")
	if status != http.StatusOK || !strings.Contains(body, `"alive"`) {
		t.Errorf("livez: want 200 alive, got %d %s", status, body)
	}

	status, body = server.get(t, "/readyz")
	if status != http.StatusOK || !strings.Contains(body, `"ready"`) {
		t.Errorf("readyz: want 200 ready, got %d %s", status, body)
	}

	// The database announces itself once it is actually connected, which is
	// where an operator finds what this process reached. start sets the
	// password to "aegis", so its absence next to a real driver/host pair is
	// what proves the dsn, which carries it, was not logged either.
	output := server.output.String()
	if !strings.Contains(output, `"database connected"`) {
		t.Errorf("expected the database to announce itself in the startup log, got:\n%s", output)
	}

	if !strings.Contains(output, `"driver":"postgres"`) {
		t.Errorf("expected the resolved driver in that announcement, got:\n%s", output)
	}

	if strings.Contains(output, "aegis:aegis@") || strings.Contains(output, "DATABASE_PASSWORD=aegis") {
		t.Errorf("expected the database password not to appear in the log, got:\n%s", output)
	}

	stopBinary(t, server)
}

// TestReadinessFailsWhileDraining covers the ordering the graceful shutdown
// depends on: readiness has to fail while the server still answers, otherwise
// the load balancer keeps routing here after the door closes.
func TestReadinessFailsWhileDraining(t *testing.T) {
	server := start(t, "HEALTH_DRAIN_DELAY=5s")
	waitUntilReady(t, server)
	server.signal(t)

	deadline := time.Now().Add(3 * time.Second)

	var status int

	var body string

	for time.Now().Before(deadline) {
		status, body = server.get(t, "/readyz")
		if status == http.StatusServiceUnavailable {
			break
		}

		time.Sleep(50 * time.Millisecond)
	}

	if status != http.StatusServiceUnavailable || !strings.Contains(body, `"not_ready"`) {
		t.Errorf("readyz while draining: want 503 not_ready, got %d %s", status, body)
	}

	// Liveness has to stay up: the process is healthy, it is only leaving
	// rotation. Failing it here would have the orchestrator restart a container
	// that is already shutting down correctly.
	if status, body := server.get(t, "/livez"); status != http.StatusOK {
		t.Errorf("livez while draining: want 200, got %d %s", status, body)
	}

	// Only waits here: the drain is already running, and signalling again would
	// abort it.
	if err := server.wait(t); err != nil {
		t.Errorf("clean shutdown expected, got %v:\n%s", err, server.output.String())
	}
}

// TestSecondSignalStopsWaiting locks in the escape hatch: whoever is on the
// other side asked twice, so the remaining pendings are abandoned and the
// process leaves with a failure status.
func TestSecondSignalStopsWaiting(t *testing.T) {
	server := start(t, "HEALTH_DRAIN_DELAY=15s")
	waitUntilReady(t, server)

	server.signal(t)
	// The first signal starts a 15s drain; the second must cut it short well
	// before that.
	time.Sleep(200 * time.Millisecond)
	server.signal(t)

	startedAt := time.Now()

	err := server.wait(t)
	if err == nil {
		t.Error("a forced exit must report a failure status")
	}

	if elapsed := time.Since(startedAt); elapsed > 5*time.Second {
		t.Errorf("the second signal should not wait for the drain, took %s", elapsed)
	}

	if !strings.Contains(server.output.String(), "second signal received") {
		t.Errorf("the forced exit should be logged, got:\n%s", server.output.String())
	}
}

// TestFailsFastOnInvalidConfiguration keeps the boot from starting half broken:
// the process must refuse to come up and say what is wrong.
func TestFailsFastOnInvalidConfiguration(t *testing.T) {
	out, err := runBinaryToCompletion(t, nil, []string{
		"HTTP_SERVER_PORT=not-a-port",
		"LOGGING_LEVEL=banana",
	})
	if err == nil {
		t.Fatalf("an invalid configuration must not boot, output:\n%s", out)
	}

	// Both problems have to be reported together, or fixing a misconfigured
	// deployment costs one restart per variable.
	for _, want := range []string{"invalid configuration", "invalid port", "unsupported level"} {
		if !strings.Contains(out, want) {
			t.Errorf("output should mention %q, got:\n%s", want, out)
		}
	}
}

// The two situations this refusal separates produce identical configuration: a
// deployment whose gateway handles TLS, and one whose certificate was simply
// forgotten. Only an operator can tell them apart, so production asks.
func TestRefusesToBootWithoutADeclaredTopology(t *testing.T) {
	out, err := runBinaryToCompletion(t, nil, []string{"HTTP_SERVER_PORT=" + freePort(t)})
	if err == nil {
		t.Fatalf("production must not guess who terminates TLS, output:\n%s", out)
	}

	for _, want := range []string{"termination is required", "public url is required"} {
		if !strings.Contains(out, want) {
			t.Errorf("output should mention %q, got:\n%s", want, out)
		}
	}
}

// Development declaring nothing is plain HTTP: no certificate to accept.
func TestDevelopmentDefaultsToPlainHTTP(t *testing.T) {
	server := startDevelopmentDefaults(t)
	waitUntilReady(t, server)

	status, body := server.get(t, "/livez")
	if status != http.StatusOK || !strings.Contains(body, `"alive"`) {
		t.Errorf("livez over plain HTTP: want 200 alive, got %d %s", status, body)
	}

	if strings.Contains(server.output.String(), "self-signed certificate generated in memory") {
		t.Errorf("nothing serves TLS, so no certificate should be minted, got:\n%s", server.output.String())
	}

	stopBinary(t, server)
}

// Termination "app" under dev needs no certificate, no gateway and no public
// url: it mints its own pair and takes the same code path production does.
func TestDevelopmentServesTLSWithAGeneratedCertificate(t *testing.T) {
	server := startDevelopment(t)
	waitUntilReady(t, server)

	status, body := server.get(t, "/livez")
	if status != http.StatusOK || !strings.Contains(body, `"alive"`) {
		t.Errorf("livez over TLS: want 200 alive, got %d %s", status, body)
	}

	if !strings.Contains(server.output.String(), "self-signed certificate generated in memory") {
		t.Errorf("the generated certificate should be announced, got:\n%s", server.output.String())
	}

	// A plain request to a TLS listener is answered in the clear, but only to
	// say it was the wrong scheme: net/http recognizes the handshake that never
	// happened and refuses rather than serving anything.
	plain, err := http.Get("http://127.0.0.1:" + server.port + "/livez")
	if err != nil {
		t.Fatalf("requesting in the clear: %v", err)
	}
	defer plain.Body.Close()

	if plain.StatusCode != http.StatusBadRequest {
		t.Errorf("plain HTTP against a TLS listener: want 400, got %d", plain.StatusCode)
	}

	stopBinary(t, server)
}
