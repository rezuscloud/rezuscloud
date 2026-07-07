package api

import (
	"encoding/json"
	"testing"
)

func TestAPIResourceList_Completeness(t *testing.T) {
	list := DefaultAPIResourceList()
	if list.APIVersion != "v1" || list.Kind != "APIResourceList" {
		t.Errorf("apiVersion/kind = %q/%q", list.APIVersion, list.Kind)
	}
	wantKinds := map[string]bool{
		"Tenant": true, "NodeGroup": true, "Machine": true,
		"ConfigPatch": true, "Provider": true, "User": true, "APIToken": true,
	}
	got := map[string]bool{}
	for _, r := range list.Resources {
		got[r.Kind] = true
		// Every resource must support at least get + list.
		hasGet, hasList := false, false
		for _, v := range r.Verbs {
			if v == "get" {
				hasGet = true
			}
			if v == "list" {
				hasList = true
			}
		}
		if !hasGet || !hasList {
			t.Errorf("%s: missing get/list verbs: %v", r.Kind, r.Verbs)
		}
	}
	for k := range wantKinds {
		if !got[k] {
			t.Errorf("missing kind %q in discovery response", k)
		}
	}
}

func TestAPIResourceList_JSONShape(t *testing.T) {
	raw, err := json.Marshal(DefaultAPIResourceList())
	if err != nil {
		t.Fatal(err)
	}
	var list APIResourceList
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if len(list.Resources) < 5 {
		t.Errorf("only %d resources in discovery", len(list.Resources))
	}
	// Verify a sample entry has the expected K8s fields.
	var tenant *APIResource
	for i := range list.Resources {
		if list.Resources[i].Kind == "Tenant" {
			tenant = &list.Resources[i]
		}
	}
	if tenant == nil {
		t.Fatal("Tenant kind not found")
	}
	if tenant.Name != "tenants" || tenant.SingularName != "tenant" {
		t.Errorf("tenant name/singular = %q/%q", tenant.Name, tenant.SingularName)
	}
}
