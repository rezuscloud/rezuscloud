//go:build integration

package metal

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/provider/openstack"
)

// TestIntegrationDiscoveryWithTalosVM creates a real Talos VM on OpenStack
// and verifies the metal provider discovers it via network scanning.
//
// Prerequisites:
//   - OpenStack credentials via OS_* env vars
//   - Talos image pre-loaded in Glance (talos-v1.12.6-openstack-amd64)
//   - Network connectivity to 192.168.7.0/24 subnet
//
// Run: go test -tags=integration -run TestIntegrationDiscoveryWithTalosVM -v -timeout 10m
func TestIntegrationDiscoveryWithTalosVM(t *testing.T) {
	authURL := os.Getenv("OS_AUTH_URL")
	if authURL == "" {
		t.Skip("OS_AUTH_URL not set, skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Step 1: Create a Talos VM via OpenStack.
	t.Log("creating Talos VM on OpenStack...")

	osCfg := &openstack.Config{
		AuthURL:         authURL,
		Username:        os.Getenv("OS_USERNAME"),
		Password:        os.Getenv("OS_PASSWORD"),
		ProjectName:     os.Getenv("OS_PROJECT_NAME"),
		UserDomainName:  os.Getenv("OS_USER_DOMAIN_NAME"),
		ProjectDomainName: os.Getenv("OS_PROJECT_DOMAIN_NAME"),
		Region:          os.Getenv("OS_REGION_NAME"),
		TalosImageName:  "talos-v1.12.6-openstack-amd64",
		NetworkName:     "ext-net",
		MachineTypeFlavor: map[string]string{
			"worker": "SCS-2V-4-10",
		},
	}

	osClient, err := openstack.NewOpenStackClient(osCfg)
	if err != nil {
		t.Fatalf("openstack client: %v", err)
	}

	vmName := fmt.Sprintf("metal-test-%d", time.Now().Unix())

	// Verify image exists.
	imageID, err := osClient.EnsureImage()
	if err != nil {
		t.Fatalf("ensure image: %v", err)
	}
	if imageID == "" {
		t.Fatal("Talos image not found in Glance — upload it first")
	}
	t.Logf("Talos image found: %s", imageID)

	vm, err := osClient.ProvisionVM(ctx, vmName, "worker", "")
	if err != nil {
		t.Fatalf("provision VM: %v", err)
	}
	t.Logf("VM created: %s at %s", vm.ID, vm.IPv4)

	// Ensure cleanup.
	defer func() {
		t.Logf("cleaning up VM %s...", vm.ID)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if err := osClient.DestroyVMs(cleanupCtx, vmName); err != nil {
			t.Logf("WARNING: failed to destroy VM: %v", err)
		}
	}()

	// Step 2: Wait for Talos to boot and enter maintenance mode.
	t.Log("waiting for Talos to enter maintenance mode...")
	time.Sleep(15 * time.Second)

	// Step 3: Run discovery scanner targeting the VM's IP.
	t.Logf("scanning for Talos node at %s:50000...", vm.IPv4)

	metalCfg := &Config{
		Discovery: DiscoveryConfig{
			Enabled:         true,
			Subnet:          vm.IPv4 + "/32",
			Port:            50000,
			IntervalSeconds: 5,
			TimeoutSeconds:  3,
			Concurrency:     1,
		},
	}

	scanner := NewDiscoveryScanner(metalCfg)

	// Probe directly.
	cidr, err := metalCfg.ParseCIDR()
	if err != nil {
		t.Fatalf("parse CIDR: %v", err)
	}

	// Retry probes for up to 2 minutes (Talos needs time to boot).
	var found bool
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		result := scanner.probeSubnet(ctx, cidr, 50000, 3*time.Second, 1)
		if result[vm.IPv4] {
			found = true
			break
		}
		t.Log("port 50000 not open yet, retrying in 10s...")
		time.Sleep(10 * time.Second)
	}

	if !found {
		t.Fatalf("failed to discover Talos node at %s:50000 within 2 minutes", vm.IPv4)
	}

	t.Logf("SUCCESS: discovered Talos maintenance-mode node at %s", vm.IPv4)

	// Step 4: Verify we can connect to the Talos API.
	conn, err := net.DialTimeout("tcp", vm.IPv4+":50000", 5*time.Second)
	if err != nil {
		t.Fatalf("failed to connect to Talos API: %v", err)
	}
	conn.Close()

	t.Log("Talos API is reachable — auto-discovery works!")
}
