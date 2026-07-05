package credentials

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
)

// SecretsSource returns the raw secrets bundle bytes for a tenant.
// Production wraps state.StoreAPI.LoadTenantSecrets; the interface allows a
// future switch to tfexec.StatePull (extracting client_configuration from TF
// state) without changing the cache.
type SecretsSource func(ctx context.Context, tenant string) ([]byte, error)

// StoreSource adapts a state.StoreAPI to SecretsSource.
func StoreSource(store state.StoreAPI) SecretsSource {
	return func(_ context.Context, tenant string) ([]byte, error) {
		return store.LoadTenantSecrets(tenant)
	}
}

// cachedBundle holds a parsed secrets bundle + the raw bytes it was parsed from.
// The raw bytes are compared on refresh to skip re-parsing if nothing changed.
type cachedBundle struct {
	bundle *secrets.Bundle
	raw    []byte
	loaded time.Time
}

// SecretsCache is an in-memory cache of tenant secrets bundles. It is the
// status-plane prerequisite (ADR 0016, #92): the status gatherer uses cached
// credentials to reach tenant APIs without re-reading from the store on every
// probe.
//
// The cache is refreshed after each successful apply (PhaseApplied) via
// Refresh. It is thread-safe. A tenant whose bundle is absent (never created,
// apply failed) returns nil — callers must handle this gracefully (ADR 0010:
// status may be absent).
type SecretsCache struct {
	source SecretsSource

	mu    sync.RWMutex
	cache map[string]cachedBundle
}

// NewSecretsCache returns a cache backed by source.
func NewSecretsCache(source SecretsSource) *SecretsCache {
	return &SecretsCache{
		source: source,
		cache:  make(map[string]cachedBundle),
	}
}

// Refresh reloads the secrets bundle for a tenant from the source and updates
// the cache. Called by the reconcile listener after PhaseApplied. If the bundle
// is absent, the cache entry is cleared (the tenant has no secrets yet).
func (c *SecretsCache) Refresh(ctx context.Context, tenant string) {
	raw, err := c.source(ctx, tenant)
	if err != nil {
		slog.Error("secrets-cache: refresh failed", "tenant", tenant, "err", err)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(raw) == 0 {
		delete(c.cache, tenant)
		return
	}

	// Skip re-parsing if the bytes haven't changed (common on re-apply with no
	// secrets rotation).
	if existing, ok := c.cache[tenant]; ok && bytesEqual(existing.raw, raw) {
		existing.loaded = time.Now()
		c.cache[tenant] = existing
		return
	}

	bundle, err := UnmarshalSecretsBundle(raw)
	if err != nil {
		slog.Error("secrets-cache: unmarshal bundle failed", "tenant", tenant, "err", err)
		return
	}

	c.cache[tenant] = cachedBundle{
		bundle: bundle,
		raw:    append([]byte(nil), raw...), // defensive copy
		loaded: time.Now(),
	}
}

// Get returns the cached secrets bundle for a tenant, or nil if not cached.
// Does NOT trigger a refresh — callers should call Refresh after apply. If the
// bundle is absent, returns (nil, false).
func (c *SecretsCache) Get(tenant string) (*secrets.Bundle, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.cache[tenant]
	if !ok || entry.bundle == nil {
		return nil, false
	}
	return entry.bundle, true
}

// Drop removes a tenant from the cache (e.g., on tenant deletion).
func (c *SecretsCache) Drop(tenant string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, tenant)
}

// Tenants returns the names of tenants with a cached bundle. Used for
// diagnostics.
func (c *SecretsCache) Tenants() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.cache))
	for t := range c.cache {
		out = append(out, t)
	}
	return out
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
