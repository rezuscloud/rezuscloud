package operationalmetrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

func TestMetricsHandler_EmptyStore(t *testing.T) {
	s, _ := state.Open(":memory:")
	defer s.Close()

	h := NewHandler(s)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "rezuscloud_info") {
		t.Error("missing rezuscloud_info metric")
	}
	if !strings.Contains(body, "rezuscloud_tenants_total 0") {
		t.Error("missing tenants_total 0")
	}
	if !strings.Contains(body, "rezuscloud_machines_total") {
		t.Error("missing machines_total metric")
	}
}

func TestMetricsHandler_WithTenantAndMachines(t *testing.T) {
	s, _ := state.Open(":memory:")
	defer s.Close()

	_, _ = s.CreateTenant("prod", state.TenantSpec{KubernetesVersion: "1.35.0"}, nil, nil)
	_, _ = s.UpdateTenantStatus("prod", state.TenantStatus{
		Reconciliation: &state.ReconciliationStatus{Phase: "applied"},
	})

	_, _ = s.CreateMachine("cp-0", state.MachineSpec{},
		map[string]string{"rezuscloud.io/tenant": "prod"}, nil)
	_, _ = s.UpdateMachineStatus("cp-0", state.MachineStatus{Stage: state.StageReady})
	_, _ = s.CreateMachine("worker-0", state.MachineSpec{},
		map[string]string{"rezuscloud.io/tenant": "prod"}, nil)
	_, _ = s.UpdateMachineStatus("worker-0", state.MachineStatus{Stage: state.StageInstalling})

	h := NewHandler(s)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "rezuscloud_tenants_total 1") {
		t.Error("expected tenants_total 1")
	}
	if !strings.Contains(body, `stage="ready"`) {
		t.Error("missing machine stage=ready")
	}
	if !strings.Contains(body, `stage="installing"`) {
		t.Error("missing machine stage=installing")
	}
	if !strings.Contains(body, `phase="applied"`) {
		t.Error("missing reconciliation phase=applied")
	}
}
