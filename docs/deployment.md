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
declares `AEGIS_TLS_TERMINATION=none`. Adding a gateway means switching that to
`proxy` and declaring `AEGIS_PROXY_TRUSTED_PROXIES` with the ranges it calls from:
without them the forwarded headers are ignored, and `proxy` refuses to boot with
the list empty. `AEGIS_PUBLIC_URL` has to be replaced either way — it is what clients
reach the deployment at, and every issuer and redirect is built from it.

The dev overlay runs the development profile and declares `none` there too, so
the pod speaks plain HTTP and the base probes apply unchanged. It used to serve
TLS from a certificate generated at boot, which exercised the same listener
production takes; that stopped being worth a security interstitial on every
browser session once the forwarded port started serving pages. Setting it back
to `app` means adjusting `AEGIS_PUBLIC_URL` to https in the same edit — the boot
validates one against the other — and patching the three probes to
`scheme: HTTPS`, which the kubelet accepts because it does not verify what a
probe is offered.

Where the certificate comes from a Secret mounted by cert-manager or similar,
point `AEGIS_TLS_CERT_FILE` and `AEGIS_TLS_KEY_FILE` at the mounted files and leave
`AEGIS_TLS_RELOAD_INTERVAL` alone: the files are rewritten in place on renewal and are
picked up without restarting the pod.

### Changing the public url

`AEGIS_PUBLIC_URL` is where the master realm's issuer came from, but only once: the
issuer was derived at creation and stored, and nothing derives it again on the
read path. Under the production profile a boot that derives a different issuer
than the one stored refuses to start, on every replica at once — every client
validates the `iss` claim byte for byte, and serving two of them silently is
worse than not serving.

So moving the deployment to a new hostname is two steps, not one. Changing
`AEGIS_PUBLIC_URL` alone takes the whole installation down.

If the new hostname is a mistake, the fix is to put `AEGIS_PUBLIC_URL` back. If the
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

## Environment variables carry the `AEGIS_` prefix

Every variable aegis reads is named `AEGIS_` plus its path in the configuration
file. **An installation configured before this change keeps none of its
settings**: an unprefixed `DATABASE_HOST` is not read at all, and the boot
fails on whatever the missing value makes invalid rather than on the rename
itself. Renaming every variable in the deployment is the whole migration, and
`env | grep AEGIS_` on a running pod is how to check one.

## The master key

Every realm's private signing key is encrypted before it reaches the database,
with one key per installation. The process refuses to boot without it in every
profile, so **a deployment that starts today stops starting** until
`AEGIS_CRYPTO_MASTER_KEY` carries 32 bytes, base64 encoded:

```sh
openssl rand -base64 32
```

Like the database password, it has no configuration file key at all, and it
belongs in a Secret created out of band rather than in a manifest anyone can
read. The base already declares `AEGIS_CRYPTO_MASTER_KEY` as a `secretKeyRef`
to `aegis-crypto/master-key`, so what a deployment adds is the Secret itself:

```sh
kubectl create secret generic aegis-crypto \
  --from-literal=master-key="$(openssl rand -base64 32)"
```

```yaml
- name: AEGIS_CRYPTO_MASTER_KEY
  valueFrom:
    secretKeyRef:
      name: aegis-crypto
      key: master-key
```

`AEGIS_CRYPTO_MASTER_KEY_FILE`, naming a path instead, is accepted everywhere the
variable is and is the better of the two where a mounted secret is available:
the value stays out of `/proc/<pid>/environ`, out of a `describe` on the
manifest, and out of any subprocess. It is a read, so nothing about it conflicts
with the read-only root filesystem. Setting both forms fails the boot rather
than picking a winner.

It is required by **every** subcommand, not only by the server. Configuration
is built and validated before the dispatch picks what to run, so
`aegisd migrate status` used as the deployment gate of the section below starts
failing until the secret is mounted into that init container or pipeline step
as well. It does not need the key to do its work; it needs it because the
process it runs in refuses to be misconfigured — and the alternative, a
migration applied by a binary that could not have booted, is worse than the
inconvenience.

### Backups

A database dump alone no longer restores a working installation. What is in
`realm_keys` is ciphertext, and the key that opens it is not in the dump. Back
it up separately, and deliberately **not** beside the dump: a backup carrying
both is a backup where the encryption is decorative.

Losing it is survivable, which is worth knowing before it happens rather than
during. Signing keys are regenerable — the recovery is a new key per realm and
clients refetching the JWKS, at the cost of every token already issued being
unverifiable for the minutes left in its lifetime. It is an outage, not a data
loss.

### Changing it

`aegisd key rewrap` is the only supported way. It runs with `AEGIS_CRYPTO_MASTER_KEY`
holding the new key and `AEGIS_CRYPTO_MASTER_KEY_PREVIOUS` holding the old one, and
moves every row from one to the other: open it with the key its recorded
`kek_id` names, reseal it under the current key, write the row.

Changing the variable without rewrapping leaves every row sealed under a
`kek_id` no running process holds, and every realm unable to sign — with
nothing failing until something asks for a signature.

It needs no maintenance window and no single transaction, and that is what the
`kek_id` column is for. Every row states which key opens it, so a process
holding both reads rows on either side of the boundary throughout, and an
interrupted run leaves the database consistent: running it again continues
where it stopped, and running it after it finished does nothing. Once a run
finds nothing left to move, drop `AEGIS_CRYPTO_MASTER_KEY_PREVIOUS` and restart. A
process still holding a previous key says so in a log line on every boot, which
is what keeps a half-finished rewrap from being forgotten.

## Migrations

There are two shapes for bringing the schema up, chosen with
`AEGIS_DATABASE_MIGRATE_ON_BOOT`.

The default, `true`, migrates during `setSchema` before the process starts
serving. This suits a single process: nothing else needs to run, and the
schema is always current when the first request lands. It also means the pod
that happens to win the race applies the migration, so it is not a fit for
more than one replica starting from an old schema at the same time.

Setting `AEGIS_DATABASE_MIGRATE_ON_BOOT=false` and running `aegisd migrate` in an
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
