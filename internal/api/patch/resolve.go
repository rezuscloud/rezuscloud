// Package patch provides HTTP handlers and helpers for ConfigPatch CRUD.
// ConfigPatches are tenant-scoped overlays applied to Talos machine configs.
package patch

import (
	"encoding/json"
	"sort"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// Patch scope values. A patch either applies to the whole tenant (cluster
// scope — the default) or to one specific machine. The targetRole filter is
// orthogonal: it narrows patch application by machine role within either
// scope.
const (
	// ScopeCluster applies the patch to every machine in the tenant.
	ScopeCluster = "cluster"
	// ScopeMachine applies the patch to the single machine named by
	// PatchSpec.TargetMachine.
	ScopeMachine = "machine"
)

// ValidScopes are the accepted scope values ("" defaults to cluster).
var ValidScopes = map[string]bool{
	"":           true,
	ScopeCluster: true,
	ScopeMachine: true,
}

// effectiveScope returns the scope of a patch, defaulting to cluster.
func effectiveScope(scope string) string {
	if scope == "" {
		return ScopeCluster
	}
	return scope
}

// ResolvePatches returns the enabled config patches that apply to a machine,
// ordered by precedence: cluster-scoped patches first, machine-scoped patches
// last. The Talos config generator applies patches in list order and the last
// writer wins, so a machine-scoped patch overrides a cluster patch on that
// machine (architecture-history, three-scope proposal; ADR 0014 kept the
// tenant-wide default).
//
// Filters: targetRole applies within either scope ("all" or empty = every
// role); a machine-scoped patch applies only when machine matches its
// TargetMachine. Within a scope, patches are ordered by name so the generated
// config is deterministic.
func ResolvePatches(store state.StoreAPI, tenant, role, machine string) ([]string, error) {
	type scoped struct {
		name    string
		rank    int // 0 = cluster, 1 = machine
		patches string
	}

	type namedSpec struct {
		PatchSpec
		name string
	}

	items, _, err := state.ListTypedByTenant(store, "configpatch", tenant,
		func(meta state.Metadata, specRaw, _ json.RawMessage) (namedSpec, error) {
			var ns namedSpec
			err := json.Unmarshal(specRaw, &ns.PatchSpec)
			ns.name = meta.Name
			return ns, err
		})
	if err != nil {
		return nil, err
	}

	var resolved []scoped
	for _, ns := range items {
		ps := ns.PatchSpec
		if !ps.Enabled {
			continue
		}
		scope := effectiveScope(ps.Scope)
		if scope == ScopeMachine && ps.TargetMachine != machine {
			continue
		}
		// Filter by role if specified ("all" applies to all roles).
		if ps.TargetRole != "" && ps.TargetRole != "all" && role != "" && ps.TargetRole != role {
			continue
		}
		rank := 0
		if scope == ScopeMachine {
			rank = 1
		}
		resolved = append(resolved, scoped{name: ns.name, rank: rank, patches: ps.Patch})
	}

	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].rank != resolved[j].rank {
			return resolved[i].rank < resolved[j].rank
		}
		return resolved[i].name < resolved[j].name
	})

	patches := make([]string, 0, len(resolved))
	for _, s := range resolved {
		patches = append(patches, s.patches)
	}
	return patches, nil
}
