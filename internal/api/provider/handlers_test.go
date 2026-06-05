package provider

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

func setupTest(t *testing.T) (*state.Store, *API) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := state.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, NewAPI(store)
}

func createTestProvider(t *testing.T, store *state.Store, providerType string) *state.Provider {
	t.Helper()
	p, err := store.UpsertProvider(providerType, state.ProviderSpec{
		Endpoint: "localhost:50190",
	}, state.ProviderStatus{
		Connected:     true,
		LastHeartbeat: time.Now().UTC(),
		Schema: &state.ProviderSchema{
			MachineTypes: []string{"standard", "gpu"},
			Regions:      []string{"eu-west-1"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	return p
}

func TestProvider_List(t *testing.T) {
	store, api := setupTest(t)
	createTestProvider(t, store, "hetzner")
	createTestProvider(t, store, "oci")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
	w := httptest.NewRecorder()
	api.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp listResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Total != 2 {
		t.Errorf("total = %d, want 2", resp.Total)
	}
}

func TestProvider_Get(t *testing.T) {
	store, api := setupTest(t)
	createTestProvider(t, store, "hetzner")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers/hetzner", nil)
	req.SetPathValue("type", "hetzner")
	w := httptest.NewRecorder()
	api.Get(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var provider state.Provider
	_ = json.NewDecoder(w.Body).Decode(&provider)
	if provider.Metadata.Name != "hetzner" {
		t.Errorf("type = %q, want %q", provider.Metadata.Name, "hetzner")
	}
	if !provider.Status.Connected {
		t.Error("provider should be connected")
	}
	if provider.Status.Schema == nil {
		t.Fatal("schema should be present")
	}
	if len(provider.Status.Schema.MachineTypes) != 2 {
		t.Errorf("machine types = %d, want 2", len(provider.Status.Schema.MachineTypes))
	}
}

func TestProvider_GetNotFound(t *testing.T) {
	_, api := setupTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers/nonexistent", nil)
	req.SetPathValue("type", "nonexistent")
	w := httptest.NewRecorder()
	api.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestProvider_UpdateStatus(t *testing.T) {
	store, api := setupTest(t)
	createTestProvider(t, store, "hetzner")

	body := map[string]any{
		"status": map[string]any{
			"connected":     false,
			"lastHeartbeat": time.Now().UTC().Format(time.RFC3339),
			"error":         "connection timeout",
		},
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/providers/hetzner/status", bytes.NewReader(b))
	req.SetPathValue("type", "hetzner")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.UpdateStatus(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d, body = %s", w.Code, http.StatusNoContent, w.Body.String())
	}

	// Verify status updated.
	provider, _ := store.GetProvider("hetzner")
	if provider.Status.Connected {
		t.Error("provider should be disconnected")
	}
	if provider.Status.Error != "connection timeout" {
		t.Errorf("error = %q, want %q", provider.Status.Error, "connection timeout")
	}
}

func TestProvider_UpdateStatus_NotFound(t *testing.T) {
	_, api := setupTest(t)

	body := map[string]any{
		"status": map[string]any{
			"connected": true,
		},
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/providers/nonexistent/status", bytes.NewReader(b))
	req.SetPathValue("type", "nonexistent")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.UpdateStatus(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestProvider_ListEmpty(t *testing.T) {
	_, api := setupTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
	w := httptest.NewRecorder()
	api.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp listResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.Total != 0 {
		t.Errorf("total = %d, want 0", resp.Total)
	}
	if resp.Items == nil {
		t.Error("items should be empty slice, not nil")
	}
}
