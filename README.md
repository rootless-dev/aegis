# aegis

A multi-tenant identity and access management service, written in Go.

Every tenant is a **realm**: its own users, its own credentials, its own signing
keys and its own issuer. An identity belongs to the realm that owns it, so the
same email address may be two different people in two different realms.

> **Status: early.** The service boots, serves and shuts down correctly, but no
> identity domain is implemented yet — there is no realm, user, credential or
> token issuance. See the [roadmap](docs/roadmap.md).

## Requirements

Go 1.26. Docker and a Kubernetes cluster are needed only for the container and
Tilt workflows.

## Quick start

```sh
make run    # run from source
make dev    # hot reload and a debugger, in a container
make ci     # everything the pipeline runs
make        # every target, grouped
```

The service listens on `:7500` and answers `/livez` and `/readyz`. `make run`
uses the development profile, which serves TLS from a certificate generated in
memory at every boot, so reach it at `https://localhost:7500` and tell the client
to skip verification (`curl -k`).

## Configuration

Four layers, each overwriting only what it declares:

```
defaults  →  YAML file  →  environment variables  →  command line
```

The file is looked up at `AEGIS_CONFIG_FILE`, then `./aegis.yaml`, then
`/etc/aegis/aegis.yaml`. See [`aegis.example.yaml`](aegis.example.yaml) and
[`.env.example`](.env.example) for every setting, and
[docs/configuration.md](docs/configuration.md) for the rules.

### Profiles and TLS

`--dev` (or `AEGIS_PROFILE=dev`) selects the development profile; production is
the default, and a production boot that declares nothing is refused rather than
guessed at — the error says how to get going. In production, `tls.termination` has to be declared, because "a
gateway handles TLS" and "the certificate was forgotten" produce the same
configuration and only an operator can tell them apart:

| `tls.termination` | who serves TLS | also required |
| --- | --- | --- |
| `app` | this process | `tls.cert_file` and `tls.key_file`, PEM encoded |
| `proxy` | a gateway in front | `proxy.trusted_proxies`, the peers allowed to set the forwarded headers |
| `none` | nobody, or a service mesh | — |

The certificate is resolved per handshake and re-read on `tls.reload_interval`,
so a rotation lands without restarting the process.

## Documentation

- [Architecture](docs/architecture.md) — layout, dependency rule, startup and shutdown
- [Configuration](docs/configuration.md) — layers, lookup, validation, settings
- [Development](docs/development.md) — hot reload, debugger, tests, Sonar
- [Deployment](docs/deployment.md) — images, Kubernetes, releases
- [Roadmap](docs/roadmap.md) — what is built and what comes next

## License

[MIT](LICENSE)
