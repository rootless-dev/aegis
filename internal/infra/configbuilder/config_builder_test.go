package configbuilder_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/infra/configbuilder"
)

// settingPrefixes covers every variable the builder reads.
var settingPrefixes = []string{
	"AEGIS_", "APP_", "PUBLIC_URL", "LOGGING_", "HTTP_SERVER_",
	"TLS_", "PROXY_", "HSTS_", "GRACEFUL_", "HEALTH_", "BANNER_", "DATABASE_",
}

// TestMain runs these tests against an empty environment. The builder reads the
// real one, so a developer shell carrying the project .env — which is how the
// service is run locally — would otherwise decide what the tests see, and they
// would fail on that machine alone.
func TestMain(m *testing.M) {
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")

		for _, prefix := range settingPrefixes {
			if strings.HasPrefix(key, prefix) {
				os.Unsetenv(key)

				break
			}
		}
	}

	os.Exit(m.Run())
}

// writeConfig puts a configuration file on disk and points the builder at it.
func writeConfig(t *testing.T, content string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "aegis.yaml")

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing the configuration file: %v", err)
	}

	t.Setenv(configbuilder.ConfigPathEnvVar, path)
}

// development is what tests that are not about the topology use to reach a
// valid configuration: declaring how TLS is terminated is a production
// requirement, and repeating it everywhere would bury what each test covers.
func development() []string {
	return []string{"--dev"}
}

// productionDatabase is what tests that are not about the database use to
// reach a valid one under the production profile: a driver is required, and
// repeating a full section inline would bury what each test covers. Env vars
// are not an option here — DATABASE_* is not read until Task 2 — so this
// goes through the file source instead, the only other one already wired for
// every field this struct declares.
const productionDatabase = `
database:
  driver: postgres
  host: db.internal
  name: aegis
  user: aegis
  ssl_mode: require
`

func TestBuildWithoutDefaultsFails(t *testing.T) {
	_, err := configbuilder.New().WithEnv().Validate().Build()

	if !errors.Is(err, configbuilder.ErrConfigInstanceNotInitialized) {
		t.Fatalf("want ErrConfigInstanceNotInitialized, got %v", err)
	}
}

// The defaults deliberately stop short of a bootable configuration: whether a
// gateway terminates TLS is something only the operator knows, and the two
// answers are indistinguishable from the outside.
func TestDefaultsAloneRefuseToDecideTheTopology(t *testing.T) {
	_, err := configbuilder.New().WithDefaults().Normalize().Validate().Build()
	if err == nil {
		t.Fatal("a production boot must not inherit a guess about who terminates TLS")
	}

	for _, want := range []string{"termination is required", "public url is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}

	// Refusing is only half of it: the way out has to be in the message, or the
	// first contact with the binary is a dead end.
	if !strings.Contains(err.Error(), "--dev") {
		t.Errorf("the error should point at the development profile, got: %v", err)
	}
}

func TestDefaultsUnderDevelopmentProduceAValidConfiguration(t *testing.T) {
	cfg, err := configbuilder.New().WithDefaults().WithFlags(development()).Normalize().Validate().Build()
	if err != nil {
		t.Fatalf("the development profile must be valid on its own, got %v", err)
	}

	if cfg.HttpServer.Address() != "0.0.0.0:7500" {
		t.Errorf("want 0.0.0.0:7500, got %s", cfg.HttpServer.Address())
	}

	// Development serves TLS itself, from a certificate it generates, so the
	// path exercised locally is the same one production takes.
	if cfg.TLS.Termination != configs.TerminationApp {
		t.Errorf("termination: want app, got %q", cfg.TLS.Termination)
	}

	if !cfg.TLS.GeneratesCertificate(cfg.Profile) {
		t.Error("development with no certificate configured should generate one")
	}

	// The wildcard bind is where it listens, not somewhere a browser can go.
	if cfg.PublicURL != "https://localhost:7500" {
		t.Errorf("public url: want https://localhost:7500, got %q", cfg.PublicURL)
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

	cfg, err := configbuilder.New().WithDefaults().WithYAML().WithFlags(development()).Normalize().Validate().Build()
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

	cfg, err := configbuilder.New().WithDefaults().WithYAML().WithEnv().WithFlags(development()).Normalize().Validate().Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The environment beats the file: it adjusts one instance without
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
	builder := configbuilder.New().WithDefaults().WithYAML().WithFlags(development()).Normalize()
	if _, err := builder.Validate().Build(); err != nil {
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

	cfg, err := configbuilder.New().WithDefaults().WithYAML().WithFlags(development()).Normalize().Validate().Build()
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

	cfg, err := configbuilder.New().WithDefaults().WithEnv().WithFlags(development()).Normalize().Validate().Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.HttpServer.Host != "0.0.0.0" {
		t.Errorf("want the default host, got %q", cfg.HttpServer.Host)
	}
}

func TestProfileComesFromTheEnvironment(t *testing.T) {
	t.Setenv(configbuilder.ProfileEnvVar, "DEV")

	cfg, err := configbuilder.New().WithDefaults().WithEnv().Normalize().Validate().Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Case folded, so a variable and a file that disagree only in spelling still
	// select the same profile.
	if cfg.Profile != configs.ProfileDev {
		t.Errorf("profile: want dev, got %q", cfg.Profile)
	}
}

// The flag is the most specific source there is: whoever typed it is looking at
// the process right now.
func TestFlagWinsOverTheEnvironment(t *testing.T) {
	writeConfig(t, productionDatabase)

	t.Setenv(configbuilder.ProfileEnvVar, "dev")
	t.Setenv("TLS_TERMINATION", "none")
	t.Setenv("PUBLIC_URL", "http://aegis.test")

	cfg, err := configbuilder.New().
		WithDefaults().
		WithYAML().
		WithEnv().
		WithFlags([]string{"--dev=false"}).
		Normalize().
		Validate().
		Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Profile != configs.ProfileProd {
		t.Errorf("profile: want prod, got %q", cfg.Profile)
	}
}

// An unpassed flag has to stay out of the way, or it would overwrite the file
// and the environment with the zero value of a bool.
func TestAbsentFlagLeavesTheEnvironmentAlone(t *testing.T) {
	t.Setenv(configbuilder.ProfileEnvVar, "dev")

	cfg, err := configbuilder.New().
		WithDefaults().
		WithEnv().
		WithFlags(nil).
		Normalize().
		Validate().
		Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Profile != configs.ProfileDev {
		t.Errorf("profile: want dev, got %q", cfg.Profile)
	}
}

func TestTrustedProxiesAreReadAsAList(t *testing.T) {
	writeConfig(t, productionDatabase)

	t.Setenv("TLS_TERMINATION", "proxy")
	t.Setenv("PUBLIC_URL", "https://auth.example.com")
	t.Setenv("PROXY_TRUSTED_PROXIES", "10.0.0.0/8, 192.168.1.10 ,")

	cfg, err := configbuilder.New().WithDefaults().WithYAML().WithEnv().Normalize().Validate().Build()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The trailing comma leaves no empty entry behind.
	if len(cfg.Proxy.TrustedProxies) != 2 {
		t.Fatalf("want 2 trusted proxies, got %v", cfg.Proxy.TrustedProxies)
	}

	networks, err := cfg.Proxy.Networks()
	if err != nil {
		t.Fatalf("parsing the networks: %v", err)
	}

	// A bare address is a single host, not a block.
	if got := networks[1].String(); got != "192.168.1.10/32" {
		t.Errorf("want 192.168.1.10/32, got %s", got)
	}
}

// The shipped example is the first thing anyone copies, so it has to be a
// configuration this binary would actually accept.
func TestTheExampleFileIsValid(t *testing.T) {
	t.Setenv(configbuilder.ConfigPathEnvVar, filepath.Join("..", "..", "..", "aegis.example.yaml"))

	if _, err := configbuilder.New().WithDefaults().WithYAML().Normalize().Validate().Build(); err != nil {
		t.Errorf("aegis.example.yaml should be valid as shipped, got %v", err)
	}
}

// Parsing lives with the other sources, so the command line is exercised as
// text rather than as a struct somebody filled in by hand.
func TestFlagsAreParsedFromTheArguments(t *testing.T) {
	cases := map[string]struct {
		args  []string
		wants configs.Profile
	}{
		"long form":     {args: []string{"--dev"}, wants: configs.ProfileDev},
		"single dash":   {args: []string{"-dev"}, wants: configs.ProfileDev},
		"explicit true": {args: []string{"--dev=true"}, wants: configs.ProfileDev},
		// Passing it as false is a decision, and it has to beat the environment
		// exactly like passing it as true does.
		"explicit false": {args: []string{"--dev=false"}, wants: configs.ProfileProd},
		"absent":         {args: nil, wants: configs.ProfileDev},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			writeConfig(t, productionDatabase)

			// Set so "absent" proves the flag stayed out of the way rather than
			// happening to agree with the default.
			t.Setenv(configbuilder.ProfileEnvVar, "dev")
			t.Setenv("TLS_TERMINATION", "none")
			t.Setenv("PUBLIC_URL", "http://aegis.test")

			cfg, err := configbuilder.New().
				WithDefaults().
				WithYAML().
				WithEnv().
				WithFlags(tc.args).
				Normalize().
				Validate().
				Build()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if cfg.Profile != tc.wants {
				t.Errorf("profile: want %q, got %q", tc.wants, cfg.Profile)
			}
		})
	}
}

func TestDatabaseComesFromTheEnvironment(t *testing.T) {
	t.Setenv("DATABASE_DRIVER", "mysql")
	t.Setenv("DATABASE_HOST", "db.internal")
	t.Setenv("DATABASE_PORT", "3306")
	t.Setenv("DATABASE_NAME", "aegis")
	t.Setenv("DATABASE_USER", "aegis")
	t.Setenv("DATABASE_PASSWORD", "secret")
	t.Setenv("DATABASE_POOL_MAX_OPEN", "40")
	t.Setenv("DATABASE_CONNECT_TIMEOUT", "3s")

	cfg, err := configbuilder.New().
		WithDefaults().
		WithEnv().
		Build()
	if err != nil {
		t.Fatalf("building the configuration: %v", err)
	}

	if cfg.Database.Driver != configs.DriverMySQL {
		t.Fatalf("expected the driver to come from the environment, got %q", cfg.Database.Driver)
	}

	if cfg.Database.Password != "secret" {
		t.Fatal("expected the password to come from the environment")
	}

	if cfg.Database.Pool.MaxOpen != 40 {
		t.Fatalf("expected max open to come from the environment, got %d", cfg.Database.Pool.MaxOpen)
	}

	if cfg.Database.ConnectTimeout != 3*time.Second {
		t.Fatalf("expected the connect timeout to come from the environment, got %s", cfg.Database.ConnectTimeout)
	}
}

// The password must never be reachable from the configuration file, or it ends
// up committed to a repository somewhere.
func TestThePasswordCannotComeFromTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aegis.yaml")

	document := "database:\n  driver: postgres\n  password: secret\n"
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("writing the configuration file: %v", err)
	}

	t.Setenv(configbuilder.ConfigPathEnvVar, path)

	_, err := configbuilder.New().WithDefaults().WithYAML().Build()
	if err == nil {
		t.Fatal("expected a password in the file to fail the boot")
	}
}
