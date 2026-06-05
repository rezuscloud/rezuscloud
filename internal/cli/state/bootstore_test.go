package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func testBootStore(t *testing.T) *BootStore {
	t.Helper()
	return NewBootStore(t.TempDir())
}

func TestBootStore_LoadEmpty(t *testing.T) {
	s := testBootStore(t)
	state, err := s.Load(context.Background(), "test-cluster")
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if state != nil {
		t.Fatalf("expected nil state for nonexistent cluster, got %+v", state)
	}
}

func TestBootStore_SaveAndLoad(t *testing.T) {
	s := testBootStore(t)
	ctx := context.Background()

	state := &BootState{
		ClusterName: "test-cluster",
		Platform:    "docker",
		Steps: map[string]StepStatus{
			"auth":      StatusComplete,
			"provision": StatusComplete,
			"verify":    StatusRunning,
		},
	}

	if err := s.Save(ctx, state); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := s.Load(ctx, "test-cluster")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.ClusterName != "test-cluster" {
		t.Errorf("ClusterName = %q, want %q", loaded.ClusterName, "test-cluster")
	}
	if loaded.Platform != "docker" {
		t.Errorf("Platform = %q, want %q", loaded.Platform, "docker")
	}
	if len(loaded.Steps) != 3 {
		t.Fatalf("len(Steps) = %d, want 3", len(loaded.Steps))
	}
	if loaded.Steps["auth"] != StatusComplete {
		t.Errorf("Steps[auth] = %q, want %q", loaded.Steps["auth"], StatusComplete)
	}
	if loaded.CreatedAt == "" {
		t.Error("CreatedAt should be set")
	}
	if loaded.UpdatedAt == "" {
		t.Error("UpdatedAt should be set")
	}
}

func TestBootStore_MarkStep(t *testing.T) {
	s := testBootStore(t)
	ctx := context.Background()

	if err := s.MarkStep(ctx, "test-cluster", "docker", "auth", StatusComplete); err != nil {
		t.Fatalf("MarkStep: %v", err)
	}

	complete, err := s.IsStepComplete(ctx, "test-cluster", "auth")
	if err != nil {
		t.Fatalf("IsStepComplete: %v", err)
	}
	if !complete {
		t.Error("auth should be complete")
	}
}

func TestBootStore_IsStepComplete(t *testing.T) {
	s := testBootStore(t)
	ctx := context.Background()

	// Not complete before marking
	complete, err := s.IsStepComplete(ctx, "test-cluster", "auth")
	if err != nil {
		t.Fatalf("IsStepComplete: %v", err)
	}
	if complete {
		t.Error("auth should not be complete")
	}

	// Mark as complete
	if err := s.MarkStep(ctx, "test-cluster", "docker", "auth", StatusComplete); err != nil {
		t.Fatal(err)
	}

	// Now complete
	complete, err = s.IsStepComplete(ctx, "test-cluster", "auth")
	if err != nil {
		t.Fatalf("IsStepComplete: %v", err)
	}
	if !complete {
		t.Error("auth should be complete after marking")
	}

	// Nonexistent step
	complete, err = s.IsStepComplete(ctx, "test-cluster", "nonexistent")
	if err != nil {
		t.Fatalf("IsStepComplete nonexistent: %v", err)
	}
	if complete {
		t.Error("nonexistent step should not be complete")
	}
}

func TestBootStore_CompletedSteps(t *testing.T) {
	s := testBootStore(t)
	ctx := context.Background()

	if err := s.MarkStep(ctx, "test-cluster", "docker", "auth", StatusComplete); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkStep(ctx, "test-cluster", "docker", "provision", StatusComplete); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkStep(ctx, "test-cluster", "docker", "verify", StatusFailed); err != nil {
		t.Fatal(err)
	}

	completed, err := s.CompletedSteps(ctx, "test-cluster")
	if err != nil {
		t.Fatalf("CompletedSteps: %v", err)
	}

	if len(completed) != 2 {
		t.Fatalf("expected 2 completed steps, got %d", len(completed))
	}
	if !completed["auth"] {
		t.Error("auth should be in completed")
	}
	if !completed["provision"] {
		t.Error("provision should be in completed")
	}
	if completed["verify"] {
		t.Error("verify should not be in completed (it failed)")
	}
}

func TestBootStore_Delete(t *testing.T) {
	s := testBootStore(t)
	ctx := context.Background()

	if err := s.MarkStep(ctx, "test-cluster", "docker", "auth", StatusComplete); err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(ctx, "test-cluster"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	state, err := s.Load(ctx, "test-cluster")
	if err != nil {
		t.Fatalf("Load after delete: %v", err)
	}
	if state != nil {
		t.Error("state should be nil after delete")
	}
}

func TestBootStore_FileFormat(t *testing.T) {
	dir := t.TempDir()
	s := NewBootStore(dir)
	ctx := context.Background()

	if err := s.MarkStep(ctx, "test-cluster", "docker", "auth", StatusComplete); err != nil {
		t.Fatal(err)
	}

	// Verify the file exists and is valid JSON
	data, err := os.ReadFile(filepath.Join(dir, "test-cluster", "boot.json"))
	if err != nil {
		t.Fatalf("read boot.json: %v", err)
	}

	body := string(data)
	if len(body) == 0 {
		t.Fatal("boot.json is empty")
	}
	if !contains(body, `"clusterName": "test-cluster"`) {
		t.Errorf("boot.json does not contain clusterName:\n%s", body)
	}
	if !contains(body, `"auth": "complete"`) {
		t.Errorf("boot.json does not contain auth step:\n%s", body)
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
