package talos

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/credentials"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
)

// --- helpers ---

func openTestStore(t *testing.T) *state.Store {
	t.Helper()
	path := t.TempDir() + "/test.db"
	s, err := state.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func genBundle(t *testing.T) *secrets.Bundle {
	t.Helper()
	b, err := secrets.NewBundle(secrets.NewClock(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// seedMachine creates a machine with a tenant label + management address.
func seedMachine(t *testing.T, store *state.Store, id, tenant, addr string) {
	t.Helper()
	labels := map[string]string{"rezuscloud.io/tenant": tenant}
	_, err := store.CreateResource("machine", id, state.MachineSpec{
		ManagementAddress: addr,
		Connected:         true,
	}, nil, labels, nil)
	if err != nil {
		t.Fatalf("create machine %s: %v", id, err)
	}
}

// --- fake Talos client ---

type fakeClient struct {
	upgradeErr  error
	rollbackErr error
	version     string
	versionErr  error
	rebootErr   error
	shutdownErr error
	dmesg       string
	dmesgErr    error

	upgrades  []upgradeCall
	rollbacks []string
	reboots   []string
	shutdowns []string
	closed    bool
}

type upgradeCall struct {
	addr, image string
}

func (f *fakeClient) Upgrade(_ context.Context, addr, image string) error {
	f.upgrades = append(f.upgrades, upgradeCall{addr, image})
	return f.upgradeErr
}

func (f *fakeClient) Rollback(_ context.Context, addr string) error {
	f.rollbacks = append(f.rollbacks, addr)
	return f.rollbackErr
}

func (f *fakeClient) Version(_ context.Context, _ string) (string, error) {
	return f.version, f.versionErr
}

func (f *fakeClient) Reboot(_ context.Context, addr string) error {
	f.reboots = append(f.reboots, addr)
	return f.rebootErr
}

func (f *fakeClient) Shutdown(_ context.Context, addr string) error {
	f.shutdowns = append(f.shutdowns, addr)
	return f.shutdownErr
}

func (f *fakeClient) Dmesg(_ context.Context, _ string) (string, error) {
	return f.dmesg, f.dmesgErr
}

func (f *fakeClient) Close() error { f.closed = true; return nil }

// fakeOpener returns a ClientOpener that always returns the given fake.
func fakeOpener(fc *fakeClient) ClientOpener {
	return func(context.Context, string, *secrets.Bundle) (TalosClient, error) {
		return fc, nil
	}
}

// failingOpener returns a ClientOpener that always errors.
func failingOpener(err error) ClientOpener {
	return func(context.Context, string, *secrets.Bundle) (TalosClient, error) {
		return nil, err
	}
}

// --- newTestMachineUpgrader ---

func newTestMachineUpgrader(t *testing.T, fc *fakeClient, store *state.Store, bundle *secrets.Bundle) *MachineUpgrader {
	t.Helper()
	cache := credentials.NewSecretsCache(func(context.Context, string) ([]byte, error) {
		raw, _ := credentials.SecretsBundleJSON(bundle)
		return raw, nil
	})
	// Seed the cache so Get succeeds.
	cache.Refresh(context.Background(), "t1")
	return New(cache, store, WithClientOpener(fakeOpener(fc)))
}

// --- tests ---

func TestInstallerImage(t *testing.T) {
	cases := []struct{ registry, version, want string }{
		{"ghcr.io/siderolabs/installer", "1.13.0", "ghcr.io/siderolabs/installer:v1.13.0"},
		{"ghcr.io/siderolabs/installer", "v1.13.0", "ghcr.io/siderolabs/installer:v1.13.0"},
		{"registry.example/installer", "1.12.6", "registry.example/installer:v1.12.6"},
	}
	for _, c := range cases {
		if got := installerImage(c.registry, c.version); got != c.want {
			t.Errorf("installerImage(%q, %q) = %q, want %q", c.registry, c.version, got, c.want)
		}
	}
}

func TestUpgradeMachine_Success(t *testing.T) {
	store := openTestStore(t)
	fc := &fakeClient{version: "v1.13.0"}
	m := newTestMachineUpgrader(t, fc, store, genBundle(t))
	seedMachine(t, store, "node-1", "t1", "10.0.0.1:50000")

	if err := m.UpgradeMachine(context.Background(), "node-1", "1.13.0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fc.upgrades) != 1 {
		t.Fatalf("expected 1 upgrade call, got %d", len(fc.upgrades))
	}
	if fc.upgrades[0].addr != "10.0.0.1:50000" {
		t.Errorf("upgrade addr = %q", fc.upgrades[0].addr)
	}
	if fc.upgrades[0].image != "ghcr.io/siderolabs/installer:v1.13.0" {
		t.Errorf("upgrade image = %q", fc.upgrades[0].image)
	}
}

func TestCheckMachineHealth_Success(t *testing.T) {
	store := openTestStore(t)
	fc := &fakeClient{version: "v1.13.0"}
	m := newTestMachineUpgrader(t, fc, store, genBundle(t))
	seedMachine(t, store, "node-1", "t1", "10.0.0.1:50000")

	if err := m.CheckMachineHealth(context.Background(), "node-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckMachineHealth_EmptyVersionFails(t *testing.T) {
	store := openTestStore(t)
	fc := &fakeClient{version: ""}
	m := newTestMachineUpgrader(t, fc, store, genBundle(t))
	seedMachine(t, store, "node-1", "t1", "10.0.0.1:50000")

	if err := m.CheckMachineHealth(context.Background(), "node-1"); err == nil {
		t.Fatal("expected error for empty version")
	}
}

func TestRollbackMachine_Success(t *testing.T) {
	store := openTestStore(t)
	fc := &fakeClient{}
	m := newTestMachineUpgrader(t, fc, store, genBundle(t))
	seedMachine(t, store, "node-1", "t1", "10.0.0.1:50000")

	if err := m.RollbackMachine(context.Background(), "node-1", "1.12.6"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fc.rollbacks) != 1 || fc.rollbacks[0] != "10.0.0.1:50000" {
		t.Errorf("rollbacks = %v, want [10.0.0.1:50000]", fc.rollbacks)
	}
}

func TestUpgradeMachine_MachineNotFound(t *testing.T) {
	store := openTestStore(t)
	fc := &fakeClient{}
	m := newTestMachineUpgrader(t, fc, store, genBundle(t))

	err := m.UpgradeMachine(context.Background(), "nope", "1.13.0")
	if err == nil || err.Error() == "" {
		t.Fatal("expected error for missing machine")
	}
}

func TestUpgradeMachine_NoManagementAddress(t *testing.T) {
	store := openTestStore(t)
	fc := &fakeClient{}
	m := newTestMachineUpgrader(t, fc, store, genBundle(t))
	// Machine with no management address.
	_, err := store.CreateResource("machine", "node-1", state.MachineSpec{}, nil,
		map[string]string{"rezuscloud.io/tenant": "t1"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	err = m.UpgradeMachine(context.Background(), "node-1", "1.13.0")
	if err == nil {
		t.Fatal("expected error for machine without address")
	}
}

func TestUpgradeMachine_NoCachedSecrets(t *testing.T) {
	store := openTestStore(t)
	fc := &fakeClient{}
	// Empty cache — no bundle for any tenant.
	cache := credentials.NewSecretsCache(func(context.Context, string) ([]byte, error) { return nil, nil })
	m := New(cache, store, WithClientOpener(fakeOpener(fc)))
	seedMachine(t, store, "node-1", "t1", "10.0.0.1:50000")

	err := m.UpgradeMachine(context.Background(), "node-1", "1.13.0")
	if err == nil {
		t.Fatal("expected error for missing cached secrets")
	}
}

func TestUpgradeMachine_ClientOpenFails(t *testing.T) {
	store := openTestStore(t)
	m := newTestMachineUpgrader(t, nil, store, genBundle(t))
	// Override the opener to fail.
	m.open = failingOpener(errors.New("dial failed"))
	seedMachine(t, store, "node-1", "t1", "10.0.0.1:50000")

	err := m.UpgradeMachine(context.Background(), "node-1", "1.13.0")
	if err == nil || err.Error() == "" {
		t.Fatal("expected open error to propagate")
	}
}

func TestUpgradeMachine_TalosUpgradeFails(t *testing.T) {
	store := openTestStore(t)
	fc := &fakeClient{upgradeErr: errors.New("node unreachable")}
	m := newTestMachineUpgrader(t, fc, store, genBundle(t))
	seedMachine(t, store, "node-1", "t1", "10.0.0.1:50000")

	err := m.UpgradeMachine(context.Background(), "node-1", "1.13.0")
	if err == nil || err.Error() == "" {
		t.Fatal("expected upgrade error to propagate")
	}
}

func TestClientClosed(t *testing.T) {
	store := openTestStore(t)
	fc := &fakeClient{}
	m := newTestMachineUpgrader(t, fc, store, genBundle(t))
	seedMachine(t, store, "node-1", "t1", "10.0.0.1:50000")

	_ = m.UpgradeMachine(context.Background(), "node-1", "1.13.0")
	if !fc.closed {
		t.Error("expected client to be closed after use")
	}
}

func TestBuildTLSConfig_MissingBundle(t *testing.T) {
	_, err := buildTLSConfig(nil)
	if err == nil {
		t.Fatal("expected error for nil bundle")
	}
}

func TestReboot_Success(t *testing.T) {
	store := openTestStore(t)
	fc := &fakeClient{}
	m := newTestMachineUpgrader(t, fc, store, genBundle(t))
	seedMachine(t, store, "node-1", "t1", "10.0.0.1:50000")

	if err := m.Reboot(context.Background(), "node-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fc.reboots) != 1 || fc.reboots[0] != "10.0.0.1:50000" {
		t.Errorf("reboots = %v, want [10.0.0.1:50000]", fc.reboots)
	}
}

func TestShutdown_Success(t *testing.T) {
	store := openTestStore(t)
	fc := &fakeClient{}
	m := newTestMachineUpgrader(t, fc, store, genBundle(t))
	seedMachine(t, store, "node-1", "t1", "10.0.0.1:50000")

	if err := m.Shutdown(context.Background(), "node-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fc.shutdowns) != 1 || fc.shutdowns[0] != "10.0.0.1:50000" {
		t.Errorf("shutdowns = %v, want [10.0.0.1:50000]", fc.shutdowns)
	}
}

func TestDmesg_Success(t *testing.T) {
	store := openTestStore(t)
	fc := &fakeClient{dmesg: "kernel: boot started\nkernel: talos initialized"}
	m := newTestMachineUpgrader(t, fc, store, genBundle(t))
	seedMachine(t, store, "node-1", "t1", "10.0.0.1:50000")

	out, err := m.Dmesg(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "talos initialized") {
		t.Errorf("dmesg output = %q, want 'talos initialized'", out)
	}
}
