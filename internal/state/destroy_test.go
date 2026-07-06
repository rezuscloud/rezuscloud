package state

import (
	"encoding/json"
	"testing"
)

// TestRemoveResourcesByTenant proves the cascade-delete used during tenant
// teardown (#171): after tofu destroy, every child resource carrying the
// rezuscloud.io/tenant label is hard-deleted, but the tenant row itself is left
// for the finalizer flow to GC.
func TestRemoveResourcesByTenant(t *testing.T) {
	s := openTestStore(t)

	_, err := s.CreateTenant("t1", TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Seed child resources across multiple types, all labelled with the tenant.
	specNG, _ := json.Marshal(map[string]any{"count": 2, "role": "worker"})
	if _, err := s.CreateResource("nodegroup", "workers", json.RawMessage(specNG), struct{}{},
		map[string]string{"rezuscloud.io/tenant": "t1"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateResource("nodegroup", "cp", json.RawMessage(specNG), struct{}{},
		map[string]string{"rezuscloud.io/tenant": "t1"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateResource("configpatch", "p1", json.RawMessage(`{}`), struct{}{},
		map[string]string{"rezuscloud.io/tenant": "t1"}, nil); err != nil {
		t.Fatal(err)
	}
	// A machine for a DIFFERENT tenant — must NOT be removed.
	if _, err := s.CreateResource("machine", "other-m", json.RawMessage(`{}`), struct{}{},
		map[string]string{"rezuscloud.io/tenant": "t2"}, nil); err != nil {
		t.Fatal(err)
	}
	// An unlabelled resource — must NOT be removed.
	if _, err := s.CreateResource("machine", "lone-m", json.RawMessage(`{}`), struct{}{},
		nil, nil); err != nil {
		t.Fatal(err)
	}

	n, err := s.RemoveResourcesByTenant("t1")
	if err != nil {
		t.Fatalf("RemoveResourcesByTenant: %v", err)
	}
	if n != 3 {
		t.Errorf("removed %d, want 3 (2 nodegroups + 1 patch)", n)
	}

	// The tenant row survives (finalizer flow GCs it separately).
	if t1, _ := s.GetTenant("t1"); t1 == nil {
		t.Error("tenant t1 was deleted by the cascade — it should survive for finalizer GC")
	}

	// t1's children are gone.
	metas, _, _, total, err := s.ListResources("nodegroup", ListOptions{LabelSelector: "rezuscloud.io/tenant=t1"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(metas) != 0 {
		t.Errorf("t1 nodegroups: %d remain, want 0", total)
	}

	// The other tenant's machine survives.
	metas, _, _, total, _ = s.ListResources("machine", ListOptions{LabelSelector: "rezuscloud.io/tenant=t2"})
	if total != 1 {
		t.Errorf("t2 machines: %d, want 1 (cascade must be tenant-scoped)", total)
	}

	// The unlabelled machine survives.
	if m, _ := s.GetMachine("lone-m"); m == nil {
		t.Error("unlabelled machine was deleted by the cascade")
	}
}

// TestDeleteTenant_SetsFinalizers confirms the finalizer contract: a soft-delete
// stamps a deletionTimestamp and exactly two finalizers so the controller knows
// teardown is pending (#171).
func TestDeleteTenant_SetsFinalizers(t *testing.T) {
	s := openTestStore(t)

	_, err := s.CreateTenant("t1", TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteTenant("t1"); err != nil {
		t.Fatal(err)
	}

	t1, _ := s.GetTenant("t1")
	if t1 == nil {
		t.Fatal("tenant should still exist (soft delete)")
	}
	if t1.Metadata.DeletionTimestamp == nil {
		t.Fatal("deletionTimestamp should be set")
	}
	want := []string{"rezuscloud.io/machines", "rezuscloud.io/secrets"}
	if len(t1.Metadata.Finalizers) != len(want) {
		t.Fatalf("finalizers = %v, want %v", t1.Metadata.Finalizers, want)
	}
	for _, f := range want {
		found := false
		for _, have := range t1.Metadata.Finalizers {
			if have == f {
				found = true
			}
		}
		if !found {
			t.Errorf("missing finalizer %q in %v", f, t1.Metadata.Finalizers)
		}
	}
}

// TestRemoveFinalizer_AutoGCsTenant proves the store auto-GCs a tenant when the
// last finalizer is removed and a deletionTimestamp is set — the final step of
// the destroy controller (#171).
func TestRemoveFinalizer_AutoGCsTenant(t *testing.T) {
	s := openTestStore(t)

	_, err := s.CreateTenant("t1", TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTenant("t1"); err != nil {
		t.Fatal(err)
	}

	removed, err := s.RemoveFinalizer("tenant", "t1", "rezuscloud.io/machines")
	if err != nil || !removed {
		t.Fatalf("remove first finalizer: removed=%v err=%v", removed, err)
	}
	// Still present — one finalizer remains.
	if t1, _ := s.GetTenant("t1"); t1 == nil {
		t.Fatal("tenant GC'd after first finalizer — should still exist")
	}

	removed, err = s.RemoveFinalizer("tenant", "t1", "rezuscloud.io/secrets")
	if err != nil || !removed {
		t.Fatalf("remove last finalizer: removed=%v err=%v", removed, err)
	}
	// Now gone — last finalizer cleared → auto-GC.
	if t1, _ := s.GetTenant("t1"); t1 != nil {
		t.Fatal("tenant still exists after last finalizer removed — should be GC'd")
	}
}
