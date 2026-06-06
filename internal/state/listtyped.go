package state

import "encoding/json"

// ListTyped returns a slice of typed resources rather than raw JSON.
//
// The build callback receives the metadata + raw spec/status JSON and is
// responsible for unmarshaling into the caller's preferred struct shape.
// Errors from build are non-fatal — the offending row is skipped, matching
// the prior inline implementation's behaviour of `continue` on unmarshal error.
//
// Example:
//
//	items, total, err := state.ListTyped(store, "configpatch", opts,
//	    func(meta state.Metadata, specRaw, statusRaw json.RawMessage) (patch.ConfigPatch, error) {
//	        var p patch.ConfigPatch
//	        p.Metadata = meta
//	        if err := json.Unmarshal(specRaw, &p.Spec); err != nil {
//	            return p, err
//	        }
//	        _ = json.Unmarshal(statusRaw, &p.Status)
//	        return p, nil
//	    })
func ListTyped[T any](
	s *Store,
	resourceType string,
	opts ListOptions,
	build func(meta Metadata, specRaw, statusRaw json.RawMessage) (T, error),
) ([]T, int, error) {
	metas, specs, statuses, total, err := s.ListResources(resourceType, opts)
	if err != nil {
		return nil, 0, err
	}
	out := make([]T, 0, total)
	for i := range metas {
		item, buildErr := build(metas[i], specs[i], statuses[i])
		if buildErr != nil {
			continue
		}
		out = append(out, item)
	}
	return out, total, nil
}

// ListTypedByTenant is sugar over ListTyped for the most common case: list
// resources with label `rezuscloud.io/tenant=<tenant>`.
func ListTypedByTenant[T any](
	s *Store,
	resourceType, tenant string,
	build func(meta Metadata, specRaw, statusRaw json.RawMessage) (T, error),
) ([]T, int, error) {
	return ListTyped(s, resourceType, ListOptions{
		LabelSelector: "rezuscloud.io/tenant=" + tenant,
	}, build)
}
