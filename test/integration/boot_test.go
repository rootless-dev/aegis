//go:build integration

// Package integration exercises the compiled binary, which the unit tests
// cannot reach: they build the configuration by hand and never go through the
// builder defaults, the assembly in New, or main itself. A default that stopped
// being valid fails only at boot, with every other test still green.
package integration_test

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	bootTimeout     = 20 * time.Second
	shutdownTimeout = 30 * time.Second
)

var binaryPath string

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	os.Exit(code)
}

// run exists so the temporary directory is still removed: os.Exit skips defers.
func run(m *testing.M) (int, error) {
	dir, err := os.MkdirTemp("", "aegis-integration-")
	if err != nil {
		return 0, fmt.Errorf("creating the work directory: %w", err)
	}
	defer os.RemoveAll(dir)

	binaryPath = filepath.Join(dir, "aegisd")

	build := exec.Command("go", "build", "-o", binaryPath, "./cmd/server")
	build.Dir = filepath.Join("..", "..")

	if out, err := build.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("building the server: %w\n%s", err, out)
	}

	return m.Run(), nil
}

type instance struct {
	cmd    *exec.Cmd
	port   string
	scheme string
	client *http.Client
	output *bytes.Buffer
}

func (i *instance) url(path string) string {
	return i.scheme + "://127.0.0.1:" + i.port + path
}

// start launches the binary from an empty working directory, on a deliberately
// minimal environment: anything inherited from the developer shell — or a .env
// picked up by the autoload — would make the run depend on the machine.
//
// The topology is declared because production refuses to guess it, which is the
// point of the setting: these runs stand in for a deployment behind something
// that is not there, so nothing terminates TLS.
func start(t *testing.T, extraEnv ...string) *instance {
	t.Helper()

	port := freePort(t)

	env := append([]string{
		"TLS_TERMINATION=none",
		"PUBLIC_URL=http://127.0.0.1:" + port,
	}, extraEnv...)

	return launch(t, port, "http", http.DefaultClient, env, nil)
}

// startDevelopment exercises the profile flag, where TLS is served from a
// certificate the process mints for itself and nothing has to be configured.
func startDevelopment(t *testing.T) *instance {
	t.Helper()

	client := &http.Client{Transport: &http.Transport{
		// The certificate was generated seconds ago for this run alone, so there
		// is no chain to verify it against.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
	}}

	return launch(t, freePort(t), "https", client, nil, []string{"--dev"})
}

func launch(t *testing.T, port, scheme string, client *http.Client, extraEnv, args []string) *instance {
	t.Helper()

	workDir := t.TempDir()
	output := &bytes.Buffer{}

	cmd := exec.Command(binaryPath, args...)
	cmd.Dir = workDir
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HTTP_SERVER_HOST=127.0.0.1",
		"HTTP_SERVER_PORT=" + port,
		// The default drain waits for a load balancer that does not exist here.
		"HEALTH_DRAIN_DELAY=1s",
	}, extraEnv...)

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the server: %v", err)
	}

	return &instance{cmd: cmd, port: port, scheme: scheme, client: client, output: output}
}

func (i *instance) waitReady(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(bootTimeout)

	for time.Now().Before(deadline) {
		resp, err := i.client.Get(i.url("/livez"))
		if err == nil {
			resp.Body.Close()

			return
		}

		// Signal 0 only probes whether the process is still there. Without it a
		// refused boot would be reported as a timeout, hiding the real reason.
		if err := i.cmd.Process.Signal(syscall.Signal(0)); err != nil {
			t.Fatalf("server exited before answering:\n%s", i.output.String())
		}

		time.Sleep(50 * time.Millisecond)
	}

	_ = i.cmd.Process.Kill()
	t.Fatalf("server did not answer within %s:\n%s", bootTimeout, i.output.String())
}

// terminate asks the process to stop and waits for it. A second signal is not
// sent: the shutdown treats it as an order to give up waiting and exits with a
// failure status.
func (i *instance) terminate(t *testing.T) error {
	t.Helper()

	i.signal(t)

	return i.wait(t)
}

func (i *instance) signal(t *testing.T) {
	t.Helper()

	if err := i.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signalling the server: %v", err)
	}
}

// wait reports how the process exited, which is what tells a clean shutdown
// from one cut short.
func (i *instance) wait(t *testing.T) error {
	t.Helper()

	exited := make(chan error, 1)

	go func() { exited <- i.cmd.Wait() }()

	select {
	case err := <-exited:
		return err
	case <-time.After(shutdownTimeout):
		_ = i.cmd.Process.Kill()
		t.Fatalf("server did not exit within %s:\n%s", shutdownTimeout, i.output.String())

		return nil
	}
}

func freePort(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer listener.Close()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("reading the reserved port: %v", err)
	}

	return port
}

func (i *instance) get(t *testing.T, path string) (int, string) {
	t.Helper()

	url := i.url(path)

	resp, err := i.client.Get(url)
	if err != nil {
		t.Fatalf("requesting %s: %v", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", url, err)
	}

	return resp.StatusCode, string(body)
}

// TestBootsOnDefaultsAndShutsDownCleanly is the reason this package exists: it
// proves the binary comes up with nothing but a port and a topology declared,
// and leaves on SIGTERM with a success status.
func TestBootsOnDefaultsAndShutsDownCleanly(t *testing.T) {
	server := start(t)
	server.waitReady(t)

	status, body := server.get(t, "/livez")
	if status != http.StatusOK || !strings.Contains(body, `"alive"`) {
		t.Errorf("livez: want 200 alive, got %d %s", status, body)
	}

	status, body = server.get(t, "/readyz")
	if status != http.StatusOK || !strings.Contains(body, `"ready"`) {
		t.Errorf("readyz: want 200 ready, got %d %s", status, body)
	}

	if err := server.terminate(t); err != nil {
		t.Errorf("clean shutdown expected, got %v:\n%s", err, server.output.String())
	}
}

// TestReadinessFailsWhileDraining covers the ordering the graceful shutdown
// depends on: readiness has to fail while the server still answers, otherwise
// the load balancer keeps routing here after the door closes.
func TestReadinessFailsWhileDraining(t *testing.T) {
	server := start(t, "HEALTH_DRAIN_DELAY=5s")
	server.waitReady(t)
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
	server.waitReady(t)

	server.signal(t)
	// The first signal starts a 15s drain; the second must cut it short well
	// before that.
	time.Sleep(200 * time.Millisecond)
	server.signal(t)

	start := time.Now()

	err := server.wait(t)
	if err == nil {
		t.Error("a forced exit must report a failure status")
	}

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the second signal should not wait for the drain, took %s", elapsed)
	}

	if !strings.Contains(server.output.String(), "second signal received") {
		t.Errorf("the forced exit should be logged, got:\n%s", server.output.String())
	}
}

// TestFailsFastOnInvalidConfiguration keeps the boot from starting half broken:
// the process must refuse to come up and say what is wrong.
func TestFailsFastOnInvalidConfiguration(t *testing.T) {
	cmd := exec.Command(binaryPath)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HTTP_SERVER_PORT=not-a-port",
		"LOGGING_LEVEL=banana",
	}

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("an invalid configuration must not boot, output:\n%s", out)
	}

	// Both problems have to be reported together, or fixing a misconfigured
	// deployment costs one restart per variable.
	for _, want := range []string{"invalid configuration", "invalid port", "unsupported level"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output should mention %q, got:\n%s", want, out)
		}
	}
}

// The two situations this refusal separates produce identical configuration: a
// deployment whose gateway handles TLS, and one whose certificate was simply
// forgotten. Only an operator can tell them apart, so production asks.
func TestRefusesToBootWithoutADeclaredTopology(t *testing.T) {
	cmd := exec.Command(binaryPath)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HTTP_SERVER_PORT=" + freePort(t),
	}

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("production must not guess who terminates TLS, output:\n%s", out)
	}

	for _, want := range []string{"termination is required", "public url is required"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output should mention %q, got:\n%s", want, out)
		}
	}
}

// Development needs no certificate, no gateway and no public url: it mints its
// own pair and serves TLS through the same code path production takes.
func TestDevelopmentServesTLSWithAGeneratedCertificate(t *testing.T) {
	server := startDevelopment(t)
	server.waitReady(t)

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

	if err := server.terminate(t); err != nil {
		t.Errorf("clean shutdown expected, got %v:\n%s", err, server.output.String())
	}
}
