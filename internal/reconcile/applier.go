// Package reconcile wires the apply queue's scheduler core to the real
// production subsystems: tfexec, the provider registry, and the state store.
//
// It provides two pieces:
//
//  1. Applier — implements applyqueue.Applier. On Apply(tenant) it loads the
//     tenant + its node groups from the store, renders per-provider `.tf.json`
//     via the registry, writes the files into the tenant's tfexec workdir, then
//     runs `tofu init` + `tofu apply -auto-approve`. Tofu reads/writes state
//     through RezusCloud's own HTTP backend (the backend.tf.json tfexec writes).
//
//  2. EnqueueBus — a state.EventBus that translates store mutations into
//     queue.Enqueue calls. When a tenant or node group is created/updated/
//     deleted, the affected tenant is enqueued for reconciliation. Combined
//     with state.MultiBus, it coexists with the watch SSE bus.
//
// This is the #87b / #99 runtime integration: the scheduler core (applyqueue)
// was merged in #87a; this package connects it to the real world.
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/rezuscloud/rezuscloud/internal/provider"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/tfexec"
)

// Applier is the production applyqueue.Applier. It renders `.tf.json` from the
// tenant's declared specs and drives `tofu init` + `tofu apply` through tfexec.
// It is safe for concurrent use across tenants (the queue runs tenants in
// parallel).
type Applier struct {
	exec     *tfexec.Exec
	registry *provider.Registry
	store    state.StoreAPI
	logf     func(format string, args ...any)
}

// Option configures an Applier.
type Option func(*Applier)

// WithLogger overrides the default log.Printf logger.
func WithLogger(fn func(format string, args ...any)) Option {
	return func(a *Applier) { a.logf = fn }
}

// NewApplier builds an Applier from the production subsystems. All three are
// required: tfexec to run tofu, the registry to render config, and the store to
// read tenant + node group specs.
func NewApplier(exec *tfexec.Exec, registry *provider.Registry, store state.StoreAPI, opts ...Option) *Applier {
	a := &Applier{
		exec:     exec,
		registry: registry,
		store:    store,
		logf:     log.Printf,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Apply reconciles a single tenant: renders its `.tf.json`, runs tofu init +
// apply. Implements applyqueue.Applier.
func (a *Applier) Apply(ctx context.Context, tenant string) error {
	t, err := a.store.GetTenant(tenant)
	if err != nil {
		return fmt.Errorf("reconcile: load tenant %q: %w", tenant, err)
	}
	if t == nil {
		return fmt.Errorf("reconcile: tenant %q not found", tenant)
	}

	ngs, err := loadNodeGroups(a.store, tenant)
	if err != nil {
		return fmt.Errorf("reconcile: load node groups for %q: %w", tenant, err)
	}

	dir, err := a.exec.Workdir(tenant)
	if err != nil {
		return fmt.Errorf("reconcile: workdir for %q: %w", tenant, err)
	}

	if err := a.renderConfig(dir, t, ngs); err != nil {
		return fmt.Errorf("reconcile: render config for %q: %w", tenant, err)
	}

	// init downloads providers and configures the backend; apply reconciles.
	if _, err := a.exec.Run(ctx, tenant, "init", "-input=false"); err != nil {
		return fmt.Errorf("reconcile: tofu init for %q: %w", tenant, err)
	}
	if _, err := a.exec.Run(ctx, tenant, "apply", "-auto-approve", "-input=false"); err != nil {
		return fmt.Errorf("reconcile: tofu apply for %q: %w", tenant, err)
	}
	return nil
}

// renderConfig writes one `<providerType>.tf.json` per registered provider that
// has matching node groups, plus cleans stale `.tf.json` files left over from
// deleted node groups or unregistered providers. If no provider has matching
// node groups, a minimal empty config is written so tofu apply is a safe no-op
// (or destroys previously-created resources that are now gone).
func (a *Applier) renderConfig(dir string, tenant *state.Tenant, ngs []state.NodeGroupSpec) error {
	// Clean stale provider configs. Keep backend.tf.json (managed by tfexec).
	if err := cleanProviderConfigs(dir); err != nil {
		return fmt.Errorf("clean stale configs: %w", err)
	}

	wroteAny := false
	for _, p := range a.registry.All() {
		matching := filterByProvider(ngs, p.Type())
		if len(matching) == 0 {
			continue
		}
		raw, err := p.Render(provider.RenderRequest{Tenant: tenant, NodeGroups: matching})
		if err != nil {
			return fmt.Errorf("provider %s render: %w", p.Type(), err)
		}
		path := filepath.Join(dir, p.Type()+".tf.json")
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		wroteAny = true
	}

	// No provider had matching node groups. Write a minimal valid config so tofu
	// init+apply succeeds (and destroys any previously-managed resources).
	if !wroteAny {
		empty := []byte(`{"terraform":{}}`)
		if err := os.WriteFile(filepath.Join(dir, "main.tf.json"), empty, 0o644); err != nil {
			return fmt.Errorf("write minimal main.tf.json: %w", err)
		}
	}
	return nil
}

// cleanProviderConfigs removes every `*.tf.json` in dir except backend.tf.json.
// This guarantees the workdir reflects exactly the current desired state — a
// deleted node group's stale config file won't linger and confuse tofu.
func cleanProviderConfigs(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if name == "backend.tf.json" {
			continue
		}
		if !strings.HasSuffix(name, ".tf.json") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// ngSpecJSON mirrors the nodegroup API's spec shape for JSON unmarshaling.
// The reconcile package does not import the api package, so it defines its own
// mirror struct that matches the wire format.
type ngSpecJSON struct {
	Count          int             `json:"count"`
	Role           string          `json:"role"`
	ProviderClass  string          `json:"providerClass,omitempty"`
	ProviderConfig json.RawMessage `json:"providerConfig,omitempty"`
	TalosVersion   string          `json:"talosVersion,omitempty"`
}

// loadNodeGroups reads every node group resource for a tenant and converts each
// to a state.NodeGroupSpec (pulling Name from metadata).
func loadNodeGroups(store state.StoreAPI, tenant string) ([]state.NodeGroupSpec, error) {
	items, _, err := state.ListTypedByTenant(store, "nodegroup", tenant,
		func(meta state.Metadata, specRaw, _ json.RawMessage) (state.NodeGroupSpec, error) {
			var s ngSpecJSON
			if err := json.Unmarshal(specRaw, &s); err != nil {
				return state.NodeGroupSpec{}, err
			}
			return state.NodeGroupSpec{
				Name:           meta.Name,
				Role:           s.Role,
				Count:          s.Count,
				ProviderClass:  s.ProviderClass,
				ProviderConfig: s.ProviderConfig,
				TalosVersion:   s.TalosVersion,
			}, nil
		})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// filterByProvider returns the node groups whose ProviderClass routes to the
// given provider type. The ProviderClass convention is "<type>:<detail>" (e.g.
// "oci:VM.Standard.A1.Flex"); the segment before the first ':' is the provider
// type. A ProviderClass without a ':' is treated as its own type (e.g. "static").
func filterByProvider(ngs []state.NodeGroupSpec, providerType string) []state.NodeGroupSpec {
	var out []state.NodeGroupSpec
	for _, ng := range ngs {
		if providerTypeOf(ng.ProviderClass) == providerType {
			out = append(out, ng)
		}
	}
	return out
}

// providerTypeOf extracts the provider type from a ProviderClass. Returns the
// segment before the first ':', or the whole string if there is no ':'.
func providerTypeOf(class string) string {
	if i := strings.IndexByte(class, ':'); i >= 0 {
		return class[:i]
	}
	return class
}
