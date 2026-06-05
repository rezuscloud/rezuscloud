package docker

import (
	"testing"
)

func TestNetworkProvider_Type(t *testing.T) {
	p := &DockerNetworkProvider{}
	if p.Type() != "docker:network" {
		t.Errorf("Type = %q, want docker:network", p.Type())
	}
}

func TestContainerProvider_Type(t *testing.T) {
	p := &DockerContainerProvider{}
	if p.Type() != "docker:container" {
		t.Errorf("Type = %q, want docker:container", p.Type())
	}
}

func TestStrVal(t *testing.T) {
	tests := []struct {
		name     string
		inputs   map[string]interface{}
		key      string
		expected string
	}{
		{"present", map[string]interface{}{"key": "value"}, "key", "value"},
		{"missing", map[string]interface{}{"key": "value"}, "other", ""},
		{"nil map", nil, "key", ""},
		{"wrong type", map[string]interface{}{"key": 123}, "key", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strVal(tt.inputs, tt.key)
			if got != tt.expected {
				t.Errorf("strVal(%v, %q) = %q, want %q", tt.inputs, tt.key, got, tt.expected)
			}
		})
	}
}

func TestIntVal(t *testing.T) {
	tests := []struct {
		name     string
		inputs   map[string]interface{}
		key      string
		expected int
	}{
		{"int", map[string]interface{}{"port": 6443}, "port", 6443},
		{"float64", map[string]interface{}{"port": float64(6443)}, "port", 6443},
		{"missing", map[string]interface{}{"port": 6443}, "other", 0},
		{"nil map", nil, "port", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := intVal(tt.inputs, tt.key)
			if got != tt.expected {
				t.Errorf("intVal(%v, %q) = %d, want %d", tt.inputs, tt.key, got, tt.expected)
			}
		})
	}
}
