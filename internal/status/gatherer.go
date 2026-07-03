// Package status provides on-demand tenant status probing (ADR 0016).
//
// Status is gathered lazily — when an API consumer requests it — and cached
// with a short TTL (default 15s). No background scrapers. The probe uses the
// cached Talos credentials (SecretsCache) to check if a tenant's Talos API is
// reachable and reports the observed version.
package status

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/credentials"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

// TenantHealth is the result of a tenant status probe.
type TenantHealth struct {
	Tenant       string    `json:"tenant"`
	Reachable    bool      `json:"reachable"`
	TalosVersion string    `json:"talosVersion,omitempty"`
	MachineCount int       `json:"machineCount"`
	Error        string    `json:"error,omitempty"`
	ProbedAt     time.Time `json:"probedAt"`
}

// MachineProbe is the seam where tenant-machine probing happens. Production
// uses the Talos API adapter; tests inject a fake.
type MachineProbe interface {
	// ProbeTenant checks the health of a tenant's machines via the Talos API.
	// Returns the observed Talos version (if reachable) and any error.
	ProbeTenant(ctx context.Context, tenant string, bundle interface{}) (version string, err error)
}

// Gatherer performs on-demand tenant status probes with a short TTL cache.
// Per ADR 0016: no background scrapers, probes fire only when requested.
type Gatherer struct {
	store        state.StoreAPI
	cache        *credentials.SecretsCache
	machineProbe MachineProbe
	ttl          time.Duration

	mu     sync.RWMutex
	cached map[string]tenantHealthEntry
}

type tenantHealthEntry struct {
	health  TenantHealth
	expires time.Time
}

// Option configures a Gatherer.
type Option func(*Gatherer)

// WithTTL overrides the default cache TTL (15s).
func WithTTL(d time.Duration) Option {
	return func(g *Gatherer) { g.ttl = d }
}

// NewGatherer creates a status Gatherer. probe may be nil — in that case,
// Gather returns a degraded result (machines counted from the store, no
// Talos API reachability check).
func NewGatherer(store state.StoreAPI, cache *credentials.SecretsCache, probe MachineProbe, opts ...Option) *Gatherer {
	g := &Gatherer{
		store:        store,
		cache:        cache,
		machineProbe: probe,
		ttl:          15 * time.Second,
		cached:       make(map[string]tenantHealthEntry),
	}
	for _, o := range opts {
		o(g)
	}
	return g
}

// Gather returns the current health of a tenant. Uses the TTL cache; if the
// cache is fresh, returns immediately. Otherwise probes.
func (g *Gatherer) Gather(ctx context.Context, tenant string) TenantHealth {
	now := time.Now().UTC()

	// Cache hit?
	g.mu.RLock()
	entry, ok := g.cached[tenant]
	g.mu.RUnlock()
	if ok && now.Before(entry.expires) {
		return entry.health
	}

	// Cache miss — probe.
	health := g.probe(ctx, tenant)
	health.ProbedAt = now

	g.mu.Lock()
	g.cached[tenant] = tenantHealthEntry{health: health, expires: now.Add(g.ttl)}
	g.mu.Unlock()

	return health
}

// probe gathers tenant health: counts machines from the store, and (if a
// MachineProbe is configured and credentials are available) checks Talos API
// reachability.
func (g *Gatherer) probe(ctx context.Context, tenant string) TenantHealth {
	machines, _, err := g.store.ListMachinesByTenant(tenant)
	if err != nil {
		return TenantHealth{Tenant: tenant, Error: err.Error()}
	}

	h := TenantHealth{
		Tenant:       tenant,
		MachineCount: len(machines),
	}

	if g.machineProbe == nil {
		// No probe configured — degraded mode: report machine count only.
		return h
	}

	// Try to reach the tenant's Talos API via cached credentials.
	bundle, ok := g.cache.Get(tenant)
	if !ok {
		h.Error = "no cached credentials"
		return h
	}

	version, err := g.machineProbe.ProbeTenant(ctx, tenant, bundle)
	if err != nil {
		h.Error = err.Error()
		return h
	}

	h.Reachable = true
	h.TalosVersion = version
	return h
}

// Drop removes a tenant's cached health (e.g., on tenant deletion).
func (g *Gatherer) Drop(tenant string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.cached, tenant)
}

// GatherAll probes all known tenants (used by the dashboard).
func (g *Gatherer) GatherAll(ctx context.Context) []TenantHealth {
	tenants, _, err := g.store.ListTenants()
	if err != nil {
		return nil
	}
	out := make([]TenantHealth, 0, len(tenants))
	for _, t := range tenants {
		out = append(out, g.Gather(ctx, t.Metadata.Name))
	}
	return out
}

// String format helper for logging.
func (h TenantHealth) String() string {
	if h.Reachable {
		return fmt.Sprintf("%s: reachable, talos=%s, machines=%d", h.Tenant, h.TalosVersion, h.MachineCount)
	}
	return fmt.Sprintf("%s: unreachable, machines=%d, err=%s", h.Tenant, h.MachineCount, h.Error)
}
