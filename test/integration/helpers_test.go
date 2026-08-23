//go:build integration

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

// instance is a running binary under test, plus enough about how it was
// launched to reach it over HTTP and tell a clean exit from a forced one.
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

// startBinary launches binaryPath from an empty working directory, on a
// deliberately minimal environment plus env: anything inherited from the
// developer shell — or a .env picked up by the autoload — would make the run
// depend on the machine. It always talks plain HTTP; the one caller that needs
// TLS (the development profile, which mints its own certificate) goes through
// launch directly.
func startBinary(t *testing.T, env []string) *instance {
	t.Helper()

	return launch(t, freePort(t), "http", http.DefaultClient, env, nil)
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

	// A normal test ends by asking the server to stop through stopBinary or
	// terminate, which leaves nothing here to kill. But a t.Fatal partway
	// through - a failed assertion on a response, not just the boot itself -
	// abandons the process without running either: Kill on an already-exited
	// process just errors, which is why the failure is discarded.
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	return &instance{cmd: cmd, port: port, scheme: scheme, client: client, output: output}
}

// waitUntilReady polls /livez until the process answers or bootTimeout
// elapses.
func waitUntilReady(t *testing.T, process *instance) {
	t.Helper()

	deadline := time.Now().Add(bootTimeout)

	for time.Now().Before(deadline) {
		resp, err := process.client.Get(process.url("/livez"))
		if err == nil {
			resp.Body.Close()

			return
		}

		// Signal 0 only probes whether the process is still there. Without it a
		// refused boot would be reported as a timeout, hiding the real reason.
		if err := process.cmd.Process.Signal(syscall.Signal(0)); err != nil {
			t.Fatalf("server exited before answering:\n%s", process.output.String())
		}

		time.Sleep(50 * time.Millisecond)
	}

	_ = process.cmd.Process.Kill()
	t.Fatalf("server did not answer within %s:\n%s", bootTimeout, process.output.String())
}

// stopBinary asks the process to stop and waits for it within shutdownTimeout,
// failing the test on a non-zero exit. A second signal is not sent here: the
// tests that need one send it themselves through signal below, since sending
// it makes the shutdown give up waiting and exit with a failure status.
func stopBinary(t *testing.T, process *instance) {
	t.Helper()

	process.signal(t)

	if err := process.wait(t); err != nil {
		t.Errorf("clean shutdown expected, got %v:\n%s", err, process.output.String())
	}
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

// runBinaryToCompletion runs the binary to exit, from an empty working
// directory and a minimal environment, and returns its combined output.
func runBinaryToCompletion(t *testing.T, args, env []string) (string, error) {
	t.Helper()

	cmd := exec.Command(binaryPath, args...)
	cmd.Dir = t.TempDir()
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)

	out, err := cmd.CombinedOutput()

	return string(out), err
}
