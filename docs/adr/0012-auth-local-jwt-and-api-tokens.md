# ADR 0012: Auth — Local JWT Users and API Tokens

## Status

Accepted

## Context

RezusCloud needs authentication for the REST API (consumed by the WebUI, the
CLI, and programmatic automation). The rejected alternative — OIDC + SAML +
PGP-signed requests — is enterprise-identity complexity inappropriate for a
personal cloud where the operator is the owner. See
[`../architecture-history/`](../architecture-history/README.md).

## Decision

**Local JWT users + API tokens**, only.

### Human users

- Username + password stored locally (bcrypt-hashed).
- Login endpoint returns a JWT (HS256, fixed TTL).
- JWT stored in an HttpOnly cookie for the WebUI; `Authorization: Bearer`
  header for the CLI and automation.
- Three roles: `admin` (full access incl. user management), `edit` (no user
  management), `view` (read-only).

### API tokens (automation)

- Random 32-byte hex strings, generated via the API.
- **SHA-256 hashed at rest**; plaintext shown once at creation time.
- Identified by a user-chosen name (e.g. `ci-deploy`).
- Scoped to the creating user's role.
- Revocable via DELETE.
- Used as `Authorization: Bearer <token>` against any `/api/v1/*` endpoint.

### First-run bootstrap

- `REZUSCLOUD_ADMIN_PASSWORD` env var creates the initial admin user on
  startup.

## Consequences

- **No OIDC, no SAML, no PGP.** No callback handling, no key management UI.
- **API tokens are the automation identity**, not a separate service-account
  type — they are scoped to a real user.
- **Migration path preserved.** If multi-tenant or enterprise needs ever
  emerge, an `Authenticator` interface can add OIDC alongside JWT; existing
  JWT users and API tokens remain valid.

## See Also

- [ADR 0013](0013-audit-log-http-middleware.md) — audit uses the identity
  resolved here
- [ADR 0003](0003-rest-api-kubernetes-model.md) — the API this protects
