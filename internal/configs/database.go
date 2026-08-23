package configs

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Driver string

const (
	DriverPostgres Driver = "postgres"
	DriverMySQL    Driver = "mysql"
	DriverMariaDB  Driver = "mariadb"
	DriverSQLite   Driver = "sqlite"
)

// A file rather than ":memory:" so a schema survives the restarts air performs
// on every save.
const developmentDatabasePath = "./aegis.dev.db"

var supportedDrivers = []Driver{DriverPostgres, DriverMySQL, DriverMariaDB, DriverSQLite}

// supportedSSLModes is Postgres' vocabulary, used for every engine so the
// setting means the same thing everywhere. Each dialect translates it.
var supportedSSLModes = []string{"disable", "prefer", "require", "verify-ca", "verify-full"}

// forcedOptionKeys are the dsn parameters the dialects set themselves,
// rejected here rather than silently overwritten so an operator who sets one
// learns why. Matched case-insensitively, like the stripping layer in
// internal/infra/database. SQLite is absent: its forced values are pragma
// names inside one shared key, which validateSQLitePragma handles instead.
var forcedOptionKeys = map[Driver][]string{
	DriverPostgres: {"sslmode", "sslrootcert", "timezone", "connect_timeout"},
	DriverMySQL:    {"parsetime", "loc", "charset", "sql_mode", "time_zone", "tls", "multistatements", "timeout"},
	DriverMariaDB:  {"parsetime", "loc", "charset", "sql_mode", "time_zone", "tls", "multistatements", "timeout"},
}

// sqliteForcedPragmaNames mirrors internal/infra/database's
// sqliteForcedPragmas, duplicated because neither package may import the other.
var sqliteForcedPragmaNames = map[string]bool{
	"foreign_keys": true,
	"busy_timeout": true,
	"journal_mode": true,
}

func (d Driver) String() string {
	return string(d)
}

// IsFileBased splits the section in two: a host, a user and TLS mean nothing
// for a local file, and a path means nothing for the others.
func (d Driver) IsFileBased() bool {
	return d == DriverSQLite
}

type Database struct {
	Driver Driver `yaml:"driver"`

	Host string `yaml:"host"`
	Port string `yaml:"port"`
	Name string `yaml:"name"`
	User string `yaml:"user"`

	// Not decodable from the file: KnownFields(true) fails the boot on it, so
	// DATABASE_PASSWORD is the only route in. A secret that can live in a
	// configuration file eventually gets committed.
	Password string `yaml:"-"`

	// Path is the sqlite file, and belongs to no other driver.
	Path string `yaml:"path"`

	SSLMode     string `yaml:"ssl_mode"`
	SSLRootCert string `yaml:"ssl_root_cert"`

	// Options carries what is specific to the installation. Anything aegis
	// depends on is forced by the dialect and rejected here.
	Options map[string]string `yaml:"options"`

	ConnectTimeout time.Duration `yaml:"connect_timeout"`

	Pool *Pool `yaml:"pool"`
}

type Pool struct {
	MaxOpen int `yaml:"max_open"`

	// Matches MaxOpen by default: a smaller idle count reopens connections
	// under load, and every reopen pays a TLS handshake.
	MaxIdle int `yaml:"max_idle"`

	// Stays under the NAT and firewall idle timeouts that drop connections
	// around an hour. Without it the symptom is a sporadic "invalid
	// connection" with no visible cause.
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`

	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"`
}

func defaultDatabase() *Database {
	return &Database{
		// Empty like the TLS termination: only the operator knows which engine
		// they run, and the boot fails until they say so.
		Driver:         "",
		ConnectTimeout: 10 * time.Second,
		Pool:           defaultPool(),
	}
}

func defaultPool() *Pool {
	return &Pool{
		MaxOpen:         25,
		MaxIdle:         25,
		ConnMaxLifetime: 30 * time.Minute,
		ConnMaxIdleTime: 5 * time.Minute,
	}
}

// normalizeForDevelopment fills in the engine that needs no server and the
// file it lives in.
func (cfg *Database) normalizeForDevelopment() {
	if cfg.Driver == "" {
		cfg.Driver = DriverSQLite
	}

	if cfg.Driver.IsFileBased() && cfg.Path == "" {
		cfg.Path = developmentDatabasePath
	}
}

func (cfg *Database) Validate(profile Profile) error {
	var errs []error

	errs = append(errs, cfg.validateDriver(profile))

	switch {
	case cfg.Driver.IsFileBased():
		errs = append(errs, cfg.validateFile())
	case slices.Contains(supportedDrivers, cfg.Driver):
		errs = append(errs, cfg.validateServer(profile))
	}

	errs = append(errs, cfg.validateOptions())

	if cfg.ConnectTimeout <= 0 {
		errs = append(errs, fmt.Errorf("database: connect timeout must be greater than zero, got %s", cfg.ConnectTimeout))
	}

	if cfg.Pool == nil {
		errs = append(errs, errors.New("database: pool configuration is missing"))
	} else {
		errs = append(errs, cfg.Pool.Validate())
	}

	return errors.Join(errs...)
}

func (cfg *Database) validateDriver(profile Profile) error {
	switch {
	case cfg.Driver == "":
		return fmt.Errorf(
			"database: driver is required: declare %q, %q or %q, or run with --dev for a local run on %q",
			DriverPostgres, DriverMySQL, DriverMariaDB, DriverSQLite,
		)
	case !slices.Contains(supportedDrivers, cfg.Driver):
		return fmt.Errorf("database: unsupported driver %q, want one of %v", cfg.Driver, supportedDrivers)
	case cfg.Driver.IsFileBased() && !profile.IsDev():
		return fmt.Errorf(
			"database: %q is a development engine and cannot run under the %q profile",
			cfg.Driver, ProfileProd,
		)
	}

	return nil
}

func (cfg *Database) validateServer(profile Profile) error {
	var errs []error

	if cfg.Host == "" {
		errs = append(errs, errors.New("database: host is empty"))
	}

	if cfg.Name == "" {
		errs = append(errs, errors.New("database: name is empty"))
	}

	if cfg.User == "" {
		errs = append(errs, errors.New("database: user is empty"))
	}

	if cfg.Path != "" {
		errs = append(errs, fmt.Errorf("database: driver %q connects to a server, so path cannot be set", cfg.Driver))
	}

	// An empty port is not an error: the dialect knows the engine's default.
	if cfg.Port != "" {
		port, err := strconv.Atoi(cfg.Port)
		if err != nil || port < 1 || port > 65535 {
			errs = append(errs, fmt.Errorf("database: invalid port %q", cfg.Port))
		}
	}

	errs = append(errs, cfg.validateSSL(profile))

	return errors.Join(errs...)
}

func (cfg *Database) validateSSL(profile Profile) error {
	var errs []error

	if cfg.SSLMode != "" && !slices.Contains(supportedSSLModes, cfg.SSLMode) {
		errs = append(errs, fmt.Errorf(
			"database: unsupported ssl mode %q, want one of %v", cfg.SSLMode, supportedSSLModes,
		))
	}

	// Unset means "prefer" once translated, and prefer drops back to plaintext
	// without saying so. "disable" stays a legitimate answer; what is refused
	// is leaving it unsaid.
	if cfg.SSLMode == "" && !profile.IsDev() {
		errs = append(errs, fmt.Errorf(
			"database: ssl_mode is required under the %q profile: declare one of %v, "+
				"or %q if the connection to the database is already private",
			ProfileProd, supportedSSLModes, "disable",
		))
	}

	// A CA nobody checks is a CA that does nothing.
	if cfg.SSLRootCert != "" && !strings.HasPrefix(cfg.SSLMode, "verify-") {
		errs = append(errs, fmt.Errorf(
			"database: ssl_root_cert is only verified by ssl_mode %q or %q, got %q",
			"verify-ca", "verify-full", cfg.SSLMode,
		))
	}

	return errors.Join(errs...)
}

func (cfg *Database) validateFile() error {
	var errs []error

	if cfg.Path == "" {
		errs = append(errs, fmt.Errorf("database: path is required for the %q driver", cfg.Driver))
	}

	// Ordered rather than a map, so the errors do not shuffle between runs.
	serverOnly := []struct {
		name  string
		value string
	}{
		{"host", cfg.Host},
		{"port", cfg.Port},
		{"name", cfg.Name},
		{"user", cfg.User},
		{"password", cfg.Password},
		{"ssl_mode", cfg.SSLMode},
		{"ssl_root_cert", cfg.SSLRootCert},
	}

	for _, field := range serverOnly {
		if field.value != "" {
			errs = append(errs, fmt.Errorf(
				"database: driver %q is file based, so %s cannot be set", cfg.Driver, field.name,
			))
		}
	}

	return errors.Join(errs...)
}

func (cfg *Database) validateOptions() error {
	if cfg.Driver == DriverSQLite {
		return cfg.validateSQLitePragma()
	}

	var errs []error

	forced := forcedOptionKeys[cfg.Driver]

	for key := range cfg.Options {
		if slices.Contains(forced, strings.ToLower(key)) {
			errs = append(errs, fmt.Errorf(
				"database: option %q is set by aegis and cannot be overridden", key,
			))
		}
	}

	return errors.Join(errs...)
}

// validateSQLitePragma rejects the pragma name rather than the "_pragma" key:
// every sqlite pragma, forced or benign, is carried as that one key's value.
func (cfg *Database) validateSQLitePragma() error {
	pragma, ok := cfg.Options["_pragma"]
	if !ok {
		return nil
	}

	name := strings.ToLower(sqlitePragmaName(pragma))
	if sqliteForcedPragmaNames[name] {
		return fmt.Errorf("database: pragma %q is set by aegis and cannot be overridden", name)
	}

	return nil
}

// sqlitePragmaName reads the name out of a "_pragma" value such as
// "foreign_keys(1)". Duplicated like sqliteForcedPragmaNames.
func sqlitePragmaName(pragma string) string {
	if idx := strings.IndexAny(pragma, "(= "); idx >= 0 {
		return pragma[:idx]
	}

	return pragma
}

func (cfg *Pool) Validate() error {
	var errs []error

	if cfg.MaxOpen <= 0 {
		errs = append(errs, fmt.Errorf("database: pool max open must be greater than zero, got %d", cfg.MaxOpen))
	}

	if cfg.MaxIdle <= 0 {
		errs = append(errs, fmt.Errorf("database: pool max idle must be greater than zero, got %d", cfg.MaxIdle))
	}

	// A higher idle limit than open can never be reached.
	if cfg.MaxIdle > cfg.MaxOpen {
		errs = append(errs, fmt.Errorf(
			"database: pool max idle (%d) cannot exceed max open (%d)", cfg.MaxIdle, cfg.MaxOpen,
		))
	}

	if cfg.ConnMaxLifetime <= 0 {
		errs = append(errs, fmt.Errorf("database: pool connection max lifetime must be greater than zero, got %s", cfg.ConnMaxLifetime))
	}

	if cfg.ConnMaxIdleTime <= 0 {
		errs = append(errs, fmt.Errorf("database: pool connection max idle time must be greater than zero, got %s", cfg.ConnMaxIdleTime))
	}

	return errors.Join(errs...)
}
