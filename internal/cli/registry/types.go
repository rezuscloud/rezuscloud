// Package registry maps resource type names to API paths.
package registry

import (
	"fmt"
	"strings"
)

// Scope describes whether a resource requires a cluster context.
type Scope int

const (
	// ScopeCluster means the resource is cluster-wide (no --cluster needed).
	ScopeCluster Scope = iota
	// ScopeClusterOptional means the resource can be cluster-wide or cluster-scoped.
	ScopeClusterOptional
	// ScopeClusterRequired means --cluster is required.
	ScopeClusterRequired
)

// ResourceType describes a resource type and its API path.
type ResourceType struct {
	// Kind is the CamelCase kind name (e.g. "Cluster", "Machine").
	Kind string
	// Names is the list of accepted CLI names (e.g. ["cluster", "clusters"]).
	Names []string
	// Path is the API path template (e.g. "api/v1/tenants").
	// For scoped resources, the path contains {cluster} placeholder.
	Path string
	// Scope indicates whether --cluster is needed.
	Scope Scope
	// Verbs lists supported verbs (get, list, create, update, delete).
	Verbs []string
}

// Registry holds all known resource types.
type Registry struct {
	types []ResourceType
}

// New creates a registry with all built-in resource types.
func New() *Registry {
	return &Registry{
		types: builtinTypes(),
	}
}

func builtinTypes() []ResourceType {
	return []ResourceType{
		{
			Kind:  "Cluster",
			Names: []string{"cluster", "clusters"},
			Path:  "api/v1/tenants",
			Scope: ScopeCluster,
			Verbs: []string{"get", "list", "create", "update", "delete"},
		},
		{
			Kind:  "Machine",
			Names: []string{"machine", "machines"},
			Path:  "api/v1/machines",
			Scope: ScopeClusterOptional,
			Verbs: []string{"get", "list", "delete"},
		},
		{
			Kind:  "NodeGroup",
			Names: []string{"nodegroup", "ng", "nodegroups"},
			Path:  "api/v1/tenants/{cluster}/node-groups",
			Scope: ScopeClusterRequired,
			Verbs: []string{"get", "list", "create", "update", "delete"},
		},
		{
			Kind:  "Provider",
			Names: []string{"provider", "providers"},
			Path:  "api/v1/providers",
			Scope: ScopeCluster,
			Verbs: []string{"get", "list"},
		},
		{
			Kind:  "JoinToken",
			Names: []string{"jointoken", "jt", "jointokens"},
			Path:  "api/v1/tenants/{cluster}/join-tokens",
			Scope: ScopeClusterRequired,
			Verbs: []string{"get", "list", "create", "delete"},
		},
		{
			Kind:  "ConfigPatch",
			Names: []string{"patch", "patches", "configpatch"},
			Path:  "api/v1/tenants/{cluster}/patches",
			Scope: ScopeClusterRequired,
			Verbs: []string{"get", "list", "create", "update", "delete"},
		},
		{
			Kind:  "User",
			Names: []string{"user", "users"},
			Path:  "api/v1/users",
			Scope: ScopeCluster,
			Verbs: []string{"get", "list", "create", "update", "delete"},
		},
	}
}

// Resolve finds a resource type by name (case-insensitive).
func (r *Registry) Resolve(name string) (*ResourceType, error) {
	lower := strings.ToLower(name)
	for i := range r.types {
		for _, n := range r.types[i].Names {
			if n == lower {
				return &r.types[i], nil
			}
		}
	}

	return nil, fmt.Errorf("resource type %q not found", name)
}

// All returns all registered resource types.
func (r *Registry) All() []ResourceType {
	result := make([]ResourceType, len(r.types))
	copy(result, r.types)
	return result
}

// APIPath returns the resolved API path for a resource type,
// substituting the cluster name if required.
func (rt *ResourceType) APIPath(cluster string) (string, error) {
	if rt.Scope == ScopeClusterRequired && cluster == "" {
		return "", fmt.Errorf("resource type %q requires --cluster flag", rt.Names[0])
	}

	return strings.ReplaceAll(rt.Path, "{cluster}", cluster), nil
}

// SupportsVerb checks if the resource type supports a given verb.
func (rt *ResourceType) SupportsVerb(verb string) bool {
	for _, v := range rt.Verbs {
		if v == verb {
			return true
		}
	}
	return false
}
