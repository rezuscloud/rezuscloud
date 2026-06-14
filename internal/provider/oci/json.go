package oci

import "encoding/json"

// jsonMarshal marshals v as indented JSON. Factored out so tfconfig.go's
// MarshalJSON avoids importing encoding/json directly (keeps the builder file
// dependency-light and testable).
func jsonMarshal(v interface{}) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
