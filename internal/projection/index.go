// Package projection turns TF state blobs into the K8s-style resource read model
// (ADR 21, Option C). It is the spec-plane read model: the REST API's
// `metadata`+`spec` fields are PROJECTIONS of the TF state blob, mapped through
// provider-declared resource schemas.
//
// Why a projection and not a separate store of resources? TF state IS the single
// source of truth (ADR 21). Every Machine/NodeGroup `spec` the API returns is
// derived from state — there is no second table of "the real machines". This
// keeps spec fields from drifting: you cannot have a Machine in the API that
// isn't backed by a TF resource instance.
//
// The projection has two layers:
//
//  1. StateSource — reads the plaintext state for a tenant. Production wraps
//     tfexec.StatePull (which decrypts via TF_ENCRYPTION). Tests use a fake
//     that returns a fixed blob.
//  2. Index — a derived `(tenant, tf_type, tf_name) → projectedResource` cache,
//     rebuilt from state after each apply (the apply queue's PhaseApplied
//     listener triggers Rebuild). The index is a CACHE, never a second source
//     of truth: deleting it and re-projecting reproduces it exactly.
//
// The K8s resource Kind of each projected resource comes from the provider
// registry's Mappings() (e.g. oci_core_instance → Machine). Field extraction
// (IP, hostname) is per-Kind; this package ships extractors for the Kinds the
// providers declare today.
package projection

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/rezuscloud/rezuscloud/internal/provider"
)

// StateSource returns the plaintext TF state for a tenant workspace. Production
// wraps tfexec.Exec.StatePull (decrypts via TF_ENCRYPTION); the store's raw
// GetState returns opaque possibly-encrypted bytes and must NOT be used directly.
type StateSource interface {
	State(ctx context.Context, tenant string) ([]byte, error)
}

// StateSourceFunc is a function adapter for StateSource.
type StateSourceFunc func(ctx context.Context, tenant string) ([]byte, error)

func (f StateSourceFunc) State(ctx context.Context, tenant string) ([]byte, error) {
	return f(ctx, tenant)
}

// Resource is a single projected resource — one TF resource instance mapped to
// a RezusCloud K8s-style Kind. This is the API's read model.
type Resource struct {
	// Tenant is the owning tenant (TF workspace).
	Tenant string `json:"tenant"`
	// Kind is the RezusCloud resource Kind ("Machine"), from the provider's
	// Mappings() for this TF resource type.
	Kind string `json:"kind"`
	// Name is the projected resource name (the TF resource name; in a for_each
	// block, the instance key). Stable across applies.
	Name string `json:"name"`
	// TFType is the fully-qualified TF resource type ("oci_core_instance").
	TFType string `json:"tfType"`
	// TFAddress is the full TF address ("oci_core_instance.cp_instance["0"]"),
	// the unique key within a tenant's state.
	TFAddress string `json:"tfAddress"`
	// Spec is the provider-extracted spec fields (IP, hostname, shape, …). The
	// shape is Kind-specific; callers type-assert to the concrete struct.
	Spec map[string]interface{} `json:"spec"`
	// StateSerial is the TF state serial this projection was built from.
	// Bumps on every apply; lets the API flag stale reads.
	StateSerial int64 `json:"stateSerial"`
}

// Extractor pulls Kind-specific spec fields from a TF instance's attributes.
// One per Kind, registered with the Index. Returns nil attributes (no spec) if
// the instance has nothing projectable.
type Extractor func(tfType string, attrs map[string]interface{}) map[string]interface{}

// Index is the derived read-model cache. It is rebuilt from state after each
// apply by Rebuild(tenant), which parses the state blob, walks every managed
// resource instance, maps it through the provider registry's Mappings() to find
// its Kind, and runs the Kind's Extractor to build the spec.
//
// Safe for concurrent use: readers (Lookup/List) and Rebuild are serialized
// per-tenant so a read never sees a half-rebuilt index, while different tenants
// rebuild concurrently.
type Index struct {
	source    StateSource
	registry  *provider.Registry
	extractor map[string]Extractor // Kind → field extractor

	// entries[tenant][tfType][tfName] → Resource. The three-level map matches
	// the (tenant, tf_type, tf_name) derived-index table shape from the issue.
	entries map[string]map[string]map[string]Resource

	// serial[tenant] is the StateSerial of the last successful Rebuild. Lets
	// readers detect a stale index (state bumped but Rebuild not yet run).
	serial map[string]int64

	mu sync.RWMutex // guards entries + serial (Rebuild writes, Lookup/List read)
}

// New returns an Index backed by source and the provider registry. Extractors
// for the provider-declared Kinds must be registered via RegisterExtractor
// before Rebuild is called; an unmapped Kind is skipped with a debug log.
func New(source StateSource, registry *provider.Registry) *Index {
	return &Index{
		source:    source,
		registry:  registry,
		extractor: make(map[string]Extractor),
		entries:   make(map[string]map[string]map[string]Resource),
		serial:    make(map[string]int64),
	}
}

// RegisterExtractor associates an Extractor with a Kind. Kinds without a
// registered extractor are skipped during Rebuild (their instances appear with
// a nil spec, which the API renders as an empty spec).
func (idx *Index) RegisterExtractor(kind string, ext Extractor) {
	idx.extractor[kind] = ext
}

// Rebuild re-projects the tenant's state into the index. Called by the apply
// queue's PhaseApplied listener (and by the periodic resync). Idempotent: a
// full delete+rebuild reproduces the index exactly (the index is a pure
// function of the state blob). Returns the number of resources projected.
func (idx *Index) Rebuild(ctx context.Context, tenant string) (int, error) {
	blob, err := idx.source.State(ctx, tenant)
	if err != nil {
		return 0, fmt.Errorf("projection: read state for %q: %w", tenant, err)
	}
	if len(blob) == 0 {
		// No state yet (tenant created, no apply run). Clear any stale entries.
		delete(idx.entries, tenant)
		delete(idx.serial, tenant)
		return 0, nil
	}

	resources, err := parseState(blob)
	if err != nil {
		return 0, fmt.Errorf("projection: parse state for %q: %w", tenant, err)
	}

	// Build the new per-tenant index from scratch, then swap atomically.
	tenantIdx := make(map[string]map[string]Resource)
	serial := resources.Serial
	projected := 0
	for _, r := range resources.Resources {
		ext, kind, ok := idx.extractorFor(r.Type)
		if !ok {
			continue // TF type not mapped by any provider — skip
		}
		for _, inst := range r.Instances {
			name := instanceName(r.Name, inst.IndexKey)
			res := Resource{
				Tenant:      tenant,
				Kind:        kind,
				Name:        name,
				TFType:      r.Type,
				TFAddress:   fmt.Sprintf("%s.%s%s", r.Type, r.Name, inst.IndexSuffix()),
				Spec:        ext(r.Type, inst.Attributes),
				StateSerial: serial,
			}
			byType, ok := tenantIdx[r.Type]
			if !ok {
				byType = make(map[string]Resource)
				tenantIdx[r.Type] = byType
			}
			byType[name] = res
			projected++
		}
	}

	idx.mu.Lock()
	idx.entries[tenant] = tenantIdx
	idx.serial[tenant] = serial
	idx.mu.Unlock()
	return projected, nil
}

// extractorFor finds the Extractor + Kind for a TF type by consulting every
// registered provider's Mappings(). Returns ok=false if no provider maps it.
func (idx *Index) extractorFor(tfType string) (Extractor, string, bool) {
	for _, p := range idx.registry.All() {
		for _, m := range p.Mappings() {
			if m.TFType == tfType {
				ext, ok := idx.extractor[m.Kind]
				if !ok {
					return nil, m.Kind, true // mapped, but no extractor (nil spec)
				}
				return ext, m.Kind, true
			}
		}
	}
	return nil, "", false
}

// Lookup returns one projected resource by TF type + name, or false.
func (idx *Index) Lookup(tenant, tfType, name string) (Resource, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	tenantIdx, ok := idx.entries[tenant]
	if !ok {
		return Resource{}, false
	}
	byType, ok := tenantIdx[tfType]
	if !ok {
		return Resource{}, false
	}
	r, ok := byType[name]
	return r, ok
}

// List returns every projected resource for a tenant, optionally filtered by
// Kind. Order is stable (sorted by TFAddress). This is what the API GET/list
// handlers call.
func (idx *Index) List(tenant string, kindFilter string) []Resource {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	tenantIdx, ok := idx.entries[tenant]
	if !ok {
		return nil
	}
	var out []Resource
	for _, byType := range tenantIdx {
		for _, r := range byType {
			if kindFilter != "" && r.Kind != kindFilter {
				continue
			}
			out = append(out, r)
		}
	}
	sortByAddress(out)
	return out
}

// Serial returns the state serial the tenant's index was last built from, or 0
// if never built. Lets the API flag a stale read (state serial > index serial).
func (idx *Index) Serial(tenant string) int64 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.serial[tenant]
}

// Drop removes a tenant's index (e.g. on tenant deletion). The index is a cache;
// dropping it loses nothing — Rebuild reproduces it from state.
func (idx *Index) Drop(tenant string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.entries, tenant)
	delete(idx.serial, tenant)
}

// --- state parsing ---

// parsedState is the subset of the TF state JSON the projection reads.
type parsedState struct {
	Version   int64           `json:"version"`
	Serial    int64           `json:"serial"`
	Lineage   string          `json:"lineage"`
	Resources []stateResource `json:"resources"`
}

// stateResource is one entry in state.resources[].
type stateResource struct {
	Mode      string          `json:"mode"` // "managed" | "data"
	Type      string          `json:"type"` // "oci_core_instance"
	Name      string          `json:"name"` // "cp_instance"
	Provider  string          `json:"provider"`
	Instances []stateInstance `json:"instances"`
}

// stateInstance is one entry in resource.instances[].
type stateInstance struct {
	// IndexKey is the for_each key or count index. JSON-decoded as whatever
	// tofu wrote (string for for_each, number for count). nil for singletons.
	IndexKey   json.RawMessage        `json:"index_key"`
	Attributes map[string]interface{} `json:"attributes"`
	SchemaVer  int64                  `json:"schema_version"`
}

// IndexSuffix renders the instance's TF address suffix ("[\"key\"]" or "[0]"),
// or "" for singletons. Used to build a unique TFAddress.
func (i stateInstance) IndexSuffix() string {
	if !hasKey(i.IndexKey) {
		return ""
	}
	// for_each keys are strings → ["key"]; count indices are numbers → [0].
	var s string
	if err := json.Unmarshal(i.IndexKey, &s); err == nil {
		// Quote-escape the string key for the address.
		escaped, _ := json.Marshal(s)
		return "[" + string(escaped) + "]"
	}
	return "[" + string(i.IndexKey) + "]"
}

// instanceName is the projected Name for a TF resource instance. For singletons
// it's the resource name; for for_each/count it's the resource name + key.
func instanceName(resName string, indexKey json.RawMessage) string {
	if !hasKey(indexKey) {
		return resName
	}
	var s string
	if err := json.Unmarshal(indexKey, &s); err == nil {
		return resName + "-" + s
	}
	// numeric index
	return resName + "-" + string(indexKey)
}

// parseState decodes the TF state JSON into the projection's view.
func parseState(blob []byte) (*parsedState, error) {
	var s parsedState
	if err := json.Unmarshal(blob, &s); err != nil {
		return nil, fmt.Errorf("invalid state JSON: %w", err)
	}
	return &s, nil
}

// hasKey reports whether indexKey is a real for_each/count key. A JSON-decoded
// nil json.RawMessage round-trips as "null" (4 bytes), so a plain len()==0
// check is insufficient — null must also count as "no key".
func hasKey(indexKey json.RawMessage) bool {
	if len(indexKey) == 0 {
		return false
	}
	return string(indexKey) != "null"
}

// sortByAddress sorts resources by TFAddress for stable API output.
func sortByAddress(rs []Resource) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j-1].TFAddress > rs[j].TFAddress; j-- {
			rs[j-1], rs[j] = rs[j], rs[j-1]
		}
	}
}
