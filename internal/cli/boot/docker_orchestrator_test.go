//go:build docker

package boot_test

import (
	"context"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/cli/boot"
	"github.com/rezuscloud/rezuscloud/internal/cli/platform/docker"
	"github.com/rezuscloud/rezuscloud/internal/cli/state"
)

func TestDockerBoot_Orchestrator(t *testing.T) {
	const clusterName = "rezusctl-boot"

	ctx := context.Background()
	dp, err := docker.New(ctx)
	if err != nil {
		t.Fatalf("docker.New: %v", err)
	}
	defer dp.Close()

	bootStore := state.NewBootStore(t.TempDir())

	orch := boot.NewDockerBootOrchestrator(dp, bootStore, t.TempDir(), boot.DockerBootSpec{
		ClusterName:       clusterName,
		KubernetesVersion: "1.35.0",
		CiliumVersion:     "1.17.3",
		ControlPlanes:     1,
		Workers:           0,
	}, &testWriter{t})

	// Run only the first 4 steps (up to container creation).
	// Bootstrap + CNI install require a fully initialized Talos etcd cluster
	// which takes 60-90s and needs Talos API bootstrap.
	// This validates the orchestrator state machine + resume capability.
	events := make(chan boot.Event, 100)

	// First run — will execute auth through create-nodes.
	t.Log("=== First run: auth → create-nodes ===")
	err = orch.RunPartial(ctx, events, "create-nodes")
	if err != nil {
		t.Fatalf("RunPartial: %v", err)
	}

	status, _ := orch.Status(ctx)
	for _, s := range status.Steps {
		t.Logf("  %s: %s", s.Name, s.Status)
	}

	// Verify step markers exist in BootStore for completed steps.
	completed, err := bootStore.CompletedSteps(ctx, clusterName)
	if err != nil {
		t.Fatalf("CompletedSteps: %v", err)
	}
	if len(completed) != 4 {
		t.Errorf("expected 4 completed steps, got %d: %v", len(completed), completed)
	}

	// Second run — should skip all completed steps.
	t.Log("=== Second run: should skip all completed steps ===")
	orch2 := boot.NewDockerBootOrchestrator(dp, bootStore, t.TempDir(), boot.DockerBootSpec{
		ClusterName:       clusterName,
		KubernetesVersion: "1.35.0",
		ControlPlanes:     1,
		Workers:           0,
	}, &testWriter{t})

	events2 := make(chan boot.Event, 100)
	err = orch2.RunPartial(ctx, events2, "create-nodes")
	if err != nil {
		t.Fatalf("RunPartial (resume): %v", err)
	}

	status2, _ := orch2.Status(ctx)
	for i := 0; i < 4; i++ {
		s := status2.Steps[i]
		if s.Status != boot.StatusComplete {
			t.Errorf("step %s should be complete on resume, got %s", s.Name, s.Status)
		}
	}

	// Cleanup.
	t.Log("=== Cleanup ===")
	if err := orch.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
}

type testWriter struct {
	t *testing.T
}

func (w *testWriter) Write(p []byte) (int, error) {
	w.t.Logf("%s", p)
	return len(p), nil
}
