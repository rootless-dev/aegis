# Development

Run `make` for every target, grouped by area.

## Running

```sh
make run   # from source
make ci    # everything the pipeline runs
```

## Hot reload and debugger

```sh
make dev
```

Brings up the compose stack, which describes the runtime environment of the
service and nothing else: the source is mounted and rebuilt by air on every
change, with delve listening on `2345`. The container runs as root and carries
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
