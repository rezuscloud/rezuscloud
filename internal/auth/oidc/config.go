// Package oidc implements federated sign-in for browser-based human users
// (ADR 0021) alongside the local JWT users of ADR 0012. The platform's own
// session JWT minting is unchanged: after a successful OIDC round-trip the
// same internal JWTManager issues the same session token a local login would.
//
// Tier-1 scope (issue #198): one OIDC provider, authorization-code flow with
// PKCE, account linking by username/email claim, provisioning with role
// mapping from the groups claim. SAML and PGP remain out of scope.
package oidc

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Config configures the OIDC relying party. It is sourced from environment
// variables (REZUSCLOUD_OIDC_*) so the client secret never lives in the
// state store — the same convention as REZUSCLOUD_JWT_SECRET and
// REZUSCLOUD_ADMIN_PASSWORD.
type Config struct {
	// Issuer is the IdP issuer URL, e.g. https://sso.example.com/application/o/rezuscloud.
	Issuer string
	// ClientID and ClientSecret are the RP credentials registered at the IdP.
	ClientID     string
	ClientSecret string
	// RedirectURL overrides the callback URL. When empty it is derived from
	// the incoming request (scheme + host + /auth/oidc/callback), which is
	// correct for the single-host deployments this product targets.
	RedirectURL string
	// DefaultRole is the role assigned to provisioned users (default "view").
	DefaultRole string
	// AdminGroup is a groups-claim value that grants the admin role at
	// provisioning time. Empty disables group-based elevation.
	AdminGroup string
	// Scopes requested from the IdP. Defaults to "openid profile email";
	// only set directly in tests.
	Scopes []string
}

// FromEnv builds a Config from REZUSCLOUD_OIDC_* variables.
func FromEnv() Config {
	cfg := Config{
		Issuer:       strings.TrimRight(os.Getenv("REZUSCLOUD_OIDC_ISSUER"), "/"),
		ClientID:     os.Getenv("REZUSCLOUD_OIDC_CLIENT_ID"),
		ClientSecret: os.Getenv("REZUSCLOUD_OIDC_CLIENT_SECRET"),
		RedirectURL:  os.Getenv("REZUSCLOUD_OIDC_REDIRECT_URL"),
		DefaultRole:  os.Getenv("REZUSCLOUD_OIDC_DEFAULT_ROLE"),
		AdminGroup:   os.Getenv("REZUSCLOUD_OIDC_ADMIN_GROUP"),
	}
	if cfg.DefaultRole == "" {
		cfg.DefaultRole = "view"
	}
	return cfg
}

// Enabled reports whether OIDC sign-in should be wired up. All three
// provider coordinates are required; partial configuration is a startup
// error, not a silent local-only fallback.
func (c Config) Enabled() bool {
	return c.Issuer != "" && c.ClientID != "" && c.ClientSecret != ""
}

// Validate returns an error for configurations that are partially set or
// carry invalid values. Called at startup so misconfiguration surfaces
// loudly instead of at first login.
func (c Config) Validate() error {
	if !c.Enabled() {
		parts := []string{"issuer", "client-id", "client-secret"}
		var missing []string
		if c.Issuer == "" {
			missing = append(missing, parts[0])
		}
		if c.ClientID == "" {
			missing = append(missing, parts[1])
		}
		if c.ClientSecret == "" {
			missing = append(missing, parts[2])
		}
		return fmt.Errorf("incomplete OIDC config, missing: %s", strings.Join(missing, ", "))
	}
	if _, err := url.Parse(c.Issuer); err != nil {
		return fmt.Errorf("invalid OIDC issuer URL: %w", err)
	}
	return nil
}

// ProviderName derives a human label from the issuer host, e.g.
// "https://sso.example.com/..." → "sso.example.com". Used on the sign-in
// button.
func (c Config) ProviderName() string {
	u, err := url.Parse(c.Issuer)
	if err != nil || u.Host == "" {
		return "OIDC"
	}
	return u.Host
}
