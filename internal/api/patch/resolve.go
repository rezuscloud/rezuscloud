// Package patch provides HTTP handlers and helpers for ConfigPatch CRUD.
// ConfigPatches are tenant-scoped overlays applied to Talos machine configs.
package patch

import (
	"encoding/json"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// ResolvePatches returns all enabled config patches for a tenant, optionally
// filtered by target role. Returns patch YAML strings ready for the Talos
// config generator.
func ResolvePatches(store state.StoreAPI, tenant, role string) ([]string, error) {
	items, _, err := state.ListTypedByTenant(store, "configpatch", tenant,
		func(meta state.Metadata, specRaw, _ json.RawMessage) (PatchSpec, error) {
			var ps PatchSpec
			err := json.Unmarshal(specRaw, &ps)
			return ps, err
		})
	if err != nil {
		return nil, err
	}

	var patches []string
	for _, ps := range items {
		if !ps.Enabled {
			continue
		}
		// Filter by role if specified.
		// "all" applies to all roles.
		if ps.TargetRole != "" && ps.TargetRole != "all" && role != "" && ps.TargetRole != role {
			continue
		}
		patches = append(patches, ps.Patch)
	}

	return patches, nil
}
