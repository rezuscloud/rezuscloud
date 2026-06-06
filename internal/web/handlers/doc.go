// Package handlers hosts sub-packages with section-specific HTTP handlers
// for the WebUI. The root web package keeps the Handler struct and shared
// helpers (render, popToast, AuthRequired) and wires together the section
// sub-packages.
//
// This is a structural refactor (issue #45). The intent is:
//
//   - web/core     — shared render helpers (render, AuthRequired, popToast, canMutate)
//   - web/handlers/authn  — login/logout
//   - web/handlers/dashboard — / and /events/stream
//   - web/handlers/clusters — /clusters/*
//   - web/handlers/machines — /machines/*
//   - web/handlers/settings — /settings/*
//
// Each sub-package owns its routes and tests. The root web.Handler becomes a
// thin wiring layer.
//
// This PR (#45) lands the authn section as the proof of pattern; subsequent
// PRs migrate the other sections. See tracking issue #38.
package handlers
