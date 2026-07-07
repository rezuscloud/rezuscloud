// Package pagination provides shared HTTP query-param parsing for list
// endpoints. It is a leaf package (no imports of any handler package) so the
// per-resource API sub-packages can use it without creating import cycles
// with the root api package (whose router imports them).
package pagination

import (
	"net/http"
	"strconv"
)

const (
	// DefaultLimit is applied when the client omits ?limit.
	DefaultLimit = 100
	// MaxLimit is the hard ceiling — larger values are clamped.
	MaxLimit = 500
)

// Params holds a parsed pagination request.
type Params struct {
	Limit  int
	Offset int
}

// Parse extracts limit + offset from the request's query string. Defaults:
// limit=100, offset=0. The limit is clamped to [1, MaxLimit]; invalid values
// fall back to the defaults rather than erroring (lenient, K8s-style).
func Parse(r *http.Request) Params {
	p := Params{Limit: DefaultLimit}
	q := r.URL.Query()
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			switch {
			case n <= 0:
				// 0 or negative → use default.
			case n > MaxLimit:
				p.Limit = MaxLimit
			default:
				p.Limit = n
			}
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			p.Offset = n
		}
	}
	return p
}

// RemainingItemCount returns how many more items exist after this page
// (total - offset - returned). Clamped to >= 0. Matches the K8s
// ListMeta.remainingItemCount field.
func RemainingItemCount(total, offset, returned int) int {
	r := total - offset - returned
	if r < 0 {
		return 0
	}
	return r
}
