package status

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/credentials"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
)

// fakeProbe is a test MachineProbe.
type fakeProbe struct {
	version string
	err     error
	calls   int
}

func (f *fakeProbe) ProbeTenant(_ context.Context, _ string, _ interface{}) (string, error) {
	f.calls++
	return f.version, f.err
}

func openTestStore(t *testing.T) *state.Store {
	t.Helper()
	s, err := state.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedTenant(t *testing.T, store *state.Store, name string) {
	t.Helper()
	_, err := store.CreateTenant(name, state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
}

func TestGatherer_NoCache_ProbesAndCaches(t *testing.T) {
	store := openTestStore(t)
	seedTenant(t, store, "t1")
	probe := &fakeProbe{version: "v1.13.0"}

	cache := credentials.NewSecretsCache(func(context.Context, string) ([]byte, error) {
		bundle, _ := credentials.SecretsBundleJSON(genTestBundle(t))
		return bundle, nil
	})
	cache.Refresh(context.Background(), "t1")

	g := NewGatherer(store, cache, probe)

	h := g.Gather(context.Background(), "t1")
	if !h.Reachable {
		t.Fatalf("expected reachable, got %+v", h)
	}
	if h.TalosVersion != "v1.13.0" {
		t.Errorf("version = %q", h.TalosVersion)
	}
	if probe.calls != 1 {
		t.Errorf("expected 1 probe call, got %d", probe.calls)
	}

	// Second call within TTL should return cached (no new probe).
	h2 := g.Gather(context.Background(), "t1")
	if probe.calls != 1 {
		t.Errorf("expected cached (1 probe), got %d", probe.calls)
	}
	_ = h2
}

func TestGatherer_NoProbe_DegradedMode(t *testing.T) {
	store := openTestStore(t)
	seedTenant(t, store, "t1")

	g := NewGatherer(store, nil, nil)
	h := g.Gather(context.Background(), "t1")

	if h.Reachable {
		t.Error("expected not reachable in degraded mode")
	}
	if h.MachineCount != 0 {
		t.Errorf("machine count = %d, want 0", h.MachineCount)
	}
}

func TestGatherer_NoCredentials_ReportsError(t *testing.T) {
	store := openTestStore(t)
	seedTenant(t, store, "t1")

	cache := credentials.NewSecretsCache(func(context.Context, string) ([]byte, error) { return nil, nil })
	probe := &fakeProbe{version: "v1.13.0"}
	g := NewGatherer(store, cache, probe)

	h := g.Gather(context.Background(), "t1")
	if h.Reachable {
		t.Error("expected not reachable without credentials")
	}
	if h.Error == "" {
		t.Error("expected error message")
	}
}

func TestGatherer_ProbeError_Propagates(t *testing.T) {
	store := openTestStore(t)
	seedTenant(t, store, "t1")

	cache := credentials.NewSecretsCache(func(context.Context, string) ([]byte, error) {
		bundle, _ := credentials.SecretsBundleJSON(genTestBundle(t))
		return bundle, nil
	})
	cache.Refresh(context.Background(), "t1")

	probe := &fakeProbe{err: errors.New("connection refused")}
	g := NewGatherer(store, cache, probe)

	h := g.Gather(context.Background(), "t1")
	if h.Reachable {
		t.Error("expected not reachable on probe error")
	}
	if h.Error == "" {
		t.Error("expected error")
	}
}

func TestGatherer_TTLExpiry_Reprobes(t *testing.T) {
	store := openTestStore(t)
	seedTenant(t, store, "t1")

	cache := credentials.NewSecretsCache(func(context.Context, string) ([]byte, error) {
		bundle, _ := credentials.SecretsBundleJSON(genTestBundle(t))
		return bundle, nil
	})
	cache.Refresh(context.Background(), "t1")

	probe := &fakeProbe{version: "v1.13.0"}
	g := NewGatherer(store, cache, probe, WithTTL(50*time.Millisecond))

	g.Gather(context.Background(), "t1") // first probe
	if probe.calls != 1 {
		t.Fatalf("expected 1 probe, got %d", probe.calls)
	}

	time.Sleep(60 * time.Millisecond)    // TTL expires
	g.Gather(context.Background(), "t1") // second probe
	if probe.calls != 2 {
		t.Fatalf("expected 2 probes after TTL expiry, got %d", probe.calls)
	}
}

func TestGatherer_Drop(t *testing.T) {
	store := openTestStore(t)
	seedTenant(t, store, "t1")

	cache := credentials.NewSecretsCache(func(context.Context, string) ([]byte, error) {
		bundle, _ := credentials.SecretsBundleJSON(genTestBundle(t))
		return bundle, nil
	})
	cache.Refresh(context.Background(), "t1")

	probe := &fakeProbe{version: "v1.13.0"}
	g := NewGatherer(store, cache, probe)

	g.Gather(context.Background(), "t1")
	g.Drop("t1")
	g.Gather(context.Background(), "t1")

	if probe.calls != 2 {
		t.Errorf("expected 2 probes after Drop, got %d", probe.calls)
	}
}

// genTestBundle creates a real secrets bundle for testing.
func genTestBundle(t *testing.T) *secrets.Bundle {
	t.Helper()
	b, err := secrets.NewBundle(secrets.NewClock(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
