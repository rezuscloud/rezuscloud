# ADR 0021: OIDC Federated Login for Browser Users

## Status

Accepted. **Amends [ADR 0012](0012-auth-local-jwt-and-api-tokens.md)** — its
"no OIDC" stance holds for Tier-1 *scope discipline* (no SAML, no PGP), but
browser-based federated sign-in is now part of the human-auth surface
([#198](https://github.com/rezuscloud/rezuscloud/issues/198)).

## Context

ADR 0012 deliberately restricted authentication to local JWT users + API
tokens, preserving one migration path: "an `Authenticator` interface can add
OIDC alongside JWT; existing JWT users and API tokens remain valid." That
path is now taken: the operator's browser identity lives in an external IdP
(Authentik, Auth0, ...), and typing a second password into RezusCloud is the
last remaining local credential surface.

The platform feature spec's Tier-1 slice asks for exactly: an OIDC
authenticator for browser-based human users, internal session-JWT minting
unchanged, and account linking / role mapping for federated identities.

## Decision

**One OIDC relying party, authorization-code + PKCE, no new dependencies.**

### Flow

- `GET /auth/oidc/login` — discovers the provider
  (`{issuer}/.well-known/openid-configuration`, cached 1h, issuer-mismatch
  checked), stores `state` + `nonce` + PKCE verifier in a short-lived
  HttpOnly cookie, redirects to the IdP with S256 code challenge.
- `GET /auth/oidc/callback` — single-use state cookie, code exchange
  (`golang.org/x/oauth2`), ID-token verification (`golang-jwt/v5`: RSA/ECDSA
  signing methods only, issuer/audience/expiry validated against the
  provider JWKS, nonce bound), then session issuance via the **existing**
  `JWTManager` — the token a local login produces and nothing else.

### Identity model

- `oidcidentity` state resource: one `(issuer, subject)` hash-named record
  per federated identity, linking to a local user. Links are stable: after
  the first login, the link — not the claims — resolves the user.
- **First-login resolution order:** existing link → link to an existing
  local user matching the normalized `preferred_username`/`email` claim
  (existing accounts keep their role and password) → provision a new user.
- **Roles are assigned only at provisioning** (`groups` claim containing
  `REZUSCLOUD_OIDC_ADMIN_GROUP` → `admin`, else
  `REZUSCLOUD_OIDC_DEFAULT_ROLE`, default `view`). Operator edits to local
  roles are never clobbered by later federated logins.
- Provisioned federated users are ordinary `user` resources — visible and
  manageable in the existing user-management surface.

### Configuration

Environment only (secrets never in the state store), following the
`REZUSCLOUD_*` convention: `REZUSCLOUD_OIDC_{ISSUER,CLIENT_ID,CLIENT_SECRET,
REDIRECT_URL,DEFAULT_ROLE,ADMIN_GROUP}`. Absent = local-only mode; a
*partial* config is a startup error, never a silent fallback. The callback
URL is derived from the request (`X-Forwarded-Proto` aware) with an explicit
override for exotic deployments.

### Explicitly unchanged

- Local username+password login remains available side by side.
- API tokens remain the automation identity (the OIDC flow is
  browser-only; the CLI keeps tokens).
- The internal JWT (issuer, claims, TTL) is bit-for-bit the same mint.

## Consequences

- **No new third-party dependency**: `golang.org/x/oauth2` (already in the
  graph) moves indirect → direct; `github.com/golang-jwt/jwt/v5` was already
  direct. JWKS parsing is ~80 lines against `crypto/rsa`/`crypto/ecdsa`.
- **Local passwords survive** as a break-glass path — losing the IdP does
  not lock the operator out.
- **One IdP for Tier-1.** Multi-provider (per-issuer identities) is express
  in the data model (issuer is half of the link key) but not yet exposed.
- SAML and PGP-signed requests stay out of scope; when enterprise demand
  appears they arrive as additional authenticators behind the same session
  mint, not as new session semantics.

## See Also

- [ADR 0012](0012-auth-local-jwt-and-api-tokens.md) — local users + API
  tokens (amended, not replaced)
- [ADR 0013](0013-audit-log-http-middleware.md) — audit resolves the same
  identity after OIDC login
