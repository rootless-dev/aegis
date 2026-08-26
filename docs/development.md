# Development

Run `make` for every target, grouped by area.

## Running

```sh
make run   # from source, development profile
make ci    # everything the pipeline runs
```

`make run` passes `--dev`, so the service comes up on plain HTTP at
`http://localhost:7500`, with the public url derived from the listener. All four
run paths — `make run`, `make dev`, `make tilt`, `make image-run` — agree on
that scheme, so a URL copied between them still works.

HTTPS is one variable away:

```sh
TLS_TERMINATION=app make run   # https://localhost:7500
```

Under the dev profile that also mints the certificate in memory, so there is no
key pair to produce. Nothing is written to disk and a new pair is generated on
the next start, which is why the browser asks for the security exception again
after every restart — the reason it is no longer the default.

Running the binary without `--dev` is running it as production, and production
refuses to boot until `tls.termination` and `public_url` are declared. There is
no default there: "a gateway handles TLS" and "the certificate was forgotten"
produce the same configuration, and only an operator can tell them apart.

### What the plain HTTP default costs

No development loop exercises TLS or `CertificateSource` any more. The listener
built from a generated certificate now lives only in the unit tests and in one
integration test that opts in with `TLS_TERMINATION=app` — and in production,
where a break would be found by a customer.

This deserves revisiting when sessions arrive. A `Secure` cookie is not stored
by the browser over plain HTTP, and the HSTS header is only sent over HTTPS, so
neither would be exercised by the loop that is about to grow login forms. Either
the default goes back to `app` then, or the session work runs against
`TLS_TERMINATION=app` deliberately.

`make assets` generates `internal/templates/assets/css/app.css` with the
Tailwind standalone CLI, downloaded and checksum-verified into `bin/` on first
use — no Node involved. `make build`, `make test`, `make test-integration` and
`make ci` all depend on it and run it for you. Calling `go build` or `go run`
directly does not, and every profile — development included — refuses to boot
without the stylesheet, with an error naming `make assets`. There is no
"unstyled page" fallback: the layout resolves the stylesheet through the same
asset function that turns a typo into a test failure, so a boot without it
would answer every route with an error document instead.

## Hot reload and debugger

```sh
make dev
```

The compose stack runs the development profile too, so the service there also
answers plain HTTP on `7500`. The source is mounted and rebuilt by air on every
change, with delve listening on `2345`, and the service talks to a Postgres
container beside it rather than to sqlite — see [The database in the
loop](#the-database-in-the-loop) below. The container runs as root and carries
`SYS_PTRACE`, because air rewrites the binary in place and delve has to trace
the process. That image never leaves a developer machine.

The build uses `-gcflags='all=-N -l'`; without disabling optimisation and
inlining, breakpoints land on lines other than the ones you set. Delve starts
with `--continue`, so the service is usable even when nobody is attached.

Air now watches `.gohtml` and `.css` too, and runs `make assets` before every
rebuild, so editing a template or its Tailwind classes regenerates the
stylesheet along with the binary.

## Kubernetes loop

```sh
make tilt
```

The binary is compiled on the host and synced into the running container, so a
change costs a compile plus a file copy instead of an image build and a load
into the cluster.

Tilt deploys `deploy/k8s/overlays/dev`, which relaxes exactly two things from
the hardened base: the read-only root filesystem, which would block the sync
from writing the binary, and the replica count. Neither should ever be promoted.

The overlay also brings up a Postgres of its own, which the application waits
for before starting — it connects during assembly, so starting them together
would only crash-loop until the server answered.

It also selects the development profile and declares `TLS_TERMINATION=none`, so
the pod speaks plain HTTP and the probes are the base ones, unpatched. The
forwarded port is therefore `http://localhost:7500`, the same address the other
run paths use.

### Leftover images

Tilt tags every build it produces and prunes them on its own, but the defaults
rarely fire in a normal session: the pruner runs hourly and only removes images
older than six hours, so a couple of hours of work leaves everything behind, and
`tilt ci` exits long before any of that happens.

The Tiltfile therefore prunes every few builds instead. What no setting covers
is images left by *earlier* sessions — the pruner only ever touches the current
run:

```sh
make tilt-clean
```

It removes only this project's `tilt-*` images. A blanket `docker image prune`
would also drop layers belonging to everything else on the machine.

Note that `live_update` is what keeps this small in the first place: a code
change syncs a binary instead of building an image, so builds are rare.

## The database in the loop

Both orchestrated environments — compose and Tilt — run against Postgres. The
engine a customer runs is what this service has to be correct against, and the
places it differs from sqlite are exactly the ones an identity provider leans
on: concurrent writes, row locking, types, transactional DDL. Developing
against sqlite produces code that passes here and fails at the customer.

Sqlite has not gone anywhere. Running the binary directly still falls back to
it, with no server to stand up and nothing to declare:

```sh
make run   # development profile, sqlite at ./aegis.dev.db
```

That is the shortcut for seeing the service up — a demo, a smoke test, a first
look. Developing against the persistence layer is what the orchestrated
environments are for.

The database, the user and the password are `aegis` / `aegis` / `devpassword` in both. In compose the database is
reachable on `5432`, and so is Tilt's, which forwards the pod's port to the same
place — they cannot both run at once. Data survives a restart in either: `docker
compose down -v` resets the compose one, and deleting the
`aegis-postgres-data` claim resets the cluster one.

In the cluster the password comes from a Secret the overlay generates, which is
the one the hardened base already reads. The development loop therefore takes
the same path production does, with a throwaway password in it.

## Migrations

```sh
aegisd migrate [flags]            apply pending migrations
aegisd migrate status [flags]     report the schema version; exits 1 when behind, 2 when dirty
aegisd migrate force <n> [flags]  record version n and clear the dirty flag
```

The subcommand has to come first, before any flag: `aegisd migrate --dev` works,
`aegisd --dev migrate` does not, because a leading dash means "no subcommand,
serve".

- `migrate` opens the database the same way the server does and applies every
  pending migration, then verifies the schema landed at the version this
  binary expects.
- `migrate status` reports the driver, the current and expected versions, and
  the dirty flag, without applying anything — which is what makes it usable as
  a gate in a pipeline or an init container.
- `migrate force <n>` only records version `n` and clears the dirty flag; it
  does not touch the schema itself. Use it after fixing whatever
  golang-migrate left half-applied, never as a substitute for that fix.

Exit codes are shared across the three, not owned by `status`:

| Code | Meaning |
| --- | --- |
| `0` | it did what it was asked |
| `1` | it failed; `status` also exits 1 when the schema is merely behind |
| `2` | a malformed command line, or a dirty schema, from `migrate` or `migrate status` |

The dirty schema is the reason 2 is not `status`'s alone. A failed migration
needs an operator and not a retry, whichever of those two ran into it, so a
pipeline can branch on 2 without caring which one it invoked. `migrate force`
never reports it: clearing that flag is the whole job.

Each subcommand runs the whole configuration builder, not just the database
section, so `aegisd migrate --dev` needs to be as complete a command line as
`aegisd --dev` would be to serve.

Once the schema is current and the server boots, the two profiles disagree on
what a stale master realm issuer means. The development profile derives the
issuer from the public url on every start and rewrites the stored one when
they differ, because the public url there is just the listener address, and a
changed port would otherwise leave a stale issuer with no symptom pointing at
the cause. Production refuses to boot on the same disagreement instead: every
client validates the issuer byte for byte, so serving two different ones
silently is worse than not starting.

## Tests

```sh
make test              # unit, with the race detector
make test-integration  # boots the compiled binary
make gosec             # security scanner
```

Integration tests are behind the `integration` build tag, so `go test ./...`
stays fast. They build the binary and run it: unit tests construct the
configuration by hand and never go through the builder defaults or `main`, so a
default that stopped being valid would fail only at boot with every other test
still green.

## Sonar

```sh
make sonar-scan
```

Runs the scanner in a container against the configured server. It needs
`SONAR_HOST_URL` and `SONAR_TOKEN`, read from `.env` — which is not committed —
or passed on the command line. The token is generated under the account security
page of that server.

In CI the token is a repository secret and the address a repository variable.
Neither lives in the repository.

Community Edition analyses a single branch, which is why CI runs the analysis
only on pushes to the default branch: running it on pull requests would
overwrite the main branch results with the branch under review.
