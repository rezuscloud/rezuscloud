package oidc

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

func newTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "oidc.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testClaims(sub, username, email string, groups []string) *IDTokenClaims {
	c := &IDTokenClaims{PreferredUsername: username, Email: email, Groups: groups}
	c.Subject = sub
	return c
}

func TestResolveIdentity_Provisions(t *testing.T) {
	store := newTestStore(t)
	cfg := Config{Issuer: "https://sso.example.com", DefaultRole: "view"}

	user, linked, err := ResolveIdentity(store, cfg, testClaims("sub-1", "jane.doe", "jane@example.com", nil))
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if linked {
		t.Error("first login must provision, not link")
	}
	if user.Metadata.Name != "jane.doe" {
		t.Errorf("username = %q, want jane.doe", user.Metadata.Name)
	}
	if user.Spec.Role != "view" {
		t.Errorf("role = %q, want the default view", user.Spec.Role)
	}

	// The identity link is persisted and re-used on the next login.
	again, linked, err := ResolveIdentity(store, cfg, testClaims("sub-1", "jane.doe", "jane@example.com", nil))
	if err != nil {
		t.Fatalf("ResolveIdentity again: %v", err)
	}
	if !linked {
		t.Error("second login must resolve via the stored identity")
	}
	if again.Metadata.Name != user.Metadata.Name {
		t.Errorf("second login resolved %q, want %q", again.Metadata.Name, user.Metadata.Name)
	}
}

func TestResolveIdentity_AdminGroupElevation(t *testing.T) {
	store := newTestStore(t)
	cfg := Config{Issuer: "https://sso.example.com", DefaultRole: "view", AdminGroup: "rezus-admins"}

	user, _, err := ResolveIdentity(store, cfg, testClaims("sub-2", "bob", "", []string{"devs", "rezus-admins"}))
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if user.Spec.Role != auth.RoleAdmin {
		t.Errorf("role = %q, want admin via groups claim", user.Spec.Role)
	}

	// Without the group: default role.
	user2, _, err := ResolveIdentity(store, cfg, testClaims("sub-3", "alice", "", []string{"devs"}))
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if user2.Spec.Role != auth.RoleView {
		t.Errorf("role = %q, want default view", user2.Spec.Role)
	}
}

func TestResolveIdentity_LinksExistingUser(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.CreateUser("carol", state.UserSpec{Role: "admin", PasswordHash: "x"}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	cfg := Config{Issuer: "https://sso.example.com"}
	user, linked, err := ResolveIdentity(store, cfg, testClaims("sub-4", "carol", "", nil))
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if !linked {
		t.Error("matching an existing local user must link, not provision")
	}
	if user.Spec.Role != "admin" {
		t.Errorf("linked user role = %q, want the existing admin role untouched", user.Spec.Role)
	}
	if user.Spec.PasswordHash != "x" {
		t.Error("linked user must be the existing record, not a new one")
	}
}

func TestResolveIdentity_LocalRoleEditsSurvive(t *testing.T) {
	store := newTestStore(t)
	cfg := Config{Issuer: "https://sso.example.com", DefaultRole: "view", AdminGroup: "g"}

	// Provisioned as view; an operator then promotes the user locally.
	user, _, err := ResolveIdentity(store, cfg, testClaims("sub-5", "dave", "", nil))
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if _, err := store.UpdateUser(user.Metadata.Name, user.Metadata.ResourceVersion,
		state.UserSpec{Role: "admin", PasswordHash: user.Spec.PasswordHash}); err != nil {
		t.Fatalf("promote: %v", err)
	}

	// A later federated login must not clobber the local role edit —
	// even when the groups claim would map lower/higher.
	after, _, err := ResolveIdentity(store, cfg, testClaims("sub-5", "dave", "", nil))
	if err != nil {
		t.Fatalf("ResolveIdentity again: %v", err)
	}
	if after.Spec.Role != "admin" {
		t.Errorf("role = %q, local edit must survive federated re-login", after.Spec.Role)
	}
}

func TestResolveIdentity_NoUsernameClaim(t *testing.T) {
	store := newTestStore(t)
	if _, _, err := ResolveIdentity(store, Config{Issuer: "https://sso"}, testClaims("sub-6", "", "", nil)); err == nil {
		t.Error("identity without username/email claims must be rejected")
	}
}

func TestNormalizeUsername(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Jane.Doe", "jane.doe"},
		{"jane@example.com", "jane"},
		{"  Spacey Name  ", "spacey-name"},
		{"Ünïcode@x.com", "n-code"}, // non-ascii dropped, local part kept
		{"--weird--", "weird"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeUsername(c.in); got != c.want {
			t.Errorf("NormalizeUsername(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	long := NormalizeUsername(strings.Repeat("a", 100))
	if len(long) > 63 {
		t.Errorf("NormalizeUsername must cap at 63 bytes, got %d", len(long))
	}
}

func TestMapRole(t *testing.T) {
	cfg := Config{DefaultRole: "edit", AdminGroup: "g"}
	if got := MapRole(cfg, []string{"g"}); got != auth.RoleAdmin {
		t.Errorf("MapRole with admin group = %q, want admin", got)
	}
	if got := MapRole(cfg, nil); got != "edit" {
		t.Errorf("MapRole without groups = %q, want edit", got)
	}
	invalid := Config{DefaultRole: "wizard"}
	if got := MapRole(invalid, nil); got != auth.RoleView {
		t.Errorf("MapRole with invalid default = %q, want view fallback", got)
	}
}
