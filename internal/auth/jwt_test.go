package auth

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

func setupAuthTest(t *testing.T) (*state.Store, *JWTManager) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, NewJWTManager("test-secret-key")
}

func createTestUser(t *testing.T, store *state.Store, username, role, password string) *state.User {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	user, err := store.CreateUser(username, state.UserSpec{
		Role:         role,
		PasswordHash: hash,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return user
}

// --- JWT Tests ---

func TestJWT_GenerateAndValidate(t *testing.T) {
	_, mgr := setupAuthTest(t)

	user := &state.User{
		Metadata: state.Metadata{Name: "admin"},
		Spec:     state.UserSpec{Role: RoleAdmin},
	}

	token, err := mgr.GenerateToken(user, DefaultTokenExpiry)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	if token.AccessToken == "" {
		t.Error("token should not be empty")
	}
	if token.TokenType != "Bearer" {
		t.Errorf("token type = %q, want %q", token.TokenType, "Bearer")
	}
	if token.ExpiresAt == 0 {
		t.Error("expiresAt should be set")
	}

	// Validate.
	claims, err := mgr.ValidateToken(token.AccessToken)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.Username != "admin" {
		t.Errorf("username = %q, want %q", claims.Username, "admin")
	}
	if claims.Role != RoleAdmin {
		t.Errorf("role = %q, want %q", claims.Role, RoleAdmin)
	}
}

func TestJWT_ExpiredToken(t *testing.T) {
	_, mgr := setupAuthTest(t)

	user := &state.User{
		Metadata: state.Metadata{Name: "test"},
		Spec:     state.UserSpec{Role: RoleView},
	}

	token, _ := mgr.GenerateToken(user, -1*time.Hour) // already expired

	_, err := mgr.ValidateToken(token.AccessToken)
	if err != ErrExpiredToken {
		t.Errorf("error = %v, want ErrExpiredToken", err)
	}
}

func TestJWT_InvalidToken(t *testing.T) {
	_, mgr := setupAuthTest(t)

	_, err := mgr.ValidateToken("invalid-token-string")
	if err != ErrInvalidToken {
		t.Errorf("error = %v, want ErrInvalidToken", err)
	}
}

func TestJWT_WrongSecret(t *testing.T) {
	_, mgr1 := setupAuthTest(t)
	mgr2 := NewJWTManager("different-secret")

	user := &state.User{
		Metadata: state.Metadata{Name: "test"},
		Spec:     state.UserSpec{Role: RoleEdit},
	}

	token, _ := mgr1.GenerateToken(user, DefaultTokenExpiry)

	_, err := mgr2.ValidateToken(token.AccessToken)
	if err != ErrInvalidToken {
		t.Errorf("error = %v, want ErrInvalidToken", err)
	}
}

func TestJWT_InvalidRole(t *testing.T) {
	_, mgr := setupAuthTest(t)

	user := &state.User{
		Metadata: state.Metadata{Name: "test"},
		Spec:     state.UserSpec{Role: "superadmin"},
	}

	_, err := mgr.GenerateToken(user, DefaultTokenExpiry)
	if err != ErrInvalidRole {
		t.Errorf("error = %v, want ErrInvalidRole", err)
	}
}

// --- Bcrypt Tests ---

func TestBcrypt_HashAndCheck(t *testing.T) {
	password := "secure-password-123"

	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if !CheckPassword(password, hash) {
		t.Error("CheckPassword should succeed with correct password")
	}

	if CheckPassword("wrong-password", hash) {
		t.Error("CheckPassword should fail with wrong password")
	}
}

func TestBcrypt_DifferentHashes(t *testing.T) {
	hash1, _ := HashPassword("same-password")
	hash2, _ := HashPassword("same-password")

	if hash1 == hash2 {
		t.Error("two hashes of the same password should differ (bcrypt salt)")
	}
}
