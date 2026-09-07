package oidc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

// identityResourceType is the state-store resource type holding one
// (issuer, subject) → username link. Resource names are derived
// deterministically from the pair, making links idempotent upserts.
const identityResourceType = "oidcidentity"

// OIDCIdentity is a stored (issuer, subject) → username link.
type OIDCIdentity struct {
	Metadata state.Metadata
	Spec     OIDCIdentitySpec
}

// OIDCIdentitySpec links one federated identity to a local user.
type OIDCIdentitySpec struct {
	Issuer    string `json:"issuer"`
	Subject   string `json:"subject"`
	Username  string `json:"username"`
	Email     string `json:"email,omitempty"`
	Provider  string `json:"provider,omitempty"` // issuer host, for display
	LinkedAt  string `json:"linkedAt"`
	LastLogin string `json:"lastLogin,omitempty"`
}

// ResolveIdentity returns the local user the federated identity maps to,
// linking or provisioning as needed. The second return value reports
// whether the login resolved via an existing identity/existing user
// (true) or provisioned a brand-new user (false):
//
//  1. An existing identity link wins (the stable mapping after first login).
//  2. Otherwise a local user matching the normalized username/email claim
//     is linked in place — existing accounts keep their role.
//  3. Otherwise a new user is provisioned with the mapped role.
//
// Roles are assigned only at provisioning: an operator editing a role
// locally must not be overwritten by later federated logins.
func ResolveIdentity(store state.StoreAPI, cfg Config, claims *IDTokenClaims) (*state.User, bool, error) {
	identity, err := getIdentity(store, cfg.Issuer, claims.Subject)
	if err != nil {
		return nil, false, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if identity != nil {
		user, err := store.GetUser(identity.Spec.Username)
		if err != nil {
			return nil, false, err
		}
		if user == nil {
			return nil, false, fmt.Errorf("oidc identity links to missing user %q", identity.Spec.Username)
		}
		identity.Spec.LastLogin = now
		if err := putIdentity(store, identity.Metadata.ResourceVersion, *identity); err != nil {
			return nil, false, err
		}
		return user, true, nil
	}

	candidate := NormalizeUsername(firstNonEmpty(claims.PreferredUsername, claims.Email))
	if candidate == "" {
		return nil, false, fmt.Errorf("id token carries no usable username or email claim")
	}

	existing, err := store.GetUser(candidate)
	if err != nil {
		return nil, false, err
	}
	linked := existing != nil
	user := existing
	if user == nil {
		provisioned, err := store.CreateUser(candidate, state.UserSpec{
			Role: MapRole(cfg, claims.Groups),
		})
		if err != nil {
			return nil, false, fmt.Errorf("provision user %q: %w", candidate, err)
		}
		user = provisioned
	}

	if err := putIdentity(store, 0, OIDCIdentity{
		Metadata: state.Metadata{Name: identityName(cfg.Issuer, claims.Subject)},
		Spec: OIDCIdentitySpec{
			Issuer:   cfg.Issuer,
			Subject:  claims.Subject,
			Username: user.Metadata.Name,
			Email:    claims.Email,
			Provider: cfg.ProviderName(),
			LinkedAt: now,
		},
	}); err != nil {
		return nil, false, fmt.Errorf("record identity link: %w", err)
	}
	return user, linked, nil
}

// MapRole derives the role for a *provisioned* user: admin when the groups
// claim contains the configured admin group, the configured default role
// otherwise.
func MapRole(cfg Config, groups []string) string {
	if cfg.AdminGroup != "" && slices.Contains(groups, cfg.AdminGroup) {
		return auth.RoleAdmin
	}
	if auth.ValidRoles[cfg.DefaultRole] {
		return cfg.DefaultRole
	}
	return auth.RoleView
}

// NormalizeUsername coerces a claim value into a safe local username:
// lowercase, DNS-label characters only, at most 63 bytes. Email claims are
// reduced to their local part.
func NormalizeUsername(claim string) string {
	v := strings.ToLower(strings.TrimSpace(claim))
	if i := strings.IndexByte(v, '@'); i > 0 {
		v = v[:i]
	}
	v = unsafeNameChars.ReplaceAllString(v, "-")
	v = strings.Trim(v, "-")
	if len(v) > 63 {
		v = strings.TrimRight(v[:63], "-")
	}
	return v
}

var unsafeNameChars = regexp.MustCompile(`[^a-z0-9.-]+`)

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// identityName derives the deterministic resource name for an (issuer,
// subject) pair. Hashing keeps names bounded and store-safe regardless of
// claim contents.
func identityName(issuer, subject string) string {
	sum := sha256.Sum256([]byte(issuer + "\x00" + subject))
	return hex.EncodeToString(sum[:])[:24]
}

func getIdentity(store state.StoreAPI, issuer, subject string) (*OIDCIdentity, error) {
	var spec OIDCIdentitySpec
	md, err := store.GetResource(identityResourceType, identityName(issuer, subject), &spec, nil)
	if err == state.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &OIDCIdentity{Metadata: md, Spec: spec}, nil
}

func putIdentity(store state.StoreAPI, version int64, identity OIDCIdentity) error {
	if version > 0 {
		_, err := store.UpdateResource(identityResourceType, identity.Metadata.Name, version, identity.Spec, nil, nil)
		return err
	}
	_, err := store.CreateResource(identityResourceType, identity.Metadata.Name, identity.Spec, struct{}{}, nil, nil)
	return err
}
