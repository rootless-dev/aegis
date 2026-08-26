# Deployment

## Images

One Dockerfile per scenario, under `docker/`. The Go toolchain version is
declared once, in the Makefile, and injected into all of them.

| File | Purpose |
| --- | --- |
| `Dockerfile.production` | distroless, non-root, ~15 MB. Also builds `debug`, the same binary on an image carrying a busybox |
| `Dockerfile.development` | air and delve, for the compose loop |
| `Dockerfile.tilt` | receives a binary compiled on the host |

```sh
make image             # current architecture
make image-multiarch   # linux/amd64 and linux/arm64
make image-debug       # production binary, shell available
```

The builder is pinned to `BUILDPLATFORM` so the compiler always runs natively
and Go cross compiles. Without that, buildx emulates the compiler under QEMU for
each target architecture, which is roughly an order of magnitude slower.

`debug` copies from the same builder rather than recompiling, so what runs there
is bit for bit what runs in production. It exists for the day something has to
be inspected in place, since the production image has no shell.

The `.git` directory is deliberately **not** excluded from the build context:
the toolchain reads it to stamp the revision and build time into the binary.
Excluding it makes that information disappear with no warning.

## Kubernetes

```sh
kubectl apply -k deploy/k8s/base
```

The base runs non-root with a read-only root filesystem, all capabilities
dropped and the default seccomp profile, in a namespace enforcing the
`restricted` Pod Security Standard. That last part turns the container security
context from something the manifest declares into something the cluster
enforces: a pod that does not satisfy it is refused rather than started.

Everything in that namespace is subject to it, including anything added later —
an init container or a migration job will need the same four settings.

### TLS and the topology

The base ships no Ingress, so nothing terminates TLS in front of the pod and it
declares `TLS_TERMINATION=none`. Adding a gateway means switching that to
`proxy` and declaring `PROXY_TRUSTED_PROXIES` with the ranges it calls from:
without them the forwarded headers are ignored, and `proxy` refuses to boot with
the list empty. `PUBLIC_URL` has to be replaced either way — it is what clients
reach the deployment at, and every issuer and redirect is built from it.

The dev overlay runs the development profile and declares `none` there too, so
the pod speaks plain HTTP and the base probes apply unchanged. It used to serve
TLS from a certificate generated at boot, which exercised the same listener
production takes; that stopped being worth a security interstitial on every
browser session once the forwarded port started serving pages. Setting it back
to `app` means adjusting `PUBLIC_URL` to https in the same edit — the boot
validates one against the other — and patching the three probes to
`scheme: HTTPS`, which the kubelet accepts because it does not verify what a
probe is offered.

Where the certificate comes from a Secret mounted by cert-manager or similar,
point `TLS_CERT_FILE` and `TLS_KEY_FILE` at the mounted files and leave
`TLS_RELOAD_INTERVAL` alone: the files are rewritten in place on renewal and are
picked up without restarting the pod.

### Changing the public url

`PUBLIC_URL` is where the master realm's issuer came from, but only once: the
issuer was derived at creation and stored, and nothing derives it again on the
read path. Under the production profile a boot that derives a different issuer
than the one stored refuses to start, on every replica at once — every client
validates the `iss` claim byte for byte, and serving two of them silently is
worse than not serving.

So moving the deployment to a new hostname is two steps, not one. Changing
`PUBLIC_URL` alone takes the whole installation down.

If the new hostname is a mistake, the fix is to put `PUBLIC_URL` back. If the
move is deliberate, stop every instance and rewrite the stored issuer against
the database:

```sql
UPDATE realms SET issuer = 'https://new-host.example.com/realms/master'
WHERE slug = 'master';
```

There is no subcommand for this yet, and it is not something a migration can
do: migrations are versioned, embedded in the binary and identical on every
installation, and the new issuer is particular to this one.

It also has a cost worth knowing before running it rather than after. Every
token already issued under the old issuer becomes unverifiable — clients reject
the `iss` claim they were given — and every cached discovery document a client
holds is wrong until it refetches. Plan it as a rotation, in a window, not as a
configuration tweak.

### Timings that have to stay in step

```
health.drain_delay  <  graceful.timeout  <  terminationGracePeriodSeconds
```

On `SIGTERM`, readiness starts failing first while connections are still being
accepted, so the load balancer stops routing to this instance. Only then does
the server stop accepting and wait for in-flight requests.

If `terminationGracePeriodSeconds` were the smaller of the three, `SIGKILL`
would land in the middle of the shutdown and cut requests in flight. If the
drain delay were the largest, it would be cut short by the shutdown budget and
the load balancer would never notice the instance leaving.

The configuration refuses to boot when the drain delay is not shorter than the
graceful timeout. The relation with `terminationGracePeriodSeconds` lives in the
manifest and is not verifiable from inside the process — keep them aligned by
hand.

## Migrations

There are two shapes for bringing the schema up, chosen with
`DATABASE_MIGRATE_ON_BOOT`.

The default, `true`, migrates during `setSchema` before the process starts
serving. This suits a single process: nothing else needs to run, and the
schema is always current when the first request lands. It also means the pod
that happens to win the race applies the migration, so it is not a fit for
more than one replica starting from an old schema at the same time.

Setting `DATABASE_MIGRATE_ON_BOOT=false` and running `aegisd migrate` in an
init container or a Job moves that work out of the pod's startup path
entirely: the schema lands once, before any replica starts, and `setSchema`
still checks the version on every boot regardless of `ON_BOOT`, so a replica
that starts against a schema still behind refuses rather than serving.

Either way, the `startupProbe`'s budget — `periodSeconds * failureThreshold`,
six minutes in `deploy/k8s/base/deployment.yaml` — is what keeps a probe from
firing mid-migration, killing the process part-way through a DDL statement and
leaving the schema dirty, which then needs a manual `aegisd migrate force` to
clear.

What that budget buys is headroom for a boot that applies many small
migrations. It is not a bound on how long migrating takes, and it cannot be
made into one: `database.migrate.timeout` bounds how long new migrations keep
being *started*, and one already running is always allowed to finish. A single
`CREATE INDEX` on a large table runs for as long as it runs, past any probe
budget, and gets `SIGKILL`ed in the middle of the statement — the exact outcome
the margin exists to avoid.

That is the real argument for the init-container or Job shape on an
installation with large tables. There, the migration is not on any pod's
startup path, so no probe is watching it and it is allowed to take the time it
needs.

## Releases

Versioning is handled by release-please from conventional commits, in a workflow
of its own. The project starts at `v0.0.1`; while it is below `1.0.0`, a feature
bumps the patch and a breaking change bumps the minor.

Releases need a fine-grained token in `RELEASE_PLEASE_TOKEN` with contents,
pull requests and issues write access. The default `GITHUB_TOKEN` is not enough:
beyond permissions, events it produces do not trigger other workflows, so a tag
it created would never start anything downstream.
