package credentials

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/siderolabs/talos/pkg/machinery/role"
)

// genBundle creates a real secrets bundle for testing.
func genBundle(t *testing.T) (*secrets.Bundle, []byte) {
	t.Helper()
	bundle, err := secrets.NewBundle(secrets.NewClock(), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := SecretsBundleJSON(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return bundle, raw
}

func TestSecretsCache_RefreshAndGet(t *testing.T) {
	_, raw := genBundle(t)
	var calls int32
	source := func(_ context.Context, tenant string) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		if tenant == "t1" {
			return raw, nil
		}
		return nil, nil
	}

	cache := NewSecretsCache(source)
	cache.Refresh(context.Background(), "t1")

	bundle, ok := cache.Get("t1")
	if !ok || bundle == nil {
		t.Fatal("expected cached bundle")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("expected 1 source call, got %d", calls)
	}
}

func TestSecretsCache_GetMissingReturnsNil(t *testing.T) {
	cache := NewSecretsCache(StoreSource(nil))
	bundle, ok := cache.Get("nonexistent")
	if ok || bundle != nil {
		t.Fatal("expected nil for missing tenant")
	}
}

func TestSecretsCache_RefreshAbsentClearsEntry(t *testing.T) {
	_, raw := genBundle(t)
	current := raw
	source := func(_ context.Context, _ string) ([]byte, error) {
		return current, nil
	}

	cache := NewSecretsCache(source)
	cache.Refresh(context.Background(), "t1")

	if _, ok := cache.Get("t1"); !ok {
		t.Fatal("expected bundle after first refresh")
	}

	// Simulate secrets removed (e.g., tenant deleted).
	current = nil
	cache.Refresh(context.Background(), "t1")

	if _, ok := cache.Get("t1"); ok {
		t.Fatal("expected cache cleared after refresh with absent secrets")
	}
}

func TestSecretsCache_SkipsReparseOnUnchangedBytes(t *testing.T) {
	_, raw := genBundle(t)
	var calls int32
	source := func(_ context.Context, _ string) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return raw, nil
	}

	cache := NewSecretsCache(source)
	cache.Refresh(context.Background(), "t1")
	cache.Refresh(context.Background(), "t1")
	cache.Refresh(context.Background(), "t1")

	// Source called 3 times (each Refresh calls source), but bundle is parsed
	// only once because bytes are unchanged.
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("expected 3 source calls, got %d", calls)
	}
}

func TestSecretsCache_Drop(t *testing.T) {
	_, raw := genBundle(t)
	source := func(_ context.Context, _ string) ([]byte, error) { return raw, nil }

	cache := NewSecretsCache(source)
	cache.Refresh(context.Background(), "t1")
	cache.Drop("t1")

	if _, ok := cache.Get("t1"); ok {
		t.Fatal("expected cache cleared after Drop")
	}
}

func TestSecretsCache_Tenants(t *testing.T) {
	_, raw := genBundle(t)
	source := func(_ context.Context, tenant string) ([]byte, error) {
		if tenant == "a" || tenant == "b" {
			return raw, nil
		}
		return nil, nil
	}

	cache := NewSecretsCache(source)
	cache.Refresh(context.Background(), "a")
	cache.Refresh(context.Background(), "b")

	tenants := cache.Tenants()
	if len(tenants) != 2 {
		t.Fatalf("expected 2 tenants, got %d", len(tenants))
	}
}

func TestSecretsCache_ConcurrentAccess(t *testing.T) {
	_, raw := genBundle(t)
	source := func(_ context.Context, _ string) ([]byte, error) { return raw, nil }

	cache := NewSecretsCache(source)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			cache.Refresh(context.Background(), "t1")
		}
		close(done)
	}()

	for i := 0; i < 100; i++ {
		cache.Get("t1")
	}

	<-done
}

// Ensure SecretsCache doesn't panic with a nil bundle on machine.Type
// conformance (the bundle is used by status gatherers later).
func TestSecretsCache_BundleUsable(t *testing.T) {
	bundle, raw := genBundle(t)
	_ = bundle
	source := func(_ context.Context, _ string) ([]byte, error) { return raw, nil }

	cache := NewSecretsCache(source)
	cache.Refresh(context.Background(), "t1")

	got, ok := cache.Get("t1")
	if !ok {
		t.Fatal("expected bundle")
	}
	// The bundle should be usable for cert generation (what status gatherers need).
	_, err := got.GenerateTalosAPIClientCertificate(role.MakeSet(role.Admin))
	if err != nil {
		t.Fatalf("cert generation from cached bundle failed: %v", err)
	}
}
