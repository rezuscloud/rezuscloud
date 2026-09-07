// Package authn implements the WebUI login + logout flow.
//
// Extracted from the root web.Handler as part of issue #45 (WebUI Handler
// god-module split). The authn section is the most self-contained WebUI
// section: only 3 routes, no shared state with other sections, and the
// helper surface is small (just render + layout wiring).
package authn

import (
	"net/http"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/auth"
	oidc "github.com/rezuscloud/rezuscloud/internal/auth/oidc"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/web/layout"
	"github.com/rezuscloud/rezuscloud/internal/web/pages"
)

// Renderer is the subset of the root web.Handler that authn needs.
// Defining it here keeps this package decoupled — the root handler implements it.
type Renderer interface {
	Render(w http.ResponseWriter, r *http.Request, props layout.BaseProps)
}

// Handler serves /login, POST /login, /logout and, when configured, the
// OIDC federated sign-in flow (ADR 0021).
type Handler struct {
	store      state.StoreAPI
	jwtManager *auth.JWTManager
	renderer   Renderer
	oidc       *oidc.Handler
}

// New creates an authn Handler.
func New(store state.StoreAPI, jwtManager *auth.JWTManager, renderer Renderer) *Handler {
	return &Handler{store: store, jwtManager: jwtManager, renderer: renderer}
}

// WithOIDC attaches the federated sign-in flow (nil disables it).
func (h *Handler) WithOIDC(o *oidc.Handler) *Handler {
	h.oidc = o
	return h
}

// RegisterRoutes registers /login, /login (POST), /logout and — when OIDC
// is configured — /auth/oidc/{login,callback}.
// Must be called on a parent mux that does not gate these routes with AuthRequired.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", h.LoginPage)
	mux.HandleFunc("POST /login", h.LoginSubmit)
	mux.HandleFunc("GET /logout", h.Logout)
	if h.oidc.Enabled() {
		h.oidc.RegisterRoutes(mux)
	}
}

// LoginPage renders the login form. The ?error= parameter carries messages
// bounced back from the OIDC callback.
func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	data := pages.LoginData{OIDCProvider: h.oidcProviderName()}
	if msg := r.URL.Query().Get("error"); msg != "" {
		data.Error = msg
	}
	h.renderer.Render(w, r, layout.BaseProps{
		Title:   "Sign In",
		Page:    "login",
		Content: pages.Login(data),
	})
}

// oidcProviderName returns the sign-in button label source when federated
// sign-in is enabled, "" otherwise.
func (h *Handler) oidcProviderName() string {
	if h.oidc.Enabled() {
		return h.oidc.ProviderName()
	}
	return ""
}

// LoginSubmit handles form submission. On failure it re-renders the form
// with an error message; on success it sets the session cookie and redirects.
func (h *Handler) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		h.renderer.Render(w, r, layout.BaseProps{
			Title:   "Sign In",
			Page:    "login",
			Content: pages.Login(pages.LoginData{Error: "Username and password are required"}),
		})
		return
	}

	user, err := h.store.GetUser(username)
	if err != nil || user == nil || !auth.CheckPassword(password, user.Spec.PasswordHash) {
		h.renderer.Render(w, r, layout.BaseProps{
			Title:   "Sign In",
			Page:    "login",
			Content: pages.Login(pages.LoginData{Error: "Invalid username or password"}),
		})
		return
	}

	tokenPair, err := h.jwtManager.GenerateToken(user, auth.DefaultTokenExpiry)
	if err != nil {
		h.renderer.Render(w, r, layout.BaseProps{
			Title:   "Sign In",
			Page:    "login",
			Content: pages.Login(pages.LoginData{Error: "Authentication temporarily unavailable"}),
		})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "rezuscloud_session",
		Value:    tokenPair.AccessToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((24 * time.Hour).Seconds()),
	})

	_ = h.store.UpdateUserLastLogin(username)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// Logout clears the session cookie and redirects to /login.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "rezuscloud_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
