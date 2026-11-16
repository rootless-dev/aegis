# Roadmap

Phases are ordering, not a schedule. Each one is designed in full only when it
starts; what follows is direction.

## Foundation

Complete. Layered configuration validated at boot, structured logging with
request correlation, ordered graceful shutdown with readiness draining, health
probes with a check registry, the OAuth 2 error response format, build identity
stamped into the binary, container images, Kubernetes manifests and CI.

Persistence followed it: a connection layer over Postgres, MySQL, MariaDB and
SQLite, and a server-rendered page surface with its own asset pipeline.

## Phase 1 — core identity

Complete. The smallest thing that is already an identity provider: it
authenticates and issues a verifiable token. End to end, an installation given
a database, a master key and two variables naming its first administrator
boots, migrates its schema, seeds the `master` realm with its signing keys and
creates that administrator; the administrator signs in at the token endpoint
and receives an access token anyone holding the realm's JWKS can verify, and a
refresh token that keeps the session alive until it is revoked, reused or
expires.

What landed, in the order it did: schema migration on boot with a version check, the `realms` table
across the four dialects, the realm aggregate with the service and repository
around it, a `master` realm seeded idempotently, and an `aegisd migrate`
subcommand for migrating outside the boot. Then the key half of the trust root:
a signing key per realm and per algorithm, encrypted at rest under an
installation master key, rotation and a JWKS published per realm, the `master`
realm seeded with its keys alongside its issuer, and an `aegisd key` subcommand
that operates all of it with no server running. Then the people: an `identity`
unique per realm by email and by an optional username, a `credential` table
kept apart from it, passwords hashed with argon2id and verified without
revealing whether a login exists, and an `aegisd identity` subcommand that
creates identities, sets passwords and disables or enables them from the
command line. Then what the phase was named for: a token
endpoint per realm, at Keycloak's path, where a password opens a server-side
session and returns an access token — a JWT signed by the realm's key and
verifiable against its JWKS — together with an opaque refresh token that is
rotated on every use, stored only as a hash, and ends its whole session when
it is presented twice. The lifetimes are columns on the realm, and so is the
switch that allows the password grant, which is on in `master` and off
everywhere else. An `aegisd session` subcommand lists and revokes sessions,
and is the only revocation there is. Last, someone to sign in: the first
administrator, created at boot in the `master` realm from an email and a
password the operator injects, acted on only while that realm has no
administrator able to sign in and ignored from then on, born with a temporary
password the log keeps warning about until it is replaced, and reset only by
`aegisd bootstrap-admin`, offline. With it the administrative scope — a grant
that is global or names one realm, held by identities of `master`, and a check
that answers whether an identity may administer a realm — operated with
`aegisd admin`.

Complete means the list below, and it is worth being exact about what that
leaves out.

- **No administration API and no console.** The administrative scope is a table
  and a check that nothing in the server calls yet, and no endpoint accepts an
  access token. Every act of administration is a subcommand run by someone with
  access to the database and the master key. This document lists the
  administration API and the console under phase 6.
- **Only `master` authenticates in practice.** The password grant is off in
  every realm but `master`, and nothing creates another realm or changes a
  realm's settings — no endpoint and no subcommand. The lifetimes are the
  defaults for the same reason.
- **The temporary password is not enforced.** It is flagged and logged; no flow
  makes anyone change it, and a login with it gets tokens.
- **The limits the earlier slices stated remain.** No logout, no introspection
  and no discovery document until phase 2. No rate limiting or brute force
  protection on the token endpoint until phase 4. Nothing removes the rows of
  sessions that are over. Changing a password does not end the identity's
  sessions.

- `realm` as the isolation boundary, with its own issuer and signing keys
- `identity` scoped to a realm, unique per realm rather than globally
- `credential` kept apart from the identity, one to many, so federated and
  second factor credentials fit later without reshaping the model
- password verification with argon2id
- server-side sessions, short-lived access tokens, refresh token rotation with
  reuse detection
- bootstrap of the master realm and the first administrator
- administrative scope, the minimum an admin API needs

## Phase 2 — OAuth 2 and OIDC surface

- clients: public and confidential, redirect URI validation, secrets
- authorization code flow with PKCE
- refresh token, client credentials and device code grants
- discovery at `/.well-known/openid-configuration`, `userinfo`, introspection,
  revocation
- ID token, standard claims, scopes and claim mapping
- RP-initiated logout and back-channel logout

## Phase 3 — authorization model

- realm roles and client roles
- groups, with hierarchy and role inheritance
- consent
- service accounts

## Phase 4 — authentication strength

- TOTP and WebAuthn or passkeys
- password policies and forced credential rotation
- brute force detection and per-realm rate limiting
- required actions: verify email, reset password, update profile
- email delivery for verification and recovery

## Phase 5 — federation

- identity brokering over OIDC and SAML
- user federation against LDAP and Active Directory
- account linking

## Phase 6 — operations

- administration API and, eventually, a console
- audit events and an event listener extension point
- metrics and tracing
- token exchange
- configurable authentication flows, which is what makes the steps of a login
  composable rather than hard coded
