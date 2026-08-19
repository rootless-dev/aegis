//go:build integration

// Package integration exercises the compiled binary, which the unit tests
// cannot reach: they build the configuration by hand and never go through the
// builder defaults, the assembly in New, or main itself. A default that stopped
// being valid fails only at boot, with every other test still green.
package integration_test

import (
	"bytes"
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
	output *bytes.Buffer
}

func (i *instance) url(path string) string {
	return "http://127.0.0.1:" + i.port + path
}

// start launches the binary from an empty working directory, on a deliberately
// minimal environment: anything inherited from the developer shell — or a .env
// picked up by the autoload — would make the run depend on the machine.
func start(t *testing.T, extraEnv ...string) *instance {
	t.Helper()

	workDir := t.TempDir()
	port := freePort(t)
	output := &bytes.Buffer{}

	cmd := exec.Command(binaryPath)
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

	return &instance{cmd: cmd, port: port, output: output}
}

func (i *instance) waitReady(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(bootTimeout)

	for time.Now().Before(deadline) {
		resp, err := http.Get(i.url("/livez"))
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

func get(t *testing.T, url string) (int, string) {
	t.Helper()

	resp, err := http.Get(url)
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
// proves the binary comes up with nothing but a port set, and leaves on SIGTERM
// with a success status.
func TestBootsOnDefaultsAndShutsDownCleanly(t *testing.T) {
	server := start(t)
	server.waitReady(t)

	status, body := get(t, server.url("/livez"))
	if status != http.StatusOK || !strings.Contains(body, `"alive"`) {
		t.Errorf("livez: want 200 alive, got %d %s", status, body)
	}

	status, body = get(t, server.url("/readyz"))
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
		status, body = get(t, server.url("/readyz"))
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
	if status, body := get(t, server.url("/livez")); status != http.StatusOK {
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
