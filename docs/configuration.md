# Configuration

## Layers

Four sources, each overwriting only what it declares:

```
defaults  →  YAML file  →  environment variables  →  command line
```

The order is deliberate: the file is what ships with the image, a variable is
how a single instance is adjusted without rebuilding anything, and a flag is
whoever is looking at the process right now.

Only flags that were actually passed count. A bool flag left out is
indistinguishable by value from one passed as false, so `Flags` carries
pointers — an absent flag must not overwrite what the file or the environment
set.

A source that also carried defaults would defeat this — an unset variable would
overwrite a value the file had deliberately set. That is why the defaults are a
layer of their own, in `configs.Default()`, and every other source only applies
what it actually contains.

## File lookup

Searched in order:

1. the path in `AEGIS_CONFIG_FILE`
2. `./aegis.yaml`
3. `/etc/aegis/aegis.yaml`

The first file found is used; the rest are fallbacks, not layers to merge.

A missing file is not an error — outside development the orchestrator injects
settings through the environment. But a path given explicitly in
`AEGIS_CONFIG_FILE` **has to exist**: an explicit path that silently does
nothing is worse than a boot that fails.

An unknown key fails the boot rather than being ignored, so `prot` instead of
`port` is caught rather than quietly doing nothing.

## Profiles

`--dev`, or `AEGIS_PROFILE=dev`, selects the development profile. Production is
the default, so forgetting to declare it never turns a deployment into the one
handing out development shortcuts.

A run that declares nothing is refused rather than guessed at, and the refusal
says how to get going:

```
invalid configuration: tls: termination is required: declare "app" (this process
serves TLS), "proxy" (a gateway does) or "none" (nobody does), or run with --dev
for a local run
```

## TLS termination

`tls.termination` has no default in production because the two answers are
indistinguishable from the outside: "a gateway terminates TLS" and "the
certificate was never configured" produce byte for byte the same configuration.
Only an operator knows which one is true, so the boot fails until they say.

| value | who serves TLS | consequences |
| --- | --- | --- |
| `app` | this process | `tls.cert_file` and `tls.key_file` are required, PEM only |
| `proxy` | a gateway in front | `proxy.trusted_proxies` is required, and a certificate here is an error |
| `none` | nobody, or a service mesh doing transparent mTLS | forwarded headers are ignored |

What the choice changes at runtime, beyond the listener:

- **the scheme** every cookie, redirect and issuer is built from. Under `proxy`
  it comes from `X-Forwarded-Proto`, and **only** when the peer falls inside
  `proxy.trusted_proxies` — otherwise any client could claim it arrived over
  HTTPS and defeat a cookie marked `Secure`;
- **the client address** used by the request log, and later by rate limiting and
  auditing. The forwarded chain is walked from the right, dropping the declared
  proxies; the first address that is not one of them is as far back as this
  deployment can vouch for. Anything to its left was written outside the trust
  boundary and could have been made up;
- **the forwarded headers themselves**, which are removed from the request when
  nobody vouched for them, rather than merely ignored.

`proxy.headers` picks the family the gateway writes: `x-forwarded`, which every
ingress emits, or `forwarded`, the RFC 7239 form some proxies use instead. Only
the configured one is read and the other is always stripped — a deployment has
one gateway, and accepting both would mean deciding what to do when they
disagree.

The certificate is checked against `public_url` when it is loaded: one that does
not cover that host completes the handshake here and is rejected there, so the
boot fails instead. A rotation that brings an uncovered certificate is refused
too, and the pair already in use keeps being served — installing the wrong one
would break every client at once, while keeping the old one breaks nobody.

Expiry is compared against the clock, not just logged: an expired pair loads
exactly like a fresh one, so it is reported as an error, and a pair inside the
last 14 days of its life as a warning. It is still served either way — refusing
to answer at all is not an improvement — but it cannot be something the log
mentions in passing.

The certificate is resolved per handshake, not pinned at startup, and re-read
every `tls.reload_interval`. Issuers rotate the files in place, and a
certificate loaded once at boot would only be renewed by restarting the process.
A reload that fails — a rotation caught between the two writes — keeps the pair
already in use rather than taking down a process that is still serving a valid
one.

The minimum TLS version and the cipher suites are deliberately not
configurable: the only answer that stays right over time is the one the standard
library keeps current, and a floor pinned in configuration ages into the weakest
thing this service still accepts.

## Validation

Everything is validated before anything starts, and **every problem is reported
at once**:

```
invalid configuration: logging: unsupported level "banana", expected one of [debug info warn error fatal panic]
http server: invalid port "abc"
```

A boot that reports one error per run costs one restart per setting.

Some rules span sections, and those are checked too: the request timeout must
not exceed the write timeout, or the connection dies before the handler can
answer; the health drain delay must be shorter than the graceful timeout, or the
drain is cut short and the load balancer never sees the instance leave rotation.

## Durations

Durations take a unit — `15s`, `1m30s`, `500ms`. A bare number is rejected in
both sources, because accepting it would silently mean nanoseconds.

## Settings

| YAML | Environment | Default |
| --- | --- | --- |
| `app_name` | `APP_NAME` | `Aegis` |
| `profile` | `AEGIS_PROFILE` (or `--dev`) | `prod` |
| `public_url` | `PUBLIC_URL` | none, required outside `dev` |
| `logging.level` | `LOGGING_LEVEL` | `INFO` |
| `logging.caller` | `LOGGING_CALLER_LEVEL` | `1` |
| `logging.time_field` | `LOGGING_TIME_FIELD` | empty |
| `logging.time_format` | `LOGGING_TIME_FORMAT` | `2006-01-02 15:04:05` |
| `logging.pretty_enabled` | `LOGGING_PRETTY_ENABLED` | `true` |
| `http_server.host` | `HTTP_SERVER_HOST` | `0.0.0.0` |
| `http_server.port` | `HTTP_SERVER_PORT` | `7500` |
| `http_server.read_header_timeout` | `HTTP_SERVER_READ_HEADER_TIMEOUT` | `5s` |
| `http_server.read_timeout` | `HTTP_SERVER_READ_TIMEOUT` | `15s` |
| `http_server.write_timeout` | `HTTP_SERVER_WRITE_TIMEOUT` | `15s` |
| `http_server.idle_timeout` | `HTTP_SERVER_IDLE_TIMEOUT` | `60s` |
| `http_server.request_timeout` | `HTTP_SERVER_REQUEST_TIMEOUT` | `10s` |
| `http_server.max_header_bytes` | `HTTP_SERVER_MAX_HEADER_BYTES` | `1048576` |
| `tls.termination` | `TLS_TERMINATION` | none, required outside `dev` |
| `tls.cert_file` | `TLS_CERT_FILE` | empty |
| `tls.key_file` | `TLS_KEY_FILE` | empty |
| `tls.reload_interval` | `TLS_RELOAD_INTERVAL` | `1h` |
| `proxy.trusted_proxies` | `PROXY_TRUSTED_PROXIES` | empty |
| `proxy.headers` | `PROXY_HEADERS` | `x-forwarded` |
| `hsts.enabled` | `HSTS_ENABLED` | `true` |
| `hsts.max_age` | `HSTS_MAX_AGE` | `8760h` |
| `hsts.include_subdomains` | `HSTS_INCLUDE_SUBDOMAINS` | `false` |
| `graceful.timeout` | `GRACEFUL_SHUTDOWN_TIMEOUT` | `20s` |
| `health.check_timeout` | `HEALTH_CHECK_TIMEOUT` | `2s` |
| `health.drain_delay` | `HEALTH_DRAIN_DELAY` | `5s` |
| `banner.enabled` | `BANNER_ENABLED` | `true` |

`read_header_timeout` is the slowloris defense: it bounds connections that
trickle headers to hold a worker. `request_timeout` is carried on the request
context and does not interrupt a handler that ignores it — it cancels what
honors it, such as a database query.

`PROXY_TRUSTED_PROXIES` is a comma separated list, taking CIDR blocks or bare
addresses, which are read as a single host. HSTS is only ever sent over a
connection that already is HTTPS: announcing it over plain HTTP asks the browser
to trust the one message an attacker on the path could have written.

An empty environment variable counts as absent, so a default cannot be cleared
by exporting the variable empty.

## What is not configurable

Version, revision and build time. They identify the binary and are stamped into
it at build time, reported by `internal/buildinfo`. A version the environment
could override is a version that may disagree with the code actually running,
and that is when the information stops being useful.
