// Package web provides the WebUI Handler struct and route wiring.
//
// All section handlers live in sub-packages under internal/web/handlers/
// (authn, dashboard, clusters, machines, settings). This file is a thin
// wiring layer that:
//
//   - Constructs the section handlers
//   - Implements the Host interface they need (Render, PopToast,
//     AuthRequired, CanMutate, IsAdmin, RedirectAction, TenantSummaries,
//     NodeGroupSummaries, ClusterNames, TenantNames,
//     BusPresent)
//   - Provides the dashboard adapters (backupAdapter, upgradeAdapter)
//
// There are no section-specific HTTP handlers in this file. To add a new
// page, create or extend a sub-package under internal/web/handlers/.
package web

import (
	"encoding/json"
	"net/http"

	"github.com/rezuscloud/rezuscloud/internal/audit"
	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/backup"
	"github.com/rezuscloud/rezuscloud/internal/dashboard"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/upgrade"
	"github.com/rezuscloud/rezuscloud/internal/watch"
	"github.com/rezuscloud/rezuscloud/internal/web/handlers/authn"
	"github.com/rezuscloud/rezuscloud/internal/web/handlers/clusters"
	dashhandler "github.com/rezuscloud/rezuscloud/internal/web/handlers/dashboard"
	"github.com/rezuscloud/rezuscloud/internal/web/handlers/machines"
	machineshandler "github.com/rezuscloud/rezuscloud/internal/web/handlers/machines"
	"github.com/rezuscloud/rezuscloud/internal/web/handlers/settings"
	"github.com/rezuscloud/rezuscloud/internal/web/layout"
	"github.com/rezuscloud/rezuscloud/internal/web/pages"
	staticFiles "github.com/rezuscloud/rezuscloud/internal/web/static"
)

// Handler is the WebUI wiring root. It owns the shared dependencies
// (store, JWT manager, watch bus, audit/backup/upgrade subsystems) and
// implements the Host interface that section sub-packages call back into.
type Handler struct {
	store          state.StoreAPI
	jwtManager     *auth.JWTManager
	bus            watch.Bus                           // optional — enables /events/stream
	auditStore     audit.Store                         // optional — enables /settings/audit
	backupSvc      *backup.Service                     // optional — enables /settings/backups
	upgradeMgr     *upgrade.Manager                    // optional — enables cluster upgrade endpoints
	metricsAgg_    dashhandler.MetricsAggregator       // optional — enables resource pressure on dashboard
	machineActions machineshandler.MachineActionRunner // optional — enables reboot/shutdown/logs
}

// NewHandler creates a WebUI handler.
// jwtManager is required for login and cookie validation.
// bus is optional — pass nil to disable the /events/stream endpoint.
func NewHandler(store state.StoreAPI, jwtManager *auth.JWTManager, bus watch.Bus) *Handler {
	return &Handler{store: store, jwtManager: jwtManager, bus: bus}
}

// --- Dependency injection ---

// WithUpgradeManager injects an upgrade manager so the WebUI can start,
// cancel, and list upgrade runs.
func (h *Handler) WithUpgradeManager(m *upgrade.Manager) *Handler {
	h.upgradeMgr = m
	return h
}

// WithBackupService injects a backup service so the WebUI can render
// /settings/backups and trigger backups.
func (h *Handler) WithBackupService(svc *backup.Service) *Handler {
	h.backupSvc = svc
	return h
}

// WithBackupComponent injects the backup subsystem component. The WebUI uses
// only the Service from it.
func (h *Handler) WithBackupComponent(c *backup.Component) *Handler {
	if c == nil {
		return h
	}
	h.backupSvc = c.Service
	return h
}

// WithAuditStore injects an audit store so the WebUI can render /settings/audit.
//
// Deprecated: prefer WithAuditComponent. Kept for callers that only need
// the read path.
func (h *Handler) WithAuditStore(s audit.Store) *Handler {
	h.auditStore = s
	return h
}

// WithMetricsAggregator injects a metrics aggregator so the dashboard can
// show resource pressure (CPU, memory, disk, pod counts).
func (h *Handler) WithMetricsAggregator(agg dashhandler.MetricsAggregator) *Handler {
	h.metricsAgg_ = agg
	return h
}

func (h *Handler) metricsAgg() dashhandler.MetricsAggregator {
	return h.metricsAgg_
}

// WithMachineActions injects the machine action runner (reboot/shutdown/logs
// via the Talos API). Optional — without it, those endpoints return 503.
func (h *Handler) WithMachineActions(r machineshandler.MachineActionRunner) *Handler {
	h.machineActions = r
	return h
}

// WithAuditComponent injects the audit subsystem component.
func (h *Handler) WithAuditComponent(c *audit.Component) *Handler {
	if c == nil {
		return h
	}
	h.auditStore = c.Store
	return h
}

// --- Route wiring ---

// RegisterRoutes registers all WebUI routes by delegating to the section
// sub-packages. The Handler itself owns no routes; it only composes them.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	authn.New(h.store, h.jwtManager, h).RegisterRoutes(mux)
	dashhandler.New(h.store, h.bus, h.auditStore, h.backupAdapter(), h.upgradeAdapter(), h.metricsAgg(), h).RegisterRoutes(mux)
	clusters.New(h.store, h.upgradeMgr, h).RegisterRoutes(mux)
	mh := machineshandler.New(h.store, h.bus, h)
	if h.machineActions != nil {
		mh.WithActions(h.machineActions)
	}
	mh.RegisterRoutes(mux)
	settings.New(h.store, h.backupSvc, h.auditStore, h).RegisterRoutes(mux)

	// Static assets (W12 logs/monitor SSE consumers).
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFiles.Files))))
}

// --- Auth middleware ---

// AuthRequired wraps a WebUI page handler with JWT cookie authentication.
// Unauthenticated requests are redirected to /login.
// On success, the username and role are added to the request context via
// auth context keys.
func (h *Handler) AuthRequired(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("rezuscloud_session")
		if err != nil || cookie.Value == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		claims, err := h.jwtManager.ValidateToken(cookie.Value)
		if err != nil {
			http.SetCookie(w, &http.Cookie{
				Name: "rezuscloud_session", Value: "", Path: "/", HttpOnly: true,
				SameSite: http.SameSiteLaxMode, MaxAge: -1,
			})
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		role := claims.Role
		if role == "" {
			role = string(auth.RoleView)
		}
		ctx := auth.WithClaims(r.Context(), claims.Username, role)
		next(w, r.WithContext(ctx))
	}
}

// --- Host interface: rendering + helpers ---

// render writes the layout.Base page to w, after injecting the username
// from the auth context. Exported as Render so sub-packages can call into
// the same layout wrapper without re-implementing it.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, props layout.BaseProps) {
	props.User = auth.UserFromContext(r.Context())
	if props.User == "" && props.Page != "login" {
		// Should not happen — AuthRequired gates this — but be defensive.
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = layout.Base(props).Render(r.Context(), w)
}

// Render satisfies the web/handlers/* Host interface (render alias).
func (h *Handler) Render(w http.ResponseWriter, r *http.Request, props layout.BaseProps) {
	h.render(w, r, props)
}

// popToast reads + clears a flash toast from a query-string param.
// Supports ?toast=... (plain message) and optionally ?toast-type=success|error.
// Returns zero-value ToastData if no message is present.
func (h *Handler) popToast(r *http.Request) layout.ToastData {
	msg := r.URL.Query().Get("toast")
	if msg == "" {
		return layout.ToastData{}
	}
	return layout.ToastData{
		Type:    r.URL.Query().Get("toast-type"),
		Message: msg,
	}
}

// PopToast satisfies the web/handlers/* Host interface.
func (h *Handler) PopToast(r *http.Request) layout.ToastData {
	return h.popToast(r)
}

// canMutate reports whether the current user has a role that permits
// mutating the resource (admin or edit). View-only users see read-only UIs.
func (h *Handler) canMutate(r *http.Request) bool {
	role := auth.RoleFromContext(r.Context())
	return role == string(auth.RoleAdmin) || role == string(auth.RoleEdit)
}

// CanMutate satisfies the web/handlers/* Host interface.
func (h *Handler) CanMutate(r *http.Request) bool {
	return h.canMutate(r)
}

// isAdmin reports whether the current user is an admin.
func (h *Handler) isAdmin(r *http.Request) bool {
	return auth.RoleFromContext(r.Context()) == string(auth.RoleAdmin)
}

// IsAdmin satisfies the web/handlers/* Host interface.
func (h *Handler) IsAdmin(r *http.Request) bool {
	return h.isAdmin(r)
}

// redirectAction emits an HTMX-aware redirect (HX-Redirect header for HTMX
// requests, plain 303 otherwise).
func (h *Handler) redirectAction(w http.ResponseWriter, r *http.Request, target string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// RedirectAction satisfies the web/handlers/* Host interface.
func (h *Handler) RedirectAction(w http.ResponseWriter, r *http.Request, target string) {
	h.redirectAction(w, r, target)
}

// BusPresent reports whether the watch bus is configured. Used by the
// dashboard and clusters sub-packages to toggle the "live updates" SSE hint.
func (h *Handler) BusPresent() bool {
	return h.bus != nil
}

// --- Host interface: tenant/machine loaders ---

// tenantSummaries loads tenants with computed phase and machine counts.
// Used by the dashboard and clusters sub-packages.
func (h *Handler) tenantSummaries() []pages.TenantSummary {
	tenants, _, _ := h.store.ListTenants()
	out := make([]pages.TenantSummary, 0, len(tenants))
	for _, t := range tenants {
		machines, _, _ := h.store.ListMachinesByTenant(t.Metadata.Name)
		nodeGroups := h.nodeGroupSummaries(t.Metadata.Name)

		// Re-load tenant to get the latest status.
		current, _ := h.store.GetTenant(t.Metadata.Name)
		if current == nil {
			current = t
		}
		status := state.ComputeTenantStatus(current, machines, nodeGroups)

		summary := pages.TenantSummary{
			Name:  t.Metadata.Name,
			Phase: string(status.Phase),
			Ready: status.Machines.Healthy,
			Total: status.Machines.Total,
		}
		if summary.Total == 0 {
			summary.Total = expectedMachineCount(nodeGroups)
		}
		out = append(out, summary)
	}
	return out
}

// expectedMachineCount sums node group counts (for forming tenants with no machines yet).
func expectedMachineCount(nodeGroups []state.NodeGroupSummary) int {
	total := 0
	for _, ng := range nodeGroups {
		total += ng.Count
	}
	return total
}

// TenantSummaries satisfies the web/handlers/* Host interface.
func (h *Handler) TenantSummaries() []pages.TenantSummary {
	return h.tenantSummaries()
}

// nodeGroupSummaries loads node groups for a tenant and returns the
// summary view used for status derivation.
func (h *Handler) nodeGroupSummaries(tenantName string) []state.NodeGroupSummary {
	items, _, _ := state.ListTypedByTenant(h.store, "nodegroup", tenantName,
		func(meta state.Metadata, specRaw, _ json.RawMessage) (state.NodeGroupSummary, error) {
			var ng struct {
				Name  string `json:"name"`
				Count int    `json:"count"`
			}
			err := json.Unmarshal(specRaw, &ng)
			return state.NodeGroupSummary{Name: ng.Name, Count: ng.Count}, err
		})
	return items
}

// NodeGroupSummaries satisfies the web/handlers/* Host interface.
func (h *Handler) NodeGroupSummaries(tenantName string) []state.NodeGroupSummary {
	return h.nodeGroupSummaries(tenantName)
}

// clusterNames returns the list of cluster names for filter dropdowns.
func (h *Handler) clusterNames() []string {
	tenants, _, _ := h.store.ListTenants()
	out := make([]string, 0, len(tenants))
	for _, t := range tenants {
		out = append(out, t.Metadata.Name)
	}
	return out
}

// ClusterNames satisfies the web/handlers/* Host interface.
func (h *Handler) ClusterNames() []string {
	return h.clusterNames()
}

// tenantNames returns the list of existing tenant names. Same as ClusterNames
// (tenants == clusters in the current UI vocabulary).
func (h *Handler) tenantNames() []string {
	return h.clusterNames()
}

// TenantNames satisfies the web/handlers/* Host interface.
func (h *Handler) TenantNames() []string {
	return h.tenantNames()
}

// --- Dashboard adapters (for the dashboard sub-package) ---

// backupAdapter wraps the optional *backup.Service into the
// dashboard.BackupReader interface. Returns nil when backups are not
// configured (the dashboard module degrades gracefully).
func (h *Handler) backupAdapter() dashboard.BackupReader {
	if h.backupSvc == nil {
		return nil
	}
	return &backupDashboardAdapter{svc: h.backupSvc}
}

type backupDashboardAdapter struct {
	svc *backup.Service
}

func (a *backupDashboardAdapter) ListSnapshots() ([]dashboard.BackupSnapshot, error) {
	snaps, err := a.svc.ListSnapshots()
	if err != nil {
		return nil, err
	}
	out := make([]dashboard.BackupSnapshot, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, dashboard.BackupSnapshot{
			CreatedAt: s.CreatedAt,
			Status:    dashboard.BackupSnapshotStatus{Status: s.Status.Status},
		})
	}
	return out, nil
}

// upgradeAdapter wraps the optional *upgrade.Manager into the
// dashboard.UpgradeReader interface. Returns nil when upgrades are not
// configured.
func (h *Handler) upgradeAdapter() dashboard.UpgradeReader {
	if h.upgradeMgr == nil {
		return nil
	}
	return &upgradeDashboardAdapter{mgr: h.upgradeMgr}
}

type upgradeDashboardAdapter struct {
	mgr *upgrade.Manager
}

func (a *upgradeDashboardAdapter) ListRuns(tenant string) ([]dashboard.UpgradeRun, error) {
	runs, err := a.mgr.ListRuns(tenant)
	if err != nil {
		return nil, err
	}
	out := make([]dashboard.UpgradeRun, 0, len(runs))
	for _, r := range runs {
		out = append(out, dashboard.UpgradeRun{
			Tenant:  r.Spec.Tenant,
			Target:  r.Spec.Target,
			Phase:   string(r.Status.Phase),
			Error:   r.Status.Error,
			Started: r.Status.StartedAt,
		})
	}
	return out, nil
}

// --- Compile-time Host interface checks ---

// These assertions ensure *Handler implements every sub-package's Host
// interface. If a method is removed or its signature drifts, the build fails
// here with a clear message.
var (
	_ authn.Renderer   = (*Handler)(nil)
	_ dashhandler.Host = (*Handler)(nil)
	_ clusters.Host    = (*Handler)(nil)
	_ machines.Host    = (*Handler)(nil)
	_ settings.Host    = (*Handler)(nil)
)
