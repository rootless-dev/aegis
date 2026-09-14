package configbuilder

import (
	"errors"
	"strings"
	"time"

	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/infra/envtools"
)

// applyEnv writes the environment over cfg. One function per section, in the
// order the sections appear in the file, so a new setting has an obvious place
// to go. It only returns an error because a secret can arrive as a file path,
// and reading a file fails.
func applyEnv(cfg *configs.Application) error {
	fromEnv(&cfg.AppName, "AEGIS_APP_NAME")
	typedFromEnv(&cfg.Profile, ProfileEnvVar)
	fromEnv(&cfg.PublicURL, "AEGIS_PUBLIC_URL")

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

	return applyCrypto(cfg.Crypto)
}

func applyCrypto(cfg *configs.Crypto) error {
	if cfg == nil {
		return nil
	}

	var errs []error

	errs = append(errs, secretFromEnv(&cfg.MasterKey, "AEGIS_CRYPTO_MASTER_KEY"))
	errs = append(errs, secretFromEnv(&cfg.MasterKeyPrevious, "AEGIS_CRYPTO_MASTER_KEY_PREVIOUS"))

	return errors.Join(errs...)
}

// The nil checks are not defensive noise: a document writing `logging:` with no
// body decodes the section as nil, and reading into it would panic on a
// configuration file that is merely odd.

func applyLogging(cfg *configs.Logging) {
	if cfg == nil {
		return
	}

	fromEnv(&cfg.Level, "AEGIS_LOGGING_LEVEL")
	fromEnv(&cfg.Caller, "AEGIS_LOGGING_CALLER_LEVEL")
	fromEnv(&cfg.TimeField, "AEGIS_LOGGING_TIME_FIELD")
	fromEnv(&cfg.TimeFormat, "AEGIS_LOGGING_TIME_FORMAT")
	fromEnv(&cfg.PrettyEnabled, "AEGIS_LOGGING_PRETTY_ENABLED")
}

func applyHttpServer(cfg *configs.HttpServer) {
	if cfg == nil {
		return
	}

	fromEnv(&cfg.Host, "AEGIS_HTTP_SERVER_HOST")
	fromEnv(&cfg.Port, "AEGIS_HTTP_SERVER_PORT")
	fromEnv(&cfg.MaxHeaderBytes, "AEGIS_HTTP_SERVER_MAX_HEADER_BYTES")
	durationFromEnv(&cfg.ReadHeaderTimeout, "AEGIS_HTTP_SERVER_READ_HEADER_TIMEOUT")
	durationFromEnv(&cfg.ReadTimeout, "AEGIS_HTTP_SERVER_READ_TIMEOUT")
	durationFromEnv(&cfg.WriteTimeout, "AEGIS_HTTP_SERVER_WRITE_TIMEOUT")
	durationFromEnv(&cfg.IdleTimeout, "AEGIS_HTTP_SERVER_IDLE_TIMEOUT")
	durationFromEnv(&cfg.RequestTimeout, "AEGIS_HTTP_SERVER_REQUEST_TIMEOUT")
}

func applyTLS(cfg *configs.TLS) {
	if cfg == nil {
		return
	}

	typedFromEnv(&cfg.Termination, "AEGIS_TLS_TERMINATION")
	fromEnv(&cfg.CertFile, "AEGIS_TLS_CERT_FILE")
	fromEnv(&cfg.KeyFile, "AEGIS_TLS_KEY_FILE")
	durationFromEnv(&cfg.ReloadInterval, "AEGIS_TLS_RELOAD_INTERVAL")
}

func applyProxy(cfg *configs.Proxy) {
	if cfg == nil {
		return
	}

	listFromEnv(&cfg.TrustedProxies, "AEGIS_PROXY_TRUSTED_PROXIES")
	typedFromEnv(&cfg.Headers, "AEGIS_PROXY_HEADERS")
}

func applyHSTS(cfg *configs.HSTS) {
	if cfg == nil {
		return
	}

	fromEnv(&cfg.Enabled, "AEGIS_HSTS_ENABLED")
	fromEnv(&cfg.IncludeSubdomains, "AEGIS_HSTS_INCLUDE_SUBDOMAINS")
	durationFromEnv(&cfg.MaxAge, "AEGIS_HSTS_MAX_AGE")
}

func applyCSP(cfg *configs.CSP) {
	if cfg == nil {
		return
	}

	fromEnv(&cfg.Enabled, "AEGIS_CSP_ENABLED")
}

func applyGraceful(cfg *configs.Graceful) {
	if cfg == nil {
		return
	}

	durationFromEnv(&cfg.Timeout, "AEGIS_GRACEFUL_SHUTDOWN_TIMEOUT")
}

func applyHealth(cfg *configs.Health) {
	if cfg == nil {
		return
	}

	durationFromEnv(&cfg.CheckTimeout, "AEGIS_HEALTH_CHECK_TIMEOUT")
	durationFromEnv(&cfg.DrainDelay, "AEGIS_HEALTH_DRAIN_DELAY")
}

func applyBanner(cfg *configs.Banner) {
	if cfg == nil {
		return
	}

	fromEnv(&cfg.Enabled, "AEGIS_BANNER_ENABLED")
}

func applyDatabase(cfg *configs.Database) {
	if cfg == nil {
		return
	}

	typedFromEnv(&cfg.Driver, "AEGIS_DATABASE_DRIVER")
	fromEnv(&cfg.Host, "AEGIS_DATABASE_HOST")
	fromEnv(&cfg.Port, "AEGIS_DATABASE_PORT")
	fromEnv(&cfg.Name, "AEGIS_DATABASE_NAME")
	fromEnv(&cfg.User, "AEGIS_DATABASE_USER")
	fromEnv(&cfg.Password, "AEGIS_DATABASE_PASSWORD")
	fromEnv(&cfg.Path, "AEGIS_DATABASE_PATH")
	fromEnv(&cfg.SSLMode, "AEGIS_DATABASE_SSL_MODE")
	fromEnv(&cfg.SSLRootCert, "AEGIS_DATABASE_SSL_ROOT_CERT")
	durationFromEnv(&cfg.ConnectTimeout, "AEGIS_DATABASE_CONNECT_TIMEOUT")

	applyDatabasePool(cfg.Pool)
	applyDatabaseMigrate(cfg.Migrate)
}

func applyDatabaseMigrate(cfg *configs.Migrate) {
	if cfg == nil {
		return
	}

	fromEnv(&cfg.OnBoot, "AEGIS_DATABASE_MIGRATE_ON_BOOT")
	durationFromEnv(&cfg.Timeout, "AEGIS_DATABASE_MIGRATE_TIMEOUT")
	durationFromEnv(&cfg.LockTimeout, "AEGIS_DATABASE_MIGRATE_LOCK_TIMEOUT")
}

func applyDatabasePool(cfg *configs.Pool) {
	if cfg == nil {
		return
	}

	fromEnv(&cfg.MaxOpen, "AEGIS_DATABASE_POOL_MAX_OPEN")
	fromEnv(&cfg.MaxIdle, "AEGIS_DATABASE_POOL_MAX_IDLE")
	durationFromEnv(&cfg.ConnMaxLifetime, "AEGIS_DATABASE_POOL_CONN_MAX_LIFETIME")
	durationFromEnv(&cfg.ConnMaxIdleTime, "AEGIS_DATABASE_POOL_CONN_MAX_IDLE_TIME")
}

func fromEnv[T envtools.AllowedEnvTypes](target *T, key string) {
	if value, ok := envtools.Lookup[T](key); ok {
		*target = value
	}
}

func secretFromEnv(target *string, key string) error {
	value, ok, err := envtools.LookupSecret(key)
	if err != nil {
		return err
	}

	if ok {
		*target = value
	}

	return nil
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
