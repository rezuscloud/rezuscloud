package validation

import (
	"errors"
	"testing"
)

func TestRegistry_ValidateRegistered(t *testing.T) {
	r := NewRegistry()
	r.RegisterFunc("widget", func(spec any) error {
		s, _ := spec.(string)
		if s == "" {
			return errors.New("empty")
		}
		return nil
	})

	if err := r.Validate("widget", ""); err == nil {
		t.Error("expected error for empty widget")
	}
	if err := r.Validate("widget", "ok"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRegistry_UnregisteredTypePasses(t *testing.T) {
	r := NewRegistry()
	if err := r.Validate("unknown", "anything"); err != nil {
		t.Errorf("unregistered type should pass, got: %v", err)
	}
}

func TestRegistry_NilReceiverIsSafe(t *testing.T) {
	var r *Registry // nil
	if err := r.Validate("tenant", "spec"); err != nil {
		t.Errorf("nil registry should be safe, got: %v", err)
	}
}

func TestRegistry_Overwrite(t *testing.T) {
	r := NewRegistry()
	r.RegisterFunc("x", ValidatorFunc(func(any) error { return errors.New("first") }))
	r.RegisterFunc("x", ValidatorFunc(func(any) error { return nil })) // overwrite
	if err := r.Validate("x", nil); err != nil {
		t.Error("overwrite should have replaced the validator")
	}
}
