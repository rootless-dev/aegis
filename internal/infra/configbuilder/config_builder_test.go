package configbuilder_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rootless-dev/aegis/internal/infra/configbuilder"
)

// writeConfig puts a configuration file on disk and points the builder at it.
func writeConfig(t *testing.T, content string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "aegis.yaml")

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing the configuration file: %v", err)
	}

	t.Setenv(configbuilder.ConfigPathEnvVar, path)
}

func TestBuildWithoutDefaultsFails(t *testing.T) {
	_, err := configbuilder.New().WithEnv().Validate().Build()

	if !errors.Is(err, configbuilder.ErrConfigInstanceNotInitialized) {
		t.Fatalf("want ErrConfigInstanceNotInitialized, got %v", err)
	}
}

func TestDefaultsAloneProduceAValidConfiguration(t *testing.T) {
	cfg, err := configbuilder.New().WithDefaults().Validate().Build()
	if err != nil {
		t.Fatalf("defaults must be valid on their own, got %v", err)
	}

	if cfg.HttpServer.Address() != "0.0.0.0:7500" {
		t.Errorf("want 0.0.0.0:7500, got %s", cfg.HttpServer.Address())
	}
}

func TestYAMLOverwritesDefaults(t *testing.T) {
	writeConfig(t, `
app_name: aegis-from-file
http_server:
  port: "9000"
  read_timeout: 45s
health:
  drain_delay: 3s
banner:
  enabled: false
`)

	cfg, err := configbuilder.New().WithDefaults().WithYAML().Validate().Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.AppName != "aegis-from-file" {
		t.Errorf("app name: want aegis-from-file, got %s", cfg.AppName)
	}

	if cfg.HttpServer.Port != "9000" {
		t.Errorf("port: want 9000, got %s", cfg.HttpServer.Port)
	}

	if cfg.HttpServer.ReadTimeout != 45*time.Second {
		t.Errorf("read timeout: want 45s, got %s", cfg.HttpServer.ReadTimeout)
	}

	// A file that says false has to win over a default of true, which is the
	// whole reason the document uses pointers.
	if cfg.Banner.Enabled {
		t.Error("banner: the file disabled it and it stayed on")
	}

	// Untouched keys keep their default rather than being reset to zero.
	if cfg.HttpServer.Host != "0.0.0.0" {
		t.Errorf("host should have kept its default, got %s", cfg.HttpServer.Host)
	}

	if cfg.HttpServer.WriteTimeout != 15*time.Second {
		t.Errorf("write timeout should have kept its default, got %s", cfg.HttpServer.WriteTimeout)
	}
}

func TestEnvOverwritesYAML(t *testing.T) {
	writeConfig(t, `
app_name: from-file
http_server:
  port: "9000"
  read_timeout: 45s
`)

	t.Setenv("HTTP_SERVER_PORT", "7777")
	t.Setenv("APP_NAME", "from-env")

	cfg, err := configbuilder.New().WithDefaults().WithYAML().WithEnv().Validate().Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The environment is the last layer: it adjusts one instance without
	// touching the file that ships with the image.
	if cfg.AppName != "from-env" || cfg.HttpServer.Port != "7777" {
		t.Errorf("environment should win, got name=%s port=%s", cfg.AppName, cfg.HttpServer.Port)
	}

	// What the environment does not mention stays as the file left it.
	if cfg.HttpServer.ReadTimeout != 45*time.Second {
		t.Errorf("read timeout should have stayed at the file value, got %s", cfg.HttpServer.ReadTimeout)
	}
}

func TestMissingFileIsNotAnError(t *testing.T) {
	// Nothing points at a file, and none of the default paths exist here.
	if _, err := configbuilder.New().WithDefaults().WithYAML().Validate().Build(); err != nil {
		t.Errorf("an absent configuration file must not fail the boot, got %v", err)
	}
}

func TestExplicitFileMustExist(t *testing.T) {
	t.Setenv(configbuilder.ConfigPathEnvVar, "/nonexistent/aegis.yaml")

	_, err := configbuilder.New().WithDefaults().WithYAML().Validate().Build()
	if err == nil {
		t.Fatal("a path given explicitly must fail when it does not exist")
	}

	if !strings.Contains(err.Error(), configbuilder.ConfigPathEnvVar) {
		t.Errorf("the error should name the variable, got %v", err)
	}
}

func TestUnknownKeyIsRejected(t *testing.T) {
	writeConfig(t, `
http_server:
  prot: "9000"
`)

	_, err := configbuilder.New().WithDefaults().WithYAML().Validate().Build()
	if err == nil {
		t.Fatal("a misspelled key must fail rather than silently do nothing")
	}

	if !strings.Contains(err.Error(), "prot") {
		t.Errorf("the error should name the offending key, got %v", err)
	}
}

func TestInvalidDurationIsReported(t *testing.T) {
	writeConfig(t, `
graceful:
  timeout: 20
`)

	_, err := configbuilder.New().WithDefaults().WithYAML().Validate().Build()
	if err == nil {
		t.Fatal("a unitless duration must be rejected")
	}

	// Accepting a bare number would silently mean nanoseconds.
	if !strings.Contains(err.Error(), "time.Duration") {
		t.Errorf("the error should point at the duration, got %v", err)
	}
}

// port is typed as a string, and an unquoted number is coerced into one rather
// than ignored. Worth pinning down: it means a file may write either form.
func TestUnquotedPortIsAccepted(t *testing.T) {
	writeConfig(t, `
http_server:
  port: 9000
`)

	cfg, err := configbuilder.New().WithDefaults().WithYAML().Validate().Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.HttpServer.Port != "9000" {
		t.Errorf("want port 9000, got %q", cfg.HttpServer.Port)
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	t.Setenv("HTTP_SERVER_PORT", "not-a-port")
	t.Setenv("LOGGING_LEVEL", "banana")
	t.Setenv("HTTP_SERVER_READ_TIMEOUT", "0s")

	_, err := configbuilder.New().WithDefaults().WithEnv().Validate().Build()
	if err == nil {
		t.Fatal("an invalid configuration must fail")
	}

	// A boot reporting one problem per run costs one restart per setting.
	for _, want := range []string{"invalid port", "unsupported level", "read timeout must be greater than zero"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

// An empty variable is treated as absent, so a default cannot be cleared by
// exporting it empty.
func TestEmptyVariableFallsBackToDefault(t *testing.T) {
	t.Setenv("HTTP_SERVER_HOST", "")

	cfg, err := configbuilder.New().WithDefaults().WithEnv().Validate().Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.HttpServer.Host != "0.0.0.0" {
		t.Errorf("want the default host, got %q", cfg.HttpServer.Host)
	}
}
