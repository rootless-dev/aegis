# Database connection

Design for the persistence layer's connection and its migration runner. Written
2026-08-20.

Persistence is the prerequisite the roadmap names for phase 1. This document
covers getting a connection open, validated and pooled, and the machinery that
will apply migrations. It does not cover the schema, and deliberately ships no
migration at all.

## Scope

In: the `database` configuration section, the driver factory, DSN construction,
database TLS, GORM's logger, connection pooling, health registration, shutdown
ordering, the migration runner, and the test strategy across four dialects.

Out, and listed under Deferred decisions: the schema, repositories, the mapping
between GORM models and `domain`, the `internal/migrations` package that will
own the SQL, the boot step that applies it, and the recovery command.

**Why the runner ships without migrations.** The runner is where the technical
risk lives — the lock, the dirty state, four different migration drivers — and
proving it against real engines now is cheaper than discovering it in the middle
of designing the domain. But there is no schema yet, and inventing a table so
the pipeline has something to migrate would be reasoning backwards from the
compiler instead of forwards from the product. So the runner is written and
tested, and it takes its migrations as a parameter.

## Why four drivers

Aegis is installed on the customer's premises and runs against the database they
already operate. Postgres, MySQL, MariaDB and SQLite are therefore product
requirements, not convenience, and the cost of dialectal SQL is accepted from
phase 1 rather than discovered in phase 2.

The consequence that outlives every other decision here: **the schema will be
public interface**. Once a customer has migrated, a column type cannot be
changed remotely, and a migration that was wrong ships to installations nobody
can reach. That clock starts at the first installed release, not at the first
commit — which is why the schema is worth designing deliberately rather than
improvising one to unblock this work.

SQLite is supported through `ncruces/go-sqlite3/gormlite`, which runs SQLite on
an embedded WebAssembly VM. The canonical driver requires cgo, and the
production image is `CGO_ENABLED=0` on `distroless/static`.

The choice of *which* pure-Go driver is not free, and the reason is worth
recording. `golang-migrate`'s `database/sqlite` blank-imports
`modernc.org/sqlite`, which registers itself with `database/sql` under the name
`sqlite`. The obvious GORM dialector, `glebarez/sqlite`, pulls in
`glebarez/go-sqlite` — a fork of that same code, under a different module path,
registering the same name. `sql.Register` panics on a duplicate name, so
linking both into one binary crashes at `init()`, before `main` runs.

`ncruces` registers as `sqlite3` instead, so the two coexist: GORM speaks to
the database through `sqlite3`, the migration runner opens its own connection
through `sqlite`. It is also the smallest of the three — 1.8 MB against
`glebarez`'s 5.7 MB — and the pair together costs the same 5.7 MB that
`glebarez` alone would have.

## Supported versions

Declared, validated at connect time, and documented. Without a floor there is no
way to know which SQL the migrations may use, and a customer on an older server
finds out through a syntax error in the middle of a migration rather than
through a message at startup.

| Engine | Minimum | Why this floor |
|---|---|---|
| Postgres | 13 | `gen_random_uuid()` without an extension; predictable `ON CONFLICT` |
| MySQL | 8.0 | CTEs, window functions, and `utf8mb4_0900` collations |
| MariaDB | 10.6 | stable `utf8mb4` behaviour and `RETURNING` where it helps |
| SQLite | bundled | the engine ships inside the binary, so there is no floor to state |

The version check also catches a mismatched declaration: an operator who
declares `mysql` and connects to MariaDB is refused, because the two will not
share a migration lineage.

## Configuration

`internal/configs/database.go`, in the anatomy every other section already
follows: struct with YAML tags, `defaultDatabase()`, `Validate(profile)` and an
entry in `sections()`.

```go
type Driver string

const (
    DriverPostgres Driver = "postgres"
    DriverMySQL    Driver = "mysql"
    DriverMariaDB  Driver = "mariadb"
    DriverSQLite   Driver = "sqlite"
)

type Database struct {
    Driver Driver `yaml:"driver"`

    // Server-backed drivers.
    Host     string `yaml:"host"`
    Port     string `yaml:"port"`
    Name     string `yaml:"name"`
    User     string `yaml:"user"`
    Password string `yaml:"-"`

    // SQLite only.
    Path string `yaml:"path"`

    SSLMode     string `yaml:"ssl_mode"`
    SSLRootCert string `yaml:"ssl_root_cert"`

    Options map[string]string `yaml:"options"`

    ConnectTimeout time.Duration `yaml:"connect_timeout"`

    Pool *Pool `yaml:"pool"`
}
```

There is no `migrate` block. Nothing applies migrations at boot yet, and a
configuration section with no consumer is a setting an operator can change to no
effect. It arrives with the boot step, in phase 1.

**SQLite gets its own field rather than overloading `Name`.** A file path is not
a database name, and writing `name: /var/lib/aegis/aegis.db` would be the kind
of overloading that reads fine to whoever wrote it and to nobody else.
Validation enforces the split in both directions, in the idiom `TLS.Validate`
already uses for a termination that does not serve TLS.

**The password is never decoded from YAML.** `yaml:"-"` combined with the
decoder's `KnownFields(true)` turns a password written in the file into a parse
error, which leaves `DATABASE_PASSWORD` as the only route. A secret that *can*
live in the configuration file will be committed by someone.

**`Driver` has no default in production**, for the same reason
`TLS.Termination` has none: empty is an operator who never said, and the boot
must fail with a message listing the options rather than guess.

**`Options` is a map rather than fields** so anything specific to the customer's
installation fits without being anticipated one setting at a time. Validation
rejects keys that collide with the forced parameters below; otherwise an
operator turns off `parseTime` without knowing it and the failure surfaces
several screens away.

### Normalize

Two things resolve only after every source has been layered, so they belong in
`Application.Normalize()` alongside the profile and the termination:

- `Driver` is case-folded and trimmed, for every source at once — `POSTGRES` in
  the environment and `postgres` in the file have to mean the same thing.
- Under the development profile only, an empty `Driver` becomes `sqlite` and an
  empty `Path` becomes `./aegis.dev.db`. This is what makes "SQLite in
  development only" a property of the structure rather than a matter of
  discipline. Production is left untouched and fails validation.

### Validation

`Validate(profile)` aggregates, like every other section, so one boot reports
every problem at once.

| Rule | Reason |
|---|---|
| `Driver` is one of the four | an unknown driver has no factory |
| `Driver` is not `sqlite` unless `profile.IsDev()` | SQLite is a development engine; in production it is an operator error, not a choice |
| server drivers: `Host`, `Name`, `User` are set | a DSN cannot be assembled without them |
| server drivers: `Path` is empty | a path set here means the operator thinks SQLite is in use |
| `sqlite`: `Path` is set, and `Host`, `Port`, `User`, `Password`, `SSLMode` are empty | same mismatch, from the other side |
| `SSLMode` is valid | one shared vocabulary, checked once |
| `SSLRootCert`, when set, requires an `SSLMode` that verifies | a CA file with `require` is a CA nobody checks |
| `Options` contains no forced key | otherwise the operator disables what the code depends on |
| `ConnectTimeout` is greater than zero | zero means wait forever, which turns an unreachable database into a hung boot |
| `Pool.MaxIdle` is not greater than `Pool.MaxOpen` | the pool would never reach the idle count, which reads as a configured value doing nothing |
| `Pool` durations and counts are greater than zero | |

### Defaults

| Setting | Default | Reason |
|---|---|---|
| `driver` | *(none in prod, `sqlite` in dev)* | forgetting it must fail, not guess |
| `path` | *(none in prod, `./aegis.dev.db` in dev)* | survives an `air` restart, unlike `:memory:` |
| `port` | *(per dialect, at DSN build time)* | 5432 and 3306 are knowledge that belongs to the dialect, not to the struct |
| `ssl_mode` | *(per dialect, at DSN build time)* | `prefer` on both: encrypted where available, without demanding a CA the customer may not have. Not a struct default, or the dev profile's SQLite would fail its own validation rule |
| `connect_timeout` | `10s` | |
| `pool.max_open` | `25` | |
| `pool.max_idle` | `25` | equal to `max_open`; see Pool |
| `pool.conn_max_lifetime` | `30m` | under the usual NAT and firewall idle timeouts |
| `pool.conn_max_idle_time` | `5m` | |

### Environment

`env_source.go` gains `applyDatabase(cfg.Database)`, called from `applyEnv` in
section order, with the same nil check as the others.

```
DATABASE_DRIVER      DATABASE_PATH             DATABASE_POOL_MAX_OPEN
DATABASE_HOST        DATABASE_SSL_MODE         DATABASE_POOL_MAX_IDLE
DATABASE_PORT        DATABASE_SSL_ROOT_CERT    DATABASE_POOL_CONN_MAX_LIFETIME
DATABASE_NAME        DATABASE_CONNECT_TIMEOUT  DATABASE_POOL_CONN_MAX_IDLE_TIME
DATABASE_USER
DATABASE_PASSWORD
```

`Options` has no environment equivalent. A map of arbitrary keys does not
survive the flat namespace of environment variables without inventing a
convention, and the settings that actually need per-instance adjustment already
have their own variable.

No new command line flag. `--skip-migrations` belongs with the boot step it
disables, and arrives with it.

## The factory

`internal/infra/database` imports nothing from `internal/configs`, exactly like
`certs`, `graceful` and `health`. It declares its own `Options` and its own
`Driver`, and `wiring.go` translates. This duplicates four constants; it is the
price of the dependency rule the architecture document states, and the same
price `Termination` already pays.

```
internal/infra/database/
  driver.go      Driver and the dialect table
  options.go     Options and Pool, independent of configs
  database.go    Open, DB, Ping, Shutdown
  logger.go      adapts GORM's logger onto the assembled one
  postgres.go    DSN, dialector, SSL translation, version floor, migration driver
  mysql.go       same, serving both MySQL and MariaDB
  sqlite.go      same, with the driver-name asymmetry documented
  pool.go        applies the limits to *sql.DB
  migrate.go     the runner, over an injected fs.FS
```

The supported dialects are one readable table rather than `init()` registration,
in the style of `sections()` and `resources()`:

```go
type dialect struct {
    dsn       func(Options) (string, error)
    dialector func(dsn string) gorm.Dialector
    pool      func(*Pool)
    version   func(context.Context, *sql.DB) error
    migrator  func(*sql.DB) (migratedb.Driver, error)
}
```

`Open` returns the three handles the assembly needs:

```go
type DB struct {
    Gorm   *gorm.DB
    SQL    *sql.DB
    Driver Driver
}
```

`SQL` is not a leaked abstraction but a necessity: GORM exposes neither `Close`
nor `Ping`, and both the ordered shutdown and the readiness check need the
`*sql.DB` underneath.

`Open` connects for real before returning — a `PingContext` bounded by
`ConnectTimeout`, then the version check — so an unreachable or unsupported
database is an initialization error with a clear message, which is why
`application.New` returns an error from every step.

### Forced DSN parameters

The DSN is assembled here rather than in `configs`, because this is the package
that knows *why* each parameter is required, and therefore the place where they
are injected with no way to turn them off.

| Dialect | Forced | What happens without it |
|---|---|---|
| MySQL, MariaDB | `parseTime=true` | every date column returns as `[]byte` and mapping breaks |
| MySQL, MariaDB | `loc=UTC`, `time_zone='+00:00'` | token and session expiry stored in the customer's server timezone |
| MySQL, MariaDB | `charset=utf8mb4` | characters outside the BMP fail to insert |
| MySQL, MariaDB | `sql_mode=STRICT_TRANS_TABLES` | silent truncation instead of an error |
| MySQL, MariaDB | `multiStatements=false` | a migration file with two statements could apply half of itself |
| Postgres | `TimeZone=UTC` | same timezone problem |
| SQLite | `_pragma=foreign_keys(1)` | foreign keys silently ignored, which is SQLite's default |
| SQLite | `_pragma=busy_timeout(5000)`, `journal_mode(WAL)` | immediate `database is locked` instead of a short wait |

### Database TLS

The three engines spell the same idea differently. **The configuration uses one
vocabulary and each dialect translates it**, so an operator who moves an
installation from Postgres to MySQL does not have to relearn the setting.

`ssl_mode` is one of `disable`, `prefer`, `require`, `verify-ca`, `verify-full`
— Postgres' vocabulary, because it is the one that already names all five
levels.

| `ssl_mode` | Postgres | MySQL, MariaDB |
|---|---|---|
| `disable` | `sslmode=disable` | `tls=false` |
| `prefer` | `sslmode=prefer` | `tls=preferred` |
| `require` | `sslmode=require` | `tls=skip-verify` |
| `verify-ca` | `sslmode=verify-ca` | registered config, chain verified, hostname not |
| `verify-full` | `sslmode=verify-full` | registered config with `ServerName` |

Note what `require` means and does not mean: on both engines it encrypts without
verifying anything, so it stops passive eavesdropping and not an active
attacker. Only the two `verify-` levels authenticate the server.

Postgres takes the CA file directly as `sslrootcert` in the DSN. MySQL does not:
a private CA requires building a `tls.Config` in Go, registering it with
`mysql.RegisterTLSConfig`, and referencing it by name in the DSN. That
registration lives in `mysql.go` and is the reason `SSLRootCert` is a
configuration field rather than an `Options` key — it cannot be expressed as a
DSN parameter at all.

A private CA on the database connection is ordinary in on-prem installations.
Discovering it is unsupported after shipping would mean a customer cannot
connect at all.

### GORM's logger

GORM writes through a logger interface of its own, and its default
implementation prints to standard output with `log.Printf`. Left alone, every
slow-query warning and every error it reports would bypass the structured
logging the rest of the service uses, arriving without the request id the
middleware chain propagates and unreadable to whatever collects logs at the
customer's site.

`logger.go` adapts `gormlogger.Interface` onto the `*log.Logger` already
assembled at boot. Query arguments stay out of the rendered SQL outside
development: in this service those arguments are credentials, tokens and
personal data.

## The migration runner

golang-migrate, over an `fs.FS` the caller provides:

```go
func (db *DB) Migrate(ctx context.Context, source fs.FS, opts MigrateOptions) error
func (db *DB) SchemaVersion(ctx context.Context) (uint, bool, error)
func (db *DB) ForceVersion(ctx context.Context, version int) error
```

**The source is a parameter, not an `embed.FS` inside this package.** The
package's job is to apply migrations; where they live is the caller's knowledge,
the same way `application/ports.go` declares `CertificateSource` because the
consumer is what knows the shape of its dependency. `internal/infra/certs` does
not know that interface exists, and `internal/infra/database` has no business
knowing a directory layout or a file naming convention either.

That is a better boundary regardless of the schema question. It also happens to
be what lets the runner exist before any migration does.

In phase 1, `internal/migrations` becomes the owner of the SQL: the
`embed.FS`, one directory per dialect, and a `For(driver)` returning the right
subtree. The assembly passes it in. Nothing in this package changes.

### The lock

golang-migrate's own, per driver: `pg_advisory_lock` on Postgres, `GET_LOCK` on
MySQL and MariaDB, nothing on SQLite, where a single process makes it moot.

**The migrator gets a dedicated `*sql.DB` with `MaxOpen(1)` and no
`ConnMaxLifetime`, opened and closed inside the call. Never the application
pool.** `GET_LOCK` is held by the session, not by the database. Handed the
application pool with its thirty minute connection lifetime, a migration that
runs longer has its connection recycled underneath it: the lock is released
mid-DDL, a second replica proceeds, and two instances migrate at once. It fails
silently and rarely, which is the worst combination for software running
somewhere nobody can inspect.

### Bounding a migration without cutting one in half

`migrate.Up` is not context aware. The timeout is enforced by asking it to stop
after the migration currently running, through golang-migrate's `GracefulStop` —
abandoning one midway is exactly how a schema becomes dirty on MySQL.

### MySQL has no transactional DDL

**Corrected 2026-08-20, after the integration suite disproved the original
claim.** This section previously said a failed migration leaves the schema dirty
only on MySQL and MariaDB, with Postgres and SQLite rolling back whole. That is
wrong, and the distinction matters because half this design is argued from it.

golang-migrate commits `SetVersion(target, true)` in a transaction of its own
*before* running the migration body, on every engine. So a failure marks
`schema_migrations.dirty` everywhere — Postgres and SQLite included — and
golang-migrate treats `dirty` as terminal, refusing every subsequent migration.

What actually differs between engines is the **schema underneath the flag**.
Postgres and SQLite have transactional DDL, so the failed migration's own
statements are rolled back: the flag is set but there is nothing to undo, and
recovery is `force` back to the previous version. MySQL and MariaDB have no
transactional DDL, so an `ALTER TABLE` that fails on its third statement leaves
the first two applied: the flag is set *and* someone has to inspect and repair
the schema by hand before forcing anything.

**Corrected again, same day, after a two-statement fixture tested the claim.**
The paragraph above still overstated it. A migration file cannot leave a
partially applied schema through this code path on any engine, because
`multiStatements=false` — which this design forces — makes MySQL and MariaDB
reject a two-statement file outright, before either statement runs. Postgres and
SQLite roll theirs back. The effect is absent everywhere.

So the one-statement-per-file rule does not *mitigate* partial application
through a migration file; combined with `multiStatements=false`, it makes that
route impossible. The rule earns its place by keeping legitimate migrations
compatible with the driver setting that closes the route.

What `SchemaDirtyError` can honestly promise an operator is therefore narrower
than first written: the migration failed and the version is marked dirty, so
nothing further will apply until someone looks. It does not promise there is
half-applied DDL to repair — on these engine versions there usually is not. The
recovery path is unchanged, and the error still names the command.

This premise fell twice in one day under test. Anything asserted here about
engine behaviour should be assumed unverified until a test in
`internal/infra/database/integration_test.go` backs it.

Once migrations run at boot, that means a half-failed migration leaves the
service unable to start, in a crash loop, at an installation nobody can reach.
Three requirements follow. The first is implemented now, because it constrains
the runner; the other two arrive with the boot step.

**One statement per migration file**, for MySQL and MariaDB. This is the
discipline that replaces the transaction the engine does not provide: a failure
either applied or did not. `multiStatements` stays off in the migration DSN, so
the driver itself rejects a file carrying two. When `internal/migrations`
exists, a CI test walks its MySQL and MariaDB directories and fails the build on
any file with more than one — the rule enforced where it still has a fix.
Postgres and SQLite are exempt: both have transactional DDL, and imposing the
rule there would multiply migrations for nothing.

**A typed, operational error**, implemented now. Before migrating, the runner
reads the version; a dirty schema means it does not attempt to migrate at all:

```go
type ErrSchemaDirty struct {
    Version uint
    Driver  Driver
}
```

The message names the version it stopped at, says the schema is partially
applied, and gives the recovery command — not a driver error leaking upward into
a support ticket.

**An escape hatch and a recovery command**, in phase 1. `--skip-migrations` and
`DATABASE_MIGRATE_ON_BOOT` let a pod crash-looping on a dirty schema start
*without* migrating, so the operator can inspect the database with the product
running. And `aegisd migrate status` / `aegisd migrate force <version>` is what
makes the typed error actionable — a message that names a command has to have a
command to name, otherwise the only remaining route is hand-editing
`schema_migrations` over SQL, in production, under pressure. `ForceVersion` is
implemented now so the command is a thin wrapper when it arrives.

There is deliberately no `up` command planned: once migrations run at boot, the
next boot applies them, and a second way to do it would be a second thing to
keep correct.

## Lifecycle

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

`setDatabase` sits before `setCertificates`: if the database does not answer,
nothing has started yet and the failure is clean, with no certificate reload
goroutine to unwind. Phase 1 inserts `migrateSchema` immediately after it, for
the same reason and with a separate message — "could not connect" and "could not
migrate" fail differently and deserve to be told apart.

### Shutdown

The database does not join `resources()`. That list is for things with a loop of
their own — a `Start()` returning an error channel — and a pool has none. Making
it satisfy `Resource` with a channel that never emits would be faking an
abstraction to fit a list.

It registers as a pending directly in `Run()`, following the precedent
`readiness drain` already sets, but **before** the resource loop:

```go
app.graceful.Register("database", app.database.Shutdown)

for _, resource := range app.resources() {
    app.registerResource(resource)
}

app.graceful.Register("readiness drain", app.health.BeginDrain)
```

Pendings resolve in reverse registration order, so the real sequence is
readiness drain, then the HTTP server stops accepting, then certificate reload,
then the database closes last — the order the architecture document already
promises.

One honest note on `sql.DB.Close()`: it takes no context and does not wait for
in-flight queries. It closes idle connections and marks busy ones to close on
release. That is safe here precisely because it runs after the server has
stopped and drained. If anything ever runs in the background after the server is
down, this assumption needs revisiting.

## Health

```go
app.health.Register("database", app.database.Ping)
```

Readiness only, never liveness. The architecture document already gives the
reason and this is literally the scenario it describes: a slow database failing
every replica at once and having all of them restarted, turning a degradation
into an outage. `Ping` honors the existing `CheckTimeout`.

## Pool

Defaults: `MaxOpen: 25`, `MaxIdle: 25`, `ConnMaxLifetime: 30m`,
`ConnMaxIdleTime: 5m`.

`MaxIdle` equals `MaxOpen` deliberately. With a lower idle count the pool closes
and reopens connections under load, and with TLS to the database every reopen
pays a handshake. `ConnMaxLifetime` at thirty minutes stays under the NAT and
firewall timeouts that commonly drop idle connections around sixty; without it
the customer sees sporadic `invalid connection` errors with no visible cause.

SQLite ignores all of it and is forced to `MaxOpen: 1`: writes serialise over the
whole file, and more connections produce only `database is locked`.

## Testing

Four dialects mean the test strategy is part of the design, not a detail left to
whoever implements it.

**Unit, no database.** DSN construction per dialect as a table test, including
that each forced parameter is present and that an `Options` key colliding with
one is rejected. Configuration validation as a table, in the shape
`configs/tls_test.go` already uses. The SQLite pool override. The `ssl_mode`
translation for each dialect.

**Integration, with real engines and real SQL.** This is where the injected
source earns itself: the test fixtures are ordinary migration files —
`CREATE TABLE`, `ALTER TABLE`, one that fails on purpose — run against Postgres,
MySQL, MariaDB and SQLite in containers. Only *which* SQL runs is synthetic; the
pipeline under test is the production one. What gets proven:

- migrations apply, and applying twice is a no-op
- up, down and up again leave the schema where it started
- two processes racing to migrate do not both proceed — the lock holds
- a migration that fails partway marks the schema dirty on every engine, and
  leaves a partially applied schema underneath it only where DDL is not
  transactional
- `ErrSchemaDirty` is returned rather than a driver error, and `ForceVersion`
  clears it
- the boot connects, answers readiness naming the database check, and shuts down
  in order

Containers come from `testcontainers-go` rather than the CI runner's `services:`
block, because the same command then works on a developer machine and in the
pipeline. The cost is honest and worth stating: running the integration suite
requires Docker.

**CI.** The integration job runs as a matrix over the four drivers, with
`fail-fast: false` — when a change breaks one dialect, seeing which of the four
survived is most of the diagnosis.

## Kubernetes

`deploy/k8s/base` needs `DATABASE_PASSWORD` sourced from a `Secret` through
`secretKeyRef`, never from the ConfigMap that carries the rest of the section,
and the remaining `DATABASE_*` variables in the existing ConfigMap.

The `startupProbe` belongs with the boot step, in phase 1: migrating at boot
means the process is not listening while DDL runs, and a `livenessProbe` at
default thresholds would kill the pod partway through — which on MySQL is
exactly how a dirty schema is born. Nothing migrates at boot yet, so nothing
needs it yet.

## Files this touches

Beyond the new package:

| File | Change |
|---|---|
| `internal/configs/application.go` | `Database` field, `Default()`, `sections()`, `Normalize()` |
| `internal/infra/configbuilder/env_source.go` | `applyDatabase` |
| `internal/application/wiring.go` | `setDatabase` |
| `internal/application/application.go` | the `database` field, the step, the shutdown pending |
| `aegis.example.yaml` | the `database` section, commented in the file's voice |
| `.env.example` | the `DATABASE_*` variables |
| `docs/configuration.md` | a `Database` entry under Settings |
| `docs/architecture.md` | the database in Startup and Shutdown |
| `docker-compose.yml` | optional `postgres`, `mysql` and `mariadb` services for testing dialects locally |
| `.gitignore` | `*.dev.db` and its WAL sidecars |
| `deploy/k8s/base` | Secret and ConfigMap entries |
| `.github/workflows/ci.yml` | driver matrix on the integration job |

## Consequences accepted

Recorded so they are not rediscovered as surprises.

- GORM was chosen over a SQL-first mapper. Its models carry ORM tags, so they
  will live in the repository layer and be mapped to and from `domain`, which
  preserves the rule that `domain` imports nothing beyond the standard library.
  The boilerplate of that mapping is the cost of keeping the rule.
- GORM's `AutoMigrate` is not used at all. It does not version, and an on-prem
  upgrade needs a schema history that is reproducible and auditable.
- The runner ships with no consumer. It is exercised only by tests until phase 1
  wires it into the boot. That is accepted because the lock, the dirty state and
  the four migration drivers are where the uncertainty is, and containers prove
  them more cheaply now than mid-domain-design.
- Development runs on SQLite while production runs on something else. Collation,
  isolation level and type affinity behave differently there, so the classes of
  bug that only appear on a real engine are invisible until CI runs the
  integration matrix. The optional compose services exist to shorten that gap
  when a change touches SQL.
- Rolling updates will make expand and contract a requirement for writing
  migrations: replicas of the previous release keep serving while a new one
  migrates, so every migration must be compatible with the code before it.
  `DROP COLUMN` and `RENAME` become two-release operations. This binds whoever
  writes the first real migration.

## Deferred decisions

Everything here is phase 1, and each is deliberate rather than forgotten.

- **The schema.** `realm` first, as the root of tenancy. It gets designed when it
  gets designed, not improvised to give the runner something to do.
- **Column type for UUID per dialect**: native `uuid` on Postgres, `BINARY(16)`
  or `CHAR(36)` on MySQL. This is public interface once a release is installed.
- **Collation forced in the DDL** — `utf8mb4_bin` on MySQL and MariaDB, with
  identifiers lowercased in the domain. Without it, `UNIQUE (realm_id, email)`
  means different things at different installations: MySQL's default collation
  is case and accent insensitive, so it treats `jose@x.com` and `josé@x.com` as
  the same identity while Postgres does not. This belongs to the migration that
  creates the first table.
- **`internal/migrations`**: the `embed.FS`, one directory per dialect, and
  `For(driver)`. MariaDB gets its own directory from the start despite sharing
  MySQL's dialector — with migration running at boot, adding it later would
  leave MariaDB installations having applied the `mysql` lineage, and the two
  could not be reconciled remotely.
- **Down migrations** ship with every migration and never run in production. The
  integration suite runs up, down and up again to prove reversibility; rolling a
  schema back against live customer data destroys rows, and recovery in
  production is forward-fix. Shipping a `down` command would offer an operator
  under pressure a button that reads like an undo and is not one.
- **The boot step**, `migrateSchema`, with its `migrate` configuration block,
  `--skip-migrations`, the `startupProbe`, and the `aegisd migrate status|force`
  recovery command.
- **The mapping between GORM models and `domain` entities**, and `realm_id` as
  an explicit parameter on every repository operation, so forgetting the tenant
  scope is a compile error rather than a leak between tenants.
