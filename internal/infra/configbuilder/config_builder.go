package configbuilder

import (
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/infra/envtools"
)

// DefaultConfigPaths are searched in order when no explicit path is given. A
// missing file is not an error: outside development the orchestrator injects
// the settings through the environment instead.
var DefaultConfigPaths = []string{
	"aegis.yaml",
	"/etc/aegis/aegis.yaml",
}

// ConfigPathEnvVar names the variable that overrides the search entirely. When
// it is set, the file has to exist: an explicit path that silently does
// nothing is worse than a failed boot.
const ConfigPathEnvVar = "AEGIS_CONFIG_FILE"

var ErrConfigInstanceNotInitialized = errors.New("configbuilder: no source was loaded, call WithDefaults before validating or building")

type ConfigBuilder struct {
	cfg *configs.Application
	err error
}

func New() *ConfigBuilder {
	return &ConfigBuilder{}
}

// WithDefaults installs the base layer. Every other source writes over it.
func (b *ConfigBuilder) WithDefaults() *ConfigBuilder {
	b.cfg = configs.Default()

	return b
}

// WithYAML applies a configuration file over whatever is loaded, overwriting
// only the keys the file declares.
func (b *ConfigBuilder) WithYAML() *ConfigBuilder {
	if b.cfg == nil {
		b.err = errors.Join(b.err, ErrConfigInstanceNotInitialized)

		return b
	}

	if path, ok := envtools.Lookup[string](ConfigPathEnvVar); ok {
		if err := loadYAML(path, b.cfg); err != nil {
			b.err = errors.Join(b.err, fmt.Errorf("configbuilder: %s=%q: %w", ConfigPathEnvVar, path, err))
		}

		return b
	}

	for _, path := range DefaultConfigPaths {
		err := loadYAML(path, b.cfg)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			b.err = errors.Join(b.err, fmt.Errorf("configbuilder: %w", err))
		}

		// The first file found wins; the remaining paths are fallbacks, not
		// layers to merge.
		return b
	}

	return b
}

// WithEnv applies the environment over whatever is loaded. It comes last
// because the file is what ships with the image, while a variable is how a
// single instance is adjusted without rebuilding anything.
func (b *ConfigBuilder) WithEnv() *ConfigBuilder {
	if b.cfg == nil {
		b.err = errors.Join(b.err, ErrConfigInstanceNotInitialized)

		return b
	}

	fromEnv(&b.cfg.AppName, "APP_NAME")

	fromEnv(&b.cfg.Logging.Level, "LOGGING_LEVEL")
	fromEnv(&b.cfg.Logging.Caller, "LOGGING_CALLER_LEVEL")
	fromEnv(&b.cfg.Logging.TimeField, "LOGGING_TIME_FIELD")
	fromEnv(&b.cfg.Logging.TimeFormat, "LOGGING_TIME_FORMAT")
	fromEnv(&b.cfg.Logging.PrettyEnabled, "LOGGING_PRETTY_ENABLED")

	fromEnv(&b.cfg.HttpServer.Host, "HTTP_SERVER_HOST")
	fromEnv(&b.cfg.HttpServer.Port, "HTTP_SERVER_PORT")
	fromEnv(&b.cfg.HttpServer.MaxHeaderBytes, "HTTP_SERVER_MAX_HEADER_BYTES")
	durationFromEnv(&b.cfg.HttpServer.ReadHeaderTimeout, "HTTP_SERVER_READ_HEADER_TIMEOUT")
	durationFromEnv(&b.cfg.HttpServer.ReadTimeout, "HTTP_SERVER_READ_TIMEOUT")
	durationFromEnv(&b.cfg.HttpServer.WriteTimeout, "HTTP_SERVER_WRITE_TIMEOUT")
	durationFromEnv(&b.cfg.HttpServer.IdleTimeout, "HTTP_SERVER_IDLE_TIMEOUT")
	durationFromEnv(&b.cfg.HttpServer.RequestTimeout, "HTTP_SERVER_REQUEST_TIMEOUT")

	durationFromEnv(&b.cfg.Graceful.Timeout, "GRACEFUL_SHUTDOWN_TIMEOUT")

	durationFromEnv(&b.cfg.Health.CheckTimeout, "HEALTH_CHECK_TIMEOUT")
	durationFromEnv(&b.cfg.Health.DrainDelay, "HEALTH_DRAIN_DELAY")

	fromEnv(&b.cfg.Banner.Enabled, "BANNER_ENABLED")

	return b
}

func (b *ConfigBuilder) Validate() *ConfigBuilder {
	if b.cfg == nil {
		b.err = errors.Join(b.err, ErrConfigInstanceNotInitialized)

		return b
	}

	b.err = errors.Join(b.err, b.cfg.Validate())

	return b
}

// Build reports every problem found along the chain at once, so a misconfigured
// boot does not have to be fixed one setting per run.
func (b *ConfigBuilder) Build() (*configs.Application, error) {
	if b.err != nil {
		return nil, b.err
	}

	if b.cfg == nil {
		return nil, ErrConfigInstanceNotInitialized
	}

	return b.cfg, nil
}

func fromEnv[T envtools.AllowedEnvTypes](target *T, key string) {
	if value, ok := envtools.Lookup[T](key); ok {
		*target = value
	}
}

func durationFromEnv(target *time.Duration, key string) {
	if value, ok := envtools.LookupDuration(key); ok {
		*target = value
	}
}
