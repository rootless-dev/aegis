# Development

Run `make` for every target, grouped by area.

## Running

```sh
make run   # from source, development profile
make ci    # everything the pipeline runs
```

`make run` passes `--dev`, so the service serves TLS from a certificate it
generates in memory at every boot: reach it at `https://localhost:7500` and tell
the client to skip verification (`curl -k`). Nothing is written to disk, and a
new pair is minted on the next start — which is why the browser asks for the
security exception again after every restart.

Running the binary without `--dev` is running it as production, and production
refuses to boot until `tls.termination` and `public_url` are declared.

## Hot reload and debugger

```sh
make dev
```

The compose stack runs the development profile too, so the service there also
answers over HTTPS on `7500`. The source is mounted and rebuilt by air on every
change, with delve listening on `2345`, and the service talks to a Postgres
container beside it rather than to sqlite — see [The database in the
loop](#the-database-in-the-loop) below. The container runs as root and carries
`SYS_PTRACE`, because air rewrites the binary in place and delve has to trace
the process. That image never leaves a developer machine.

The build uses `-gcflags='all=-N -l'`; without disabling optimisation and
inlining, breakpoints land on lines other than the ones you set. Delve starts
with `--continue`, so the service is usable even when nobody is attached.

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

It also selects the development profile, which means the pod serves TLS from a
generated certificate, so all three probes are patched to `scheme: HTTPS` and
the forwarded port speaks HTTPS. The kubelet does not verify the certificate a
probe is offered, which is what makes a self-signed one workable there.

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
