package upgrade

import (
	"path/filepath"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// TestNewManager_VerifiesNoGlobalState ensures NewManager returns a fully
// populated *Manager and that two calls with different stores return
// distinct managers (i.e. no shared package-level state).
func TestNewManager_VerifiesNoGlobalState(t *testing.T) {
	store1, err := state.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatalf("open store1: %v", err)
	}
	t.Cleanup(func() { _ = store1.Close() })

	store2, err := state.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatalf("open store2: %v", err)
	}
	t.Cleanup(func() { _ = store2.Close() })

	mgr1 := NewManager(store1)
	mgr2 := NewManager(store2)

	if mgr1 == nil || mgr2 == nil {
		t.Fatal("expected non-nil managers")
	}
	if mgr1 == mgr2 {
		t.Error("expected distinct managers for distinct stores")
	}
	if mgr1.store != store1 || mgr2.store != store2 {
		t.Error("managers not bound to the correct store")
	}
	if mgr1.running == nil || mgr2.running == nil {
		t.Error("running map not initialized")
	}
}
