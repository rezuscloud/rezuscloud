//go:build docker

package boot_test

import (
	"context"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/cli/boot"
	"github.com/rezuscloud/rezuscloud/internal/cli/platform/docker"
	"github.com/rezuscloud/rezuscloud/internal/cli/state"
)

func TestDockerBoot_FullEndToEnd(t *testing.T) {
	const clusterName = "rezusctl-e2e"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	dp, err := docker.New(ctx)
	if err != nil {
		t.Fatalf("docker.New: %v", err)
	}
	defer dp.Close()

	store := state.NewBootStore(t.TempDir())

	orch := boot.NewDockerBootOrchestrator(dp, store, t.TempDir(), boot.DockerBootSpec{
		ClusterName:       clusterName,
		KubernetesVersion: "1.35.0",
		CiliumVersion:     "1.19.3",
		ControlPlanes:     1,
		Workers:           0,
	}, &testWriter{t})

	events := make(chan boot.Event, 100)

	t.Log("=== Full boot: all 7 steps ===")
	err = orch.Run(ctx, events)
	if err != nil {
		status, _ := orch.Status(ctx)
		for _, s := range status.Steps {
			t.Logf("  %s: %s %s", s.Name, s.Status, s.Error)
		}
		t.Fatalf("Run: %v", err)
	}

	status, _ := orch.Status(ctx)
	for _, s := range status.Steps {
		t.Logf("  %s: %s (%s)", s.Name, s.Status, s.Duration)
	}

	if !status.Complete {
		t.Fatal("boot should be complete")
	}

	// Cleanup.
	t.Log("=== Cleanup ===")
	if err := orch.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
}
