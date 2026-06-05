// Package web provides HTTP handlers for the WebUI dashboard.
// It renders server-side HTML using templ templates and calls
// the internal store directly (no HTTP roundtrip).
package web

import (
	"encoding/json"
	"net/http"

	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/web/layout"
	"github.com/rezuscloud/rezuscloud/internal/web/pages"
)

// Handler serves the WebUI.
type Handler struct {
	store *state.Store
}

// NewHandler creates a WebUI handler.
func NewHandler(store *state.Store) *Handler {
	return &Handler{store: store}
}

// RegisterRoutes registers WebUI routes.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /", h.Dashboard)
	mux.HandleFunc("GET /tenants", h.TenantsList)
	mux.HandleFunc("GET /tenants/{name}", h.TenantDetail)
	mux.HandleFunc("GET /login", h.LoginPage)
	mux.HandleFunc("POST /login", h.LoginSubmit)
	mux.HandleFunc("GET /logout", h.Logout)
}

// --- Helpers ---

func (h *Handler) render(w http.ResponseWriter, r *http.Request, props layout.BaseProps) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = layout.Base(props).Render(r.Context(), w)
}

func getUsername(r *http.Request) string {
	// In a real app, this would extract from JWT/session.
	// For now, check the Authorization header.
	return ""
}

// --- Handlers ---

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	data := pages.DashboardData{}

	// Get counts.
	tenantMetas, _, _, tc, _ := h.store.ListResources("tenant", state.ListOptions{})
	machineMetas, _, _, mc, _ := h.store.ListResources("machine", state.ListOptions{})
	providerMetas, _, _, pc, _ := h.store.ListResources("provider", state.ListOptions{})
	ngMetas, _, _, nc, _ := h.store.ListResources("nodegroup", state.ListOptions{})

	data.TenantCount = tc
	data.MachineCount = mc
	data.ProviderCount = pc
	data.NodeGroupCount = nc

	// Build tenant summaries.
	data.Tenants = make([]pages.TenantSummary, 0, len(tenantMetas))
	for _, m := range tenantMetas {
		ts := pages.TenantSummary{
			Name:  m.Name,
			Phase: "active",
		}
		// Count machines for this tenant.
		machineOpts := state.ListOptions{
			LabelSelector: "rezuscloud.io/tenant=" + m.Name,
		}
		_, _, _, total, _ := h.store.ListResources("machine", machineOpts)
		ts.Total = total
		data.Tenants = append(data.Tenants, ts)
	}

	_ = machineMetas
	_ = providerMetas
	_ = ngMetas

	h.render(w, r, layout.BaseProps{
		Title:   "Dashboard",
		Page:    "dashboard",
		User:    getUsername(r),
		Content: pages.Dashboard(data),
	})
}

func (h *Handler) TenantsList(w http.ResponseWriter, r *http.Request) {
	metas, _, _, _, _ := h.store.ListResources("tenant", state.ListOptions{})

	tenants := make([]pages.TenantSummary, 0, len(metas))
	for _, m := range metas {
		tenants = append(tenants, pages.TenantSummary{
			Name:  m.Name,
			Phase: "active",
		})
	}

	h.render(w, r, layout.BaseProps{
		Title:   "Tenants",
		Page:    "tenants",
		User:    getUsername(r),
		Content: pages.TenantsList(tenants),
	})
}

func (h *Handler) TenantDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	// Get tenant.
	var spec state.TenantSpec
	meta, err := h.store.GetResource("tenant", name, &spec, nil)
	if err != nil || meta.Name == "" {
		http.Error(w, "tenant not found", http.StatusNotFound)
		return
	}

	data := pages.TenantDetailData{
		Name:         name,
		Phase:        "active",
		K8sVersion:   spec.KubernetesVersion,
		TalosVersion: spec.TalosVersion,
	}

	// Node groups.
	ngOpts := state.ListOptions{LabelSelector: "rezuscloud.io/tenant=" + name}
	ngMetas, ngSpecs, _, _, _ := h.store.ListResources("nodegroup", ngOpts)
	data.NodeGroups = make([]pages.NodeGroupRow, 0, len(ngMetas))
	for i, m := range ngMetas {
		var ngSpec struct {
			Name  string `json:"name"`
			Role  string `json:"role"`
			Count int    `json:"count"`
		}
		_ = json.Unmarshal(ngSpecs[i], &ngSpec)
		data.NodeGroups = append(data.NodeGroups, pages.NodeGroupRow{
			Name:  m.Name,
			Role:  ngSpec.Role,
			Count: ngSpec.Count,
		})
	}

	// Machines.
	machOpts := state.ListOptions{LabelSelector: "rezuscloud.io/tenant=" + name}
	machMetas, machSpecs, _, _, _ := h.store.ListResources("machine", machOpts)
	data.Machines = make([]pages.MachineRow, 0, len(machMetas))
	for i, m := range machMetas {
		var machSpec struct {
			Connected bool   `json:"connected"`
			Role      string `json:"role"`
		}
		_ = json.Unmarshal(machSpecs[i], &machSpec)
		data.Machines = append(data.Machines, pages.MachineRow{
			ID:        m.Name,
			Stage:     "unknown",
			Connected: machSpec.Connected,
			Role:      machSpec.Role,
		})
	}

	// Patches.
	patchMetas, patchSpecs, _, _, _ := h.store.ListResources("configpatch", ngOpts)
	data.Patches = make([]pages.PatchRow, 0, len(patchMetas))
	for i, m := range patchMetas {
		var ps struct {
			Format     string `json:"format"`
			TargetRole string `json:"targetRole"`
			Enabled    bool   `json:"enabled"`
		}
		_ = json.Unmarshal(patchSpecs[i], &ps)
		data.Patches = append(data.Patches, pages.PatchRow{
			Name:       m.Name,
			Format:     ps.Format,
			TargetRole: ps.TargetRole,
			Enabled:    ps.Enabled,
		})
	}

	h.render(w, r, layout.BaseProps{
		Title:   name,
		Page:    "tenants",
		User:    getUsername(r),
		Content: pages.TenantDetail(data),
	})
}

func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, layout.BaseProps{
		Title:   "Sign In",
		Page:    "login",
		Content: pages.Login(pages.LoginData{}),
	})
}

func (h *Handler) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		h.render(w, r, layout.BaseProps{
			Title:   "Sign In",
			Page:    "login",
			Content: pages.Login(pages.LoginData{Error: "Username and password are required"}),
		})
		return
	}

	// TODO: Authenticate against auth.JWTManager.
	// For now, redirect to dashboard on any login.
	http.SetCookie(w, &http.Cookie{
		Name:     "rezuscloud_session",
		Value:    "placeholder",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "rezuscloud_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
