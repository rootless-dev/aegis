package configbuilder

import (
	"strings"
	"time"

	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/infra/envtools"
)

// applyEnv writes the environment over cfg. One function per section, in the
// order the sections appear in the file, so a new setting has an obvious place
// to go.
func applyEnv(cfg *configs.Application) {
	fromEnv(&cfg.AppName, "APP_NAME")
	typedFromEnv(&cfg.Profile, ProfileEnvVar)
	fromEnv(&cfg.PublicURL, "PUBLIC_URL")

	applyLogging(cfg.Logging)
	applyHttpServer(cfg.HttpServer)
	applyTLS(cfg.TLS)
	applyProxy(cfg.Proxy)
	applyHSTS(cfg.HSTS)
	applyCSP(cfg.CSP)
	applyGraceful(cfg.Graceful)
	applyHealth(cfg.Health)
	applyBanner(cfg.Banner)
	applyDatabase(cfg.Database)
}

// The nil checks are not defensive noise: a document writing `logging:` with no
// body decodes the section as nil, and reading into it would panic on a
// configuration file that is merely odd.

func applyLogging(cfg *configs.Logging) {
	if cfg == nil {
		return
	}

	fromEnv(&cfg.Level, "LOGGING_LEVEL")
	fromEnv(&cfg.Caller, "LOGGING_CALLER_LEVEL")
	fromEnv(&cfg.TimeField, "LOGGING_TIME_FIELD")
	fromEnv(&cfg.TimeFormat, "LOGGING_TIME_FORMAT")
	fromEnv(&cfg.PrettyEnabled, "LOGGING_PRETTY_ENABLED")
}

func applyHttpServer(cfg *configs.HttpServer) {
	if cfg == nil {
		return
	}

	fromEnv(&cfg.Host, "HTTP_SERVER_HOST")
	fromEnv(&cfg.Port, "HTTP_SERVER_PORT")
	fromEnv(&cfg.MaxHeaderBytes, "HTTP_SERVER_MAX_HEADER_BYTES")
	durationFromEnv(&cfg.ReadHeaderTimeout, "HTTP_SERVER_READ_HEADER_TIMEOUT")
	durationFromEnv(&cfg.ReadTimeout, "HTTP_SERVER_READ_TIMEOUT")
	durationFromEnv(&cfg.WriteTimeout, "HTTP_SERVER_WRITE_TIMEOUT")
	durationFromEnv(&cfg.IdleTimeout, "HTTP_SERVER_IDLE_TIMEOUT")
	durationFromEnv(&cfg.RequestTimeout, "HTTP_SERVER_REQUEST_TIMEOUT")
}

func applyTLS(cfg *configs.TLS) {
	if cfg == nil {
		return
	}

	typedFromEnv(&cfg.Termination, "TLS_TERMINATION")
	fromEnv(&cfg.CertFile, "TLS_CERT_FILE")
	fromEnv(&cfg.KeyFile, "TLS_KEY_FILE")
	durationFromEnv(&cfg.ReloadInterval, "TLS_RELOAD_INTERVAL")
}

func applyProxy(cfg *configs.Proxy) {
	if cfg == nil {
		return
	}

	listFromEnv(&cfg.TrustedProxies, "PROXY_TRUSTED_PROXIES")
	typedFromEnv(&cfg.Headers, "PROXY_HEADERS")
}

func applyHSTS(cfg *configs.HSTS) {
	if cfg == nil {
		return
	}

	fromEnv(&cfg.Enabled, "HSTS_ENABLED")
	fromEnv(&cfg.IncludeSubdomains, "HSTS_INCLUDE_SUBDOMAINS")
	durationFromEnv(&cfg.MaxAge, "HSTS_MAX_AGE")
}

func applyCSP(cfg *configs.CSP) {
	if cfg == nil {
		return
	}

	fromEnv(&cfg.Enabled, "CSP_ENABLED")
}

func applyGraceful(cfg *configs.Graceful) {
	if cfg == nil {
		return
	}

	durationFromEnv(&cfg.Timeout, "GRACEFUL_SHUTDOWN_TIMEOUT")
}

func applyHealth(cfg *configs.Health) {
	if cfg == nil {
		return
	}

	durationFromEnv(&cfg.CheckTimeout, "HEALTH_CHECK_TIMEOUT")
	durationFromEnv(&cfg.DrainDelay, "HEALTH_DRAIN_DELAY")
}

func applyBanner(cfg *configs.Banner) {
	if cfg == nil {
		return
	}

	fromEnv(&cfg.Enabled, "BANNER_ENABLED")
}

func applyDatabase(cfg *configs.Database) {
	if cfg == nil {
		return
	}

	typedFromEnv(&cfg.Driver, "DATABASE_DRIVER")
	fromEnv(&cfg.Host, "DATABASE_HOST")
	fromEnv(&cfg.Port, "DATABASE_PORT")
	fromEnv(&cfg.Name, "DATABASE_NAME")
	fromEnv(&cfg.User, "DATABASE_USER")
	fromEnv(&cfg.Password, "DATABASE_PASSWORD")
	fromEnv(&cfg.Path, "DATABASE_PATH")
	fromEnv(&cfg.SSLMode, "DATABASE_SSL_MODE")
	fromEnv(&cfg.SSLRootCert, "DATABASE_SSL_ROOT_CERT")
	durationFromEnv(&cfg.ConnectTimeout, "DATABASE_CONNECT_TIMEOUT")

	applyDatabasePool(cfg.Pool)
	applyDatabaseMigrate(cfg.Migrate)
}

func applyDatabaseMigrate(cfg *configs.Migrate) {
	if cfg == nil {
		return
	}

	fromEnv(&cfg.OnBoot, "DATABASE_MIGRATE_ON_BOOT")
	durationFromEnv(&cfg.Timeout, "DATABASE_MIGRATE_TIMEOUT")
	durationFromEnv(&cfg.LockTimeout, "DATABASE_MIGRATE_LOCK_TIMEOUT")
}

func applyDatabasePool(cfg *configs.Pool) {
	if cfg == nil {
		return
	}

	fromEnv(&cfg.MaxOpen, "DATABASE_POOL_MAX_OPEN")
	fromEnv(&cfg.MaxIdle, "DATABASE_POOL_MAX_IDLE")
	durationFromEnv(&cfg.ConnMaxLifetime, "DATABASE_POOL_CONN_MAX_LIFETIME")
	durationFromEnv(&cfg.ConnMaxIdleTime, "DATABASE_POOL_CONN_MAX_IDLE_TIME")
}

func fromEnv[T envtools.AllowedEnvTypes](target *T, key string) {
	if value, ok := envtools.Lookup[T](key); ok {
		*target = value
	}
}

// typedFromEnv fills a named string type — a profile, a termination — which the
// type set of envtools cannot name on its own.
func typedFromEnv[T ~string](target *T, key string) {
	if value, ok := envtools.Lookup[string](key); ok {
		*target = T(value)
	}
}

func durationFromEnv(target *time.Duration, key string) {
	if value, ok := envtools.LookupDuration(key); ok {
		*target = value
	}
}

// listFromEnv reads a comma separated list. An entry that is only whitespace is
// dropped, so a trailing comma does not become an empty item that fails
// validation for no reason.
func listFromEnv(target *[]string, key string) {
	raw, ok := envtools.Lookup[string](key)
	if !ok {
		return
	}

	entries := strings.Split(raw, ",")
	list := make([]string, 0, len(entries))

	for _, entry := range entries {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			list = append(list, trimmed)
		}
	}

	*target = list
}
