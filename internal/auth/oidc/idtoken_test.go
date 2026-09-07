package oidc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestVerifyIDToken(t *testing.T) {
	idp := newTestIdP(t)
	ctx := context.Background()
	client := idp.Client()

	valid := func(nonce string) string {
		return idp.mint(nonce, "sub-1", "jane", "jane@example.com", nil)
	}

	t.Run("happy path", func(t *testing.T) {
		claims, err := VerifyIDToken(ctx, client, valid("n-123"), idp.issuer, idp.URL+"/jwks", "rezuscloud-test", "n-123")
		if err != nil {
			t.Fatalf("VerifyIDToken: %v", err)
		}
		if claims.Subject != "sub-1" || claims.PreferredUsername != "jane" {
			t.Errorf("claims = %+v", claims)
		}
	})

	t.Run("wrong nonce rejected", func(t *testing.T) {
		if _, err := VerifyIDToken(ctx, client, valid("n-123"), idp.issuer, idp.URL+"/jwks", "rezuscloud-test", "n-other"); err == nil {
			t.Error("nonce mismatch must be rejected")
		}
	})

	t.Run("empty nonce rejected", func(t *testing.T) {
		if _, err := VerifyIDToken(ctx, client, valid("n-123"), idp.issuer, idp.URL+"/jwks", "rezuscloud-test", ""); err == nil {
			t.Error("empty expected nonce must be rejected")
		}
	})

	t.Run("wrong audience rejected", func(t *testing.T) {
		if _, err := VerifyIDToken(ctx, client, valid("n"), idp.issuer, idp.URL+"/jwks", "other-client", "n"); err == nil {
			t.Error("audience mismatch must be rejected")
		}
	})

	t.Run("wrong issuer rejected", func(t *testing.T) {
		if _, err := VerifyIDToken(ctx, client, valid("n"), "https://evil.example.com", idp.URL+"/jwks", "rezuscloud-test", "n"); err == nil {
			t.Error("issuer mismatch must be rejected")
		}
	})

	t.Run("expired rejected", func(t *testing.T) {
		expired := map[string]any{
			"iss": idp.issuer, "sub": "sub-1", "aud": "rezuscloud-test", "nonce": "n",
			"exp": time.Now().Add(-time.Hour).Unix(), "iat": time.Now().Add(-2 * time.Hour).Unix(),
		}
		raw := mintRS256(t, idp.key, "test-key", expired)
		if _, err := VerifyIDToken(ctx, client, raw, idp.issuer, idp.URL+"/jwks", "rezuscloud-test", "n"); err == nil {
			t.Error("expired token must be rejected")
		}
	})

	t.Run("missing exp rejected", func(t *testing.T) {
		noExp := map[string]any{"iss": idp.issuer, "sub": "s", "aud": "rezuscloud-test", "nonce": "n"}
		raw := mintRS256(t, idp.key, "test-key", noExp)
		if _, err := VerifyIDToken(ctx, client, raw, idp.issuer, idp.URL+"/jwks", "rezuscloud-test", "n"); err == nil {
			t.Error("token without exp must be rejected")
		}
	})

	t.Run("unknown kid rejected", func(t *testing.T) {
		raw := mintRS256(t, idp.key, "rogue-key", map[string]any{
			"iss": idp.issuer, "sub": "s", "aud": "rezuscloud-test", "nonce": "n", "exp": time.Now().Add(time.Minute).Unix(),
		})
		if _, err := VerifyIDToken(ctx, client, raw, idp.issuer, idp.URL+"/jwks", "rezuscloud-test", "n"); err == nil {
			t.Error("unknown kid must be rejected")
		}
	})

	t.Run("garbage rejected", func(t *testing.T) {
		if _, err := VerifyIDToken(ctx, client, "not.a.jwt", idp.issuer, idp.URL+"/jwks", "rezuscloud-test", "n"); err == nil {
			t.Error("garbage must be rejected")
		}
	})
}

func TestIDTokenClaims_GroupsStringOrArray(t *testing.T) {
	t.Run("array groups", func(t *testing.T) {
		var c IDTokenClaims
		if err := c.UnmarshalJSON([]byte(`{"groups":["admins","devs"]}`)); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(c.Groups) != 2 || c.Groups[0] != "admins" {
			t.Errorf("Groups = %v", c.Groups)
		}
	})
	t.Run("string groups", func(t *testing.T) {
		var c IDTokenClaims
		if err := c.UnmarshalJSON([]byte(`{"groups":"admins"}`)); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(c.Groups) != 1 || c.Groups[0] != "admins" {
			t.Errorf("Groups = %v", c.Groups)
		}
	})
}

func TestNewHandlerValidation(t *testing.T) {
	t.Run("disabled config yields nil handler", func(t *testing.T) {
		h, err := NewHandler(Config{}, nil, nil)
		if err != nil || h != nil {
			t.Errorf("want (nil, nil), got (%v, %v)", h, err)
		}
	})
	t.Run("partial config is a startup error", func(t *testing.T) {
		_, err := NewHandler(Config{Issuer: "https://sso.example.com", ClientID: "x"}, nil, nil)
		if err == nil {
			t.Error("partial config must fail loudly, not silently disable")
		}
	})
	t.Run("handler nil-safe methods", func(t *testing.T) {
		var h *Handler
		if h.Enabled() {
			t.Error("nil handler must report disabled")
		}
		if h.ProviderName() != "" {
			t.Error("nil handler must have no provider name")
		}
	})
}

// smoke-test the redirect URI derivation rules.
func TestRedirectURI(t *testing.T) {
	h := &Handler{}
	t.Run("derives from request", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "https://demo.rezus.cloud/auth/oidc/callback", nil)
		r.Header.Set("X-Forwarded-Proto", "https")
		if got := h.redirectURI(r); got != "https://demo.rezus.cloud/auth/oidc/callback" {
			t.Errorf("redirectURI = %q", got)
		}
	})
	t.Run("override wins", func(t *testing.T) {
		h2 := &Handler{cfg: Config{RedirectURL: "https://fixed.example.com/auth/oidc/callback"}}
		r := httptest.NewRequest(http.MethodGet, "https://other.example.com/auth/oidc/callback", nil)
		if got := h2.redirectURI(r); !strings.HasPrefix(got, "https://fixed.example.com") {
			t.Errorf("redirectURI = %q, want the configured override", got)
		}
	})
}
