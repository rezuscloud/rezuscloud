package reconcile

import (
	"context"
	"log/slog"
	"strings"

	"github.com/rezuscloud/rezuscloud/internal/applyqueue"
	"github.com/rezuscloud/rezuscloud/internal/projection"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

// StoreEnricher is an applyqueue.Listener that merges projected TF-state
// machines into the store after each successful apply. This is the convergence
// between the two data planes (ADR 0005): the projection index is a cache over
// TF state; the store is the management plane's own data. After apply, cloud-
// created instances that don't exist in the store yet are created as machine
// resources so they appear in the API, WebUI, and dashboard.
//
// Matching: projected machines are matched to store machines by management
// address (the instance's primary IP). A projected machine with no store
// counterpart is created; one with a counterpart enriches its status.
type StoreEnricher struct {
	store state.StoreAPI
	index *projection.Index
}

// NewStoreEnricher returns a listener that enriches the store from the
// projection index after PhaseApplied.
func NewStoreEnricher(store state.StoreAPI, index *projection.Index) *StoreEnricher {
	return &StoreEnricher{store: store, index: index}
}

// Listener returns the applyqueue.Listener interface.
func (e *StoreEnricher) Listener() applyqueue.Listener {
	return func(tenant string, phase applyqueue.Phase, err error) {
		if phase != applyqueue.PhaseApplied {
			return
		}
		go e.enrich(context.Background(), tenant)
	}
}

// enrich reads projected Machines and creates/updates store machine records.
func (e *StoreEnricher) enrich(ctx context.Context, tenant string) {
	resources := e.index.List(tenant, "Machine")
	if len(resources) == 0 {
		return
	}

	// Load existing store machines for this tenant to avoid duplicates.
	existing, _, err := e.store.ListMachinesByTenant(tenant)
	if err != nil {
		slog.Error("enrich: list machines failed", "tenant", tenant, "err", err)
		return
	}

	// Index existing machines by management address for dedup.
	byAddr := make(map[string]*state.Machine, len(existing))
	byName := make(map[string]*state.Machine, len(existing))
	for _, m := range existing {
		byAddr[m.Spec.ManagementAddress] = m
		byName[m.Metadata.Name] = m
	}

	for _, r := range resources {
		addr := stringFromSpec(r.Spec, "address", "public_ip", "private_ip", "providerId")
		name := r.Name
		if name == "" {
			name = r.TFAddress
		}

		// Dedup: skip if this machine already exists (by name or address).
		if _, ok := byName[name]; ok {
			continue
		}
		if addr != "" {
			if _, ok := byAddr[addr]; ok {
				continue
			}
		}

		// Create a new machine record for this projected instance.
		spec := state.MachineSpec{
			ManagementAddress: addr,
			Connected:         false, // will be set by status-plane probing later
		}
		// Infer role from the TF resource type or name.
		role := "worker"
		if strings.Contains(strings.ToLower(name), "control") || strings.Contains(r.TFType, "control") {
			role = "controlplane"
		}
		labels := map[string]string{
			"rezuscloud.io/tenant": tenant,
			"rezuscloud.io/role":   role,
		}

		_, err := e.store.CreateMachine(name, spec, labels, nil)
		if err != nil {
			slog.Error("enrich: create machine failed", "machine", name, "tenant", tenant, "err", err)
			continue
		}
		slog.Info("enrich: created machine from projection", "machine", name, "tenant", tenant, "addr", addr)
	}
}

// stringFromSpec returns the first non-empty string value from a projected
// spec map, trying the given keys in order.
func stringFromSpec(spec map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := spec[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
