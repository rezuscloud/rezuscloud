package projection

import (
	"encoding/json"
	"strconv"
)

// jsonMarshal / jsonUnmarshal are thin wrappers so the extractors/index files
// don't each import encoding/json (keeps the diff minimal and centralizes the
// JSON dependency for a future swap to a faster codec).
func jsonMarshal(v interface{}) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v interface{}) error { return json.Unmarshal(b, v) }

// formatFloat renders a JSON-decoded number back to its minimal string form
// (TF state attributes like "ocpus": 4 decode to float64).
func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
