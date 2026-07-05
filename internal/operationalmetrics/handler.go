// Package metrics provides the /metrics endpoint for rezuscloud's own
// operational metrics (Prometheus text format). This is distinct from
// internal/metrics/ (the Prometheus *client* for resource pressure viz).
//
// No external dependency — the handler reads from the store on each scrape
// and emits Prometheus exposition format text directly.
package operationalmetrics

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/version"
)

// Handler serves GET /metrics in Prometheus text exposition format.
// It reads operational state from the store on each scrape — no background
// collection, no in-memory counters. The store query is cheap (single pass
// over resources table).
type Handler struct {
	store state.StoreAPI
}

// NewHandler creates a metrics handler backed by the given store.
func NewHandler(store state.StoreAPI) *Handler {
	return &Handler{store: store}
}

// RegisterRoutes registers the /metrics endpoint.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /metrics", h.ServeHTTP)
}

// ServeHTTP emits Prometheus-format metrics.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder

	// Build info metric (constant).
	b.WriteString("# HELP rezuscloud_info Build metadata.\n")
	b.WriteString("# TYPE rezuscloud_info gauge\n")
	b.WriteString(fmt.Sprintf("rezuscloud_info{version=%q,commit=%q} 1\n",
		version.Version, version.GitCommit))

	// Tenant count.
	tenants, _, _ := h.store.ListTenants()
	b.WriteString("# HELP rezuscloud_tenants_total Total number of tenants.\n")
	b.WriteString("# TYPE rezuscloud_tenants_total gauge\n")
	b.WriteString(fmt.Sprintf("rezuscloud_tenants_total %d\n", len(tenants)))

	// Machine count by stage.
	machines, _, _ := h.store.ListMachines()
	byStage := make(map[string]int)
	for _, m := range machines {
		stage := string(m.Status.Stage)
		if stage == "" {
			stage = "unknown"
		}
		byStage[stage]++
	}
	b.WriteString("# HELP rezuscloud_machines_total Total machines by stage.\n")
	b.WriteString("# TYPE rezuscloud_machines_total gauge\n")
	if len(byStage) == 0 {
		b.WriteString("rezuscloud_machines_total{stage=\"none\"} 0\n")
	} else {
		for stage, count := range byStage {
			b.WriteString(fmt.Sprintf("rezuscloud_machines_total{stage=%q} %d\n", stage, count))
		}
	}

	// Reconciliation status by phase (from tenant status).
	byPhase := make(map[string]int)
	for _, t := range tenants {
		phase := "unknown"
		if t.Status.Reconciliation != nil && t.Status.Reconciliation.Phase != "" {
			phase = t.Status.Reconciliation.Phase
		}
		byPhase[phase]++
	}
	b.WriteString("# HELP rezuscloud_reconciliation_total Tenants by reconciliation phase.\n")
	b.WriteString("# TYPE rezuscloud_reconciliation_total gauge\n")
	if len(byPhase) == 0 {
		b.WriteString("rezuscloud_reconciliation_total{phase=\"none\"} 0\n")
	} else {
		for phase, count := range byPhase {
			b.WriteString(fmt.Sprintf("rezuscloud_reconciliation_total{phase=%q} %d\n", phase, count))
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}
