// Package validation provides a systematic per-resource-type validation
// contract (#175). Each resource type registers a Validator; handlers call
// Validate before persisting so the contract is uniform across the API surface
// instead of scattered inline checks.
//
// The registry is keyed by the resource-type string the store uses ("tenant",
// "nodegroup", "machine", "configpatch", …). Validators are registered once
// (typically at Router construction) and called from create/update handlers.
package validation

import (
	"fmt"
	"sync"
)

// Validator validates a resource spec before it is persisted. The spec is
// passed as any; each implementation type-asserts to its concrete spec type.
// Returning nil means the spec is acceptable.
type Validator interface {
	Validate(spec any) error
}

// ValidatorFunc adapts a function to Validator.
type ValidatorFunc func(spec any) error

// Validate implements Validator.
func (f ValidatorFunc) Validate(spec any) error { return f(spec) }

// Registry holds per-resource-type validators. The zero value is a usable
// empty registry (Validate returns nil for unregistered types — no validation).
type Registry struct {
	mu sync.RWMutex
	m  map[string]Validator
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{m: make(map[string]Validator)}
}

// Register associates a Validator with a resource type. Calling Register twice
// for the same type overwrites (last-writer-wins); this is intentional so test
// setups can inject fakes.
func (r *Registry) Register(resourceType string, v Validator) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[resourceType] = v
}

// RegisterFunc is a convenience wrapper for Register(rt, ValidatorFunc(f)).
func (r *Registry) RegisterFunc(resourceType string, f func(any) error) {
	r.Register(resourceType, ValidatorFunc(f))
}

// Validate runs the registered validator for resourceType against spec. If no
// validator is registered, it returns nil (no validation for that type). This
// is deliberate — not every resource type needs validation, and the absence of
// a validator is not an error.
func (r *Registry) Validate(resourceType string, spec any) error {
	if r == nil {
		return nil // no registry configured → no validation (tests/standalone)
	}
	r.mu.RLock()
	v, ok := r.m[resourceType]
	r.mu.RUnlock()
	if !ok {
		return nil
	}
	if err := v.Validate(spec); err != nil {
		return fmt.Errorf("%s: %w", resourceType, err)
	}
	return nil
}
