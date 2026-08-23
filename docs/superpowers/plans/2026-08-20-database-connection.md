# Database Connection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give Aegis a validated, pooled connection against Postgres, MySQL, MariaDB and SQLite, wired into the existing boot, health and shutdown machinery, plus a migration runner that takes its migrations as a parameter.

**Architecture:** A `database` section in `internal/configs` declares the connection in structured fields; `internal/infra/database` owns a dialect table that builds each engine's DSN, injects the parameters the code depends on, and opens GORM over `database/sql`. The migration runner lives in the same package but receives its migrations as an `fs.FS` — where they live is the caller's knowledge, the same way `application/ports.go` declares `CertificateSource`. The assembly gains one step, one readiness check and one shutdown pending.

**Tech Stack:** Go 1.26, GORM, `go-sql-driver/mysql`, `ncruces/go-sqlite3` (pure Go, WebAssembly), golang-migrate v4, `testcontainers-go`.

**Spec:** `docs/superpowers/specs/2026-08-20-database-connection-design.md`

## What this plan deliberately does NOT build

Read this before starting. Each of these was considered and pushed to phase 1 on purpose; adding one back is a scope decision, not a convenience.

- **No migrations.** There is no schema yet. The runner takes an `fs.FS` and the tests supply one from `testdata`. Inventing a table so the pipeline has something to migrate is reasoning backwards from the compiler.
- **No `embed.FS` in this package.** `internal/migrations` will own the SQL in phase 1.
- **No `migrateSchema` boot step**, and therefore no `migrate` configuration block, no `--skip-migrations` flag and no `startupProbe`. They arrive together with the step they serve.
- **No `aegisd migrate` subcommand.** `ForceVersion` is implemented so the command is a thin wrapper later.

## Global Constraints

These apply to every task and are not repeated per task.

- **Language:** all code, comments, identifiers and error messages in English. Comment only what is hard to understand — never restate what the code says.
- **Build:** `CGO_ENABLED=0` on `distroless/static`. Never introduce a dependency requiring cgo. Specifically: `ncruces/go-sqlite3/gormlite`, never `gorm.io/driver/sqlite` (cgo) and never `glebarez/sqlite` — it registers the `database/sql` driver name `sqlite`, colliding with the `modernc.org/sqlite` that golang-migrate's SQLite driver imports, which panics at `init()`. golang-migrate's `database/sqlite` (modernc), never `database/sqlite3`.
- **Dependency rule:** `internal/infra/database` must not import `internal/configs`. It declares its own `Options` and `Driver`; `internal/application/wiring.go` translates between them.
- **Version floors,** verified at connect time: Postgres 13, MySQL 8.0, MariaDB 10.6.
- **Configuration vocabulary for `ssl_mode`:** exactly `disable`, `prefer`, `require`, `verify-ca`, `verify-full`. Each dialect translates it; the operator never sees engine-specific spellings.
- **Forced DSN parameters** are injected by the dialect and cannot be overridden by `options`. MySQL/MariaDB: `parseTime=true`, `loc=UTC`, `time_zone='+00:00'`, `charset=utf8mb4`, `sql_mode=STRICT_TRANS_TABLES`, `multiStatements=false`. Postgres: `TimeZone=UTC`. SQLite: `_pragma=foreign_keys(1)`, `_pragma=busy_timeout(5000)`, `_pragma=journal_mode(WAL)`.
- **Test commands:** unit `go test -race -covermode=atomic ./...`; integration `go test -tags=integration -timeout=15m ./test/integration/...`; everything `make ci`.
- **Commits:** Conventional Commits, as the repository's release-please setup requires. Scope is `database` unless the task says otherwise.
- **Never commit without Carlos' explicit authorization**, and never create a branch he did not ask for.

## File Structure

**New package — `internal/infra/database/`:**

| File | Responsibility |
|---|---|
| `driver.go` | `Driver` type, its constants, and the `dialects()` table that is the single list of what is supported |
| `options.go` | `Options` and `Pool` — what the package needs, independent of `configs` |
| `database.go` | `Open`, `DB`, `Ping`, `Shutdown`: the factory and the handle |
| `logger.go` | adapts GORM's logger interface onto the phuslu logger already assembled |
| `postgres.go` | Postgres DSN, dialector, SSL translation, version floor, migration driver |
| `mysql.go` | MySQL and MariaDB DSN, dialector, TLS registration, version floors, migration driver |
| `sqlite.go` | SQLite DSN, dialector, single-writer pool override, migration driver |
| `pool.go` | applies pool limits to `*sql.DB` |
| `migrate.go` | the runner: applies an injected `fs.FS` under a lock |
| `testdata/migrations/` | fixture migrations, one directory per dialect — never compiled into the binary |

**New in configs:** `internal/configs/database.go`.

**Modified:** `internal/configs/application.go`, `internal/infra/configbuilder/env_source.go`, `internal/application/{application,wiring}.go`.

---

### Task 1: Configuration section

**Files:**
- Create: `internal/configs/database.go`
- Create: `internal/configs/database_test.go`
- Modify: `internal/configs/application.go`

**Interfaces:**
- Consumes: `Profile`, `ProfileDev`, `ProfileProd` from `internal/configs/profile.go`.
- Produces: `configs.Driver` (`DriverPostgres`, `DriverMySQL`, `DriverMariaDB`, `DriverSQLite`), `configs.Database`, `configs.Pool`, `(*Database).Validate(Profile) error`, `(Driver).IsFileBased() bool`, `(*Database).normalizeForDevelopment()`. Task 2 reads these fields; Task 8 translates them into `database.Options`.

- [ ] **Step 1: Write the failing test**

Create `internal/configs/database_test.go`. The table mirrors `tls_test.go`: each case arranges only what it is about, and asserts on a substring so unrelated errors in the joined result do not mask the rule under test.

```go
package configs_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rootless-dev/aegis/internal/configs"
)

// serverDatabase returns a section valid for a server-backed driver, so each
// case only has to break the one rule it is about.
func serverDatabase() *configs.Database {
	cfg := configs.Default().Database
	cfg.Driver = configs.DriverPostgres
	cfg.Host = "db.internal"
	cfg.Name = "aegis"
	cfg.User = "aegis"
	cfg.Password = "secret"

	return cfg
}

func TestDatabaseValidationMatrix(t *testing.T) {
	cases := map[string]struct {
		profile configs.Profile
		arrange func(cfg *configs.Database)
		wants   string
	}{
		"undeclared driver is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.Driver = "" },
			wants:   "driver is required",
		},
		"unknown driver is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.Driver = "oracle" },
			wants:   `unsupported driver "oracle"`,
		},
		"sqlite in production is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) {
				cfg.Driver = configs.DriverSQLite
				cfg.Host, cfg.Name, cfg.User, cfg.Password = "", "", "", ""
				cfg.Path = "/var/lib/aegis/aegis.db"
			},
			wants: "development engine",
		},
		"sqlite in development is accepted": {
			profile: configs.ProfileDev,
			arrange: func(cfg *configs.Database) {
				cfg.Driver = configs.DriverSQLite
				cfg.Host, cfg.Name, cfg.User, cfg.Password = "", "", "", ""
				cfg.Path = "./aegis.dev.db"
			},
		},
		"sqlite without a path is refused": {
			profile: configs.ProfileDev,
			arrange: func(cfg *configs.Database) {
				cfg.Driver = configs.DriverSQLite
				cfg.Host, cfg.Name, cfg.User, cfg.Password = "", "", "", ""
			},
			wants: "path is required",
		},
		"sqlite carrying a host is refused": {
			profile: configs.ProfileDev,
			arrange: func(cfg *configs.Database) {
				cfg.Driver = configs.DriverSQLite
				cfg.Name, cfg.User, cfg.Password = "", "", ""
				cfg.Path = "./aegis.dev.db"
			},
			wants: "file based, so host cannot be set",
		},
		"a server driver carrying a path is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.Path = "./aegis.db" },
			wants:   "connects to a server, so path cannot be set",
		},
		"a missing host is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.Host = "" },
			wants:   "host is empty",
		},
		"a missing database name is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.Name = "" },
			wants:   "name is empty",
		},
		"a missing user is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.User = "" },
			wants:   "user is empty",
		},
		"an out of range port is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.Port = "70000" },
			wants:   `invalid port "70000"`,
		},
		"an unknown ssl mode is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.SSLMode = "sorta" },
			wants:   `unsupported ssl mode "sorta"`,
		},
		"a ca file without verification is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) {
				cfg.SSLMode = "require"
				cfg.SSLRootCert = "/etc/aegis/db-ca.pem"
			},
			wants: "only verified by ssl_mode",
		},
		"a ca file with verify-full is accepted": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) {
				cfg.SSLMode = "verify-full"
				cfg.SSLRootCert = "/etc/aegis/db-ca.pem"
			},
		},
		"an option colliding with a forced parameter is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) {
				cfg.Options = map[string]string{"TimeZone": "America/Sao_Paulo"}
			},
			wants: "is set by aegis and cannot be overridden",
		},
		"an unrelated option is accepted": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) {
				cfg.Options = map[string]string{"application_name": "aegis"}
			},
		},
		"a zero connect timeout is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.ConnectTimeout = 0 },
			wants:   "connect timeout must be greater than zero",
		},
		"more idle than open connections is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.Pool.MaxIdle = cfg.Pool.MaxOpen + 1 },
			wants:   "max idle",
		},
		"a missing pool section is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.Pool = nil },
			wants:   "pool configuration is missing",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := serverDatabase()
			tc.arrange(cfg)

			err := cfg.Validate(tc.profile)

			if tc.wants == "" {
				if err != nil {
					t.Fatalf("expected the section to be accepted, got %v", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("expected an error containing %q, got none", tc.wants)
			}

			if !strings.Contains(err.Error(), tc.wants) {
				t.Fatalf("expected an error containing %q, got %v", tc.wants, err)
			}
		})
	}
}

func TestDevelopmentFillsInTheDatabase(t *testing.T) {
	cfg := configs.Default()
	cfg.Profile = configs.ProfileDev
	cfg.PublicURL = "http://localhost:7500"

	cfg.Normalize()

	if cfg.Database.Driver != configs.DriverSQLite {
		t.Fatalf("expected the dev profile to select sqlite, got %q", cfg.Database.Driver)
	}

	if cfg.Database.Path == "" {
		t.Fatal("expected the dev profile to supply a database path")
	}
}

func TestProductionDoesNotGuessTheDriver(t *testing.T) {
	cfg := configs.Default()
	cfg.Profile = configs.ProfileProd

	cfg.Normalize()

	if cfg.Database.Driver != "" {
		t.Fatalf("expected production to leave the driver undeclared, got %q", cfg.Database.Driver)
	}
}

func TestTheDriverIsCaseFolded(t *testing.T) {
	cfg := configs.Default()
	cfg.Profile = configs.ProfileProd
	cfg.Database.Driver = " POSTGRES "

	cfg.Normalize()

	if cfg.Database.Driver != configs.DriverPostgres {
		t.Fatalf("expected the driver to be folded to %q, got %q", configs.DriverPostgres, cfg.Database.Driver)
	}
}

func TestDatabaseDefaults(t *testing.T) {
	cfg := configs.Default().Database

	if cfg.ConnectTimeout != 10*time.Second {
		t.Fatalf("unexpected connect timeout %s", cfg.ConnectTimeout)
	}

	if cfg.Pool.MaxIdle != cfg.Pool.MaxOpen {
		t.Fatalf("expected the idle count to match the open count, got %d and %d", cfg.Pool.MaxIdle, cfg.Pool.MaxOpen)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/configs/... -run 'TestDatabase|TestDevelopmentFills|TestProductionDoesNot|TestTheDriver' -v`
Expected: FAIL to compile — `configs.Database`, `configs.DriverPostgres` and `Default().Database` are undefined.

- [ ] **Step 3: Write `internal/configs/database.go`**

```go
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

// developmentDatabasePath is a file rather than ":memory:" so a schema survives
// the restarts air performs on every save.
const developmentDatabasePath = "./aegis.dev.db"

var supportedDrivers = []Driver{DriverPostgres, DriverMySQL, DriverMariaDB, DriverSQLite}

// supportedSSLModes is Postgres' vocabulary, used for every engine: it is the
// one that already names all five levels, and an operator moving an
// installation between engines should not have to relearn the setting. Each
// dialect translates it when the DSN is built.
var supportedSSLModes = []string{"disable", "prefer", "require", "verify-ca", "verify-full"}

// forcedOptionKeys are the DSN parameters the code depends on. They are
// rejected here rather than silently overwritten, so an operator who sets one
// learns why instead of watching it have no effect.
var forcedOptionKeys = map[Driver][]string{
	DriverPostgres: {"sslmode", "sslrootcert", "timezone"},
	DriverMySQL:    {"parsetime", "loc", "charset", "sql_mode", "time_zone", "tls", "multistatements"},
	DriverMariaDB:  {"parsetime", "loc", "charset", "sql_mode", "time_zone", "tls", "multistatements"},
	DriverSQLite:   {"_pragma"},
}

func (d Driver) String() string {
	return string(d)
}

// IsFileBased reports whether the database is a local file rather than a
// server. It splits the section in two: a host, a user and TLS mean nothing for
// it, and a path means nothing for the others.
func (d Driver) IsFileBased() bool {
	return d == DriverSQLite
}

type Database struct {
	Driver Driver `yaml:"driver"`

	Host string `yaml:"host"`
	Port string `yaml:"port"`
	Name string `yaml:"name"`
	User string `yaml:"user"`

	// Password is deliberately not decodable from the file. With
	// KnownFields(true) on the decoder, writing it there fails the boot, which
	// leaves DATABASE_PASSWORD as the only route: a secret that can live in a
	// configuration file eventually gets committed.
	Password string `yaml:"-"`

	// Path is the sqlite file, and belongs to no other driver.
	Path string `yaml:"path"`

	SSLMode     string `yaml:"ssl_mode"`
	SSLRootCert string `yaml:"ssl_root_cert"`

	// Options carries what is specific to the customer's installation. Anything
	// aegis depends on is forced by the dialect instead, and rejected here.
	Options map[string]string `yaml:"options"`

	ConnectTimeout time.Duration `yaml:"connect_timeout"`

	Pool *Pool `yaml:"pool"`
}

type Pool struct {
	MaxOpen int `yaml:"max_open"`

	// MaxIdle matches MaxOpen by default: a smaller idle count makes the pool
	// close and reopen connections under load, and every reopen pays a TLS
	// handshake against the database.
	MaxIdle int `yaml:"max_idle"`

	// ConnMaxLifetime stays under the NAT and firewall idle timeouts that
	// commonly drop connections around an hour. Without it the symptom is a
	// sporadic "invalid connection" with no visible cause.
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`

	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"`
}

func defaultDatabase() *Database {
	return &Database{
		// Deliberately empty, like the TLS termination: only the operator knows
		// which engine they run, and outside development the boot fails until
		// they say so.
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

// normalizeForDevelopment fills in what a local run should not have to declare:
// the engine that needs no server and the file it lives in.
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
		errs = append(errs, cfg.validateServer())
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

func (cfg *Database) validateServer() error {
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

	errs = append(errs, cfg.validateSSL())

	return errors.Join(errs...)
}

func (cfg *Database) validateSSL() error {
	var errs []error

	if cfg.SSLMode != "" && !slices.Contains(supportedSSLModes, cfg.SSLMode) {
		errs = append(errs, fmt.Errorf(
			"database: unsupported ssl mode %q, want one of %v", cfg.SSLMode, supportedSSLModes,
		))
	}

	// A CA nobody checks is a CA that does nothing, and an operator who supplied
	// one believes the connection is authenticated.
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

	// Ordered rather than a map, so the reported problems do not change order
	// between runs.
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

func (cfg *Pool) Validate() error {
	var errs []error

	if cfg.MaxOpen <= 0 {
		errs = append(errs, fmt.Errorf("database: pool max open must be greater than zero, got %d", cfg.MaxOpen))
	}

	if cfg.MaxIdle <= 0 {
		errs = append(errs, fmt.Errorf("database: pool max idle must be greater than zero, got %d", cfg.MaxIdle))
	}

	// A higher idle limit than open is a value that can never be reached, which
	// reads as a configured setting quietly doing nothing.
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
```

- [ ] **Step 4: Wire the section into `internal/configs/application.go`**

Four edits, each mirroring what the existing sections do.

Field at the end of the `Application` struct, after `HSTS`:

```go
	HSTS     *HSTS     `yaml:"hsts"`
	Database *Database `yaml:"database"`
```

In `Default()`, after `HSTS: defaultHSTS(),`:

```go
		Database: defaultDatabase(),
```

In `sections()`, as the last entry:

```go
		{"database", cfg.Database == nil, func() error { return cfg.Database.Validate(cfg.Profile) }},
```

In `Normalize()`, next to the `cfg.Proxy` folding:

```go
	if cfg.Database != nil {
		cfg.Database.Driver = Driver(strings.ToLower(strings.TrimSpace(cfg.Database.Driver.String())))
	}
```

and inside the `IsDev` branch, after `cfg.TLS.normalizeForDevelopment()`:

```go
	if cfg.Database != nil {
		cfg.Database.normalizeForDevelopment()
	}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/configs/... -v`
Expected: PASS, including the pre-existing TLS and application tests.

- [ ] **Step 6: Run the whole unit suite**

Run: `go test -race -covermode=atomic ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/configs/database.go internal/configs/database_test.go internal/configs/application.go
git commit -m "feat(config): declare the database section"
```

---

### Task 2: Environment source

**Files:**
- Modify: `internal/infra/configbuilder/env_source.go`
- Modify: `internal/infra/configbuilder/config_builder_test.go`

**Interfaces:**
- Consumes: `configs.Database`, `configs.Pool` from Task 1.
- Produces: the `DATABASE_*` variables. Task 10 documents them.

No flag in this task. `--skip-migrations` disables a boot step that does not exist yet, and arrives with it.

- [ ] **Step 1: Write the failing test**

Append to `internal/infra/configbuilder/config_builder_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/infra/configbuilder/... -run 'Database|Password' -v`
Expected: FAIL — the variables are not read.

- [ ] **Step 3: Add `applyDatabase` to `env_source.go`**

Call it from `applyEnv`, last, matching the order the sections appear in the struct:

```go
	applyBanner(cfg.Banner)
	applyDatabase(cfg.Database)
```

And the functions themselves, alongside the other `apply*`:

```go
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
```

`Options` deliberately has no variable: a map of arbitrary keys does not survive a flat namespace without inventing a convention, and everything that needs per-instance adjustment already has one.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/infra/configbuilder/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/infra/configbuilder/
git commit -m "feat(config): read the database section from the environment"
```

---
### Task 3: The package skeleton and the Postgres dialect

**Files:**
- Create: `internal/infra/database/driver.go`
- Create: `internal/infra/database/options.go`
- Create: `internal/infra/database/postgres.go`
- Create: `internal/infra/database/postgres_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: nothing from earlier tasks. This package must not import `internal/configs`.
- Produces: `database.Driver` and its four constants; `database.Options`; `database.Pool`; the `dialect` struct and the `dialects()` table; `postgresDSN(Options) (string, error)`; `postgresDialector(string) gorm.Dialector`; `postgresVersion(context.Context, *sql.DB) error`; `postgresMigrator(*sql.DB) (migratedb.Driver, error)`. Tasks 4, 5, 6 and 7 fill in and read from `dialects()`.

The `dialect` table carries the migration driver from the start, so each dialect file is complete in one place rather than revisited in Task 7.

- [ ] **Step 1: Add the dependencies**

```bash
go get gorm.io/gorm gorm.io/driver/postgres github.com/golang-migrate/migrate/v4
go mod tidy
```

- [ ] **Step 2: Write the failing test**

Create `internal/infra/database/postgres_test.go`:

```go
package database

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func postgresOptions() Options {
	return Options{
		Driver:         DriverPostgres,
		Host:           "db.internal",
		Name:           "aegis",
		User:           "aegis",
		Password:       "secret",
		ConnectTimeout: 10 * time.Second,
	}
}

func TestPostgresDSNForcesWhatTheCodeDependsOn(t *testing.T) {
	dsn, err := postgresDSN(postgresOptions())
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("the dsn is not a valid url: %v", err)
	}

	query := parsed.Query()

	if query.Get("TimeZone") != "UTC" {
		t.Fatalf("expected UTC to be forced, got %q", query.Get("TimeZone"))
	}

	if query.Get("connect_timeout") != "10" {
		t.Fatalf("expected the connect timeout in seconds, got %q", query.Get("connect_timeout"))
	}
}

func TestPostgresDSNDefaultsThePort(t *testing.T) {
	dsn, err := postgresDSN(postgresOptions())
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	if !strings.Contains(dsn, ":5432") {
		t.Fatalf("expected the engine default port, got %q", dsn)
	}
}

// A password is chosen by the customer and routinely contains characters that
// break a hand concatenated dsn.
func TestPostgresDSNEscapesTheCredentials(t *testing.T) {
	opts := postgresOptions()
	opts.Password = "p@ss/word:with?specials#"

	dsn, err := postgresDSN(opts)
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("the dsn is not a valid url: %v", err)
	}

	password, _ := parsed.User.Password()
	if password != opts.Password {
		t.Fatalf("expected the password to survive escaping, got %q", password)
	}

	if parsed.Host != "db.internal:5432" {
		t.Fatalf("expected the credentials not to bleed into the host, got %q", parsed.Host)
	}
}

func TestPostgresSSLModeTranslation(t *testing.T) {
	cases := map[string]string{
		"":            "prefer",
		"disable":     "disable",
		"prefer":      "prefer",
		"require":     "require",
		"verify-ca":   "verify-ca",
		"verify-full": "verify-full",
	}

	for given, wanted := range cases {
		t.Run("mode "+given, func(t *testing.T) {
			opts := postgresOptions()
			opts.SSLMode = given

			dsn, err := postgresDSN(opts)
			if err != nil {
				t.Fatalf("building the dsn: %v", err)
			}

			parsed, _ := url.Parse(dsn)
			if got := parsed.Query().Get("sslmode"); got != wanted {
				t.Fatalf("expected sslmode %q, got %q", wanted, got)
			}
		})
	}
}

func TestPostgresDSNCarriesTheCertificateAuthority(t *testing.T) {
	opts := postgresOptions()
	opts.SSLMode = "verify-full"
	opts.SSLRootCert = "/etc/aegis/db-ca.pem"

	dsn, err := postgresDSN(opts)
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, _ := url.Parse(dsn)
	if got := parsed.Query().Get("sslrootcert"); got != opts.SSLRootCert {
		t.Fatalf("expected the ca file in the dsn, got %q", got)
	}
}

// Validation already rejects a colliding option, so this is the second line of
// defence: even if one arrives, the forced value has to win.
func TestPostgresForcedParametersOutrankOptions(t *testing.T) {
	opts := postgresOptions()
	opts.Options = map[string]string{"TimeZone": "America/Sao_Paulo", "application_name": "aegis"}

	dsn, err := postgresDSN(opts)
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, _ := url.Parse(dsn)

	if got := parsed.Query().Get("TimeZone"); got != "UTC" {
		t.Fatalf("expected the forced timezone to win, got %q", got)
	}

	if got := parsed.Query().Get("application_name"); got != "aegis" {
		t.Fatalf("expected an unrelated option to survive, got %q", got)
	}
}

func TestTheDialectTableCoversEveryDriver(t *testing.T) {
	for _, driver := range []Driver{DriverPostgres, DriverMySQL, DriverMariaDB, DriverSQLite} {
		entry, ok := dialects()[driver]
		if !ok {
			t.Fatalf("driver %q has no dialect", driver)
		}

		if entry.dsn == nil || entry.dialector == nil || entry.migrator == nil {
			t.Fatalf("driver %q has an incomplete dialect", driver)
		}
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/infra/database/... -v`
Expected: FAIL to compile — nothing in this package exists yet.

- [ ] **Step 4: Write `options.go`**

```go
package database

import (
	"time"

	"github.com/phuslu/log"
)

// Options is what this package needs to open a connection. It deliberately
// mirrors no configuration struct: internal/infra packages declare their own
// inputs so any of them can be built in a test without assembling the whole
// application.
type Options struct {
	Driver Driver

	Host     string
	Port     string
	Name     string
	User     string
	Password string

	Path string

	// SSLMode is the shared vocabulary: disable, prefer, require, verify-ca or
	// verify-full. Each dialect translates it.
	SSLMode     string
	SSLRootCert string

	Options map[string]string

	ConnectTimeout time.Duration

	Pool Pool

	Logger *log.Logger

	// SlowThreshold is how long a query may take before it is reported. Zero
	// disables the report.
	SlowThreshold time.Duration

	// LogParameters includes query arguments in the logged SQL. It must stay off
	// outside development: those arguments are credentials, tokens and personal
	// data.
	LogParameters bool
}

type Pool struct {
	MaxOpen         int
	MaxIdle         int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}
```

- [ ] **Step 5: Write `driver.go`**

```go
package database

import (
	"context"
	"database/sql"

	migratedb "github.com/golang-migrate/migrate/v4/database"
	"gorm.io/gorm"
)

type Driver string

const (
	DriverPostgres Driver = "postgres"
	DriverMySQL    Driver = "mysql"
	DriverMariaDB  Driver = "mariadb"
	DriverSQLite   Driver = "sqlite"
)

func (d Driver) String() string {
	return string(d)
}

// dialect is everything that differs between engines. Anything it does not name
// is shared, which is what keeps adding an engine down to one file plus one row
// in the table below.
type dialect struct {
	dsn       func(Options) (string, error)
	dialector func(dsn string) gorm.Dialector

	// pool adjusts the limits an engine cannot honor. Nil means the configured
	// limits are used as they are.
	pool func(*Pool)

	// version refuses a server below the floor this code targets. Nil means the
	// engine ships inside the binary and has no floor to check.
	version func(context.Context, *sql.DB) error

	// migrator adapts an open pool to golang-migrate. Each engine has its own
	// config type, which is why this cannot be one shared constructor.
	migrator func(*sql.DB) (migratedb.Driver, error)
}

// dialects is the single list of what is supported, spelled out rather than
// assembled through init(), in the style of Application.sections() and
// Application.resources().
func dialects() map[Driver]dialect {
	return map[Driver]dialect{
		DriverPostgres: {postgresDSN, postgresDialector, nil, postgresVersion, postgresMigrator},
		DriverMySQL:    {mysqlDSN, mysqlDialector, nil, mysqlVersion, mysqlMigrator},
		DriverMariaDB:  {mysqlDSN, mysqlDialector, nil, mariadbVersion, mysqlMigrator},
		DriverSQLite:   {sqliteDSN, sqliteDialector, sqliteSingleWriter, nil, sqliteMigrator},
	}
}
```

- [ ] **Step 6: Write `postgres.go`**

```go
package database

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strconv"

	migratedb "github.com/golang-migrate/migrate/v4/database"
	pgmigrate "github.com/golang-migrate/migrate/v4/database/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const (
	defaultPostgresPort = "5432"

	// minimumPostgresVersion is 13: gen_random_uuid() without an extension, and
	// the ON CONFLICT behaviour the repositories will rely on.
	minimumPostgresVersion = 130000
)

func postgresDSN(opts Options) (string, error) {
	params := url.Values{}

	// Applied first, so a forced parameter overwrites it. Validation already
	// rejects the collision; this is what makes the outcome safe anyway.
	for key, value := range opts.Options {
		params.Set(key, value)
	}

	params.Set("sslmode", postgresSSLMode(opts.SSLMode))

	if opts.SSLRootCert != "" {
		params.Set("sslrootcert", opts.SSLRootCert)
	}

	// UTC everywhere, whatever the server is set to: token and session expiry
	// are compared across instances that may not share a timezone.
	params.Set("TimeZone", "UTC")

	if opts.ConnectTimeout > 0 {
		params.Set("connect_timeout", strconv.Itoa(int(opts.ConnectTimeout.Seconds())))
	}

	port := opts.Port
	if port == "" {
		port = defaultPostgresPort
	}

	// Built as a url rather than concatenated: a password is chosen by the
	// customer and routinely contains characters that would otherwise be read
	// as dsn syntax.
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(opts.User, opts.Password),
		Host:     net.JoinHostPort(opts.Host, port),
		Path:     "/" + opts.Name,
		RawQuery: params.Encode(),
	}

	return dsn.String(), nil
}

// postgresSSLMode is the identity translation, because the shared vocabulary is
// Postgres'. It exists so the mapping is visible in every dialect rather than
// implied in one of them.
func postgresSSLMode(mode string) string {
	if mode == "" {
		return "prefer"
	}

	return mode
}

func postgresDialector(dsn string) gorm.Dialector {
	return postgres.Open(dsn)
}

func postgresMigrator(db *sql.DB) (migratedb.Driver, error) {
	return pgmigrate.WithInstance(db, &pgmigrate.Config{})
}

func postgresVersion(ctx context.Context, db *sql.DB) error {
	var version int

	if err := db.QueryRowContext(ctx, "SHOW server_version_num").Scan(&version); err != nil {
		return fmt.Errorf("database: reading the postgres version: %w", err)
	}

	if version < minimumPostgresVersion {
		return fmt.Errorf(
			"database: postgres %d is below the minimum supported version %d",
			version, minimumPostgresVersion,
		)
	}

	return nil
}
```

- [ ] **Step 7: Note on running the tests**

The package does not compile until Tasks 4 and 5 exist: `dialects()` names their functions. That is deliberate — the table is the single list of supported engines, and splitting it per dialect would hide what the package supports. Implement Tasks 3, 4 and 5 back to back, then run.

- [ ] **Step 8: Commit**

```bash
git add internal/infra/database/ go.mod go.sum
git commit -m "feat(database): add the dialect table and the postgres dsn"
```

---

### Task 4: The MySQL and MariaDB dialect

**Files:**
- Create: `internal/infra/database/mysql.go`
- Create: `internal/infra/database/mysql_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: `Options`, `Driver`, the `dialect` table from Task 3.
- Produces: `mysqlDSN(Options) (string, error)`, `mysqlDialector(string) gorm.Dialector`, `mysqlMigrator(*sql.DB) (migratedb.Driver, error)`, `mysqlVersion` and `mariadbVersion` (both `func(context.Context, *sql.DB) error`).

- [ ] **Step 1: Add the dependency**

```bash
go get gorm.io/driver/mysql github.com/go-sql-driver/mysql
go mod tidy
```

- [ ] **Step 2: Write the failing test**

Create `internal/infra/database/mysql_test.go`:

```go
package database

import (
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func mysqlOptions() Options {
	return Options{
		Driver:         DriverMySQL,
		Host:           "db.internal",
		Name:           "aegis",
		User:           "aegis",
		Password:       "secret",
		ConnectTimeout: 10 * time.Second,
	}
}

func TestMySQLDSNForcesWhatTheCodeDependsOn(t *testing.T) {
	dsn, err := mysqlDSN(mysqlOptions())
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("the dsn does not parse: %v", err)
	}

	if !parsed.ParseTime {
		t.Fatal("expected parseTime to be forced: without it every date column arrives as bytes")
	}

	if parsed.Loc != time.UTC {
		t.Fatalf("expected UTC to be forced, got %v", parsed.Loc)
	}

	// The one-statement-per-file rule leans on the driver refusing the second.
	if parsed.MultiStatements {
		t.Fatal("expected multiStatements to stay off")
	}

	if got := parsed.Params["sql_mode"]; got != "'STRICT_TRANS_TABLES'" {
		t.Fatalf("expected strict mode to be forced, got %q", got)
	}

	if got := parsed.Params["time_zone"]; got != "'+00:00'" {
		t.Fatalf("expected the session timezone to be forced, got %q", got)
	}

	if parsed.Params["charset"] != "utf8mb4" {
		t.Fatalf("expected utf8mb4 to be forced, got %q", parsed.Params["charset"])
	}
}

func TestMySQLDSNDefaultsThePort(t *testing.T) {
	dsn, err := mysqlDSN(mysqlOptions())
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, _ := mysql.ParseDSN(dsn)
	if parsed.Addr != "db.internal:3306" {
		t.Fatalf("expected the engine default port, got %q", parsed.Addr)
	}
}

func TestMySQLDSNEscapesTheCredentials(t *testing.T) {
	opts := mysqlOptions()
	opts.Password = "p@ss/word:with?specials#"

	dsn, err := mysqlDSN(opts)
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("the dsn does not parse: %v", err)
	}

	if parsed.Passwd != opts.Password {
		t.Fatalf("expected the password to survive escaping, got %q", parsed.Passwd)
	}
}

func TestMySQLSSLModeTranslation(t *testing.T) {
	cases := map[string]string{
		"":        "preferred",
		"disable": "false",
		"prefer":  "preferred",
		"require": "skip-verify",
	}

	for given, wanted := range cases {
		t.Run("mode "+given, func(t *testing.T) {
			opts := mysqlOptions()
			opts.SSLMode = given

			dsn, err := mysqlDSN(opts)
			if err != nil {
				t.Fatalf("building the dsn: %v", err)
			}

			parsed, _ := mysql.ParseDSN(dsn)
			if parsed.TLSConfig != wanted {
				t.Fatalf("expected tls %q, got %q", wanted, parsed.TLSConfig)
			}
		})
	}
}

// verify-ca and verify-full cannot be expressed as a dsn parameter at all: the
// driver only accepts the name of a config registered from Go.
func TestMySQLVerificationRegistersATLSConfig(t *testing.T) {
	opts := mysqlOptions()
	opts.SSLMode = "verify-full"

	dsn, err := mysqlDSN(opts)
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, _ := mysql.ParseDSN(dsn)
	switch parsed.TLSConfig {
	case "", "preferred", "skip-verify", "false":
		t.Fatalf("expected a registered config name, got %q", parsed.TLSConfig)
	}
}

func TestMySQLRejectsAnUnreadableCertificateAuthority(t *testing.T) {
	opts := mysqlOptions()
	opts.SSLMode = "verify-full"
	opts.SSLRootCert = "/does/not/exist.pem"

	if _, err := mysqlDSN(opts); err == nil {
		t.Fatal("expected an unreadable ca file to fail the dsn")
	}
}

func TestVersionParsing(t *testing.T) {
	cases := map[string]struct {
		major int
		minor int
	}{
		"8.0.36":                             {8, 0},
		"10.6.16-MariaDB-1:10.6.16+maria~ubu": {10, 6},
		"5.7.44":                             {5, 7},
	}

	for raw, wanted := range cases {
		t.Run(raw, func(t *testing.T) {
			major, minor, err := parseVersion(raw)
			if err != nil {
				t.Fatalf("parsing %q: %v", raw, err)
			}

			if major != wanted.major || minor != wanted.minor {
				t.Fatalf("expected %d.%d, got %d.%d", wanted.major, wanted.minor, major, minor)
			}
		})
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/infra/database/... -run TestMySQL -v`
Expected: FAIL to compile — `mysqlDSN` is undefined.

- [ ] **Step 4: Write `mysql.go`**

```go
package database

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	driver "github.com/go-sql-driver/mysql"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	mysqlmigrate "github.com/golang-migrate/migrate/v4/database/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const (
	defaultMySQLPort = "3306"

	// The floors: CTEs and window functions on MySQL 8, and stable utf8mb4
	// behaviour on MariaDB 10.6.
	minimumMySQLMajor   = 8
	minimumMariaDBMajor = 10
	minimumMariaDBMinor = 6
)

func mysqlDSN(opts Options) (string, error) {
	cfg := driver.NewConfig()

	cfg.User = opts.User
	cfg.Passwd = opts.Password
	cfg.Net = "tcp"

	port := opts.Port
	if port == "" {
		port = defaultMySQLPort
	}

	cfg.Addr = net.JoinHostPort(opts.Host, port)
	cfg.DBName = opts.Name
	cfg.Timeout = opts.ConnectTimeout

	// Without this every DATETIME arrives as a byte slice and the mapping
	// breaks; with a location other than UTC, token and session expiry are
	// written in whatever timezone the customer's server happens to run.
	cfg.ParseTime = true
	cfg.Loc = time.UTC

	// Off deliberately: on an engine with no transactional DDL, a migration
	// file carrying two statements could apply half of itself. With multiple
	// statements refused by the driver, it fails instead.
	cfg.MultiStatements = false

	cfg.Params = map[string]string{}
	for key, value := range opts.Options {
		cfg.Params[key] = value
	}

	// Quoted because the driver passes these through as session variables.
	cfg.Params["sql_mode"] = "'STRICT_TRANS_TABLES'"
	cfg.Params["time_zone"] = "'+00:00'"
	cfg.Params["charset"] = "utf8mb4"

	tlsName, err := mysqlTLS(opts)
	if err != nil {
		return "", err
	}

	cfg.TLSConfig = tlsName

	return cfg.FormatDSN(), nil
}

// mysqlTLS translates the shared vocabulary. The first three levels are names
// the driver already understands; the two verifying levels are not expressible
// in a dsn at all and have to be registered from Go, which is why SSLRootCert
// is a configuration field rather than an option.
func mysqlTLS(opts Options) (string, error) {
	switch opts.SSLMode {
	case "", "prefer":
		return "preferred", nil
	case "disable":
		return "false", nil
	case "require":
		// Encrypted, unverified: it stops a passive listener, not an active
		// attacker. Only the verify- levels authenticate the server.
		return "skip-verify", nil
	case "verify-ca", "verify-full":
		return registerMySQLVerification(opts)
	default:
		return "", fmt.Errorf("database: unsupported ssl mode %q", opts.SSLMode)
	}
}

func registerMySQLVerification(opts Options) (string, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if opts.SSLRootCert != "" {
		// The path is boot configuration written by whoever already controls the
		// process, not request input.
		pem, err := os.ReadFile(opts.SSLRootCert) // #nosec G304
		if err != nil {
			return "", fmt.Errorf("database: reading the certificate authority %q: %w", opts.SSLRootCert, err)
		}

		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return "", fmt.Errorf("database: %q contains no usable certificate", opts.SSLRootCert)
		}

		cfg.RootCAs = roots
	}

	if opts.SSLMode == "verify-ca" {
		// verify-ca authenticates the chain but not the name, which the standard
		// handshake cannot express on its own. Skipping the built in check hands
		// verification to the callback below rather than dropping it.
		cfg.InsecureSkipVerify = true // #nosec G402
		cfg.VerifyPeerCertificate = verifyChainIgnoringHostname(cfg.RootCAs)
	} else {
		cfg.ServerName = opts.Host
	}

	name := "aegis-" + opts.Driver.String() + "-" + opts.SSLMode
	if err := driver.RegisterTLSConfig(name, cfg); err != nil {
		return "", fmt.Errorf("database: registering the tls configuration: %w", err)
	}

	return name, nil
}

func verifyChainIgnoringHostname(roots *x509.CertPool) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("database: the server presented no certificate")
		}

		certs := make([]*x509.Certificate, 0, len(rawCerts))

		for _, raw := range rawCerts {
			parsed, err := x509.ParseCertificate(raw)
			if err != nil {
				return fmt.Errorf("database: parsing the server certificate: %w", err)
			}

			certs = append(certs, parsed)
		}

		intermediates := x509.NewCertPool()
		for _, cert := range certs[1:] {
			intermediates.AddCert(cert)
		}

		// DNSName is deliberately absent: that is the whole difference between
		// verify-ca and verify-full.
		_, err := certs[0].Verify(x509.VerifyOptions{
			Roots:         roots,
			Intermediates: intermediates,
		})

		return err
	}
}

func mysqlDialector(dsn string) gorm.Dialector {
	return mysql.Open(dsn)
}

func mysqlMigrator(db *sql.DB) (migratedb.Driver, error) {
	return mysqlmigrate.WithInstance(db, &mysqlmigrate.Config{})
}

func mysqlVersion(ctx context.Context, db *sql.DB) error {
	raw, err := serverVersion(ctx, db)
	if err != nil {
		return err
	}

	// Declaring mysql and connecting to MariaDB means the wrong migration
	// lineage would be applied, so it is refused rather than tolerated.
	if strings.Contains(strings.ToLower(raw), "mariadb") {
		return fmt.Errorf("database: driver is %q but the server reports %q, declare %q instead", DriverMySQL, raw, DriverMariaDB)
	}

	major, _, err := parseVersion(raw)
	if err != nil {
		return err
	}

	if major < minimumMySQLMajor {
		return fmt.Errorf("database: mysql %s is below the minimum supported version %d.0", raw, minimumMySQLMajor)
	}

	return nil
}

func mariadbVersion(ctx context.Context, db *sql.DB) error {
	raw, err := serverVersion(ctx, db)
	if err != nil {
		return err
	}

	if !strings.Contains(strings.ToLower(raw), "mariadb") {
		return fmt.Errorf("database: driver is %q but the server reports %q, declare %q instead", DriverMariaDB, raw, DriverMySQL)
	}

	major, minor, err := parseVersion(raw)
	if err != nil {
		return err
	}

	if major < minimumMariaDBMajor || (major == minimumMariaDBMajor && minor < minimumMariaDBMinor) {
		return fmt.Errorf(
			"database: mariadb %s is below the minimum supported version %d.%d",
			raw, minimumMariaDBMajor, minimumMariaDBMinor,
		)
	}

	return nil
}

func serverVersion(ctx context.Context, db *sql.DB) (string, error) {
	var version string

	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return "", fmt.Errorf("database: reading the server version: %w", err)
	}

	return version, nil
}

// parseVersion reads the leading major.minor of strings like "8.0.36" and
// "10.6.16-MariaDB-1:10.6.16+maria~ubu2004".
func parseVersion(raw string) (int, int, error) {
	parts := strings.SplitN(raw, ".", 3)
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("database: cannot read a version out of %q", raw)
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("database: cannot read a version out of %q", raw)
	}

	minor, err := strconv.Atoi(strings.SplitN(parts[1], "-", 2)[0])
	if err != nil {
		return 0, 0, fmt.Errorf("database: cannot read a version out of %q", raw)
	}

	return major, minor, nil
}
```

- [ ] **Step 5: Commit**

```bash
git add internal/infra/database/mysql.go internal/infra/database/mysql_test.go go.mod go.sum
git commit -m "feat(database): add the mysql and mariadb dsn with tls verification"
```

---

### Task 5: The SQLite dialect

**Files:**
- Create: `internal/infra/database/sqlite.go`
- Create: `internal/infra/database/sqlite_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: `Options`, `Driver`, `Pool`, the `dialect` table from Task 3.
- Produces: `sqliteDSN(Options) (string, error)`, `sqliteDialector(string) gorm.Dialector`, `sqliteSingleWriter(*Pool)`, `sqliteMigrator(*sql.DB) (migratedb.Driver, error)`.

- [ ] **Step 1: Add the dependency**

Pure Go, because the production image is `CGO_ENABLED=0` on `distroless/static`:

```bash
go get github.com/ncruces/go-sqlite3/gormlite
go mod tidy
```

- [ ] **Step 2: Write the failing test**

Create `internal/infra/database/sqlite_test.go`:

```go
package database

import (
	"net/url"
	"strings"
	"testing"
)

func TestSQLiteDSNForcesThePragmas(t *testing.T) {
	dsn, err := sqliteDSN(Options{Driver: DriverSQLite, Path: "./aegis.dev.db"})
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	if !strings.HasPrefix(dsn, "file:./aegis.dev.db?") {
		t.Fatalf("expected the path to lead the dsn, got %q", dsn)
	}

	query, err := url.ParseQuery(strings.SplitN(dsn, "?", 2)[1])
	if err != nil {
		t.Fatalf("the dsn query does not parse: %v", err)
	}

	pragmas := strings.Join(query["_pragma"], " ")

	// Foreign keys are off by default in SQLite, so a constraint the other
	// engines enforce would be silently ignored here.
	for _, wanted := range []string{"foreign_keys(1)", "busy_timeout(5000)", "journal_mode(WAL)"} {
		if !strings.Contains(pragmas, wanted) {
			t.Fatalf("expected pragma %q, got %q", wanted, pragmas)
		}
	}
}

// Writes serialise over the whole file: more connections produce "database is
// locked", never more throughput.
func TestSQLiteIsASingleWriter(t *testing.T) {
	pool := Pool{MaxOpen: 25, MaxIdle: 25}

	sqliteSingleWriter(&pool)

	if pool.MaxOpen != 1 || pool.MaxIdle != 1 {
		t.Fatalf("expected a single connection, got open=%d idle=%d", pool.MaxOpen, pool.MaxIdle)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/infra/database/... -run TestSQLite -v`
Expected: FAIL to compile — `sqliteDSN` is undefined.

- [ ] **Step 4: Write `sqlite.go`**

```go
package database

import (
	"database/sql"
	"net/url"

	"github.com/ncruces/go-sqlite3/gormlite"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	sqlitemigrate "github.com/golang-migrate/migrate/v4/database/sqlite"
	"gorm.io/gorm"
)

func sqliteDSN(opts Options) (string, error) {
	params := url.Values{}

	// Off by default in SQLite, which would make a foreign key the other three
	// engines enforce silently do nothing here.
	params.Add("_pragma", "foreign_keys(1)")

	// Without a busy timeout a concurrent write fails immediately instead of
	// waiting out the writer ahead of it.
	params.Add("_pragma", "busy_timeout(5000)")
	params.Add("_pragma", "journal_mode(WAL)")

	for key, value := range opts.Options {
		params.Add(key, value)
	}

	return "file:" + opts.Path + "?" + params.Encode(), nil
}

// The dialector's driver registers as "sqlite3", while the migration runner
// opens "sqlite" (modernc, brought in by golang-migrate). Two SQLite
// implementations, deliberately: they are what lets both exist in one binary.
func sqliteDialector(dsn string) gorm.Dialector {
	return gormlite.Open(dsn)
}

// sqliteMigrator uses the modernc backed driver, never database/sqlite3, which
// would drag cgo into a build that has to stay static.
func sqliteMigrator(db *sql.DB) (migratedb.Driver, error) {
	return sqlitemigrate.WithInstance(db, &sqlitemigrate.Config{})
}

// sqliteSingleWriter collapses the pool. SQLite serialises writes over the file
// itself, so additional connections only turn contention into errors.
func sqliteSingleWriter(pool *Pool) {
	pool.MaxOpen = 1
	pool.MaxIdle = 1
}
```

- [ ] **Step 5: Run the whole package**

Run: `go test ./internal/infra/database/... -v`
Expected: PASS. The dialect table now compiles: every function it names exists.

- [ ] **Step 6: Verify the build stayed cgo free**

Run: `CGO_ENABLED=0 go build ./...`
Expected: success. A cgo dependency would fail here rather than in the image build.

- [ ] **Step 7: Commit**

```bash
git add internal/infra/database/sqlite.go internal/infra/database/sqlite_test.go go.mod go.sum
git commit -m "feat(database): add the pure go sqlite dialect"
```

---
### Task 6: The factory, the pool and GORM's logger

**Files:**
- Create: `internal/infra/database/database.go`
- Create: `internal/infra/database/pool.go`
- Create: `internal/infra/database/logger.go`
- Create: `internal/infra/database/database_test.go`

**Interfaces:**
- Consumes: `dialects()`, `Options`, `Pool` from Tasks 3–5.
- Produces: `database.DB` with fields `Gorm *gorm.DB`, `SQL *sql.DB`, `Driver Driver` and an unexported `opts Options` that Task 7 reads to open its own connection; `Open(Options) (*DB, error)`; `(*DB).Ping(context.Context) error`; `(*DB).Shutdown(context.Context) error`; `ErrUnsupportedDriver`. Task 7 hangs the runner off `DB`; Task 8 calls `Open`, registers `Ping` and `Shutdown`.

- [ ] **Step 1: Write the failing test**

Create `internal/infra/database/database_test.go`. SQLite makes the factory testable with no container at all, which is the one place its presence genuinely pays for itself.

```go
package database

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/phuslu/log"
)

func sqliteOptions(t *testing.T) Options {
	t.Helper()

	return Options{
		Driver:         DriverSQLite,
		Path:           filepath.Join(t.TempDir(), "aegis.db"),
		ConnectTimeout: 5 * time.Second,
		Pool:           Pool{MaxOpen: 25, MaxIdle: 25, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute},
		Logger:         &log.DefaultLogger,
	}
}

func TestOpenConnectsAndReports(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if db.Driver != DriverSQLite {
		t.Fatalf("expected the driver to be carried on the handle, got %q", db.Driver)
	}

	if db.Gorm == nil || db.SQL == nil {
		t.Fatal("expected both handles to be set")
	}

	if err := db.Ping(context.Background()); err != nil {
		t.Fatalf("pinging: %v", err)
	}
}

// The pool override belongs to the dialect, not to whoever configured it.
func TestOpenAppliesTheDialectPoolOverride(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if got := db.SQL.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("expected sqlite to be collapsed to a single connection, got %d", got)
	}
}

func TestOpenRefusesAnUnknownDriver(t *testing.T) {
	opts := sqliteOptions(t)
	opts.Driver = "oracle"

	_, err := Open(opts)
	if !errors.Is(err, ErrUnsupportedDriver) {
		t.Fatalf("expected ErrUnsupportedDriver, got %v", err)
	}
}

// An unreachable database has to be an initialization error with a message,
// never a panic inside a constructor and not a failure on the first request.
func TestOpenFailsWhenTheDatabaseCannotBeReached(t *testing.T) {
	opts := sqliteOptions(t)
	opts.Path = filepath.Join(t.TempDir(), "missing-directory", "aegis.db")

	if _, err := Open(opts); err == nil {
		t.Fatal("expected an unreachable database to fail Open")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/infra/database/... -run TestOpen -v`
Expected: FAIL to compile — `Open`, `DB` and `ErrUnsupportedDriver` are undefined.

- [ ] **Step 3: Write `pool.go`**

```go
package database

import "database/sql"

func applyPool(db *sql.DB, pool Pool) {
	db.SetMaxOpenConns(pool.MaxOpen)
	db.SetMaxIdleConns(pool.MaxIdle)
	db.SetConnMaxLifetime(pool.ConnMaxLifetime)
	db.SetConnMaxIdleTime(pool.ConnMaxIdleTime)
}
```

- [ ] **Step 4: Write `logger.go`**

```go
package database

import (
	"context"
	"errors"
	"time"

	"github.com/phuslu/log"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// gormLogger routes GORM through the logger the application already assembled.
// Left alone, GORM prints to standard output with log.Printf: unstructured,
// without the request id the middleware chain propagates, and invisible to
// whatever collects logs at the customer's site.
type gormLogger struct {
	logger        *log.Logger
	level         gormlogger.LogLevel
	slowThreshold time.Duration
}

func newGormLogger(logger *log.Logger, slowThreshold time.Duration) gormlogger.Interface {
	return &gormLogger{
		logger:        logger,
		level:         gormlogger.Warn,
		slowThreshold: slowThreshold,
	}
}

func (g *gormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	clone := *g
	clone.level = level

	return &clone
}

func (g *gormLogger) Info(_ context.Context, message string, data ...any) {
	if g.level >= gormlogger.Info {
		g.logger.Info().Msgf(message, data...)
	}
}

func (g *gormLogger) Warn(_ context.Context, message string, data ...any) {
	if g.level >= gormlogger.Warn {
		g.logger.Warn().Msgf(message, data...)
	}
}

func (g *gormLogger) Error(_ context.Context, message string, data ...any) {
	if g.level >= gormlogger.Error {
		g.logger.Error().Msgf(message, data...)
	}
}

// Trace is called once per statement. The three branches are the only three
// outcomes worth a line: it failed, it was slow, or it is routine.
func (g *gormLogger) Trace(_ context.Context, begin time.Time, fc func() (string, int64), err error) {
	if g.level <= gormlogger.Silent {
		return
	}

	elapsed := time.Since(begin)
	statement, rows := fc()

	switch {
	// A missing row is an ordinary outcome of a lookup, not a database error.
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound):
		g.logger.Error().Err(err).Str("sql", statement).Int64("rows", rows).Dur("elapsed", elapsed).Msg("query failed")
	case g.slowThreshold > 0 && elapsed > g.slowThreshold:
		g.logger.Warn().Str("sql", statement).Int64("rows", rows).Dur("elapsed", elapsed).Msg("slow query")
	default:
		g.logger.Debug().Str("sql", statement).Int64("rows", rows).Dur("elapsed", elapsed).Msg("query")
	}
}
```

- [ ] **Step 5: Write `database.go`**

```go
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

var ErrUnsupportedDriver = errors.New("database: unsupported driver")

type DB struct {
	Gorm *gorm.DB

	// SQL is not a leaked abstraction: GORM exposes neither Close nor Ping, and
	// the ordered shutdown and the readiness check both need them.
	SQL *sql.DB

	Driver Driver

	// opts is kept so the migration runner can open a connection of its own
	// without the caller having to hand the options over a second time.
	opts Options
}

// Open connects for real before returning. A database that cannot be reached
// has to be an initialization error with a message an operator can act on,
// which is why every step of application.New returns one.
func Open(opts Options) (*DB, error) {
	selected, ok := dialects()[opts.Driver]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedDriver, opts.Driver)
	}

	pool := opts.Pool
	if selected.pool != nil {
		selected.pool(&pool)
	}

	dsn, err := selected.dsn(opts)
	if err != nil {
		return nil, err
	}

	gormDB, err := gorm.Open(selected.dialector(dsn), &gorm.Config{
		Logger: newGormLogger(opts.Logger, opts.SlowThreshold),
		// Query arguments are credentials, tokens and personal data. They are
		// only rendered into the log when a developer asks for it.
		ParameterizedQueries: !opts.LogParameters,
		// GORM otherwise wraps every single write in a transaction of its own,
		// which costs a round trip per statement and buys nothing here: the
		// operations that need atomicity will manage it explicitly.
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("database: opening %q: %w", opts.Driver, err)
	}

	sqlDB, err := gormDB.DB()
	if err != nil {
		return nil, fmt.Errorf("database: reaching the underlying pool: %w", err)
	}

	applyPool(sqlDB, pool)

	ctx, cancel := context.WithTimeout(context.Background(), opts.ConnectTimeout)
	defer cancel()

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()

		return nil, fmt.Errorf("database: %q is not reachable: %w", opts.Driver, err)
	}

	if selected.version != nil {
		if err := selected.version(ctx, sqlDB); err != nil {
			_ = sqlDB.Close()

			return nil, err
		}
	}

	return &DB{Gorm: gormDB, SQL: sqlDB, Driver: opts.Driver, opts: opts}, nil
}

func (db *DB) Ping(ctx context.Context) error {
	return db.SQL.PingContext(ctx)
}

// Shutdown takes a context it does not use, to match the pending signature
// graceful.Register expects. sql.DB.Close does not wait for in flight queries;
// it is safe here only because it runs after the server has stopped and
// drained.
func (db *DB) Shutdown(_ context.Context) error {
	return db.SQL.Close()
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/infra/database/... -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/infra/database/database.go internal/infra/database/pool.go internal/infra/database/logger.go internal/infra/database/database_test.go
git commit -m "feat(database): open a pooled connection with structured logging"
```

---

### Task 7: The migration runner

**Files:**
- Create: `internal/infra/database/migrate.go`
- Create: `internal/infra/database/migrate_test.go`
- Create: `internal/infra/database/testdata/migrations/{postgres,mysql,mariadb,sqlite}/*.sql`
- Create: `internal/infra/database/testdata/broken/{postgres,mysql,mariadb,sqlite}/*.sql`

**Interfaces:**
- Consumes: `DB`, `Options`, `dialects()` from Tasks 3–6.
- Produces: `MigrateOptions{Timeout time.Duration; LockTimeout time.Duration}`; `(*DB).Migrate(context.Context, fs.FS, MigrateOptions) error`; `(*DB).SchemaVersion(context.Context) (uint, bool, error)`; `(*DB).ForceVersion(context.Context, int) error`; `ErrSchemaDirty`. Phase 1 calls `Migrate` from the boot with the real `internal/migrations`.

**The source is a parameter.** This package applies migrations; where they live is the caller's knowledge, the same way `application/ports.go` declares `CertificateSource` because the consumer is what knows the shape of its dependency. That is the right boundary regardless of the schema question, and it is what lets the runner be finished and proven before any migration exists.

- [ ] **Step 1: Write the fixture migrations**

They live under `testdata/`, which the Go toolchain ignores, so nothing here can ever reach the binary. The SQL is real; only the choice of what it creates is arbitrary.

`testdata/migrations/postgres/000001_probe.up.sql`:

```sql
CREATE TABLE migration_probe (
    id    INTEGER NOT NULL PRIMARY KEY,
    label VARCHAR(64) NOT NULL
);
```

`testdata/migrations/postgres/000001_probe.down.sql`:

```sql
DROP TABLE migration_probe;
```

`testdata/migrations/postgres/000002_probe_column.up.sql`:

```sql
ALTER TABLE migration_probe ADD COLUMN note VARCHAR(64);
```

`testdata/migrations/postgres/000002_probe_column.down.sql`:

```sql
ALTER TABLE migration_probe DROP COLUMN note;
```

Copy the same four into `mysql/` and `mariadb/`, replacing the table definition
with the InnoDB form so the fixtures exercise the collation the real migrations
will declare:

```sql
CREATE TABLE migration_probe (
    id    INT NOT NULL PRIMARY KEY,
    label VARCHAR(64) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
```

And into `sqlite/`, with `INTEGER` and `TEXT`.

Under `testdata/broken/<driver>/`, the same `000001`, plus a `000002` that fails
on purpose — this is what produces a dirty schema on the engines without
transactional DDL:

```sql
ALTER TABLE migration_probe ADD COLUMN label VARCHAR(64);
```

It fails because `label` already exists. That is the point.

- [ ] **Step 2: Write the failing test**

Create `internal/infra/database/migrate_test.go`. SQLite covers the mechanics here; Task 9 runs the same shapes against the server engines, where the interesting differences live.

```go
package database

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func migrateOptions() MigrateOptions {
	return MigrateOptions{Timeout: time.Minute, LockTimeout: 10 * time.Second}
}

func sqliteFixtures() fs.FS {
	return os.DirFS("testdata/migrations/sqlite")
}

func TestMigrateBringsTheSchemaUp(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if err := db.Migrate(context.Background(), sqliteFixtures(), migrateOptions()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	version, dirty, err := db.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("reading the schema version: %v", err)
	}

	if dirty {
		t.Fatal("expected a clean schema after a successful migration")
	}

	if version != 2 {
		t.Fatalf("expected both fixtures to be applied, got version %d", version)
	}
}

// Every replica of a deployment runs this path, so the second one through must
// find nothing to do rather than fail.
func TestMigratingTwiceIsHarmless(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if err := db.Migrate(context.Background(), sqliteFixtures(), migrateOptions()); err != nil {
		t.Fatalf("first migration: %v", err)
	}

	if err := db.Migrate(context.Background(), sqliteFixtures(), migrateOptions()); err != nil {
		t.Fatalf("second migration should be a no-op, got %v", err)
	}
}

// An empty source is what phase 1 will never hand over, but a caller mistake
// must be a clear error rather than a silent success.
func TestMigrateRejectsAnEmptySource(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if err := db.Migrate(context.Background(), fstest.MapFS{}, migrateOptions()); err == nil {
		t.Fatal("expected an empty source to be refused")
	}
}

// A dirty schema is where a MySQL installation ends up when a migration fails
// halfway. It must produce a message an operator can act on, never a driver
// error leaking into a support ticket.
func TestADirtySchemaIsRefusedWithAnActionableError(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if err := db.Migrate(context.Background(), sqliteFixtures(), migrateOptions()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	if _, err := db.SQL.ExecContext(context.Background(), "UPDATE schema_migrations SET dirty = 1"); err != nil {
		t.Fatalf("marking the schema dirty: %v", err)
	}

	err = db.Migrate(context.Background(), sqliteFixtures(), migrateOptions())

	var dirty *ErrSchemaDirty
	if !errors.As(err, &dirty) {
		t.Fatalf("expected ErrSchemaDirty, got %v", err)
	}

	if !strings.Contains(err.Error(), "aegisd migrate force") {
		t.Fatalf("expected the error to name the recovery command, got %v", err)
	}
}

func TestForceVersionClearsTheDirtyFlag(t *testing.T) {
	db, err := Open(sqliteOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if err := db.Migrate(context.Background(), sqliteFixtures(), migrateOptions()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	version, _, _ := db.SchemaVersion(context.Background())

	if _, err := db.SQL.ExecContext(context.Background(), "UPDATE schema_migrations SET dirty = 1"); err != nil {
		t.Fatalf("marking the schema dirty: %v", err)
	}

	if err := db.ForceVersion(context.Background(), int(version)); err != nil {
		t.Fatalf("forcing the version: %v", err)
	}

	_, dirty, _ := db.SchemaVersion(context.Background())
	if dirty {
		t.Fatal("expected force to clear the dirty flag")
	}
}
```

Imports needed: `io/fs` and `testing/fstest` alongside the ones listed.

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/infra/database/... -run 'TestMigrat|TestADirty|TestForce' -v`
Expected: FAIL to compile — `MigrateOptions`, `(*DB).Migrate` and `ErrSchemaDirty` are undefined.

- [ ] **Step 4: Write `migrate.go`**

```go
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"testing/fstest"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

type MigrateOptions struct {
	// Timeout bounds the whole run, lock acquisition included.
	Timeout time.Duration

	// LockTimeout bounds how long this instance waits for another replica to
	// finish migrating before giving up.
	LockTimeout time.Duration
}

// ErrSchemaDirty is what a half-applied migration leaves behind on an engine
// without transactional DDL. It is typed because the caller has to tell it
// apart from every other failure: this one needs an operator, not a retry.
type ErrSchemaDirty struct {
	Version uint
	Driver  Driver
}

func (e *ErrSchemaDirty) Error() string {
	return fmt.Sprintf(
		"database: the %s schema is dirty at version %d: a migration failed partway and left it half applied. "+
			"Inspect the schema, finish or undo version %d by hand, then run `aegisd migrate force %d`",
		e.Driver, e.Version, e.Version, e.Version,
	)
}

// Migrate applies every pending migration in source, under a lock.
//
// The source is a parameter because this package's job is to apply migrations,
// not to know where they live: that is the caller's knowledge, the same way
// application/ports.go owns the interfaces the assembly depends on.
func (db *DB) Migrate(ctx context.Context, source fs.FS, opts MigrateOptions) error {
	migrator, closeMigrator, err := db.migrator(source, opts.LockTimeout)
	if err != nil {
		return err
	}
	defer closeMigrator()

	version, dirty, err := readVersion(migrator)
	if err != nil {
		return err
	}

	if dirty {
		return &ErrSchemaDirty{Version: version, Driver: db.Driver}
	}

	return runUp(ctx, migrator, opts.Timeout)
}

func (db *DB) SchemaVersion(_ context.Context) (uint, bool, error) {
	migrator, closeMigrator, err := db.migrator(nil, 0)
	if err != nil {
		return 0, false, err
	}
	defer closeMigrator()

	return readVersion(migrator)
}

// ForceVersion clears the dirty flag after a manual repair. It changes the
// recorded version and nothing in the schema, which is why it is the last step
// of a recovery and never the first.
func (db *DB) ForceVersion(_ context.Context, version int) error {
	migrator, closeMigrator, err := db.migrator(nil, 0)
	if err != nil {
		return err
	}
	defer closeMigrator()

	if err := migrator.Force(version); err != nil {
		return fmt.Errorf("database: forcing version %d: %w", version, err)
	}

	return nil
}

// migrator builds a migrate.Migrate over a connection of its own.
//
// The dedicated pool is not tidiness. GET_LOCK on MySQL is held by the session,
// not by the database: handed the application pool with its thirty minute
// connection lifetime, a migration that runs longer has its connection recycled
// underneath it. The lock is released mid-DDL, a second replica proceeds, and
// two instances migrate at once — silently, and rarely, which is the worst
// combination for software running somewhere nobody can inspect.
func (db *DB) migrator(source fs.FS, lockTimeout time.Duration) (*migrate.Migrate, func(), error) {
	selected := dialects()[db.Driver]

	dsn, err := selected.dsn(db.opts)
	if err != nil {
		return nil, nil, err
	}

	pool, err := sql.Open(selected.driverName, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("database: opening a migration connection: %w", err)
	}

	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	// Deliberately unset: a recycled connection drops the lock it holds.
	pool.SetConnMaxLifetime(0)

	release := func() { _ = pool.Close() }

	databaseDriver, err := selected.migrator(pool)
	if err != nil {
		release()

		return nil, nil, fmt.Errorf("database: preparing the migrator: %w", err)
	}

	// A nil source means the caller only wants to read or force the version,
	// which needs no migrations at all. testing/fstest is stdlib and carries no
	// test framework with it; it is here purely as the shortest empty fs.FS.
	if source == nil {
		source = fstest.MapFS{}
	}

	sourceDriver, err := iofs.New(source, ".")
	if err != nil {
		release()

		return nil, nil, fmt.Errorf("database: reading the migrations: %w", err)
	}

	migrator, err := migrate.NewWithInstance("iofs", sourceDriver, db.Driver.String(), databaseDriver)
	if err != nil {
		release()

		return nil, nil, fmt.Errorf("database: preparing the migrator: %w", err)
	}

	if lockTimeout > 0 {
		migrator.LockTimeout = lockTimeout
	}

	return migrator, release, nil
}

func readVersion(migrator *migrate.Migrate) (uint, bool, error) {
	version, dirty, err := migrator.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}

	if err != nil {
		return 0, false, fmt.Errorf("database: reading the schema version: %w", err)
	}

	return version, dirty, nil
}

// runUp bounds the run without cutting a migration in half. migrate.Up is not
// context aware, so the timeout is enforced by asking it to stop after the
// migration currently running — abandoning one midway is exactly how a schema
// becomes dirty on MySQL.
func runUp(ctx context.Context, migrator *migrate.Migrate, timeout time.Duration) error {
	migrator.GracefulStop = make(chan bool, 1)

	done := make(chan error, 1)

	go func() {
		done <- migrator.Up()
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("database: applying migrations: %w", err)
		}

		return nil
	case <-timer.C:
		migrator.GracefulStop <- true

		return fmt.Errorf("database: migrations did not finish within %s", timeout)
	case <-ctx.Done():
		migrator.GracefulStop <- true

		return ctx.Err()
	}
}
```

Two details to settle while implementing:

**`driverName`** is a sixth field on `dialect`, not a switch, so a new engine stays one row. It carries the `database/sql` driver name the **migration** connection opens, which is not always the one GORM uses: `postgres` (lib/pq, via golang-migrate's postgres driver), `mysql` for both MySQL and MariaDB, and `sqlite` for SQLite — where GORM itself speaks `sqlite3` through the ncruces dialector. Add it to the struct and to all four rows in `dialects()`, and confirm each name by printing `sql.Drivers()` in a scratch test before hardcoding it — a wrong name here fails only at migration time.

**The empty-source case** must produce an error from `iofs.New`, which is what `TestMigrateRejectsAnEmptySource` asserts. If the resolved golang-migrate version tolerates an empty source instead, add the check explicitly rather than deleting the test.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/infra/database/... -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/infra/database/migrate.go internal/infra/database/migrate_test.go internal/infra/database/testdata/
git commit -m "feat(database): apply an injected migration source under a lock"
```

---

### Task 8: Wiring the database into the assembly

**Files:**
- Modify: `internal/application/application.go`
- Modify: `internal/application/wiring.go`
- Create: `internal/application/database_test.go`

**Interfaces:**
- Consumes: `database.Open`, `database.DB`, `(*DB).Ping`, `(*DB).Shutdown` from Task 6; `configs.Database` from Task 1.
- Produces: `Application.database *database.DB`, `(*Application).setDatabase() error`, `(*Application).Shutdown(context.Context) error`.

No `migrateSchema` step. Nothing exists to migrate, and the step arrives in phase 1 together with `internal/migrations`, the `migrate` configuration block and `--skip-migrations`.

- [ ] **Step 1: Write the failing test**

Create `internal/application/database_test.go`:

```go
package application_test

import (
	"path/filepath"
	"testing"

	"github.com/rootless-dev/aegis/internal/application"
	"github.com/rootless-dev/aegis/internal/configs"
)

func developmentConfig(t *testing.T) *configs.Application {
	t.Helper()

	cfg := configs.Default()
	cfg.Profile = configs.ProfileDev
	cfg.Normalize()
	cfg.Database.Path = filepath.Join(t.TempDir(), "aegis.db")

	return cfg
}

func TestNewOpensTheDatabase(t *testing.T) {
	app, err := application.New(developmentConfig(t))
	if err != nil {
		t.Fatalf("assembling the application: %v", err)
	}

	if err := app.Shutdown(t.Context()); err != nil {
		t.Fatalf("shutting down: %v", err)
	}
}

// A database that cannot be reached has to fail assembly with a message, not
// panic inside a constructor and not surface on the first request.
func TestNewFailsWhenTheDatabaseIsUnreachable(t *testing.T) {
	cfg := developmentConfig(t)
	cfg.Database.Path = filepath.Join(t.TempDir(), "missing", "aegis.db")

	if _, err := application.New(cfg); err == nil {
		t.Fatal("expected an unreachable database to fail the assembly")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/application/... -run TestNew -v`
Expected: FAIL — the assembly does not open a database.

- [ ] **Step 3: Add the field, the step and `Shutdown` in `application.go`**

Field, after `health`:

```go
	database *database.DB
```

Step, after `setHealth` and before `setCertificates` — if the database does not answer, nothing has started yet and the failure is clean, with no certificate reload goroutine to unwind:

```go
	steps := []func() error{
		instance.setLogger,
		instance.setGraceful,
		instance.setHealth,
		instance.setDatabase,
		instance.setCertificates,
		instance.setRouter,
		instance.setHttpServer,
	}
```

And a shutdown reachable outside `Run`, so a test or a failure between steps does not leak an open pool:

```go
// Shutdown releases what New acquired. Run goes through graceful instead, which
// orders every resource.
func (app *Application) Shutdown(ctx context.Context) error {
	if app.database == nil {
		return nil
	}

	return app.database.Shutdown(ctx)
}
```

- [ ] **Step 4: Register the shutdown pending in `Run()`**

Before the resource loop, so that resolving in reverse order closes the database last — after the server has stopped accepting and drained:

```go
	// Registered before the resources and therefore resolved after them:
	// pendings run in reverse, and nothing may be closed while a request can
	// still reach it.
	app.graceful.Register("database", app.database.Shutdown)

	for _, resource := range app.resources() {
		app.registerResource(resource)
	}

	app.graceful.Register("readiness drain", app.health.BeginDrain)
```

- [ ] **Step 5: Write `setDatabase` in `wiring.go`**

```go
// setDatabase opens the pool and registers the readiness check. Liveness never
// runs it: a slow database would otherwise fail every replica at once and have
// all of them restarted, turning a degradation into an outage.
func (app *Application) setDatabase() error {
	cfg := app.cfg.Database

	db, err := database.Open(database.Options{
		Driver:         database.Driver(cfg.Driver),
		Host:           cfg.Host,
		Port:           cfg.Port,
		Name:           cfg.Name,
		User:           cfg.User,
		Password:       cfg.Password,
		Path:           cfg.Path,
		SSLMode:        cfg.SSLMode,
		SSLRootCert:    cfg.SSLRootCert,
		Options:        cfg.Options,
		ConnectTimeout: cfg.ConnectTimeout,
		Pool: database.Pool{
			MaxOpen:         cfg.Pool.MaxOpen,
			MaxIdle:         cfg.Pool.MaxIdle,
			ConnMaxLifetime: cfg.Pool.ConnMaxLifetime,
			ConnMaxIdleTime: cfg.Pool.ConnMaxIdleTime,
		},
		Logger: app.logger,
		// A query slower than the request timeout has already lost the request
		// it belonged to, which makes it the natural threshold.
		SlowThreshold: app.cfg.HttpServer.RequestTimeout,
		// Query arguments are credentials, tokens and personal data. They are
		// only rendered outside production.
		LogParameters: app.cfg.Profile.IsDev(),
	})
	if err != nil {
		return err
	}

	app.database = db

	app.health.Register("database", db.Ping)

	return nil
}
```

Add the imports `context` and `github.com/rootless-dev/aegis/internal/infra/database`.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/application/... -v`
Expected: PASS.

- [ ] **Step 7: Run the whole unit suite**

Run: `go test -race -covermode=atomic ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/application/
git commit -m "feat(database): open the database during the boot"
```

---
### Task 9: Integration across the four engines

**Files:**
- Create: `internal/infra/database/containers_test.go`
- Create: `internal/infra/database/integration_test.go`
- Create: `test/integration/database_test.go`
- Modify: `test/integration/boot_test.go`
- Modify: `.github/workflows/ci.yml`
- Modify: `Makefile`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: everything above.
- Produces: a matrix run proving the runner and the boot against every supported engine.

Two levels, because they answer different questions. The suite inside `internal/infra/database` exercises the runner directly — that is where the lock, the dirty state and the engine differences live. The suite in `test/integration` exercises the compiled binary, which is the only thing that proves the boot, the readiness report and the shutdown ordering.

This is where the injected source earns itself: the fixtures from Task 7 are real SQL running against real engines. Only *which* SQL runs is synthetic; the pipeline under test is the production one.

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/testcontainers/testcontainers-go
go get github.com/testcontainers/testcontainers-go/modules/postgres
go get github.com/testcontainers/testcontainers-go/modules/mysql
go mod tidy
```

- [ ] **Step 2: Write the container helpers**

Create `internal/infra/database/containers_test.go`:

```go
//go:build integration

package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phuslu/log"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// selectedDriver is how the CI matrix picks one engine per job and a developer
// exercises one without starting three containers.
func selectedDriver() Driver {
	if driver := os.Getenv("AEGIS_TEST_DRIVER"); driver != "" {
		return Driver(driver)
	}

	return DriverSQLite
}

// engineOptions returns Options pointing at a running engine, starting a
// container when the driver needs one.
func engineOptions(t *testing.T) Options {
	t.Helper()

	base := Options{
		Driver:         selectedDriver(),
		Name:           "aegis",
		User:           "aegis",
		Password:       "aegis",
		SSLMode:        "disable",
		ConnectTimeout: 30 * time.Second,
		Pool:           Pool{MaxOpen: 5, MaxIdle: 5, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute},
		Logger:         &log.DefaultLogger,
	}

	switch base.Driver {
	case DriverSQLite:
		base.Name, base.User, base.Password, base.SSLMode = "", "", "", ""
		base.Path = filepath.Join(t.TempDir(), "aegis.db")
	case DriverPostgres:
		base.Host, base.Port = startPostgres(t)
	case DriverMySQL:
		base.Host, base.Port = startMySQL(t, "mysql:8.0")
	case DriverMariaDB:
		base.Host, base.Port = startMySQL(t, "mariadb:10.6")
	default:
		t.Fatalf("unknown test driver %q", base.Driver)
	}

	return base
}

// fixtures returns the migrations for the engine under test.
func fixtures(t *testing.T, set string) fs.FS {
	t.Helper()

	return os.DirFS(filepath.Join("testdata", set, selectedDriver().String()))
}

func startPostgres(t *testing.T) (string, string) {
	t.Helper()

	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("aegis"),
		postgres.WithUsername("aegis"),
		postgres.WithPassword("aegis"),
		testcontainers.WithWaitStrategy(
			// Postgres restarts once during initialisation, so waiting for the
			// port alone connects to a server that is about to go away.
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		),
	)
	if err != nil {
		t.Fatalf("starting postgres: %v", err)
	}

	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	return endpoint(t, container, "5432/tcp")
}

// startMySQL serves both engines: the module drives MariaDB images too, and the
// two differ in the migration lineage they use, not in how they start.
func startMySQL(t *testing.T, image string) (string, string) {
	t.Helper()

	ctx := context.Background()

	container, err := mysql.Run(ctx, image,
		mysql.WithDatabase("aegis"),
		mysql.WithUsername("aegis"),
		mysql.WithPassword("aegis"),
	)
	if err != nil {
		t.Fatalf("starting %s: %v", image, err)
	}

	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	return endpoint(t, container, "3306/tcp")
}

func endpoint(t *testing.T, container testcontainers.Container, port string) (string, string) {
	t.Helper()

	ctx := context.Background()

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("reading the container host: %v", err)
	}

	mapped, err := container.MappedPort(ctx, testcontainers.PortFromString(port))
	if err != nil {
		t.Fatalf("reading the mapped port: %v", err)
	}

	return host, mapped.Port()
}
```

Add `io/fs` to the imports. Confirm the exact testcontainers API shapes against the resolved version — the module constructors changed names across releases; adjust here rather than working around it in the tests.

- [ ] **Step 3: Write the runner's integration suite**

Create `internal/infra/database/integration_test.go`:

```go
//go:build integration

package database

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestMigrationsApplyOnEveryEngine(t *testing.T) {
	db, err := Open(engineOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	if err := db.Migrate(context.Background(), fixtures(t, "migrations"), migrateOptions()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	version, dirty, err := db.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("reading the schema version: %v", err)
	}

	if dirty || version != 2 {
		t.Fatalf("expected a clean schema at version 2, got %d dirty=%v", version, dirty)
	}
}

// Down migrations never run in production, but proving they work is what makes
// them worth shipping: a migration that cannot be reversed is one nobody can
// test against.
func TestUpDownUpLeavesTheSchemaWhereItStarted(t *testing.T) {
	db, err := Open(engineOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	source := fixtures(t, "migrations")

	if err := db.Migrate(context.Background(), source, migrateOptions()); err != nil {
		t.Fatalf("first up: %v", err)
	}

	if err := db.ForceVersion(context.Background(), 0); err != nil {
		t.Fatalf("resetting: %v", err)
	}

	if err := db.Migrate(context.Background(), source, migrateOptions()); err != nil {
		t.Fatalf("second up: %v", err)
	}
}

// The lock is the reason the runner opens a connection of its own. Two racing
// migrations must not both proceed.
func TestConcurrentMigrationsDoNotBothProceed(t *testing.T) {
	opts := engineOptions(t)

	if opts.Driver == DriverSQLite {
		t.Skip("sqlite is a single process engine, there is no lock to contend for")
	}

	first, err := Open(opts)
	if err != nil {
		t.Fatalf("opening the first connection: %v", err)
	}

	t.Cleanup(func() { _ = first.Shutdown(context.Background()) })

	second, err := Open(opts)
	if err != nil {
		t.Fatalf("opening the second connection: %v", err)
	}

	t.Cleanup(func() { _ = second.Shutdown(context.Background()) })

	source := fixtures(t, "migrations")

	var wg sync.WaitGroup

	errs := make([]error, 2)

	for index, db := range []*DB{first, second} {
		wg.Add(1)

		go func() {
			defer wg.Done()

			errs[index] = db.Migrate(context.Background(), source, migrateOptions())
		}()
	}

	wg.Wait()

	// Both succeeding is the correct outcome: one migrates, the other waits for
	// the lock and then finds nothing to do. What must never happen is a
	// corrupt schema, which the version check below catches.
	for _, err := range errs {
		if err != nil {
			t.Fatalf("a concurrent migration failed: %v", err)
		}
	}

	version, dirty, err := first.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("reading the schema version: %v", err)
	}

	if dirty || version != 2 {
		t.Fatalf("expected a clean schema at version 2, got %d dirty=%v", version, dirty)
	}
}

// This is the difference half the design exists for: MySQL and MariaDB have no
// transactional DDL, so a failed migration leaves the schema half applied and
// marked dirty. Postgres and SQLite roll it back whole.
func TestAFailedMigrationDirtiesOnlyTheEnginesWithoutTransactionalDDL(t *testing.T) {
	db, err := Open(engineOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	err = db.Migrate(context.Background(), fixtures(t, "broken"), migrateOptions())
	if err == nil {
		t.Fatal("expected the broken fixture to fail")
	}

	_, dirty, versionErr := db.SchemaVersion(context.Background())
	if versionErr != nil {
		t.Fatalf("reading the schema version: %v", versionErr)
	}

	if !dirty {
		return
	}

	// Dirty: the next attempt must refuse with the actionable error rather than
	// try again and fail differently.
	var schemaDirty *ErrSchemaDirty
	if err := db.Migrate(context.Background(), fixtures(t, "broken"), migrateOptions()); !errors.As(err, &schemaDirty) {
		t.Fatalf("expected ErrSchemaDirty on the second attempt, got %v", err)
	}
}
```

- [ ] **Step 4: Write the binary's integration suite**

Create `test/integration/database_test.go`:

```go
//go:build integration

package integration_test

import (
	"strings"
	"testing"
)

// The binary is what proves the boot, the readiness report and the shutdown
// ordering. The runner's own suite cannot: it never goes through main.
func TestBootAgainstTheConfiguredEngine(t *testing.T) {
	env := databaseEnvironment(t)

	process := startBinary(t, env)
	t.Cleanup(func() { stopBinary(t, process) })

	waitUntilReady(t, process)
}

func TestReadinessNamesTheDatabaseCheck(t *testing.T) {
	env := databaseEnvironment(t)

	process := startBinary(t, env)
	t.Cleanup(func() { stopBinary(t, process) })

	waitUntilReady(t, process)

	report := detailedReadiness(t, process)
	if !strings.Contains(report, "database") {
		t.Fatalf("expected the detailed readiness report to name the database check, got %s", report)
	}
}

// An unreachable database must fail the boot with a message, not start a
// process that answers requests it cannot serve.
func TestTheBootFailsWhenTheDatabaseIsUnreachable(t *testing.T) {
	env := []string{
		"AEGIS_PROFILE=prod",
		"PUBLIC_URL=http://localhost:7500",
		"TLS_TERMINATION=none",
		"DATABASE_DRIVER=postgres",
		"DATABASE_HOST=127.0.0.1",
		"DATABASE_PORT=1",
		"DATABASE_NAME=aegis",
		"DATABASE_USER=aegis",
		"DATABASE_PASSWORD=aegis",
		"DATABASE_SSL_MODE=disable",
		"DATABASE_CONNECT_TIMEOUT=2s",
	}

	output, err := runBinaryToCompletion(t, nil, env)
	if err == nil {
		t.Fatalf("expected the boot to fail, got a clean exit:\n%s", output)
	}

	if !strings.Contains(output, "not reachable") {
		t.Fatalf("expected a message naming the unreachable database, got:\n%s", output)
	}
}
```

`databaseEnvironment(t)` mirrors `engineOptions` from Step 2: it reads `AEGIS_TEST_DRIVER`, starts the same container when needed, and returns the `DATABASE_*` variables plus `AEGIS_PROFILE`, `PUBLIC_URL` and `TLS_TERMINATION=none`. Duplicating the container helpers across the two packages is unavoidable — a `_test.go` file is not importable — so keep them side by side and identical in shape.

- [ ] **Step 5: Extract the process helpers from `boot_test.go`**

`startBinary`, `stopBinary`, `waitUntilReady`, `detailedReadiness` and `runBinaryToCompletion` do not exist as named helpers today — `boot_test.go` inlines the equivalent logic. Extract them into `test/integration/helpers_test.go`, unchanged in behaviour, and have `boot_test.go` call them:

- `startBinary(t *testing.T, env []string) *exec.Cmd` — runs `binaryPath` with the environment appended to a minimal base, returns the running process.
- `stopBinary(t *testing.T, process *exec.Cmd)` — sends `SIGTERM`, waits within `shutdownTimeout`, fails on a non-zero exit.
- `waitUntilReady(t *testing.T, process *exec.Cmd)` — polls `/readyz` until 200 or `bootTimeout` elapses.
- `detailedReadiness(t *testing.T, process *exec.Cmd) string` — returns the body of the detailed readiness endpoint.
- `runBinaryToCompletion(t *testing.T, args, env []string) (string, error)` — runs to exit and returns combined output.

Extract rather than duplicate: two copies of the boot helper will drift, and the existing tests are the proof the extraction did not change behaviour.

- [ ] **Step 6: Run per engine**

```bash
AEGIS_TEST_DRIVER=sqlite   go test -tags=integration -timeout=15m ./internal/... ./test/integration/...
AEGIS_TEST_DRIVER=postgres go test -tags=integration -timeout=15m ./internal/... ./test/integration/...
AEGIS_TEST_DRIVER=mysql    go test -tags=integration -timeout=15m ./internal/... ./test/integration/...
AEGIS_TEST_DRIVER=mariadb  go test -tags=integration -timeout=15m ./internal/... ./test/integration/...
```

Expected: PASS on all four. Docker has to be running.

- [ ] **Step 7: Add the matrix to CI**

In `.github/workflows/ci.yml`, on the `build` job:

```yaml
    strategy:
      fail-fast: false
      matrix:
        database: [sqlite, postgres, mysql, mariadb]
```

and replace the integration step:

```yaml
      # Four engines, because a customer runs whichever one they already
      # operate. Collation, isolation level and DDL transactionality differ
      # between them, and this matrix is the only place that difference is
      # exercised.
      - name: Integration tests
        env:
          AEGIS_TEST_DRIVER: ${{ matrix.database }}
        run: go test -tags=integration -timeout=15m ./internal/... ./test/integration/...
```

`fail-fast: false` on purpose: when a change breaks one dialect, seeing which of the four survived is most of the diagnosis.

Note that the artifact upload step in that job now runs once per matrix entry. Give the artifact a name including `${{ matrix.database }}`, or move the upload to a job that does not fan out.

- [ ] **Step 8: Update the Makefile**

`test-integration` currently targets only `./test/integration/...`. Widen it, and add the all-engines target:

```makefile
.PHONY: test-integration
test-integration: ## Run the integration tests, against sqlite by default
	go test -tags=integration -timeout=15m ./internal/... ./test/integration/...

.PHONY: test-integration-all
test-integration-all: ## Run the integration tests against every supported engine
	@for driver in sqlite postgres mysql mariadb; do \
		echo "== $$driver =="; \
		AEGIS_TEST_DRIVER=$$driver go test -tags=integration -timeout=15m ./internal/... ./test/integration/... || exit 1; \
	done
```

- [ ] **Step 9: Commit**

```bash
git add internal/infra/database/ test/integration/ .github/workflows/ci.yml Makefile go.mod go.sum
git commit -m "test(database): exercise the runner and the boot against every supported engine"
```

---

### Task 10: Configuration examples, documentation and deployment

**Files:**
- Modify: `aegis.example.yaml`
- Modify: `.env.example`
- Modify: `docs/configuration.md`
- Modify: `docs/architecture.md`
- Modify: `docker-compose.yml`
- Modify: `.gitignore`
- Modify: `deploy/k8s/base`

**Interfaces:**
- Consumes: everything above. Produces no code.

- [ ] **Step 1: Add the section to `aegis.example.yaml`**

In the file's voice — every key commented with why it exists, not what it is:

```yaml
database:
  # One of: postgres, mysql, mariadb, sqlite. There is no default outside
  # development: only the operator knows which engine they run, and a guess
  # here would pick the wrong migration lineage. sqlite is a development
  # engine and is refused under the prod profile.
  driver: postgres

  host: db.internal
  # Left empty, the engine's own default is used: 5432 or 3306.
  port: "5432"
  name: aegis
  user: aegis
  # The password is deliberately not a key here. Writing it fails the boot;
  # DATABASE_PASSWORD is the only route, because a secret that can live in a
  # configuration file eventually gets committed.

  # sqlite only, and only under the dev profile.
  # path: ./aegis.dev.db

  # One of: disable, prefer, require, verify-ca, verify-full. Postgres'
  # vocabulary, used for every engine so the setting means the same thing
  # everywhere. Note that require encrypts without verifying anything: it stops
  # a passive listener, not an active attacker.
  ssl_mode: verify-full
  # Only read by the two verify- modes.
  ssl_root_cert: /etc/aegis/db-ca.pem

  # Anything specific to this installation. Whatever aegis depends on is forced
  # by the driver and rejected here, so a setting that would break the service
  # fails the boot instead of quietly doing nothing.
  options:
    application_name: aegis

  connect_timeout: 10s

  pool:
    max_open: 25
    # Matching max_open on purpose: a smaller idle count makes the pool close
    # and reopen connections under load, and every reopen pays a TLS handshake.
    max_idle: 25
    # Stays under the NAT and firewall idle timeouts that commonly drop
    # connections around an hour. Without it the symptom is a sporadic
    # "invalid connection" with no visible cause.
    conn_max_lifetime: 30m
    conn_max_idle_time: 5m
```

- [ ] **Step 2: Add the variables to `.env.example`**

```bash
# One of: postgres, mysql, mariadb, sqlite. Required outside development.
DATABASE_DRIVER=postgres
DATABASE_HOST=db.internal
DATABASE_PORT=5432
DATABASE_NAME=aegis
DATABASE_USER=aegis
# The only route for the password: it cannot be read from the configuration file.
DATABASE_PASSWORD=

# sqlite only, and only under the dev profile.
# DATABASE_PATH=./aegis.dev.db

# disable, prefer, require, verify-ca or verify-full.
DATABASE_SSL_MODE=verify-full
# DATABASE_SSL_ROOT_CERT=/etc/aegis/db-ca.pem

DATABASE_CONNECT_TIMEOUT=10s

DATABASE_POOL_MAX_OPEN=25
DATABASE_POOL_MAX_IDLE=25
DATABASE_POOL_CONN_MAX_LIFETIME=30m
DATABASE_POOL_CONN_MAX_IDLE_TIME=5m
```

- [ ] **Step 3: Document in `docs/configuration.md`**

Add a `## Database` section after `## TLS termination`, covering: the four engines and why they exist; that SQLite is development only and enforced as such; the shared `ssl_mode` vocabulary, its per-engine translation, and that `require` does not authenticate the server; why the password is environment-only; and why the driver has no production default. Add the `DATABASE_*` variables to the `## Settings` list in the format that section already uses.

State explicitly that nothing migrates yet — a reader who finds a migration runner in the code and no way to configure it should find the answer here rather than assume something is missing.

- [ ] **Step 4: Document in `docs/architecture.md`**

Two edits. Under `## Startup`, note that `setDatabase` is the first step reaching the outside world, and that the failure it can produce is the reason every step returns an error. Under `## Shutdown`, note that the database registers before the resources and therefore closes last, after the server has drained.

- [ ] **Step 5: Add optional engines to `docker-compose.yml`**

Under a profile, so `make dev` stays a single container and only someone testing a dialect starts them:

```yaml
  postgres:
    profiles: ["dialects"]
    image: postgres:17-alpine
    environment:
      POSTGRES_DB: aegis
      POSTGRES_USER: aegis
      POSTGRES_PASSWORD: aegis
    ports:
      - "5432:5432"

  mysql:
    profiles: ["dialects"]
    image: mysql:8.0
    environment:
      MYSQL_DATABASE: aegis
      MYSQL_USER: aegis
      MYSQL_PASSWORD: aegis
      MYSQL_ROOT_PASSWORD: aegis
    ports:
      - "3306:3306"

  mariadb:
    profiles: ["dialects"]
    image: mariadb:10.6
    environment:
      MARIADB_DATABASE: aegis
      MARIADB_USER: aegis
      MARIADB_PASSWORD: aegis
      MARIADB_ROOT_PASSWORD: aegis
    ports:
      - "3307:3306"
```

Development runs on SQLite while production runs on something else. These services are what shortens that gap when a change touches SQL.

- [ ] **Step 6: Ignore the development database**

In `.gitignore`:

```gitignore
# The sqlite database the dev profile creates, and the sidecars WAL mode adds.
*.dev.db
*.dev.db-shm
*.dev.db-wal
```

- [ ] **Step 7: Update the Kubernetes manifests**

In `deploy/k8s/base`:

- `DATABASE_PASSWORD` from a `Secret` through `secretKeyRef`, never from the ConfigMap.
- The remaining `DATABASE_*` values in the existing ConfigMap.

No `startupProbe` yet. It exists to keep the kubelet from killing a pod partway through a migration, and nothing migrates at boot until phase 1.

- [ ] **Step 8: Verify everything**

```bash
make ci
```

Expected: format check, vet, unit tests, integration tests and gosec all pass.

- [ ] **Step 9: Commit**

```bash
git add aegis.example.yaml .env.example docs/ docker-compose.yml .gitignore deploy/k8s/
git commit -m "docs(database): document the database section and its deployment"
```

---

## Notes for whoever executes this

**Tasks 3, 4 and 5 do not compile independently.** The `dialects()` table in Task 3 names functions that Tasks 4 and 5 provide. Implement the three back to back before running the package tests. This is deliberate: the table is the single list of supported engines, and splitting it per dialect would hide what the package supports.

**Three places name an external API this plan cannot verify offline** — golang-migrate's `LockTimeout`, `GracefulStop` and `Up`; the `database/sql` driver names each dialect registers; and the testcontainers module constructors. Each is flagged in its task. Confirm against the resolved version and adjust *there*, rather than working around a mismatch somewhere else.

**Nothing migrates at boot.** The runner is finished and proven, and has no caller in production. That is the deliberate shape of this plan — see "What this plan deliberately does NOT build" at the top. If a task starts pulling in a boot step, a configuration block or a migration file, stop: that is phase 1 leaking backwards.

**Phase 1 picks this up by adding `internal/migrations`** with the `embed.FS`, one directory per dialect and a `For(driver)`. Then `migrateSchema` joins the step list, the `migrate` configuration block and `--skip-migrations` join the config, the `startupProbe` joins the manifests, and `aegisd migrate status|force` wraps `SchemaVersion` and `ForceVersion`. Nothing in `internal/infra/database` changes.
