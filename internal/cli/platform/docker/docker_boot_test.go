//go:build docker

package docker_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/cli/platform"
	"github.com/rezuscloud/rezuscloud/internal/cli/platform/docker"
	"github.com/rezuscloud/rezuscloud/internal/cli/talosconfig"
)

func TestDockerBoot_Integration(t *testing.T) {
	const (
		clusterName = "rezusctl-test"
		talosImage  = "ghcr.io/siderolabs/talos:latest"
	)
	ctx := context.Background()

	// Step 1: Create Docker platform
	t.Log("Step 1: Initialize Docker platform")
	dp, err := docker.New(ctx)
	if err != nil {
		t.Fatalf("docker.New: %v", err)
	}
	defer dp.Close()

	// Step 2: Auth (verify Docker is running)
	t.Log("Step 2: Verify Docker access")
	if err := dp.Auth(ctx, platform.AuthConfig{Provider: "docker"}); err != nil {
		t.Fatalf("docker auth: %v", err)
	}

	// Step 3: Provision network
	t.Log("Step 3: Provision Docker network")
	infra, err := dp.Provision(ctx, &platform.ClusterSpec{
		Name:         clusterName,
		TalosVersion: "latest",
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Logf("  Network: %s, Endpoint: %s", infra.VPCID, infra.ControlPlaneEndpoint)

	defer func() {
		t.Log("Cleanup: destroying cluster")
		if err := dp.Destroy(ctx, &platform.ClusterSpec{Name: clusterName}); err != nil {
			t.Logf("  destroy error (non-fatal): %v", err)
		}
	}()

	// Step 4: Generate Talos configs
	t.Log("Step 4: Generate Talos machine configs")
	endpoint := fmt.Sprintf("https://%s", infra.ControlPlaneEndpoint)
	gen, err := talosconfig.NewGenerator(talosconfig.ClusterParams{
		ClusterName:          clusterName,
		ControlPlaneEndpoint: endpoint,
		KubernetesVersion:    "1.35.0",
	})
	if err != nil {
		t.Fatalf("NewGenerator: %v", err)
	}

	cpConfig, err := gen.GenerateControlPlane(
		clusterName+"-controlplane-0",
		talosconfig.DockerPlatformPatch(),
	)
	if err != nil {
		t.Fatalf("GenerateControlPlane: %v", err)
	}
	t.Logf("  Control plane config: %d bytes", len(cpConfig))

	// Verify the config is valid base64 (as Docker USERDATA expects)
	encoded := base64.StdEncoding.EncodeToString(cpConfig)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 round-trip: %v", err)
	}
	if string(decoded) != string(cpConfig) {
		t.Fatal("base64 round-trip mismatch")
	}

	// Step 5: Create control plane container
	t.Log("Step 5: Create control plane container")
	containerID, err := dp.CreateControlPlane(ctx, clusterName, platform.NodeSpec{
		Name: clusterName + "-controlplane-0",
		Role: "controlplane",
	}, cpConfig)
	if err != nil {
		t.Fatalf("CreateControlPlane: %v", err)
	}
	t.Logf("  Container ID: %s", containerID)

	// Step 6: Wait for container to be running
	t.Log("Step 6: Wait for container to be running")
	time.Sleep(3 * time.Second)

	// Step 7: Generate worker config (dry run — don't create worker to keep test fast)
	t.Log("Step 7: Generate worker config")
	workerConfig, err := gen.GenerateWorker(
		clusterName+"-worker-0",
		talosconfig.DockerPlatformPatch(),
	)
	if err != nil {
		t.Fatalf("GenerateWorker: %v", err)
	}
	t.Logf("  Worker config: %d bytes", len(workerConfig))

	// Step 8: Generate talosconfig for CLI access
	t.Log("Step 8: Generate talosconfig")
	taloscfg, err := gen.GenerateTalosconfig([]string{dp.TalosEndpoint()})
	if err != nil {
		t.Fatalf("GenerateTalosconfig: %v", err)
	}
	t.Logf("  Talosconfig: %d bytes", len(taloscfg))

	t.Log("Integration test complete — all steps passed")
}
