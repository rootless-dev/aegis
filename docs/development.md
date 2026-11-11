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
AEGIS_TLS_TERMINATION=app make run   # https://localhost:7500
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
integration test that opts in with `AEGIS_TLS_TERMINATION=app` — and in production,
where a break would be found by a customer.

Sessions have arrived without forcing the question: they are reached through
the token endpoint, which sets no cookie. It deserves revisiting when the
browser login does. A `Secure` cookie is not stored by the browser over plain
HTTP, and the HSTS header is only sent over HTTPS, so neither would be
exercised by the loop that is about to grow login forms. Either the default
goes back to `app` then, or that work runs against `AEGIS_TLS_TERMINATION=app`
deliberately.

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

It also selects the development profile and declares `AEGIS_TLS_TERMINATION=none`, so
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

## The administrator

Each environment gets the same first administrator, `admin@aegis.localhost`
with the password `change-me-on-first-login`, by a different road:

| Environment | Where the credentials come from |
| --- | --- |
| `make run` | `.env`, once `make bootstrap-admin` has written them there |
| `make dev` | literals in `docker-compose.yml` |
| `make tilt` | the dev overlay: the email as a plain value, the password from the `aegis-bootstrap-admin` Secret it generates |

```sh
make bootstrap-admin   # writes the two variables into .env, unless some are already there
make run               # the boot creates the administrator
```

Compose and Tilt need nothing: the administrator is created on the first boot
against an empty database. `make run` is the one that asks for a step, because
the binary has no development fallback for a credential, the same reason
`make master-key` exists. In the cluster the email is a plain value and not a
key of the Secret because Tilt redacts Secret values from the logs it shows,
and that would blank the address out of the line saying who was created.

The boot acts only while the master realm has no administrator able to sign
in. After that the variables are ignored, and the log says so at `INFO`:
editing the password in `.env` and restarting changes nothing.

Sign in through the token endpoint, with the email as the login — no username
is configured in any of the three:

```sh
curl -s http://localhost:7500/realms/master/protocol/openid-connect/token \
  -d grant_type=password \
  --data-urlencode username=admin@aegis.localhost \
  -d password=change-me-on-first-login
```

The password is created temporary. Nothing forces its replacement yet, so the
login works and the server warns about it instead: once at the token endpoint
for each login, and on every boot with `this administrator's password is still
the temporary one`. Replacing it is what silences both:

```sh
AEGIS_IDENTITY_PASSWORD=<a new one> \
  go run ./cmd/aegisd identity set-password master admin@aegis.localhost --dev
```

`aegisd bootstrap-admin` is the reset, and unlike the boot it always acts: the
identity holding the configured email becomes a global administrator with the
configured password, temporary again, and its sessions are ended. It reads the
same variables, so with `.env` in place it is:

```sh
go run ./cmd/aegisd bootstrap-admin --dev
```

It also seeds the master realm when no server has booted on the database yet,
so it works straight after `aegisd migrate`. Do not confuse it with
`make bootstrap-admin`, which only writes two lines into `.env`.

Who administers what is `aegisd admin`:

```sh
aegisd admin list [flags]                     list who administers what
aegisd admin grant <login> [realm] [flags]    grant administration of everything, or of one realm
aegisd admin revoke <login> [realm] [flags]   take it back
```

```sh
go run ./cmd/aegisd admin grant dev --dev           # dev administers every realm
go run ./cmd/aegisd admin grant dev master --dev    # or only the realm named
go run ./cmd/aegisd admin list --dev
```

`dev` there is the identity the [next section](#identities) creates. The login
is an identity of the `master` realm, the only one whose identities
administer. Without a realm the grant is global. Granting what is already held
and revoking what is not both exit 1. Nothing reads these grants at runtime
yet: there is no administration API, so a grant changes what `admin list`
prints and what the authorization check will answer once something calls it.

## Identities

```sh
aegisd identity create <realm> <email> [username] [--temporary] [flags]   create an identity
aegisd identity set-password <realm> <login> [--temporary] [flags]        set or replace the password
aegisd identity list <realm> [flags]                                      list a realm's identities
aegisd identity disable <realm> <login> [flags]                           refuse authentication for the identity
aegisd identity enable <realm> <login> [flags]                            allow it again
```

Beyond the administrator above nothing is seeded, so every other user of a
development database is created here. A login is an email or a username. The password is never an argument — it
would sit in `ps` and in the shell history — and is read from
`AEGIS_IDENTITY_PASSWORD`, or from the file `AEGIS_IDENTITY_PASSWORD_FILE`
names; setting both is an error.

```sh
make run   # once, and stop it: the first boot is what seeds the master realm
AEGIS_IDENTITY_PASSWORD=devpassword \
  go run ./cmd/aegisd identity create master dev@example.com dev --dev
```

That writes to the sqlite file `make run` uses. Against compose or Tilt, give
the command the same `AEGIS_DATABASE_*` variables the service there runs with:
it opens the database the way the server does, and like every subcommand it
needs `AEGIS_CRYPTO_MASTER_KEY`, which `make master-key` writes into `.env`.

- `create` without a password leaves the identity with no credential; it cannot
  authenticate until `set-password` gives it one.
- `--temporary` marks the password as one that has to be changed at the next
  login. With no password supplied it is a usage error.
- `disable` and `enable` change whether the identity may authenticate; neither
  touches its credential.

## Tokens

With an identity in `master` and the server running, the token endpoint trades
its password for a pair of tokens:

```sh
curl -s http://localhost:7500/realms/master/protocol/openid-connect/token \
  -d grant_type=password -d username=dev -d password=devpassword
```

`username` takes an email as well; pass it with `--data-urlencode`. The answer
carries `access_token`, `refresh_token`, `expires_in`, `refresh_expires_in` and
`session_state`, which is the session's id. The access token is a JWT: its
payload decodes with any JWT tool, and its signature verifies against
`http://localhost:7500/realms/master/protocol/openid-connect/certs`.

```sh
curl -s http://localhost:7500/realms/master/protocol/openid-connect/token \
  -d grant_type=refresh_token -d refresh_token=<refresh_token from the last answer>
```

Every refresh returns a new refresh token and consumes the one it was given.
Presenting a consumed one again is read as theft and ends the session, so
running that second command twice with the same value is the quickest way to
watch reuse detection work — and to lose the session. Log in again to get
another.

Only `master` accepts the password grant; any other realm answers
`unauthorized_client`. The parameters go in the body, as `-d` sends them: the
endpoint does not read the query string. An access token lasts five minutes
and a session thirty without a refresh, ten hours at most. Those are per-realm
columns with no command or setting to change them yet.

An identity created with `--temporary` still gets tokens here, and so does the
bootstrapped administrator, with a warning in the server's log: there is no
change-password flow yet.

## Sessions

```sh
aegisd session list <realm> <login> [flags]          list an identity's sessions, newest first
aegisd session revoke <realm> <session-id> [flags]   end one session
aegisd session revoke-all <realm> <login> [flags]    end every live session of an identity
```

```sh
go run ./cmd/aegisd session list master dev --dev
```

`list` shows each session as `live`, `expired`, or `ended:` followed by why —
`revoked`, `reuse` or `realm_disabled`. The id `revoke` takes is the one in the
first column, which is also the `session_state` of the token answer. Like
`aegisd identity`, these open the database themselves and need no server
running — and run just as well beside one, which is how the integration tests
revoke a session and then watch its refresh fail.

Revoking makes the session's next refresh fail. The access token already
issued keeps verifying until it expires, because nothing checks it against the
database — that is why it is short-lived.

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
