# Roadmap

Phases are ordering, not a schedule. Each one is designed in full only when it
starts; what follows is direction.

## Foundation

Complete. Layered configuration validated at boot, structured logging with
request correlation, ordered graceful shutdown with readiness draining, health
probes with a check registry, the OAuth 2 error response format, build identity
stamped into the binary, container images, Kubernetes manifests and CI.

Persistence is not started, and it is the prerequisite for phase 1.

## Phase 1 — core identity

The smallest thing that is already an identity provider: it authenticates and
issues a verifiable token.

- `realm` as the isolation boundary, with its own issuer and signing keys
- `identity` scoped to a realm, unique per realm rather than globally
- `credential` kept apart from the identity, one to many, so federated and
  second factor credentials fit later without reshaping the model
- password verification with argon2id
- server-side sessions, short-lived access tokens, refresh token rotation with
  reuse detection
- signing key per realm, encrypted at rest, with rotation and a published JWKS
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
