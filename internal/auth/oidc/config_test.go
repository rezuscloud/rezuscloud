package oidc

import (
	"testing"
)

func TestFromEnv(t *testing.T) {
	t.Run("empty by default", func(t *testing.T) {
		cfg := FromEnv()
		if cfg.Enabled() {
			t.Error("config must be disabled without env vars")
		}
		if cfg.DefaultRole != "view" {
			t.Errorf("DefaultRole = %q, want view", cfg.DefaultRole)
		}
	})

	t.Run("enabled with full config", func(t *testing.T) {
		t.Setenv("REZUSCLOUD_OIDC_ISSUER", "https://sso.example.com/app")
		t.Setenv("REZUSCLOUD_OIDC_CLIENT_ID", "rezuscloud")
		t.Setenv("REZUSCLOUD_OIDC_CLIENT_SECRET", "shh")
		t.Setenv("REZUSCLOUD_OIDC_DEFAULT_ROLE", "edit")
		t.Setenv("REZUSCLOUD_OIDC_ADMIN_GROUP", "admins")

		cfg := FromEnv()
		if !cfg.Enabled() {
			t.Fatal("config must be enabled")
		}
		if cfg.DefaultRole != "edit" {
			t.Errorf("DefaultRole = %q, want edit", cfg.DefaultRole)
		}
		if cfg.AdminGroup != "admins" {
			t.Errorf("AdminGroup = %q, want admins", cfg.AdminGroup)
		}
		if cfg.Issuer != "https://sso.example.com/app" {
			t.Errorf("Issuer = %q (trailing slash must be trimmed)", cfg.Issuer)
		}
	})

	t.Run("trailing slash trimmed", func(t *testing.T) {
		t.Setenv("REZUSCLOUD_OIDC_ISSUER", "https://sso.example.com/")
		if got := FromEnv().Issuer; got != "https://sso.example.com" {
			t.Errorf("Issuer = %q, want trailing slash trimmed", got)
		}
	})
}

func TestValidate(t *testing.T) {
	t.Run("partial config errors", func(t *testing.T) {
		err := Config{Issuer: "https://sso.example.com"}.Validate()
		if err == nil {
			t.Error("partial config must be rejected")
		}
	})

	t.Run("full config passes", func(t *testing.T) {
		cfg := Config{Issuer: "https://sso.example.com", ClientID: "x", ClientSecret: "y"}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate: %v", err)
		}
	})
}

func TestProviderName(t *testing.T) {
	cases := []struct {
		issuer, want string
	}{
		{"https://sso.example.com/application/o/rezuscloud", "sso.example.com"},
		{"https://auth0.example.auth0.com/", "auth0.example.auth0.com"},
		{"not a url", "OIDC"},
	}
	for _, c := range cases {
		p := Config{Issuer: c.issuer}
		if got := p.ProviderName(); got != c.want {
			t.Errorf("ProviderName(%q) = %q, want %q", c.issuer, got, c.want)
		}
	}
}
