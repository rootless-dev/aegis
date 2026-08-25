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

## Releases

Versioning is handled by release-please from conventional commits, in a workflow
of its own. The project starts at `v0.0.1`; while it is below `1.0.0`, a feature
bumps the patch and a breaking change bumps the minor.

Releases need a fine-grained token in `RELEASE_PLEASE_TOKEN` with contents,
pull requests and issues write access. The default `GITHUB_TOKEN` is not enough:
beyond permissions, events it produces do not trigger other workflows, so a tag
it created would never start anything downstream.
