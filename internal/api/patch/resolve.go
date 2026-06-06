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
func ResolvePatches(store *state.Store, tenant, role string) ([]string, error) {
	opts := state.ListOptions{
		LabelSelector: "rezuscloud.io/tenant=" + tenant,
	}
	metas, specs, _, _, err := store.ListResources("configpatch", opts)
	if err != nil {
		return nil, err
	}

	var patches []string
	for i := range metas {
		var ps PatchSpec
		if err := json.Unmarshal(specs[i], &ps); err != nil {
			continue
		}

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
