# ADR 15: Frontend Stack — templ + HTMX + Tailwind

Status: Accepted (2026-06-05)

## Context

The reference platform (`arch/`) specifies a Vue 3 single-page application with gRPC-Web transport, Monaco editor, JSONForms, Storybook, Vitest, and Playwright. This is a 68-route enterprise console.

RezusCloud is a personal cloud — one or two operators, not a team. The cost of running a Node.js build pipeline, maintaining a separate SPA codebase, and bridging gRPC-Web to a REST backend is not justified at this scale.

## Decision

Adopt **server-rendered HTML with HTMX** as the WebUI architecture:

- **templ** for type-safe Go HTML templates (no string interpolation)
- **HTMX 2.x** for partial page swaps and SSE-driven live updates
- **Alpine.js 3.x** for client-side reactivity (dropdowns, modals, form state)
- **Tailwind CSS v4** for styling (matches the RezusCloud design system)
- **REST API** as the sole transport — no gRPC-Web bridge
- **No Node.js build** — templ generates Go code at build time, CSS is pre-compiled

## Consequences

### What we gain
- One language (Go) for the entire management plane — frontend and backend
- No SPA hydration, no client-side routing, no state synchronization
- Smaller attack surface (no JSONForms, no Monaco, no dynamic eval)
- Works without JavaScript for read-only views (progressive enhancement)
- HA-ingress compatible (works in Home Assistant iframe, no X-Frame-Options issues)

### What we lose
- Rich in-browser editors (Monaco for YAML, JSONForms for schema-driven forms) — replaced with `<textarea>` + syntax highlighting via server-side rendering
- Client-side routing (instant page transitions) — replaced with full-page swaps via HTMX
- Storybook component previews — design-system HTML files serve the same purpose
- gRPC-Web streaming — SSE over HTTP/1.1 is sufficient for our event volume

### Explicit non-goals
- No SPA, no Vue/React/Svelte
- No Node.js runtime in production
- No gRPC-Web
- No Monaco editor, no JSONForms
- No Storybook, no Vitest, no Playwright (Go tests cover the handler layer)

## Reuse of design system

The RezusCloud design system (`design-system/components/*.html`) provides 36 HTML component examples. Each component is implemented as a templ snippet in `internal/web/components/` and reused across pages. No CSS framework lock-in — Tailwind utilities wrap the design tokens.

## Migration path

If scale or feature requirements ever demand an SPA (multi-tenant SaaS, real-time collaboration), the REST API surface is preserved. A separate SPA can be built against the same `/api/v1/*` endpoints without touching the backend. The templ-based WebUI would be deprecated, not refactored.

## See Also

- [ADR 16](0016-auth-jwt-only.md) — Auth scope decision (no OIDC/SAML)
- [ADR 17](0017-dropped-enterprise-features.md) — Features not built
