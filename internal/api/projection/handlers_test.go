package projection

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	proj "github.com/rezuscloud/rezuscloud/internal/projection"
	"github.com/rezuscloud/rezuscloud/internal/provider"
)

// fakeSource returns a fixed state blob for testing.
type fakeSource struct {
	blob []byte
}

func (f *fakeSource) State(_ context.Context, _ string) ([]byte, error) {
	return f.blob, nil
}

// testProvider declares the oci_core_instance → Machine mapping so the
// projection can resolve TF types to Kinds without importing a real provider.
type testProvider struct{}

func (p *testProvider) Type() string { return "oci" }
func (p *testProvider) Mappings() []provider.TFResourceMapping {
	return []provider.TFResourceMapping{{TFType: "oci_core_instance", Kind: "Machine"}}
}
func (p *testProvider) Render(_ provider.RenderRequest) ([]byte, error) { return nil, nil }

func newTestIndex(t *testing.T) *proj.Index {
	t.Helper()
	// Minimal TF state with one oci_core_instance.
	blob := []byte(`{
		"version": 4,
		"serial": 3,
		"resources": [
			{
				"mode": "managed",
				"type": "oci_core_instance",
				"name": "cp",
				"instances": [
					{
						"attributes": {
							"id": "ocid1.instance.abc",
							"display_name": "talos-oci-c-foo",
							"shape": "VM.Standard.A1.Flex",
							"public_ip": "129.152.1.1"
						}
					}
				]
			},
			{
				"mode": "managed",
				"type": "null_resource",
				"name": "demo",
				"instances": [
					{"attributes": {"id": "123"}}
				]
			}
		]
	}`)

	registry := provider.NewRegistry()
	registry.Register(&testProvider{})
	idx := proj.New(&fakeSource{blob: blob}, registry)
	// Register a Machine extractor that reads standard instance fields.
	idx.RegisterExtractor("Machine", func(tfType string, attrs map[string]interface{}) map[string]interface{} {
		spec := map[string]interface{}{}
		for _, k := range []string{"id", "display_name", "shape", "public_ip"} {
			if v, ok := attrs[k]; ok {
				spec[k] = v
			}
		}
		return spec
	})
	if _, err := idx.Rebuild(context.Background(), "personal"); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	return idx
}

func TestProjectionAPI_ListByTenant(t *testing.T) {
	idx := newTestIndex(t)
	api := NewAPI(idx)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/personal/projected", nil)
	req.SetPathValue("tenant", "personal")
	w := httptest.NewRecorder()
	api.ListByTenant(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp listResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	// oci_core_instance maps to Machine; null_resource is unmapped and skipped.
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1", resp.Total)
	}
	if resp.Items[0].Kind != "Machine" {
		t.Errorf("kind = %q, want Machine", resp.Items[0].Kind)
	}
	if resp.Items[0].TFType != "oci_core_instance" {
		t.Errorf("tfType = %q", resp.Items[0].TFType)
	}
}

func TestProjectionAPI_ListByTenantKindFilter(t *testing.T) {
	idx := newTestIndex(t)
	api := NewAPI(idx)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/personal/projected?kind=Machine", nil)
	req.SetPathValue("tenant", "personal")
	w := httptest.NewRecorder()
	api.ListByTenant(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var resp listResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Total != 1 {
		t.Errorf("total = %d, want 1 (Machine filter)", resp.Total)
	}
}

func TestProjectionAPI_ListByTenantKindPath(t *testing.T) {
	idx := newTestIndex(t)
	api := NewAPI(idx)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/personal/projected/Machine", nil)
	req.SetPathValue("tenant", "personal")
	req.SetPathValue("kind", "Machine")
	w := httptest.NewRecorder()
	api.ListByTenantKind(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp listResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Total != 1 {
		t.Errorf("total = %d, want 1", resp.Total)
	}
}

func TestProjectionAPI_ListByTenantEmpty(t *testing.T) {
	idx := newTestIndex(t)
	api := NewAPI(idx)

	// A tenant with no projection (never applied).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/unknown/projected", nil)
	req.SetPathValue("tenant", "unknown")
	w := httptest.NewRecorder()
	api.ListByTenant(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp listResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Total != 0 || len(resp.Items) != 0 {
		t.Errorf("expected empty, got total=%d", resp.Total)
	}
}

func TestProjectionAPI_ListAll(t *testing.T) {
	idx := newTestIndex(t)
	api := NewAPI(idx)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projected", nil)
	w := httptest.NewRecorder()
	api.ListAll(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp listResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Total != 1 {
		t.Errorf("total = %d, want 1", resp.Total)
	}
}

func TestProjectionAPI_NilIndexReturns503(t *testing.T) {
	api := NewAPI(nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/personal/projected", nil)
	req.SetPathValue("tenant", "personal")
	w := httptest.NewRecorder()
	api.ListByTenant(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}
