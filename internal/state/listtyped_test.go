package state

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

// TestListTyped_ReturnsBuiltItems verifies that ListTyped calls the build
// callback for each row and returns the constructed slice + total.
func TestListTyped_ReturnsBuiltItems(t *testing.T) {
	store := newTestStoreHelper(t)

	// Seed 3 nodegroup resources for tenant "alpha".
	for _, name := range []string{"ng-a", "ng-b", "ng-c"} {
		spec := NodeGroupSpec{Name: name, Role: "worker", Count: 3}
		_, err := store.CreateResource("nodegroup", name, spec, nil,
			map[string]string{"rezuscloud.io/tenant": "alpha"}, nil)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	type ngRow struct {
		Name  string
		Role  string
		Count int
	}

	items, total, err := ListTyped[ngRow](store, "nodegroup", ListOptions{
		LabelSelector: "rezuscloud.io/tenant=alpha",
	}, func(meta Metadata, specRaw, _ json.RawMessage) (ngRow, error) {
		var spec NodeGroupSpec
		_ = json.Unmarshal(specRaw, &spec)
		return ngRow{Name: meta.Name, Role: spec.Role, Count: spec.Count}, nil
	})
	if err != nil {
		t.Fatalf("ListTyped: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(items) != 3 {
		t.Fatalf("items = %v, want 3 entries", items)
	}
}

// TestListTypedByTenant_VerifyLabelFilter verifies ListTypedByTenant constructs
// the correct label selector and returns only the right tenant's rows.
func TestListTypedByTenant_VerifyLabelFilter(t *testing.T) {
	store := newTestStoreHelper(t)

	_, _ = store.CreateResource("nodegroup", "ng-a", NodeGroupSpec{Role: "worker", Count: 1},
		nil, map[string]string{"rezuscloud.io/tenant": "alpha"}, nil)
	_, _ = store.CreateResource("nodegroup", "ng-b", NodeGroupSpec{Role: "worker", Count: 2},
		nil, map[string]string{"rezuscloud.io/tenant": "beta"}, nil)

	items, total, err := ListTypedByTenant(store, "nodegroup", "alpha",
		func(meta Metadata, _, _ json.RawMessage) (string, error) {
			return meta.Name, nil
		})
	if err != nil {
		t.Fatalf("ListTypedByTenant: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	if len(items) != 1 || items[0] != "ng-a" {
		t.Errorf("items = %v, want [ng-a]", items)
	}
}

// TestListTyped_SkipsOnBuildError verifies a build-callback failure causes
// the row to be skipped (matching the prior `continue` behaviour).
func TestListTyped_SkipsOnBuildError(t *testing.T) {
	store := newTestStoreHelper(t)
	_, _ = store.CreateResource("nodegroup", "ng-good", NodeGroupSpec{Role: "worker", Count: 1}, nil, nil, nil)
	_, _ = store.CreateResource("nodegroup", "ng-bad", NodeGroupSpec{Role: "worker", Count: 2}, nil, nil, nil)

	count := 0
	items, _, err := ListTyped(store, "nodegroup", ListOptions{},
		func(meta Metadata, _, _ json.RawMessage) (string, error) {
			count++
			if meta.Name == "ng-bad" {
				return "", fmt.Errorf("synthetic build error")
			}
			return meta.Name, nil
		})
	if err != nil {
		t.Fatalf("ListTyped: %v", err)
	}
	if count != 2 {
		t.Errorf("build callback invoked %d times, want 2", count)
	}
	if len(items) != 1 || items[0] != "ng-good" {
		t.Errorf("items = %v, want [ng-good]", items)
	}
}

// newTestStoreHelper opens a per-test store at a tempdir.
func newTestStoreHelper(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "listtyped.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
