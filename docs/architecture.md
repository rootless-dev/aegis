# Architecture

## Layout

```
cmd/server            entry point: signals and exit code, nothing else
internal/application  assembly and lifecycle
internal/configs      configuration structs, defaults and validation
internal/http         server, middleware, response format
internal/infra        logging, graceful shutdown, health, env parsing
internal/buildinfo    identity of the running binary
test/integration      exercises the compiled binary
```

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
seconds per replica, which would bury real traffic in the request log. They
still pass through a recoverer, so a panic answers a status rather than dropping
the connection.

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
