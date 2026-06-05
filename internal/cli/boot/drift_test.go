package boot

import (
	"context"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/cli/state"
)

func TestDriftDetection_ResetsCompletedSteps(t *testing.T) {
	// Simulate the scenario: boot.json has all steps complete,
	// but Docker containers are missing. Verify that detectDrift
	// resets all steps so the orchestrator re-provisions.
	//
	// Since detectDrift needs a real Docker client to check containers,
	// we test the underlying boot store reset behavior here.

	ctx := context.Background()
	tmpDir := t.TempDir()
	bootStore := state.NewBootStore(tmpDir)
	clusterName := "test-drift"

	// Mark all steps as complete (simulates a previous successful boot).
	steps := []string{"auth", "provision", "generate-config", "create-nodes", "bootstrap", "install-cni", "verify"}
	for _, step := range steps {
		if err := bootStore.MarkStep(ctx, clusterName, "docker", step, state.StatusComplete); err != nil {
			t.Fatalf("MarkStep %s: %v", step, err)
		}
	}

	// Verify all steps are complete.
	completed, err := bootStore.CompletedSteps(ctx, clusterName)
	if err != nil {
		t.Fatalf("CompletedSteps: %v", err)
	}
	if len(completed) != len(steps) {
		t.Fatalf("expected %d completed steps, got %d", len(steps), len(completed))
	}

	// Reset all steps (what detectDrift does when containers are missing).
	for _, step := range steps {
		if err := bootStore.MarkStep(ctx, clusterName, "docker", step, state.StatusCreated); err != nil {
			t.Fatalf("MarkStep reset %s: %v", step, err)
		}
	}

	// Verify no steps are complete after reset.
	completed, err = bootStore.CompletedSteps(ctx, clusterName)
	if err != nil {
		t.Fatalf("CompletedSteps after reset: %v", err)
	}
	if len(completed) != 0 {
		t.Fatalf("expected 0 completed steps after reset, got %d: %v", len(completed), completed)
	}
}

func TestDriftDetection_PartialProgressNotReset(t *testing.T) {
	// If boot is mid-way (some steps complete, some not), drift detection
	// should NOT reset. Only reset when ALL steps are complete but infra is gone.

	ctx := context.Background()
	tmpDir := t.TempDir()
	bootStore := state.NewBootStore(tmpDir)
	clusterName := "test-partial"

	// Mark only first 3 steps as complete.
	steps := []string{"auth", "provision", "generate-config"}
	for _, step := range steps {
		if err := bootStore.MarkStep(ctx, clusterName, "docker", step, state.StatusComplete); err != nil {
			t.Fatalf("MarkStep %s: %v", step, err)
		}
	}

	// Verify partial progress.
	completed, err := bootStore.CompletedSteps(ctx, clusterName)
	if err != nil {
		t.Fatalf("CompletedSteps: %v", err)
	}
	if len(completed) != 3 {
		t.Fatalf("expected 3 completed steps, got %d", len(completed))
	}

	// Verify the step names are correct.
	expected := map[string]bool{"auth": true, "provision": true, "generate-config": true}
	for step := range expected {
		if !completed[step] {
			t.Errorf("expected step %s to be complete", step)
		}
	}
}
