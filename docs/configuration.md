# Configuration

## Layers

Three sources, each overwriting only what it declares:

```
defaults  →  YAML file  →  environment variables
```

The environment comes last on purpose: the file is what ships with the image,
while a variable is how a single instance is adjusted without rebuilding
anything.

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
| `graceful.timeout` | `GRACEFUL_SHUTDOWN_TIMEOUT` | `20s` |
| `health.check_timeout` | `HEALTH_CHECK_TIMEOUT` | `2s` |
| `health.drain_delay` | `HEALTH_DRAIN_DELAY` | `5s` |
| `banner.enabled` | `BANNER_ENABLED` | `true` |

`read_header_timeout` is the slowloris defense: it bounds connections that
trickle headers to hold a worker. `request_timeout` is carried on the request
context and does not interrupt a handler that ignores it — it cancels what
honors it, such as a database query.

An empty environment variable counts as absent, so a default cannot be cleared
by exporting the variable empty.

## What is not configurable

Version, revision and build time. They identify the binary and are stamped into
it at build time, reported by `internal/buildinfo`. A version the environment
could override is a version that may disagree with the code actually running,
and that is when the information stops being useful.
