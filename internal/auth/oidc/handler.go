package oidc

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

// flowCookie is the short-lived HttpOnly cookie binding the browser round
// trip: CSRF state, ID-token nonce, and the PKCE verifier.
const (
	flowCookie    = "rezuscloud_oidc_flow"
	flowTTL       = 10 * time.Minute
	sessionCookie = "rezuscloud_session"
)

// Handler serves the OIDC authorization-code flow for browser sign-in.
// A nil *Handler is a valid "disabled" value — callers check Enabled.
type Handler struct {
	cfg        Config
	store      state.StoreAPI
	jwtManager *auth.JWTManager
	client     *http.Client
}

// NewHandler builds the OIDC flow handler. Returns nil when the config is
// fully absent (local-only mode); a *partial* config is a startup error —
// silently falling back to local-only would hide misconfiguration.
func NewHandler(cfg Config, store state.StoreAPI, jwtManager *auth.JWTManager) (*Handler, error) {
	if !cfg.Enabled() {
		if cfg.Issuer != "" || cfg.ClientID != "" || cfg.ClientSecret != "" {
			return nil, fmt.Errorf("incomplete OIDC config: issuer, client-id and client-secret are all required")
		}
		return nil, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Handler{
		cfg:        cfg,
		store:      store,
		jwtManager: jwtManager,
		client:     http.DefaultClient,
	}, nil
}

// Enabled reports whether federated sign-in is active.
func (h *Handler) Enabled() bool { return h != nil }

// ProviderName is the human label for the sign-in button (issuer host).
func (h *Handler) ProviderName() string {
	if h == nil {
		return ""
	}
	return h.cfg.ProviderName()
}

// RegisterRoutes registers the flow endpoints on the public (pre-auth) mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/oidc/login", h.Login)
	mux.HandleFunc("GET /auth/oidc/callback", h.Callback)
}

// flowState is the signed content of the flow cookie.
type flowState struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
}

// Login begins the flow: stash state+nonce+PKCE verifier in an HttpOnly
// cookie, redirect to the IdP's authorization endpoint.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	meta, err := discover(r.Context(), h.client, h.cfg.Issuer)
	if err != nil {
		h.fail(w, r, "identity provider unreachable")
		return
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		h.fail(w, r, "sign-in could not start")
		return
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	fs := flowState{
		State:    hex.EncodeToString(raw[:16]),
		Nonce:    hex.EncodeToString(raw[16:]),
		Verifier: verifier,
	}
	blob, err := json.Marshal(fs)
	if err != nil {
		h.fail(w, r, "sign-in could not start")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     flowCookie,
		Value:    base64.RawURLEncoding.EncodeToString(blob),
		Path:     "/auth/oidc/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(flowTTL.Seconds()),
	})

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {h.cfg.ClientID},
		"redirect_uri":          {h.redirectURI(r)},
		"scope":                 {strings.Join(h.scopes(), " ")},
		"state":                 {fs.State},
		"nonce":                 {fs.Nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	http.Redirect(w, r, meta.AuthURL+"?"+q.Encode(), http.StatusSeeOther)
}

// Callback finishes the flow: validate state, exchange the code, verify the
// ID token, resolve/link the federated identity, mint the internal session
// JWT, and land the user on the WebUI.
func (h *Handler) Callback(w http.ResponseWriter, r *http.Request) {
	fs, err := h.takeFlowCookie(w, r)
	if err != nil {
		h.fail(w, r, "sign-in session expired — try again")
		return
	}
	if r.FormValue("state") != fs.State || r.FormValue("state") == "" {
		h.fail(w, r, "sign-in state mismatch — try again")
		return
	}
	code := r.FormValue("code")
	if code == "" {
		h.fail(w, r, "identity provider returned no authorization code")
		return
	}

	meta, err := discover(r.Context(), h.client, h.cfg.Issuer)
	if err != nil {
		h.fail(w, r, "identity provider unreachable")
		return
	}

	oauthCfg := &oauth2.Config{
		ClientID:     h.cfg.ClientID,
		ClientSecret: h.cfg.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  meta.AuthURL,
			TokenURL: meta.TokenURL,
		},
		RedirectURL: h.redirectURI(r),
		Scopes:      h.scopes(),
	}
	tok, err := oauthCfg.Exchange(r.Context(), code, oauth2.SetAuthURLParam("code_verifier", fs.Verifier))
	if err != nil {
		h.fail(w, r, "token exchange failed")
		return
	}

	rawIDToken, ok := tok.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		h.fail(w, r, "identity provider returned no id_token")
		return
	}
	claims, err := VerifyIDToken(r.Context(), h.client, rawIDToken, h.cfg.Issuer, meta.JWKSURL, h.cfg.ClientID, fs.Nonce)
	if err != nil {
		h.fail(w, r, "id token rejected")
		return
	}

	user, _, err := ResolveIdentity(h.store, h.cfg, claims)
	if err != nil {
		h.fail(w, r, "account linking failed")
		return
	}

	pair, err := h.jwtManager.GenerateToken(user, auth.DefaultTokenExpiry)
	if err != nil {
		h.fail(w, r, "session minting failed")
		return
	}

	// Replace the flow cookie with the session cookie, exactly as the local
	// login handler would have set it.
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    pair.AccessToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(auth.DefaultTokenExpiry.Seconds()),
	})
	http.SetCookie(w, &http.Cookie{
		Name:     flowCookie,
		Value:    "",
		Path:     "/auth/oidc/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// takeFlowCookie reads and clears the flow cookie, returning the decoded
// flow state.
func (h *Handler) takeFlowCookie(w http.ResponseWriter, r *http.Request) (flowState, error) {
	c, err := r.Cookie(flowCookie)
	if err != nil || c.Value == "" {
		return flowState{}, fmt.Errorf("no flow cookie")
	}
	blob, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return flowState{}, fmt.Errorf("decode flow cookie: %w", err)
	}
	var fs flowState
	if err := json.Unmarshal(blob, &fs); err != nil || fs.State == "" || fs.Verifier == "" {
		return flowState{}, fmt.Errorf("malformed flow cookie")
	}
	// Clear immediately: the state is single-use.
	http.SetCookie(w, &http.Cookie{
		Name: flowCookie, Value: "", Path: "/auth/oidc/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	return fs, nil
}

// redirectURI derives the callback URL from the incoming request, honouring
// proxy headers (HTTPRoute/ingress) and the optional configured override.
func (h *Handler) redirectURI(r *http.Request) string {
	if h.cfg.RedirectURL != "" {
		return h.cfg.RedirectURL
	}
	scheme := "https"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + r.Host + "/auth/oidc/callback"
}

func (h *Handler) scopes() []string {
	if len(h.cfg.Scopes) > 0 {
		return h.cfg.Scopes
	}
	return []string{"openid", "profile", "email"}
}

// fail bounces back to the local login page with an error banner.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusSeeOther)
}
