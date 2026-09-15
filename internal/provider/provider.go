// Package provider defines the contract for RezusCloud-side provider modules.
//
// A provider is NOT a Terraform plugin binary (see ADR 22). It is a RezusCloud
// Go module with four responsibilities:
//
//  1. Render standard `.tf.json` configuration from RezusCloud resource specs,
//     using off-the-shelf registry providers (oci, openstack, talos, …).
//  2. Declare the mapping between TF resource types and RezusCloud K8s-style
//     resources (e.g. `oci_core_instance` → Machine), consumed by the state
//     projection layer (Phase 4, #91) to turn tofu state into resource status.
//  3. (Deferred) Optionally discover bare-metal nodes.
//  4. (Deferred) Fill TF gaps with Go logic.
//
// The reconciliation flow (ADR 22): a controller calls Render → writes the
// `.tf.json` to a per-tenant workdir → the apply queue (#87a) exec's tofu
// init/apply → tofu stores state in the #84 backend → Phase 4 projects state
// via Mappings() into resource status.
//
// This package's Provider interface is deliberately narrow: Render + Mappings +
// Type. Cloud-access health checks, UI panels, and node discovery land in
// follow-up issues; the interface grows as those land.
package provider

import "github.com/rezuscloud/rezuscloud/internal/state"

// Provider generates standard `.tf.json` config from RezusCloud specs and
// declares TF-resource → K8s-resource mappings. Implementations live under
// internal/provider/<type>/ (e.g. internal/provider/oci).
//
// Implementations must be safe for concurrent use: the reconciliation scheduler
// may render multiple tenants in parallel.
type Provider interface {
	// Type is the provider identifier: "oci", "openstack", "metal". Matches the
	// NodeGroupSpec.ProviderClass prefix a tenant uses to route a node group to
	// its provider (e.g. "oci:VM.Standard.A1.Flex").
	Type() string

	// Render generates the `.tf.json` configuration that realizes the tenant's
	// declared infrastructure. The returned bytes are a single valid TF JSON
	// document written to the tenant workdir as main.tf.json.
	//
	// The implementation must:
	//   - declare required_providers (off-the-shelf registry sources only)
	//   - not embed cloud credentials (those are injected into the tofu process
	//     environment as bootstrap creds per ADR 22; the provider block reads
	//     them via standard TF provider env conventions)
	//   - reproduce the proven lifecycle patterns (stable naming via random_pet,
	//     ignore_changes on user_data) from the reference talos-iac modules
	Render(req RenderRequest) ([]byte, error)

	// Mappings declares which TF resource types this provider creates and the
	// RezusCloud K8s-style resource Kind each projects to. Phase 4 (#91) reads
	// this to map tofu state objects → resource status. A provider that creates
	// no trackable resources returns nil.
	Mappings() []TFResourceMapping
}

// RenderRequest carries the tenant spec a provider renders config for. Only the
// node groups whose ProviderClass routes to this provider are included; the
// caller (a reconciler) filters them.
type RenderRequest struct {
	// Tenant is the cluster being realized. The provider uses the tenant's
	// version fields (KubernetesVersion, TalosVersion) and endpoint.
	Tenant *state.Tenant

	// NodeGroups are the node groups this provider must materialize, already
	// filtered to those routing to this provider (ProviderClass prefix match).
	NodeGroups []state.NodeGroupSpec
}

// TFResourceMapping links a TF resource type to a RezusCloud resource Kind.
// Example: {TFType: "oci_core_instance", Kind: "Machine"}.
type TFResourceMapping struct {
	// TFType is the fully-qualified TF resource type as it appears in state
	// (e.g. "oci_core_instance", "openstack_compute_instance_v2").
	TFType string

	// Kind is the RezusCloud K8s-style resource Kind the TF object projects to
	// (e.g. "Machine", "NodeGroup").
	Kind string
}

// Registry maps provider types to implementations. The controller uses it to
// look up the provider for a node group's ProviderClass at render time.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

// Register adds a provider. Panics on duplicate Type() (a programming error —
// providers are registered at startup). Safe for concurrent use only during
// the registration phase that precedes serving.
func (r *Registry) Register(p Provider) {
	t := p.Type()
	if _, dup := r.providers[t]; dup {
		panic("provider: duplicate registration for type " + t)
	}
	r.providers[t] = p
}

// Lookup returns the provider for a type, or nil if none is registered.
func (r *Registry) Lookup(typ string) Provider {
	return r.providers[typ]
}

// All returns every registered provider (for global operations like projecting
// all known TF resource types). Order is not guaranteed.
func (r *Registry) All() []Provider {
	out := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p)
	}
	return out
}
