package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestTFConfig_RoundTripsAsValidJSON ensures the builder output is parseable
// JSON with the expected top-level shape — the foundation every provider relies
// on. (Structural TF validity is proven by each provider's integration test
// via `tofu validate`.)
func TestTFConfig_RoundTripsAsValidJSON(t *testing.T) {
	c := NewTFConfig()
	c.AddRequiredProviders(
		ReqProvider{Name: "oci", Source: "oracle/oci", Version: ">= 6.0"},
		ReqProvider{Name: "random", Source: "hashicorp/random"},
	)
	c.AddProvider("oci", Obj{"region": "us-phoenix-1"})
	c.AddResource("null_resource", "a", Obj{"triggers": Obj{"x": "1"}})
	c.AddDataSource("external", "ext", Obj{"program": []string{"echo"}})

	out, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	for _, key := range []string{"terraform", "provider", "resource", "data"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing top-level key %q in %s", key, out)
		}
	}
}

func TestTFConfig_RequiredProvidersIsSingleObject(t *testing.T) {
	// The bug from #88: required_providers must be ONE object, not an array of
	// single-key objects. Pin the correct shape.
	c := NewTFConfig()
	c.AddRequiredProviders(
		ReqProvider{Name: "oci", Source: "oracle/oci"},
		ReqProvider{Name: "talos", Source: "siderolabs/talos"},
	)
	out, _ := json.Marshal(c)
	var m struct {
		Terraform struct {
			RequiredProviders map[string]interface{} `json:"required_providers"`
		} `json:"terraform"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Must be a single object keyed by provider name, not an array.
	if _, ok := m.Terraform.RequiredProviders["oci"]; !ok {
		t.Errorf("required_providers[oci] missing: %v", m.Terraform.RequiredProviders)
	}
	if _, ok := m.Terraform.RequiredProviders["talos"]; !ok {
		t.Errorf("required_providers[talos] missing: %v", m.Terraform.RequiredProviders)
	}
}

func TestTFConfig_RequiredProvidersVersionOmittedWhenEmpty(t *testing.T) {
	c := NewTFConfig()
	c.AddRequiredProviders(ReqProvider{Name: "x", Source: "x/y"}) // no version
	out, _ := json.Marshal(c)
	if strings.Contains(string(out), `"version"`) {
		t.Errorf("version should be omitted when empty: %s", out)
	}
}

func TestTFConfig_AddResourceMergesSameType(t *testing.T) {
	// Two resources of the same TYPE share one TYPE-keyed object.
	c := NewTFConfig()
	c.AddResource("null_resource", "a", Obj{"x": 1})
	c.AddResource("null_resource", "b", Obj{"y": 2})
	out, _ := json.Marshal(c)
	var m struct {
		Resource map[string]map[string]interface{} `json:"resource"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	resType := m.Resource["null_resource"]
	if resType["a"] == nil || resType["b"] == nil {
		t.Errorf("expected both a and b under null_resource: %v", resType)
	}
}

func TestTFConfig_OmitsEmptySections(t *testing.T) {
	c := NewTFConfig()
	c.AddProvider("x", Obj{}) // only a provider, no resources/data/terraform
	out, _ := json.Marshal(c)
	if strings.Contains(string(out), `"resource"`) || strings.Contains(string(out), `"data"`) || strings.Contains(string(out), `"terraform"`) {
		t.Errorf("empty sections should be omitted: %s", out)
	}
	if !strings.Contains(string(out), `"provider"`) {
		t.Errorf("non-empty provider section should be present: %s", out)
	}
}
