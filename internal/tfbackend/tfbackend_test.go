package tfbackend

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := New(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

func TestStateCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// GET missing → not found, no error.
	if _, found, err := s.GetState(ctx, "tenant-a"); err != nil || found {
		t.Fatalf("expected not-found, got found=%v err=%v", found, err)
	}

	// PUT then GET round-trips the opaque bytes (we do not interpret them).
	want := []byte(`{"version":4,"serial":7,"resources":[]}`)
	if err := s.PutState(ctx, "tenant-a", want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, found, err := s.GetState(ctx, "tenant-a")
	if err != nil || !found {
		t.Fatalf("get after put: found=%v err=%v", found, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("state bytes changed: got %q want %q", got, want)
	}

	// PUT again upserts (overwrites) rather than erroring.
	if err := s.PutState(ctx, "tenant-a", []byte(`{"version":4,"serial":8}`)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, _, _ = s.GetState(ctx, "tenant-a")
	if string(got) != `{"version":4,"serial":8}` {
		t.Fatalf("upserted bytes wrong: %q", got)
	}

	// DELETE returns found=true.
	found, err = s.DeleteState(ctx, "tenant-a")
	if err != nil || !found {
		t.Fatalf("delete existing: found=%v err=%v", found, err)
	}
	// Second DELETE → found=false.
	found, err = s.DeleteState(ctx, "tenant-a")
	if err != nil || found {
		t.Fatalf("delete again: found=%v err=%v", found, err)
	}
}

func TestLockUnlock(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	info := LockInfo{ID: "lock-1", Who: "ci@host", Operation: "OperationTypeLock"}

	// Acquire succeeds.
	if holder, err := s.Lock(ctx, "tenant-a", info); err != nil || holder != nil {
		t.Fatalf("first lock: holder=%v err=%v", holder, err)
	}

	// Second lock on the same workspace is rejected with the holder.
	holder, err := s.Lock(ctx, "tenant-a", LockInfo{ID: "lock-2"})
	if !isLockHeld(err) {
		t.Fatalf("second lock: expected ErrLockHeld, got %v", err)
	}
	if holder == nil || holder.ID != "lock-1" {
		t.Fatalf("conflicting holder = %+v, want lock-1", holder)
	}

	// Unlock with the wrong ID is a mismatch (lock stays held).
	released, mismatch, err := s.Unlock(ctx, "tenant-a", "lock-2")
	if err != nil || released || !mismatch {
		t.Fatalf("unlock mismatch: released=%v mismatch=%v err=%v", released, mismatch, err)
	}
	// Workspace is still locked.
	if _, err := s.Lock(ctx, "tenant-a", LockInfo{ID: "x"}); !isLockHeld(err) {
		t.Fatalf("expected still locked after mismatch")
	}

	// Correct unlock releases it.
	released, mismatch, err = s.Unlock(ctx, "tenant-a", "lock-1")
	if err != nil || !released || mismatch {
		t.Fatalf("correct unlock: released=%v mismatch=%v err=%v", released, mismatch, err)
	}
	// Now a new lock succeeds.
	if holder, err := s.Lock(ctx, "tenant-a", LockInfo{ID: "lock-3"}); err != nil || holder != nil {
		t.Fatalf("lock after unlock: holder=%v err=%v", holder, err)
	}
}

func isLockHeld(err error) bool { return err != nil && err == ErrLockHeld }

// --- HTTP handler ---

func TestHandlerReadMissingReturns404(t *testing.T) {
	h := NewHandler(newTestStore(t))
	req := httptest.NewRequest(http.MethodGet, "/tfstate?ID=ghost", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestHandlerWriteReadRoundTrip(t *testing.T) {
	h := NewHandler(newTestStore(t))
	want := []byte(`{"version":4,"serial":3,"terraformversion":"1.6.0"}`)

	// POST
	req := httptest.NewRequest(http.MethodPost, "/tfstate?ID=prod", bytes.NewReader(want))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("post code = %d, want 200", rec.Code)
	}

	// GET
	req = httptest.NewRequest(http.MethodGet, "/tfstate?ID=prod", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get code = %d, want 200", rec.Code)
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestHandlerLockConflictReturns423WithHolder(t *testing.T) {
	h := NewHandler(newTestStore(t))

	first := LockInfo{ID: "l-1", Who: "alice"}
	body, _ := json.Marshal(first)

	// First LOCK → 200.
	req := httptest.NewRequest("LOCK", "/tfstate?ID=prod", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first lock code = %d, want 200", rec.Code)
	}

	// Second LOCK → 423 with the holder in the body.
	second := LockInfo{ID: "l-2"}
	body2, _ := json.Marshal(second)
	req = httptest.NewRequest("LOCK", "/tfstate?ID=prod", bytes.NewReader(body2))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusLocked {
		t.Fatalf("conflict code = %d, want 423", rec.Code)
	}
	var holder LockInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &holder); err != nil {
		t.Fatalf("decode holder: %v", err)
	}
	if holder.ID != "l-1" {
		t.Fatalf("holder ID = %q, want l-1", holder.ID)
	}
}

func TestHandlerUnlockMismatchReturns409(t *testing.T) {
	h := NewHandler(newTestStore(t))

	// Acquire lock l-1 directly via the store.
	if _, err := h.store.Lock(context.Background(), "prod", LockInfo{ID: "l-1"}); err != nil {
		t.Fatalf("seed lock: %v", err)
	}

	// UNLOCK with the wrong ID → 409.
	body := `{"ID":"wrong"}`
	req := httptest.NewRequest("UNLOCK", "/tfstate?ID=prod", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("mismatch code = %d, want 409", rec.Code)
	}

	// Correct UNLOCK → 200.
	body = `{"ID":"l-1"}`
	req = httptest.NewRequest("UNLOCK", "/tfstate?ID=prod", bytes.NewReader([]byte(body)))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("correct unlock code = %d, want 200", rec.Code)
	}
}

func TestHandlerDeleteMissingReturns404(t *testing.T) {
	h := NewHandler(newTestStore(t))
	req := httptest.NewRequest(http.MethodDelete, "/tfstate?ID=ghost", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete-missing code = %d, want 404", rec.Code)
	}
}

func TestWorkspaceIDDefaultsToDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/tfstate", nil)
	if got := workspaceID(req); got != "default" {
		t.Fatalf("workspaceID = %q, want default", got)
	}
}
