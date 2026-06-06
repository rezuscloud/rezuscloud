package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAPITokenHandlers_CreateAndGet(t *testing.T) {
	store, _ := setupAuthTest(t)
	_ = createTestUser(t, store, "alice", RoleEdit, "pw123456")

	h := NewAPITokenHandlers(store)

	// POST /api/v1/users/alice/api-tokens (as alice).
	body := bytes.NewReader([]byte(`{}`))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/alice/api-tokens", body)
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("name", "alice")
	req = req.WithContext(WithClaims(context.Background(), "alice", RoleEdit))
	w := httptest.NewRecorder()
	h.CreateForUser(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %s", w.Code, w.Body.String())
	}
	var created CreateAPITokenResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(created.Secret, "rez_") {
		t.Errorf("secret = %q, want rez_ prefix", created.Secret)
	}
	if !strings.HasPrefix(created.ID, "tok_") {
		t.Errorf("id = %q, want tok_ prefix", created.ID)
	}
	if created.UserName != "alice" {
		t.Errorf("userName = %q, want alice", created.UserName)
	}
	if created.Role != RoleEdit {
		t.Errorf("role = %q, want %q", created.Role, RoleEdit)
	}

	// GET /api/v1/api-tokens/{id} — secret MUST NOT be present.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/api-tokens/"+created.ID, nil)
	req.SetPathValue("id", created.ID)
	req = req.WithContext(WithClaims(context.Background(), "alice", RoleEdit))
	w = httptest.NewRecorder()
	h.Get(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("get: status = %d, body = %s", w.Code, w.Body.String())
	}
	var got APITokenResponse
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got.ID != created.ID {
		t.Errorf("id mismatch")
	}
	if strings.Contains(w.Body.String(), "rez_") {
		t.Errorf("get response must not contain plaintext secret: %s", w.Body.String())
	}
}

func TestAPITokenHandlers_CreateForbiddenForOtherUser(t *testing.T) {
	store, _ := setupAuthTest(t)
	_ = createTestUser(t, store, "alice", RoleEdit, "pw123456")
	_ = createTestUser(t, store, "bob", RoleView, "pw123456")

	h := NewAPITokenHandlers(store)

	// bob trying to create token for alice.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/alice/api-tokens", bytes.NewReader([]byte(`{}`)))
	req.SetPathValue("name", "alice")
	req = req.WithContext(WithClaims(context.Background(), "bob", RoleView))
	w := httptest.NewRecorder()
	h.CreateForUser(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-user create should be forbidden, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestAPITokenHandlers_Delete(t *testing.T) {
	store, _ := setupAuthTest(t)
	_ = createTestUser(t, store, "alice", RoleAdmin, "pw123456")

	h := NewAPITokenHandlers(store)
	hash := HashAPIToken("rez_test123")
	tok, err := store.CreateAPIToken("tok_xyz", "alice", hash, nil)
	if err != nil {
		t.Fatalf("seed token: %v", err)
	}

	// DELETE as alice (owner).
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/api-tokens/"+tok.ID, nil)
	req.SetPathValue("id", tok.ID)
	req = req.WithContext(WithClaims(context.Background(), "alice", RoleAdmin))
	w := httptest.NewRecorder()
	h.Delete(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d, body = %s", w.Code, w.Body.String())
	}

	// Confirm gone.
	got, _ := store.GetAPIToken(tok.ID)
	if got != nil {
		t.Errorf("token still present after delete")
	}
}

func TestAPITokenHandlers_ListForUser(t *testing.T) {
	store, _ := setupAuthTest(t)
	_ = createTestUser(t, store, "alice", RoleEdit, "pw123456")
	_ = createTestUser(t, store, "bob", RoleView, "pw123456")

	hash1 := HashAPIToken("rez_aaaaaa")
	hash2 := HashAPIToken("rez_bbbbbb")
	hash3 := HashAPIToken("rez_cccccc")
	if _, err := store.CreateAPIToken("tok_a", "alice", hash1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAPIToken("tok_b", "alice", hash2, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAPIToken("tok_c", "bob", hash3, nil); err != nil {
		t.Fatal(err)
	}

	h := NewAPITokenHandlers(store)

	// alice lists her own — should see 2.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/alice/api-tokens", nil)
	req.SetPathValue("name", "alice")
	req = req.WithContext(WithClaims(context.Background(), "alice", RoleEdit))
	w := httptest.NewRecorder()
	h.ListForUser(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}
	var list APITokenListResponse
	_ = json.NewDecoder(w.Body).Decode(&list)
	if list.Total != 2 {
		t.Errorf("alice tokens: got %d, want 2", list.Total)
	}

	// bob tries to list alice's — forbidden.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/users/alice/api-tokens", nil)
	req.SetPathValue("name", "alice")
	req = req.WithContext(WithClaims(context.Background(), "bob", RoleView))
	w = httptest.NewRecorder()
	h.ListForUser(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-user list should be forbidden, got %d", w.Code)
	}
}

func TestAPITokenHandlers_ListAllAdmin(t *testing.T) {
	store, _ := setupAuthTest(t)
	_ = createTestUser(t, store, "alice", RoleAdmin, "pw123456")
	_ = createTestUser(t, store, "bob", RoleView, "pw123456")

	hash1 := HashAPIToken("rez_aaaaaa")
	hash2 := HashAPIToken("rez_bbbbbb")
	_, _ = store.CreateAPIToken("tok_a", "alice", hash1, nil)
	_, _ = store.CreateAPIToken("tok_b", "bob", hash2, nil)

	h := NewAPITokenHandlers(store)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/api-tokens", nil)
	req = req.WithContext(WithClaims(context.Background(), "alice", RoleAdmin))
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}
	var list APITokenListResponse
	_ = json.NewDecoder(w.Body).Decode(&list)
	if list.Total != 2 {
		t.Errorf("admin list: got %d, want 2", list.Total)
	}

	// bob (non-admin) cannot list all.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/api-tokens", nil)
	req = req.WithContext(WithClaims(context.Background(), "bob", RoleView))
	w = httptest.NewRecorder()
	h.List(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("non-admin list-all should be forbidden, got %d", w.Code)
	}
}

func TestAPITokenHandlers_Expiry(t *testing.T) {
	store, _ := setupAuthTest(t)
	_ = createTestUser(t, store, "alice", RoleEdit, "pw123456")

	body := bytes.NewReader([]byte(`{"expiresInDays": 7}`))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/alice/api-tokens", body)
	req.SetPathValue("name", "alice")
	req = req.WithContext(WithClaims(context.Background(), "alice", RoleEdit))
	w := httptest.NewRecorder()

	h := NewAPITokenHandlers(store)
	h.CreateForUser(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d", w.Code)
	}
	var created CreateAPITokenResponse
	_ = json.NewDecoder(w.Body).Decode(&created)
	if created.ExpiresAt == nil {
		t.Errorf("expiresAt should be set")
	}
	expected := time.Now().UTC().Add(7 * 24 * time.Hour).Unix()
	delta := *created.ExpiresAt - expected
	if delta < -60 || delta > 60 {
		t.Errorf("expiresAt = %d, want ~%d", *created.ExpiresAt, expected)
	}
}

func TestVerifyAPIToken_Lookup(t *testing.T) {
	store, _ := setupAuthTest(t)
	_ = createTestUser(t, store, "alice", RoleEdit, "pw123456")

	hash := HashAPIToken("rez_lookup_secret")
	_, _ = store.CreateAPIToken("tok_l", "alice", hash, nil)

	user, tok, ok := VerifyAPIToken(store, "rez_lookup_secret")
	if !ok || user == nil || tok == nil {
		t.Fatalf("verify should succeed, got user=%v tok=%v ok=%v", user, tok, ok)
	}
	if user.Metadata.Name != "alice" {
		t.Errorf("user.Name = %q, want alice", user.Metadata.Name)
	}
	if tok.ID != "tok_l" {
		t.Errorf("token.ID = %q, want tok_l", tok.ID)
	}

	// Wrong plaintext.
	_, _, ok = VerifyAPIToken(store, "rez_unknown")
	if ok {
		t.Errorf("verify should fail for unknown token")
	}
}

func TestVerifyAPIToken_Expired(t *testing.T) {
	store, _ := setupAuthTest(t)
	_ = createTestUser(t, store, "alice", RoleEdit, "pw123456")

	expired := time.Now().UTC().Add(-1 * time.Hour)
	hash := HashAPIToken("rez_expired")
	_, _ = store.CreateAPIToken("tok_e", "alice", hash, &expired)

	_, _, ok := VerifyAPIToken(store, "rez_expired")
	if ok {
		t.Errorf("verify should fail for expired token")
	}
}

func TestAuthenticateWithTokens_APIToken(t *testing.T) {
	store, _ := setupAuthTest(t)
	_ = createTestUser(t, store, "alice", RoleView, "pw123456")

	hash := HashAPIToken("rez_middleware_test")
	_, _ = store.CreateAPIToken("tok_m", "alice", hash, nil)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if UserFromContext(r.Context()) != "alice" {
			t.Errorf("user in ctx = %q, want alice", UserFromContext(r.Context()))
		}
		if RoleFromContext(r.Context()) != RoleView {
			t.Errorf("role in ctx = %q, want %q", RoleFromContext(r.Context()), RoleView)
		}
		w.WriteHeader(http.StatusOK)
	})

	mgr := NewJWTManager("secret")
	mw := AuthenticateWithTokens(mgr, StoreTokenVerifier{Store: store}, next)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
	req.Header.Set("Authorization", "Bearer rez_middleware_test")
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !called {
		t.Fatalf("middleware: status=%d called=%v body=%s", w.Code, called, w.Body.String())
	}
}

func TestAuthenticateWithTokens_StillAcceptsJWT(t *testing.T) {
	store, _ := setupAuthTest(t)
	user := createTestUser(t, store, "alice", RoleEdit, "pw123456")
	jwtMgr := NewJWTManager("secret")

	pair, err := jwtMgr.GenerateToken(user, DefaultTokenExpiry)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	called := false
	mw := AuthenticateWithTokens(jwtMgr, StoreTokenVerifier{Store: store}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !called {
		t.Fatalf("middleware: status=%d called=%v", w.Code, called)
	}
}

func TestAuthenticateWithTokens_RejectsInvalidAPIToken(t *testing.T) {
	store, _ := setupAuthTest(t)
	mw := AuthenticateWithTokens(NewJWTManager("secret"), StoreTokenVerifier{Store: store}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
	req.Header.Set("Authorization", "Bearer rez_does_not_exist")
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}
