# Architecture

## Layout

```
cmd/aegisd             entry point: signals and exit code, nothing else
internal/application   assembly and lifecycle
internal/cli           subcommand dispatch for `aegisd migrate` and its verbs
internal/configs       configuration structs, defaults and validation
internal/domain/realm  the realm aggregate; imports nothing internal
internal/service       the use cases; declares the interfaces they consume
internal/repository    implements the service interfaces over GORM
internal/migrations    the embedded schema SQL, one directory per dialect
internal/handler/page  the handlers for the HTML surface
internal/http          server, middleware, response format
internal/http/assets   fingerprints and serves the static files
internal/http/render   composes layouts with pages and writes HTML, over an injected filesystem
internal/infra         logging, graceful shutdown, health, env parsing
internal/buildinfo     identity of the running binary
internal/templates     owns the embedded templates and assets; declares two filesystems and no behaviour
test/integration       exercises the compiled binary
```

`internal/templates` declares the two embeds and nothing else: a template and an
asset are deliberately separate filesystems, because a template is executed and
never served raw, an asset is served raw, and one filesystem for both would let
the file server hand out `layouts/base.gohtml` as text.

`internal/handler/page` sits at that depth, not under `internal/http`, on
purpose. `internal/http/*` is mechanism — transport, no business rule, no
dependency on domain or service — while a handler is a layer, where a request
becomes a use case call. Keeping the layers at one level is what makes the
dependency rule readable from the tree.

## Dependency rule

Dependencies point inward. `internal/http/server`, `internal/http/response`,
`internal/infra/graceful` and `internal/infra/health` import nothing internal:
each declares its own `Options` rather than depending on the configuration
structs, so any of them can be built in a test without assembling the whole
application.

Only `internal/application` knows about everything, because wiring is its job.
It is the composition root: it translates `configs` into each package's options,
decides the middleware chain, and lists the resources in startup order.

The interfaces it depends on are declared there too, in `ports.go` and
`resources.go`, never in the packages that satisfy them. `CertificateSource` is
the current one: `internal/infra/certs` does not know it exists, which is what
leaves room for a KMS or an ACME client to answer handshakes later without
either side being rewritten. Anything reaching the outside world is expected to
arrive the same way.

The router is a chi instance, and it exists only inside `internal/application`.
Handlers are `http.Handler` throughout, so no package outside the assembly knows
which router is in use.

## Domain, service and repository

The realm slice adds three packages below `internal/application`, and the
direction of every import between them is the dependency rule made concrete.
`internal/domain/realm` depends on nothing outside the standard library and
`github.com/google/uuid` — it is the innermost layer, and the realm aggregate
lives there with no notion of persistence or transport. `internal/service`
declares the interfaces its use cases consume, `RealmRepository` and `Store`,
rather than depending on whatever ends up implementing them — the same
reasoning the dependency rule above already applies to `CertificateSource`.
`internal/repository` implements those interfaces and therefore imports
`service` to do it, which points the dependency inward rather than outward:
`Store` could not be declared beside its implementation and still typecheck,
since Go requires an interface method's return type to match exactly, and a
method returning a concrete repository type would not satisfy an interface
declared over the abstract one. `internal/application` is the package that
imports all three, because wiring them together is its job: it builds a
`repository.NewStore` over the GORM handle and hands it to
`service.NewRealmService`.

`Realm` carries private fields with accessors and no setter for `id`, `slug`,
`issuer` or `createdAt`, which is what makes the issuer immutable in a way the
compiler enforces rather than a way people have to remember. It has two
constructors for two different origins: `New` is birth — it generates a
UUIDv7, derives the issuer from the realm's slug and the process's public URL,
and validates everything — while `Rehydrate` is the repository's door back in,
and takes the stored issuer as given rather than deriving it. Deriving the
issuer inside `Rehydrate` would recompute every realm's identity from whatever
public URL the reading process happens to hold, which is exactly the drift the
stored `issuer` column exists to prevent: an issuer is derived once, at
creation, and stored, never recomputed on a read.

## Database schema

`internal/migrations` owns the embedded SQL that builds the aegis schema, one
directory per dialect — postgres, mysql, mariadb, sqlite — each covered by its
own `//go:embed` glob and exposed through `For`, which roots the returned tree
at that dialect's directory so the runner sees migration files rather than a
directory of directories. MariaDB has carried its own directory since day one
even though it is byte for byte identical to MySQL today, because splitting it
out later is not possible remotely: an installation that already migrated
carries its lineage in `aegis_schema_migrations`, and there is no way to
retarget that recorded history from where it lives.

That control table is named `aegis_schema_migrations` rather than
golang-migrate's own `schema_migrations` default, because aegis runs on-prem
against a customer's own database and may share it with another application.
If that neighbour also happens to use golang-migrate, a shared table name
would have aegis read someone else's recorded version and apply nothing — with
no error, since golang-migrate has no way to know the version it read was
never its own.

Every migration file holds exactly one statement, and this is forced rather
than merely conventional: the MySQL DSN sets `MultiStatements` to false,
because MySQL and MariaDB have no transactional DDL, so a file carrying two
statements could apply the first and fail the second, leaving a dirty schema
behind. A test enforces the same rule against every dialect, Postgres and
SQLite included, so a violation is caught before it can reach an engine where
it would only fail at the worst time.

A second test, checking that every dialect directory carries the same set of
versions, exists because the per-engine test jobs cannot catch what it
catches: golang-migrate applies whatever it finds in the source and stops
without error, so a version added to three dialect directories and forgotten
in the fourth leaves every job green. The customer running that fourth engine
is the one who discovers it.

`Latest` derives the highest version a dialect carries by reading its
directory rather than from a declared constant, because a constant is one more
thing to forget to bump. An empty directory is an error rather than version
zero, because zero already means "nothing applied yet," and reporting it for a
dialect with no migrations at all would compare the boot's version check
against a lie.

The boot's version check is asymmetric on purpose: a schema older than the
binary is refused, a schema newer than the binary is tolerated. The first half
is what stops a query from reaching a column no migration ever added. The
second half exists for rollback — during a rolling update the previous binary
keeps running beside the new one, which has already migrated the schema
forward, and a binary that refused a version it did not recognise would remove
the ability to roll back exactly when a rollback is needed. A schema recorded
dirty — a prior migration failed and left the state uncertain — is refused
regardless of direction, and recovery is manual: inspect and fix the schema by
hand, then clear the flag with `aegisd migrate force <version>`.

`aegisd migrate` exposes the same subsystem as a subcommand, opening the
database through the same configuration translation the server uses so a
migration never runs under different TLS or pool settings than the process
that will serve traffic. Bare, it applies pending migrations and then verifies
the result the same way boot does. `migrate status` runs no migration at all
and exits 1 when the schema is behind, 2 when it is dirty, which is what makes
it usable as a deployment gate — an init container or a pipeline step that
fails before any replica starts. `migrate force <version>` clears the dirty
flag after a manual repair without touching the schema itself.

## Startup

`main` parses nothing and holds the single exit point. Everything else lives in
`run`, so deferred calls still happen — `os.Exit`, which a fatal log ends up
calling, skips them.

`application.New` walks a list of steps and can fail. `setDatabase` is the
first one that actually does: it is the first step reaching the outside
world, opening the pool and registering the readiness check before anything
later in the list can run. Every step shares its `error` return because of
this one — the signature was there in anticipation, so a dependency reaching
the outside world did not force the assembly to be rewritten, and a connection
failure surfaces as an error instead of a panic inside a constructor.

The list now reads `setLogger`, `setGraceful`, `setHealth`, `setDatabase`,
`setSchema`, `setServices`, `setCertificates`, `setWeb`, `setRouter`,
`setHttpServer`. `setSchema` brings the database up to the version this binary
carries — applying pending migrations only when migration on boot is enabled —
and then verifies the schema version regardless of whether a migration ran:
the check is not conditional on it, because an operator who forgot
`aegisd migrate` with migration on boot disabled would otherwise serve
requests until the first query touched a missing column. `setServices` builds
the store and the realm service and then seeds the master realm; it is a step
of its own, separate from `setSchema`, because the seed needs a
`*service.RealmService` something has to own — built as a local instead, the
admin API would end up constructing a second one that is not the
application's.

The seed only ever reads and writes the master realm's issuer, never its
status: an operator who disabled the master realm did so on purpose, and the
boot has no business reversing that. When the stored issuer already matches
what this process derives from its configured public URL, the seed leaves the
realm untouched. When it does not, development and production diverge for
reasons that belong to each: development rewrites the stored issuer, because
its public URL is derived from the listener and changing the port would
otherwise leave a stale issuer with no symptom pointing at the cause;
production refuses to boot instead, because every client validates the issuer
byte for byte, and discovering after the fact that every one of them rejected
the new issuer is worse than not starting.

## Shutdown

Resources register a shutdown pending with `internal/infra/graceful`. Pendings
resolve in reverse registration order, like `defer`, so registering in startup
order means the HTTP server stops accepting requests before the database closes.

Readiness draining is registered last and therefore resolves first: on `SIGTERM`
`/readyz` starts failing while the server is still accepting connections, giving
the load balancer time to take the instance out of rotation. Only then does the
server stop accepting. The database registers before both — the resources and
the drain pending alike — so it is the last one open: nothing closes it until
the server has drained and stopped accepting, and no in-flight request can be
turned into a connection error by a pool that closed too early.

A pending that fails does not stop the others — the goal is to close as much as
possible before the process dies. A second signal abandons what is left and
exits with a failure status.

## HTTP

The middleware chain, outside in: request id, request logger, panic recovery,
request timeout. Request id comes first so the logger and the recoverer both
carry it; the recoverer sits inside the logger so the 500 it produces appears on
the request line.

The probes are mounted outside that group. The orchestrator polls them every few
seconds per replica, which would bury real traffic in the request log. Assets
are mounted bare alongside them, for the same reason: a stylesheet request is
not a page view. Both still pass through a recoverer, so a panic answers a
status rather than dropping the connection.

Behind that, two groups share the base chain — request id, the forwarding
decision, the request logger, HSTS where enabled, the request timeout — and
diverge only in how each closes it. The API surface recovers into JSON, the
same writer the bare-mounted probes and assets fall back on. The page surface
recovers into HTML and layers the security headers on top of the chain, so an
unhandled panic still renders as a page rather than a JSON object a browser
would show as text.

The response format comes from the route group, never from the `Accept`
header. Negotiating it on the error path would make the format depend on
something the caller controls — the caller who broke the request being the one
to decide the shape of the answer. The group is fixed at registration, so it
cannot be.

Assets carry their own guarantees instead of the chain above: `nosniff`, a
`Content-Security-Policy: default-src 'none'`, and a `Cache-Control` good for a
year, because the path itself is fingerprinted by the content's hash and changes
the moment the file does. That hash is verified again on every request rather
than merely stripped off the URL, which is what keeps the year-long promise
honest instead of a name for whatever happens to be sitting behind it. The
policy is there for one route in particular: `/favicon.ico` answers
`image/svg+xml`, and an SVG navigated to directly is a document that can execute
script — harmless for an icon committed to this repository, and the wrong
default the day a realm's own logo is served from the same code.

The page surface's CSP carries no `unsafe-inline` and no `unsafe-eval`
anywhere in it, so no template may carry a `<style>` block, a `style=`
attribute, an inline event handler or an inline `<script>`. That constraint
landed with the first page rather than after the console arrived: retrofitting
it later would mean rewriting every template already written under a looser
policy. A test in `internal/templates` walks the embedded `.gohtml` files and
fails on any of the four, so the rule is enforced rather than remembered.

Errors are written in the OAuth 2 format of RFC 6749 section 5.2, which is what
the endpoints of this service speak.

## Health

Liveness and readiness answer different questions. A failing liveness gets the
container killed and restarted; a failing readiness only takes it out of
rotation.

Liveness therefore checks nothing external. A slow database would otherwise fail
every replica at once and have all of them restarted, turning a degradation into
an outage.

Readiness runs the registered checks concurrently, so a probe costs the slowest
check rather than their sum. Each one reports as an object, the same shape in
every profile, so what changes between them is how much is inside it:

```json
{"status":"ready","checks":{"database":{"status":"ok"}}}
```

Outside development that is all of it. The verdict is public because a check
name says which dependency is down and nothing else; what stays in is the
failure itself and anything a check describes — the server, the pool, the
version — since those describe internal topology on an endpoint reachable from
outside. Under the development profile the whole description is rendered, and
that is also where an ordered shutdown is distinguishable: draining runs no
checks at all, so it is the one `not_ready` with nothing in `checks`.

A failure is logged whatever the response shows. The probe is a moment and the
public report may withhold the reason, so the reason is recorded either way.

The detailed variant reports the description in any profile, and exists for the
authenticated administration surface.
