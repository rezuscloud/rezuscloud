// Package web provides HTTP handlers for the WebUI dashboard.
// It renders server-side HTML using templ templates and calls
// the internal store directly (no HTTP roundtrip).
package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/api/patch"
	"github.com/rezuscloud/rezuscloud/internal/audit"
	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/backup"
	"github.com/rezuscloud/rezuscloud/internal/credentials"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/statemachine"
	"github.com/rezuscloud/rezuscloud/internal/talosconfig"
	"github.com/rezuscloud/rezuscloud/internal/upgrade"
	"github.com/rezuscloud/rezuscloud/internal/watch"
	"github.com/rezuscloud/rezuscloud/internal/web/layout"
	"github.com/rezuscloud/rezuscloud/internal/web/pages"
	staticFiles "github.com/rezuscloud/rezuscloud/internal/web/static"
	"sigs.k8s.io/yaml"
)

// Handler serves the WebUI.
type Handler struct {
	store      *state.Store
	jwtManager *auth.JWTManager
	bus        *watch.Bus  // optional — enables /events/stream
	auditStore audit.Store // optional — enables /settings/audit page
}

// NewHandler creates a WebUI handler.
// jwtManager is required for login and cookie validation.
// bus is optional — pass nil to disable the /events/stream endpoint.
func NewHandler(store *state.Store, jwtManager *auth.JWTManager, bus *watch.Bus) *Handler {
	return &Handler{store: store, jwtManager: jwtManager, bus: bus}
}

// WithAuditStore injects an audit store so the WebUI can render
// /settings/audit without going through the REST API.
func (h *Handler) WithAuditStore(s audit.Store) *Handler {
	h.auditStore = s
	return h
}

// RegisterRoutes registers WebUI routes. Must be called after WebUIAuthMiddleware
// is installed on the parent mux (if authentication is desired).
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Public (no auth required) — login/logout flow.
	mux.HandleFunc("GET /login", h.LoginPage)
	mux.HandleFunc("POST /login", h.LoginSubmit)
	mux.HandleFunc("GET /logout", h.Logout)

	// Authenticated pages.
	mux.HandleFunc("GET /", h.AuthRequired(h.Dashboard))
	mux.HandleFunc("GET /clusters", h.AuthRequired(h.TenantsList))
	mux.HandleFunc("GET /clusters/create", h.AuthRequired(h.ClusterCreatePage))
	mux.HandleFunc("POST /clusters/create", h.AuthRequired(h.ClusterCreateSubmit))
	mux.HandleFunc("GET /clusters/{name}", h.AuthRequired(h.TenantDetail))
	mux.HandleFunc("GET /clusters/{name}/{tab}", h.AuthRequired(h.TenantDetail))
	mux.HandleFunc("POST /clusters/{name}/patches/create", h.AuthRequired(h.ClusterPatchCreate))
	mux.HandleFunc("GET /clusters/{name}/patches/{patch}", h.AuthRequired(h.ClusterPatchEditPage))
	mux.HandleFunc("POST /clusters/{name}/patches/{patch}/save", h.AuthRequired(h.ClusterPatchSave))
	mux.HandleFunc("POST /clusters/{name}/patches/{patch}/delete", h.AuthRequired(h.ClusterPatchDelete))
	mux.HandleFunc("POST /clusters/{name}/patches/{patch}/toggle", h.AuthRequired(h.ClusterPatchToggle))
	mux.HandleFunc("GET /clusters/{name}/patches/preview", h.AuthRequired(h.ClusterPatchesPreview))
	mux.HandleFunc("DELETE /clusters/{name}", h.AuthRequired(h.ClusterDelete))
	mux.HandleFunc("POST /clusters/{name}/nodegroups/{ng}/scale", h.AuthRequired(h.NodeGroupScale))
	mux.HandleFunc("GET /clusters/{name}/kubeconfig", h.AuthRequired(h.ClusterKubeconfig))
	mux.HandleFunc("GET /clusters/{name}/talosconfig", h.AuthRequired(h.ClusterTalosconfig))
	mux.HandleFunc("POST /clusters/{name}/upgrade/start", h.AuthRequired(h.ClusterUpgradeStart))
	mux.HandleFunc("POST /clusters/{name}/upgrade/{id}/cancel", h.AuthRequired(h.ClusterUpgradeCancel))

	// Machines (W4).
	mux.HandleFunc("GET /machines", h.AuthRequired(h.MachinesList))
	mux.HandleFunc("GET /machines/jointokens", h.AuthRequired(h.JoinTokensList))
	mux.HandleFunc("POST /machines/jointokens", h.AuthRequired(h.JoinTokenCreate))
	mux.HandleFunc("GET /machines/join-manual", h.AuthRequired(h.ManualJoinPage))
	mux.HandleFunc("GET /machines/pending", h.AuthRequired(h.MachinesPending))
	mux.HandleFunc("GET /machines/{id}", h.AuthRequired(h.MachineDetail))
	mux.HandleFunc("GET /machines/{id}/logs", h.AuthRequired(h.MachineLogs))
	mux.HandleFunc("GET /machines/{id}/logs/poll", h.AuthRequired(h.MachineLogsPoll))
	mux.HandleFunc("GET /machines/{id}/monitor", h.AuthRequired(h.MachineMonitor))
	mux.HandleFunc("GET /machines/{id}/events", h.AuthRequired(h.MachineEvents))
	mux.HandleFunc("GET /machines/{id}/config", h.AuthRequired(h.MachineConfig))
	mux.HandleFunc("GET /machines/{id}/kernel-args", h.AuthRequired(h.MachineKernelArgs))
	mux.HandleFunc("POST /machines/{id}/kernel-args", h.AuthRequired(h.MachineKernelArgsSave))
	mux.HandleFunc("POST /machines/{id}/restart", h.AuthRequired(h.MachineRestart))
	mux.HandleFunc("POST /machines/{id}/shutdown", h.AuthRequired(h.MachineShutdown))
	mux.HandleFunc("POST /machines/{id}/approve", h.AuthRequired(h.MachineApprove))
	mux.HandleFunc("DELETE /machines/{id}", h.AuthRequired(h.MachineDelete))

	// Settings (W8 backups).
	mux.HandleFunc("GET /settings/backups", h.AuthRequired(h.BackupsPage))
	mux.HandleFunc("POST /settings/backups/database", h.AuthRequired(h.BackupsRunDatabase))
	mux.HandleFunc("POST /settings/backups/resources", h.AuthRequired(h.BackupsRunResources))
	mux.HandleFunc("POST /settings/backups/restore", h.AuthRequired(h.BackupsRestore))
	mux.HandleFunc("POST /settings/backups/policy", h.AuthRequired(h.BackupsPolicySave))

	// Settings (W9 users + API tokens).
	mux.HandleFunc("GET /settings/users", h.AuthRequired(h.UsersPage))
	mux.HandleFunc("POST /settings/users", h.AuthRequired(h.UserCreate))
	mux.HandleFunc("POST /settings/users/{name}", h.AuthRequired(h.UserUpdate))
	mux.HandleFunc("POST /settings/users/{name}/delete", h.AuthRequired(h.UserDelete))
	mux.HandleFunc("GET /settings/api-tokens", h.AuthRequired(h.APITokensPage))
	mux.HandleFunc("POST /settings/users/{name}/api-tokens", h.AuthRequired(h.APITokenCreate))
	mux.HandleFunc("POST /settings/api-tokens/{id}/delete", h.AuthRequired(h.APITokenDelete))

	// Settings (W10 audit).
	mux.HandleFunc("GET /settings/audit", h.AuthRequired(h.AuditPage))

	// Settings index (W13).
	mux.HandleFunc("GET /settings", h.AuthRequired(h.SettingsIndexPage))

	// Providers + manual join (W11).
	mux.HandleFunc("GET /providers", h.AuthRequired(h.ProvidersPage))

	// Legacy /tenants aliases (kept for backward compatibility; /clusters is the
	// user-facing name per W2). New code should use /clusters/*.
	mux.HandleFunc("GET /tenants", h.AuthRequired(h.TenantsList))
	mux.HandleFunc("GET /tenants/{name}", h.AuthRequired(h.TenantDetail))

	// SSE stream — optional, only when bus is configured.
	if h.bus != nil {
		mux.HandleFunc("GET /events/stream", h.AuthRequired(h.EventsStream))
	}

	// Static assets (W12 logs/monitor SSE consumers).
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFiles.Files))))
}

// AuthRequired wraps a WebUI page handler with JWT cookie authentication.
// Unauthenticated requests are redirected to /login.
// On success, the username and role are added to the request context via auth context keys.
func (h *Handler) AuthRequired(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("rezuscloud_session")
		if err != nil || cookie.Value == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		claims, err := h.jwtManager.ValidateToken(cookie.Value)
		if err != nil {
			// Expired or invalid — clear cookie and redirect.
			http.SetCookie(w, &http.Cookie{
				Name: "rezuscloud_session", Value: "", Path: "/", HttpOnly: true,
				SameSite: http.SameSiteLaxMode, MaxAge: -1,
			})
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		// Inject user/role into context via the auth package's context keys.
		ctx := auth.WithClaims(r.Context(), claims.Username, claims.Role)
		next(w, r.WithContext(ctx))
	}
}

// --- Helpers ---

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

// currentTab extracts the tab segment from the URL path. Returns "overview"
// when no tab is set (the cluster detail root URL) or for unknown tabs.
func currentTab(r *http.Request) string {
	tab := r.PathValue("tab")
	switch tab {
	case "", "overview", "patches", "backups", "upgrade", "settings":
		if tab == "" {
			return "overview"
		}
		return tab
	default:
		return "overview"
	}
}

// canMutate reports whether the current user has a role that permits
// mutating the resource (admin or edit). View-only users see read-only UIs.
func (h *Handler) canMutate(r *http.Request) bool {
	role := auth.RoleFromContext(r.Context())
	return role == string(auth.RoleAdmin) || role == string(auth.RoleEdit)
}

// tenantSummaries loads tenants with computed phase and machine counts.
func (h *Handler) tenantSummaries() []pages.TenantSummary {
	tenants, _, _ := h.store.ListTenants()
	out := make([]pages.TenantSummary, 0, len(tenants))
	for _, t := range tenants {
		machines, _, _ := h.store.ListMachinesByTenant(t.Metadata.Name)
		nodeGroups := h.nodeGroupSummaries(t.Metadata.Name)

		status := statemachine.ComputeTenantStatus(t, machines, nodeGroups)

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

// nodeGroupSummaries loads node groups for a tenant and returns the statemachine summary view.
func (h *Handler) nodeGroupSummaries(tenantName string) []statemachine.NodeGroupSummary {
	opts := state.ListOptions{LabelSelector: "rezuscloud.io/tenant=" + tenantName}
	_, specs, _, _, _ := h.store.ListResources("nodegroup", opts)

	out := make([]statemachine.NodeGroupSummary, 0, len(specs))
	for _, raw := range specs {
		var ng struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		}
		if err := json.Unmarshal(raw, &ng); err != nil {
			continue
		}
		out = append(out, statemachine.NodeGroupSummary{
			Name:  ng.Name,
			Count: ng.Count,
		})
	}
	return out
}

// expectedMachineCount sums node group counts (for forming tenants with no machines yet).
func expectedMachineCount(nodeGroups []statemachine.NodeGroupSummary) int {
	total := 0
	for _, ng := range nodeGroups {
		total += ng.Count
	}
	return total
}

// --- Handlers ---

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	data := pages.DashboardData{}

	tenants, _, _ := h.store.ListTenants()
	machines, _, _ := h.store.ListMachines()
	providers, _ := h.store.ListProviders()
	_, _, _, ngCount, _ := h.store.ListResources("nodegroup", state.ListOptions{})

	data.TenantCount = len(tenants)
	data.MachineCount = len(machines)
	data.ProviderCount = len(providers)
	data.NodeGroupCount = ngCount

	data.Tenants = h.tenantSummaries()

	// SSE hint: signal to the template that live updates are available.
	data.LiveStream = h.bus != nil

	toast := h.popToast(r)
	h.render(w, r, layout.BaseProps{
		Title:   "Dashboard",
		Page:    "dashboard",
		Content: pages.Dashboard(data),
		Toast:   toast,
	})
}

func (h *Handler) TenantsList(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, layout.BaseProps{
		Title: "Clusters",
		Page:  "clusters",
		Content: pages.TenantsList(pages.TenantListData{
			Tenants:    h.tenantSummaries(),
			LiveStream: h.bus != nil,
		}),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Clusters", Current: true},
		},
	})
}

func (h *Handler) TenantDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	// Get tenant.
	var spec state.TenantSpec
	meta, err := h.store.GetResource("tenant", name, &spec, nil)
	if err != nil || meta.Name == "" {
		http.NotFound(w, r)
		return
	}

	// Compute phase.
	machines, _, _ := h.store.ListMachinesByTenant(name)
	nodeGroups := h.nodeGroupSummaries(name)

	// Reload tenant with status from store (status may have been updated since load).
	tenant, _ := h.store.GetTenant(name)
	if tenant == nil {
		tenant = &state.Tenant{Metadata: meta, Spec: spec}
	}

	status := statemachine.ComputeTenantStatus(tenant, machines, nodeGroups)

	data := pages.TenantDetailData{
		Name:             name,
		Phase:            string(status.Phase),
		K8sVersion:       spec.KubernetesVersion,
		TalosVersion:     spec.TalosVersion,
		CurrentTab:       currentTab(r),
		CanMutate:        h.canMutate(r),
		UpgradeComponent: r.URL.Query().Get("component"),
		UpgradeTarget:    r.URL.Query().Get("version"),
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

	// Machines — real stage from status.
	data.Machines = make([]pages.MachineRow, 0, len(machines))
	for _, m := range machines {
		role := m.Status.Role
		if role == "" {
			role = m.Metadata.Labels["rezuscloud.io/role"]
		}
		data.Machines = append(data.Machines, pages.MachineRow{
			ID:        m.Metadata.Name,
			Stage:     string(m.Status.Stage),
			Connected: m.Spec.Connected,
			Role:      role,
			NodeGroup: m.Metadata.Labels["rezuscloud.io/nodegroup"],
		})
	}

	// Upgrade runs.
	runs, _ := upgrade.GetManager(h.store).ListRuns(name)
	data.UpgradeRuns = make([]pages.UpgradeRunRow, 0, len(runs))
	for _, run := range runs {
		data.UpgradeRuns = append(data.UpgradeRuns, pages.UpgradeRunRow{
			ID:            run.Metadata.Name,
			Component:     run.Spec.Component,
			Target:        run.Spec.Target,
			Phase:         string(run.Status.Phase),
			Completed:     run.Status.Completed,
			TotalMachines: run.Status.TotalMachines,
			StartedAt:     run.Status.StartedAt.Format("2006-01-02 15:04"),
			Error:         run.Status.Error,
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
		tr := ps.TargetRole
		if tr == "" {
			tr = "all"
		}
		data.Patches = append(data.Patches, pages.PatchRow{
			Name:       m.Name,
			Format:     ps.Format,
			TargetRole: tr,
			Enabled:    ps.Enabled,
			UpdatedAt:  m.UpdatedAt.Format("2006-01-02 15:04"),
		})
	}

	// Effective patch preview (for patches tab).
	previewRole := r.URL.Query().Get("role")
	if previewRole == "" {
		previewRole = "controlplane"
	}
	data.PreviewRole = previewRole
	resolved, err := patch.ResolvePatches(h.store, name, previewRole)
	if err != nil {
		data.PatchPreview = "# failed to resolve patches: " + err.Error()
	} else if len(resolved) == 0 {
		data.PatchPreview = "# no patches apply to role \"" + previewRole + "\""
	} else {
		data.PatchPreview = strings.Join(resolved, "\n---\n")
	}

	toast := h.popToast(r)
	h.render(w, r, layout.BaseProps{
		Title:   name,
		Page:    "cluster",
		Content: pages.TenantDetail(data),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Clusters", URL: "/clusters"},
			{Name: name, Current: true},
		},
		Toast: toast,
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

	user, err := h.store.GetUser(username)
	if err != nil || user == nil || !auth.CheckPassword(password, user.Spec.PasswordHash) {
		h.render(w, r, layout.BaseProps{
			Title:   "Sign In",
			Page:    "login",
			Content: pages.Login(pages.LoginData{Error: "Invalid username or password"}),
		})
		return
	}

	token, err := h.jwtManager.GenerateToken(user, auth.DefaultTokenExpiry)
	if err != nil {
		h.render(w, r, layout.BaseProps{
			Title:   "Sign In",
			Page:    "login",
			Content: pages.Login(pages.LoginData{Error: "Authentication temporarily unavailable"}),
		})
		return
	}

	_ = h.store.UpdateUserLastLogin(username)

	http.SetCookie(w, &http.Cookie{
		Name:     "rezuscloud_session",
		Value:    token.AccessToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(auth.DefaultTokenExpiry.Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "rezuscloud_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// EventsStream multiplexes all resource type events into a single SSE stream.
// Used by the dashboard for live updates via HTMX sse extension.
func (h *Handler) EventsStream(w http.ResponseWriter, r *http.Request) {
	if h.bus == nil {
		http.NotFound(w, r)
		return
	}

	// Subscribe to all resource types we care about.
	resourceTypes := []string{"tenant", "machine", "nodegroup", "provider", "jointoken", "configpatch"}
	type subscription struct {
		typ    string
		ch     <-chan watch.Event
		cancel func()
	}
	subs := make([]subscription, 0, len(resourceTypes))
	for _, t := range resourceTypes {
		ch, cancel := h.bus.Subscribe(t)
		subs = append(subs, subscription{typ: t, ch: ch, cancel: cancel})
		defer cancel()
	}

	// Multiplex into one channel.
	multiplex := make(chan watch.Event, len(resourceTypes)*4)
	done := r.Context().Done()
	for _, s := range subs {
		go func(s subscription) {
			for {
				select {
				case <-done:
					return
				case ev, ok := <-s.ch:
					if !ok {
						return
					}
					// Tag the event with the resource type for client routing.
					if obj, ok := ev.Object.(map[string]any); ok {
						obj["type"] = s.typ
					}
					select {
					case multiplex <- ev:
					case <-done:
						return
					}
				}
			}
		}(s)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)
	if canFlush {
		flusher.Flush()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-multiplex:
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := w.Write([]byte("data: ")); err != nil {
				return
			}
			if _, err := w.Write(data); err != nil {
				return
			}
			if _, err := w.Write([]byte("\n\n")); err != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
	}
}

// --- W3: Cluster CRUD handlers ---

// ClusterCreatePage renders the /clusters/create form (initial GET).
func (h *Handler) ClusterCreatePage(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, layout.BaseProps{
		Title:   "Create Cluster",
		Page:    "clusters",
		Content: pages.ClusterCreate(pages.ClusterCreateData{}),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Clusters", URL: "/clusters"},
			{Name: "Create", Current: true},
		},
	})
}

// ClusterCreateSubmit handles POST /clusters/create.
//
// Form fields: name, kubernetesVersion, talosVersion.
// On validation error, re-renders the form with the error inline. The HTMX
// response replaces the form element (hx-target="this" hx-swap="outerHTML").
// On success, sends HX-Redirect response header so the browser navigates to
// the new cluster's detail page.
func (h *Handler) ClusterCreateSubmit(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	data := pages.ClusterCreateData{
		Name:         r.FormValue("name"),
		K8sVersion:   r.FormValue("kubernetesVersion"),
		TalosVersion: r.FormValue("talosVersion"),
	}

	// Validate name.
	if data.Name == "" {
		data.NameError = "Name is required."
	} else if !validClusterName(data.Name) {
		data.NameError = "Name must match ^[a-z][a-z0-9-]{1,62}$ (lowercase, start with a letter, 2–63 chars)."
	}

	// Default versions if not supplied (the dropdown has defaults, but be defensive).
	if data.K8sVersion == "" {
		data.K8sVersion = "1.35.0"
	}
	if data.TalosVersion == "" {
		data.TalosVersion = "1.12.0"
	}

	if data.NameError != "" {
		h.renderClusterCreateForm(w, r, data)
		return
	}

	// Build the API request and submit it server-side.
	spec := state.TenantSpec{
		KubernetesVersion: data.K8sVersion,
		TalosVersion:      data.TalosVersion,
	}

	_, err := h.store.CreateTenant(data.Name, spec, nil, nil)
	if err != nil {
		// Detect duplicate-name errors from the underlying UNIQUE constraint.
		// SQLite reports this as a text containing "UNIQUE constraint failed".
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			data.FormError = "A cluster with this name already exists."
		} else {
			data.FormError = "Failed to create cluster: " + err.Error()
		}
		h.renderClusterCreateForm(w, r, data)
		return
	}

	// Auto-generate secrets bundle so kubeconfig/talosconfig download works
	// immediately on the new cluster's detail page.
	bundle, err := credentials.GenerateSecretsBundle(spec.TalosVersion)
	if err == nil {
		bundleJSON, err := credentials.SecretsBundleJSON(bundle)
		if err == nil {
			_ = h.store.SaveTenantSecrets(data.Name, bundleJSON)
		}
	}

	// HTMX redirect: browser navigates to the new cluster.
	w.Header().Set("HX-Redirect", "/clusters/"+data.Name+"?toast="+url.QueryEscape("Cluster "+data.Name+" created")+"&toast-type=success")
	w.WriteHeader(http.StatusNoContent)
}

// renderClusterCreateForm renders just the form fragment (no Base layout) for
// HTMX partial swap on validation errors.
func (h *Handler) renderClusterCreateForm(w http.ResponseWriter, r *http.Request, data pages.ClusterCreateData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = pages.ClusterCreate(data).Render(r.Context(), w)
}

// validClusterName validates the cluster name against ^[a-z][a-z0-9-]{1,62}$.
func validClusterName(name string) bool {
	if len(name) < 2 || len(name) > 63 {
		return false
	}
	if name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		isLetter := c >= 'a' && c <= 'z'
		isDigit := c >= '0' && c <= '9'
		isHyphen := c == '-'
		if !isLetter && !isDigit && !isHyphen {
			return false
		}
	}
	return true
}

// ClusterDelete handles DELETE /clusters/{name} (HTMX-driven from the delete
// modal in the Settings tab). Deletes the tenant and redirects to /clusters
// with a success toast.
func (h *Handler) ClusterDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.NotFound(w, r)
		return
	}

	// Check role (only admin/edit can delete).
	role := auth.RoleFromContext(r.Context())
	if role != string(auth.RoleAdmin) && role != string(auth.RoleEdit) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	_ = h.store.DeleteTenant(name)
	_ = h.store.RemoveTenantSecrets(name)

	// HTMX redirect: browser navigates to /clusters with a toast.
	w.Header().Set("HX-Redirect", "/clusters?toast="+url.QueryEscape("Cluster "+name+" deleted")+"&toast-type=success")
	w.WriteHeader(http.StatusNoContent)
}

// NodeGroupScale handles POST /clusters/{tenant}/nodegroups/{ng}/scale.
// Updates the node group's count by re-writing its spec via UpdateResource.
// Returns an HTMX redirect to refresh the cluster detail page.
func (h *Handler) NodeGroupScale(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("name")
	ngName := r.PathValue("ng")
	if tenant == "" || ngName == "" {
		http.NotFound(w, r)
		return
	}

	// Check role.
	role := auth.RoleFromContext(r.Context())
	if role != string(auth.RoleAdmin) && role != string(auth.RoleEdit) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	_ = r.ParseForm()
	countStr := r.FormValue("count")
	count, err := strconv.Atoi(countStr)
	if err != nil || count < 0 || count > 100 {
		http.Error(w, "Invalid count", http.StatusBadRequest)
		return
	}

	// Load existing nodegroup spec, update count, save back.
	resourceKey := "nodegroup:" + tenant + ":" + ngName
	var existing struct {
		Name  string `json:"name"`
		Role  string `json:"role"`
		Count int    `json:"count"`
	}
	meta, err := h.store.GetResource("nodegroup", ngName, &existing, nil)
	if err != nil || meta.Name == "" {
		http.NotFound(w, r)
		return
	}
	// Verify the node group belongs to this tenant.
	if meta.Labels["rezuscloud.io/tenant"] != tenant {
		http.NotFound(w, r)
		return
	}

	existing.Count = count
	_, err = h.store.UpdateResource("nodegroup", ngName, meta.ResourceVersion, existing, meta.Labels, meta.Annotations)
	if err != nil {
		http.Error(w, "Failed to scale node group: "+err.Error(), http.StatusInternalServerError)
		return
	}

	_ = resourceKey // kept for log/debug; no current use.
	w.Header().Set("HX-Redirect", "/clusters/"+tenant+"?toast="+url.QueryEscape("Node group "+ngName+" scaled to "+countStr)+"&toast-type=success")
	w.WriteHeader(http.StatusNoContent)
}

// ClusterKubeconfig proxies GET /clusters/{name}/kubeconfig to the API.
// Returns the YAML file as an attachment (Content-Disposition).
func (h *Handler) ClusterKubeconfig(w http.ResponseWriter, r *http.Request) {
	h.credentialDownload(w, r, "kubeconfig")
}

// ClusterTalosconfig proxies GET /clusters/{name}/talosconfig to the API.
// Returns the YAML file as an attachment.
func (h *Handler) ClusterTalosconfig(w http.ResponseWriter, r *http.Request) {
	h.credentialDownload(w, r, "talosconfig")
}

func (h *Handler) ClusterUpgradeStart(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		h.redirectAction(w, r, "/clusters/"+name+"/upgrade?toast="+url.QueryEscape("Invalid form")+"&toast-type=error")
		return
	}
	component := strings.TrimSpace(r.FormValue("component"))
	version := strings.TrimSpace(r.FormValue("version"))
	user := auth.UserFromContext(r.Context())
	if user == "" {
		user = "web"
	}
	_, err := upgrade.GetManager(h.store).StartRun(name, component, version, user)
	if err != nil {
		h.redirectAction(w, r, "/clusters/"+name+"/upgrade?component="+url.QueryEscape(component)+"&version="+url.QueryEscape(version)+"&toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.redirectAction(w, r, "/clusters/"+name+"/upgrade?toast="+url.QueryEscape("Upgrade run started")+"&toast-type=success")
}

func (h *Handler) ClusterUpgradeCancel(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := r.PathValue("name")
	runID := r.PathValue("id")
	if err := upgrade.GetManager(h.store).CancelRun(runID); err != nil {
		h.redirectAction(w, r, "/clusters/"+name+"/upgrade?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.redirectAction(w, r, "/clusters/"+name+"/upgrade?toast="+url.QueryEscape("Upgrade canceled")+"&toast-type=success")
}

func (h *Handler) redirectAction(w http.ResponseWriter, r *http.Request, target string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (h *Handler) backupService() (*backup.Service, error) {
	root := os.Getenv("REZUSCLOUD_BACKUP_DIR")
	if root == "" {
		root = filepath.Join(os.TempDir(), "rezuscloud-backups")
	}
	fs, err := backup.NewFileStore(root)
	if err != nil {
		return nil, err
	}
	mgr := backup.NewManager(fs, backup.Config{Prefix: "backups"})
	return backup.NewService(mgr, h.store), nil
}

func (h *Handler) BackupsPage(w http.ResponseWriter, r *http.Request) {
	svc, err := h.backupService()
	if err != nil {
		http.Error(w, "backup service unavailable", http.StatusServiceUnavailable)
		return
	}
	snapshots, _ := svc.ListSnapshots()
	policy, _ := svc.GetPolicy()

	lastSuccess := "never"
	failed := 0
	if len(snapshots) > 0 {
		for _, snap := range snapshots {
			if snap.Status.Status == "success" && lastSuccess == "never" {
				lastSuccess = snap.CreatedAt
			}
			if snap.Status.Status == "failed" {
				failed++
			}
		}
	}
	data := pages.BackupsPageData{
		Snapshots:   snapshots,
		Retention:   policy.Retention,
		LastSuccess: lastSuccess,
		Failures:    failed,
		RPOEstimate: rpoEstimate(lastSuccess),
		CanMutate:   h.canMutate(r),
	}
	toast := h.popToast(r)
	h.render(w, r, layout.BaseProps{
		Title:   "Backups",
		Page:    "settings-backups",
		Content: pages.BackupsPage(data),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Settings", URL: "/settings"},
			{Name: "Backups", Current: true},
		},
		Toast: toast,
	})
}

func (h *Handler) BackupsRunDatabase(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	svc, err := h.backupService()
	if err != nil {
		h.redirectAction(w, r, "/settings/backups?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	if _, err := svc.TriggerDatabase(r.Context()); err != nil {
		h.redirectAction(w, r, "/settings/backups?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.redirectAction(w, r, "/settings/backups?toast=database+backup+created&toast-type=success")
}

func (h *Handler) BackupsRunResources(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	svc, err := h.backupService()
	if err != nil {
		h.redirectAction(w, r, "/settings/backups?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	if _, err := svc.TriggerResources(r.Context()); err != nil {
		h.redirectAction(w, r, "/settings/backups?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.redirectAction(w, r, "/settings/backups?toast=resources+backup+created&toast-type=success")
}

func (h *Handler) BackupsRestore(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.redirectAction(w, r, "/settings/backups?toast=invalid+restore+request&toast-type=error")
		return
	}
	snapshotID := strings.TrimSpace(r.FormValue("snapshotID"))
	dryRun := r.FormValue("dryRun") == "true"
	svc, err := h.backupService()
	if err != nil {
		h.redirectAction(w, r, "/settings/backups?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	result, err := svc.Restore(r.Context(), snapshotID, dryRun)
	if err != nil {
		h.redirectAction(w, r, "/settings/backups?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	msg := "restore applied"
	if dryRun {
		msg = "restore dry-run: " + strconv.Itoa(result.ResourcesSeen) + " resources"
	}
	h.redirectAction(w, r, "/settings/backups?toast="+url.QueryEscape(msg)+"&toast-type=success")
}

func (h *Handler) BackupsPolicySave(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.redirectAction(w, r, "/settings/backups?toast=invalid+policy+form&toast-type=error")
		return
	}
	retentionStr := strings.TrimSpace(r.FormValue("retention"))
	retention, err := strconv.Atoi(retentionStr)
	if err != nil || retention <= 0 {
		h.redirectAction(w, r, "/settings/backups?toast=retention+must+be+positive&toast-type=error")
		return
	}
	svc, err := h.backupService()
	if err != nil {
		h.redirectAction(w, r, "/settings/backups?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	if err := svc.UpdatePolicy(backup.Policy{Retention: retention}); err != nil {
		h.redirectAction(w, r, "/settings/backups?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.redirectAction(w, r, "/settings/backups?toast=retention+updated&toast-type=success")
}

func rpoEstimate(lastSuccess string) string {
	if lastSuccess == "" || lastSuccess == "never" {
		return "unknown"
	}
	t, err := time.Parse(time.RFC3339, lastSuccess)
	if err != nil {
		return "unknown"
	}
	d := time.Since(t)
	if d < time.Minute {
		return "<1m"
	}
	if d < time.Hour {
		return strconv.Itoa(int(d.Minutes())) + "m"
	}
	return strconv.Itoa(int(d.Hours())) + "h"
}

// credentialDownload is the shared implementation for kubeconfig/talosconfig.
// kind is "kubeconfig" or "talosconfig".
func (h *Handler) credentialDownload(w http.ResponseWriter, r *http.Request, kind string) {
	name := r.PathValue("name")
	if name == "" {
		http.NotFound(w, r)
		return
	}

	tenant, err := h.store.GetTenant(name)
	if err != nil || tenant == nil {
		http.NotFound(w, r)
		return
	}

	bundleJSON, err := h.store.LoadTenantSecrets(name)
	if err != nil || bundleJSON == nil {
		http.NotFound(w, r)
		return
	}

	bundle, err := credentials.UnmarshalSecretsBundle(bundleJSON)
	if err != nil || bundle == nil {
		http.Error(w, "Failed to load secrets", http.StatusInternalServerError)
		return
	}

	var data []byte
	switch kind {
	case "kubeconfig":
		data, err = credentials.GenerateKubeconfig(credentials.KubeconfigRequest{
			ClusterName:     name,
			ClusterEndpoint: tenant.Spec.ControlPlaneEndpoint,
			Bundle:          bundle,
		})
	case "talosconfig":
		data, err = credentials.GenerateTalosconfig(credentials.TalosconfigRequest{
			ClusterName:     name,
			MachineLinkAddr: tenant.Spec.ControlPlaneEndpoint,
			Bundle:          bundle,
		})
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Failed to generate "+kind+": "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+"-"+kind+".yaml"))
	_, _ = w.Write(data)
}

// --- W6: ConfigPatch management ---

func (h *Handler) ClusterPatchCreate(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	patchName := strings.TrimSpace(r.FormValue("name"))
	format := strings.TrimSpace(r.FormValue("format"))
	targetRole := strings.TrimSpace(r.FormValue("targetRole"))
	enabled := r.FormValue("enabled") == "true"
	body := r.FormValue("patch")
	if targetRole == "all" {
		targetRole = ""
	}
	if err := validatePatchInput(format, targetRole, body); err != nil {
		http.Redirect(w, r, "/clusters/"+name+"/patches?toast="+url.QueryEscape(err.Error())+"&toast-type=error", http.StatusSeeOther)
		return
	}
	spec := patch.PatchSpec{Patch: body, Format: format, TargetRole: targetRole, Enabled: enabled}
	labels := map[string]string{"rezuscloud.io/tenant": name}
	if targetRole != "" {
		labels["rezuscloud.io/role"] = targetRole
	}
	if _, err := h.store.CreateResource("configpatch", patchName, spec, nil, labels, nil); err != nil {
		http.Redirect(w, r, "/clusters/"+name+"/patches?toast="+url.QueryEscape("create failed: "+err.Error())+"&toast-type=error", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/clusters/"+name+"/patches?toast=patch+created&toast-type=success", http.StatusSeeOther)
}

func (h *Handler) ClusterPatchEditPage(w http.ResponseWriter, r *http.Request) {
	cluster := r.PathValue("name")
	patchName := r.PathValue("patch")
	var spec patch.PatchSpec
	md, err := h.store.GetResource("configpatch", patchName, &spec, nil)
	if err != nil || md.Name == "" || md.Labels["rezuscloud.io/tenant"] != cluster {
		http.NotFound(w, r)
		return
	}
	tr := spec.TargetRole
	if tr == "" {
		tr = "all"
	}
	h.render(w, r, layout.BaseProps{
		Title: "Patch " + patchName,
		Page:  "cluster",
		Content: pages.PatchEdit(pages.PatchEditData{
			Cluster:    cluster,
			Name:       patchName,
			Format:     spec.Format,
			TargetRole: tr,
			Enabled:    spec.Enabled,
			Patch:      spec.Patch,
			CanMutate:  h.canMutate(r),
		}),
		Breadcrumb: []layout.BreadcrumbItem{{Name: "Clusters", URL: "/clusters"}, {Name: cluster, URL: "/clusters/" + cluster + "/patches"}, {Name: patchName, Current: true}},
		Toast:      h.popToast(r),
	})
}

func (h *Handler) ClusterPatchSave(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	cluster := r.PathValue("name")
	patchName := r.PathValue("patch")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	format := strings.TrimSpace(r.FormValue("format"))
	targetRole := strings.TrimSpace(r.FormValue("targetRole"))
	if targetRole == "all" {
		targetRole = ""
	}
	enabled := r.FormValue("enabled") == "true"
	body := r.FormValue("patch")
	if err := validatePatchInput(format, targetRole, body); err != nil {
		http.Redirect(w, r, "/clusters/"+cluster+"/patches/"+patchName+"?toast="+url.QueryEscape(err.Error())+"&toast-type=error", http.StatusSeeOther)
		return
	}
	var old patch.PatchSpec
	md, err := h.store.GetResource("configpatch", patchName, &old, nil)
	if err != nil || md.Name == "" || md.Labels["rezuscloud.io/tenant"] != cluster {
		http.NotFound(w, r)
		return
	}
	newSpec := patch.PatchSpec{Patch: body, Format: format, TargetRole: targetRole, Enabled: enabled}
	labels := md.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	labels["rezuscloud.io/tenant"] = cluster
	if targetRole != "" {
		labels["rezuscloud.io/role"] = targetRole
	} else {
		delete(labels, "rezuscloud.io/role")
	}
	if _, err := h.store.UpdateResource("configpatch", patchName, md.ResourceVersion, newSpec, labels, md.Annotations); err != nil {
		http.Redirect(w, r, "/clusters/"+cluster+"/patches/"+patchName+"?toast="+url.QueryEscape("save failed: "+err.Error())+"&toast-type=error", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/clusters/"+cluster+"/patches/"+patchName+"?toast=patch+saved&toast-type=success", http.StatusSeeOther)
}

func (h *Handler) ClusterPatchDelete(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	cluster := r.PathValue("name")
	patchName := r.PathValue("patch")
	var spec patch.PatchSpec
	md, err := h.store.GetResource("configpatch", patchName, &spec, nil)
	if err != nil || md.Name == "" || md.Labels["rezuscloud.io/tenant"] != cluster {
		http.NotFound(w, r)
		return
	}
	if err := h.store.RemoveResource("configpatch", patchName); err != nil {
		http.Redirect(w, r, "/clusters/"+cluster+"/patches/"+patchName+"?toast="+url.QueryEscape("delete failed: "+err.Error())+"&toast-type=error", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/clusters/"+cluster+"/patches?toast=patch+deleted&toast-type=success", http.StatusSeeOther)
}

func (h *Handler) ClusterPatchToggle(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	cluster := r.PathValue("name")
	patchName := r.PathValue("patch")
	var spec patch.PatchSpec
	md, err := h.store.GetResource("configpatch", patchName, &spec, nil)
	if err != nil || md.Name == "" || md.Labels["rezuscloud.io/tenant"] != cluster {
		http.NotFound(w, r)
		return
	}
	spec.Enabled = !spec.Enabled
	if _, err := h.store.UpdateResource("configpatch", patchName, md.ResourceVersion, spec, md.Labels, md.Annotations); err != nil {
		http.Redirect(w, r, "/clusters/"+cluster+"/patches/"+patchName+"?toast="+url.QueryEscape("toggle failed: "+err.Error())+"&toast-type=error", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/clusters/"+cluster+"/patches/"+patchName+"?toast=toggled&toast-type=success", http.StatusSeeOther)
}

func (h *Handler) ClusterPatchesPreview(w http.ResponseWriter, r *http.Request) {
	cluster := r.PathValue("name")
	role := r.URL.Query().Get("role")
	if role == "" {
		role = "controlplane"
	}
	resolved, err := patch.ResolvePatches(h.store, cluster, role)
	if err != nil {
		http.Error(w, "resolve patches: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if len(resolved) == 0 {
		_, _ = w.Write([]byte("# no patches apply"))
		return
	}
	_, _ = w.Write([]byte(strings.Join(resolved, "\n---\n")))
}

func validatePatchInput(format, targetRole, body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("patch body must not be empty")
	}
	validFormats := map[string]bool{"": true, "strategic": true, "json6902": true, "text": true}
	if !validFormats[format] {
		return fmt.Errorf("format must be strategic, json6902, or text")
	}
	validTargets := map[string]bool{"": true, "all": true, "controlplane": true, "worker": true, "kernel": true}
	if !validTargets[targetRole] {
		return fmt.Errorf("target role must be all, controlplane, worker, kernel")
	}
	switch format {
	case "", "strategic":
		var y any
		if err := yaml.Unmarshal([]byte(body), &y); err != nil {
			return fmt.Errorf("invalid YAML: %v", err)
		}
	case "json6902":
		var ops []map[string]any
		if err := json.Unmarshal([]byte(body), &ops); err != nil {
			return fmt.Errorf("invalid JSON patch: %v", err)
		}
		if len(ops) == 0 {
			return fmt.Errorf("json6902 patch must contain operations")
		}
	}
	return nil
}

// --- W4: Machines & Join Tokens ---

// MachinesList handles GET /machines.
func (h *Handler) MachinesList(w http.ResponseWriter, r *http.Request) {
	cluster := strings.TrimSpace(r.URL.Query().Get("cluster"))
	stage := strings.TrimSpace(r.URL.Query().Get("stage"))
	connectedOnly := r.URL.Query().Get("connected") == "true"

	machines, _, err := h.store.ListMachines()
	if err != nil {
		http.Error(w, "Failed to list machines: "+err.Error(), http.StatusInternalServerError)
		return
	}

	rows := make([]pages.MachineFleetRow, 0, len(machines))
	for _, m := range machines {
		if cluster != "" && m.Metadata.Labels["rezuscloud.io/tenant"] != cluster {
			continue
		}
		if stage != "" && string(m.Status.Stage) != stage {
			continue
		}
		if connectedOnly && !m.Spec.Connected {
			continue
		}
		rows = append(rows, machineFleetRow(m))
	}

	h.render(w, r, layout.BaseProps{
		Title: "Machines",
		Page:  "machines",
		Content: pages.MachinesList(pages.MachinesListData{
			Machines:        rows,
			FilterCluster:   cluster,
			FilterStage:     stage,
			FilterConnected: connectedOnly,
			ClusterNames:    h.clusterNames(),
			Stages:          machineStages,
			LiveStream:      h.bus != nil,
		}),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Machines", Current: true},
		},
	})
}

// MachinesPending handles GET /machines/pending.
// Shows machines that are not yet ready (initializing, installing, configuring).
func (h *Handler) MachinesPending(w http.ResponseWriter, r *http.Request) {
	machines, _, err := h.store.ListMachines()
	if err != nil {
		http.Error(w, "Failed to list machines: "+err.Error(), http.StatusInternalServerError)
		return
	}

	pendingStages := map[state.MachineStage]bool{
		state.StageInitializing: true,
		state.StageInstalling:   true,
		state.StageConfiguring:  true,
	}

	rows := make([]pages.MachineFleetRow, 0)
	for _, m := range machines {
		if pendingStages[m.Status.Stage] {
			rows = append(rows, machineFleetRow(m))
		}
	}

	h.render(w, r, layout.BaseProps{
		Title: "Pending Machines",
		Page:  "machines",
		Content: pages.MachinesList(pages.MachinesListData{
			Machines:     rows,
			ClusterNames: h.clusterNames(),
			Stages:       machineStages,
			LiveStream:   h.bus != nil,
		}),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Machines", URL: "/machines"},
			{Name: "Pending", Current: true},
		},
	})
}

// MachineDetail handles GET /machines/{id}.
func (h *Handler) MachineDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}

	m, err := h.store.GetMachine(id)
	if err != nil {
		http.Error(w, "Failed to load machine: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if m == nil {
		http.NotFound(w, r)
		return
	}

	data := pages.MachineDetailData{
		ID:         m.Metadata.Name,
		Cluster:    m.Metadata.Labels["rezuscloud.io/tenant"],
		Role:       m.Status.Role,
		Stage:      string(m.Status.Stage),
		Connected:  m.Spec.Connected,
		NodeGroup:  m.Metadata.Labels["rezuscloud.io/node-group"],
		LastSeen:   formatAge(m.Metadata.UpdatedAt),
		Talos:      m.Status.TalosVersion,
		Kubernetes: m.Status.K8sVersion,
		Schematic:  schematicID(m.Status.Schematic),
		CanMutate:  h.canMutate(r),
	}
	if m.Status.Hardware != nil {
		data.Hardware = &pages.HardwareView{
			Arch:      m.Status.Hardware.Arch,
			CPU:       hardwareCPU(m.Status.Hardware),
			MemoryMB:  hardwareMemoryMB(m.Status.Hardware),
			DiskCount: len(m.Status.Hardware.BlockDevices),
			DiskTotal: hardwareDiskTotal(m.Status.Hardware),
		}
	}
	if m.Status.Network != nil {
		data.Network = &pages.NetworkView{
			Hostname:  m.Status.Network.Hostname,
			Addresses: m.Status.Network.Addresses,
		}
	}

	h.render(w, r, layout.BaseProps{
		Title:   "Machine " + shortDisplayID(id),
		Page:    "machine",
		Content: pages.MachineDetail(data),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Machines", URL: "/machines"},
			{Name: shortDisplayID(id), Current: true},
		},
	})
}

// MachineLogs handles GET /machines/{id}/logs.
func (h *Handler) MachineLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}

	m, err := h.store.GetMachine(id)
	if err != nil {
		http.Error(w, "Failed to load machine: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if m == nil {
		http.NotFound(w, r)
		return
	}

	cluster := m.Metadata.Labels["rezuscloud.io/tenant"]

	sseURL := ""
	if cluster != "" {
		sseURL = "/api/v1/tenants/" + cluster + "/machines/" + id + "/logs?follow=true"
	}

	data := pages.MachineLogsData{
		MachineID:       id,
		Cluster:         cluster,
		Lines:           h.recentLogs(id),
		DownloadURL:     "/api/v1/tenants/" + cluster + "/machines/" + id + "/logs?tail=1000",
		SSEURL:          sseURL,
		FallbackPollURL: "/machines/" + id + "/logs/poll",
		PollInterval:    "5s",
	}

	// If requested via HTMX (polling), return just the inner lines.
	if r.Header.Get("HX-Request") == "true" && r.URL.Query().Get("partial") == "1" {
		logPartial(w, data.Lines)
		return
	}

	h.render(w, r, layout.BaseProps{
		Title:   "Logs — " + shortDisplayID(id),
		Page:    "machine",
		Content: pages.MachineLogs(data),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Machines", URL: "/machines"},
			{Name: shortDisplayID(id), URL: "/machines/" + id},
			{Name: "Logs", Current: true},
		},
	})
}

// MachineLogsPoll handles GET /machines/{id}/logs/poll (HTMX partial).
func (h *Handler) MachineLogsPoll(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	logPartial(w, h.recentLogs(id))
}

// logPartial writes just the log lines div (no layout) for HTMX swaps.
func logPartial(w http.ResponseWriter, lines []pages.LogLine) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for _, line := range lines {
		fmt.Fprintf(w, `<div class="ds-logs-line">`+
			`<span class="ds-logs-time">%s</span>`,
			line.Timestamp)
		if line.Level != "" {
			fmt.Fprintf(w, `<span class="ds-logs-level ds-logs-level--%s">[%s]</span>`, line.Level, line.Level)
		}
		if line.Source != "" {
			fmt.Fprintf(w, `<span class="ds-logs-source">%s</span>`, line.Source)
		}
		fmt.Fprintf(w, `<span class="ds-logs-msg">%s</span></div>`+"\n", line.Message)
	}
}

// recentLogs returns the most recent log lines for a machine.
// For v1: returns synthetic log entries when the store has no real logs.
// TODO(W7+): wire to the real log provider (machine link).
func (h *Handler) recentLogs(machineID string) []pages.LogLine {
	return []pages.LogLine{
		{
			Timestamp: time.Now().UTC().Format("15:04:05"),
			Message:   fmt.Sprintf("Log streaming is stubbed for machine %s. Real implementation in W7+.", machineID),
			Level:     "info",
			Source:    "rezuscloud",
		},
	}
}

// MachineConfig handles GET /machines/{id}/config.
func (h *Handler) MachineConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}

	m, err := h.store.GetMachine(id)
	if err != nil {
		http.Error(w, "Failed to load machine: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if m == nil {
		http.NotFound(w, r)
		return
	}

	cluster := m.Metadata.Labels["rezuscloud.io/tenant"]

	// Generate the config by calling the API endpoint logic directly.
	config, err := h.generateMachineConfig(cluster, id, m)
	if err != nil {
		http.Error(w, "Failed to generate config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.render(w, r, layout.BaseProps{
		Title: "Config — " + shortDisplayID(id),
		Page:  "machine",
		Content: pages.MachineConfig(pages.MachineConfigData{
			MachineID:   id,
			Cluster:     cluster,
			ConfigYAML:  config,
			DownloadURL: "/api/v1/tenants/" + cluster + "/machines/" + id + "/config?download=true",
		}),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Machines", URL: "/machines"},
			{Name: shortDisplayID(id), URL: "/machines/" + id},
			{Name: "Config", Current: true},
		},
	})
}

// MachineKernelArgs handles GET /machines/{id}/kernel-args.
func (h *Handler) MachineKernelArgs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}

	m, err := h.store.GetMachine(id)
	if err != nil {
		http.Error(w, "Failed to load machine: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if m == nil {
		http.NotFound(w, r)
		return
	}

	cluster := m.Metadata.Labels["rezuscloud.io/tenant"]

	// Look up an existing kernel-args patch for this cluster.
	existing, existingName := h.findKernelArgsPatch(cluster)

	h.render(w, r, layout.BaseProps{
		Title: "Kernel args — " + shortDisplayID(id),
		Page:  "machine",
		Content: pages.KernelArgs(pages.KernelArgsData{
			MachineID:         id,
			Cluster:           cluster,
			Existing:          existing,
			ExistingPatchName: existingName,
			FormValue:         existing,
			CanMutate:         h.canMutate(r),
		}),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Machines", URL: "/machines"},
			{Name: shortDisplayID(id), URL: "/machines/" + id},
			{Name: "Kernel args", Current: true},
		},
	})
}

// MachineKernelArgsSave handles POST /machines/{id}/kernel-args.
func (h *Handler) MachineKernelArgsSave(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	m, _ := h.store.GetMachine(id)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	cluster := m.Metadata.Labels["rezuscloud.io/tenant"]
	if cluster == "" {
		http.Error(w, "machine has no cluster assignment", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	raw := strings.TrimSpace(r.FormValue("args"))
	if raw == "" {
		http.Redirect(w, r, "/machines/"+id+"/kernel-args?toast=no+args+provided&toast-type=error", http.StatusSeeOther)
		return
	}

	// Validate: split on newlines, each line must be non-empty, no whitespace,
	// and start with one of the allowed prefixes.
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.ContainsAny(line, " \t") {
			http.Redirect(w, r, "/machines/"+id+"/kernel-args?toast=whitespace+not+allowed+in+args&toast-type=error", http.StatusSeeOther)
			return
		}
		if !isValidKernelArg(line) {
			http.Redirect(w, r, "/machines/"+id+"/kernel-args?toast=disallowed+kernel+arg+prefix:+"+url.QueryEscape(line)+"&toast-type=error", http.StatusSeeOther)
			return
		}
	}

	// Build the patch YAML: each arg becomes a YAML list item.
	patchYAML := buildKernelArgsPatch(lines)

	// Check if a kernel-args patch already exists for this cluster.
	existing, existingName := h.findKernelArgsPatch(cluster)
	_ = existing

	if existingName != "" {
		// Update the existing patch.
		var ps patch.PatchSpec
		md, err := h.store.GetResource("configpatch", existingName, &ps, nil)
		if err != nil {
			http.Error(w, "load existing patch: "+err.Error(), http.StatusInternalServerError)
			return
		}
		ps.Patch = patchYAML
		if _, err := h.store.UpdateResource("configpatch", existingName, md.ResourceVersion, ps, nil, nil); err != nil {
			http.Error(w, "update patch: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		// Create a new patch.
		name := "kernel-args-" + cluster
		ps := patch.PatchSpec{
			Patch:      patchYAML,
			Format:     "strategic",
			TargetRole: "",
			Enabled:    true,
		}
		labels := map[string]string{
			"rezuscloud.io/tenant": cluster,
			"rezuscloud.io/kind":   "kernel-args",
		}
		if _, err := h.store.CreateResource("configpatch", name, ps, nil, labels, nil); err != nil {
			http.Error(w, "create patch: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	http.Redirect(w, r, "/machines/"+id+"/kernel-args?toast=kernel+args+saved&toast-type=success", http.StatusSeeOther)
}

// generateMachineConfig produces the Talos machine config YAML for display.
func (h *Handler) generateMachineConfig(tenantName, machineID string, m *state.Machine) (string, error) {
	tenant, err := h.store.GetTenant(tenantName)
	if err != nil {
		return "", err
	}
	if tenant == nil {
		return "", fmt.Errorf("tenant %q not found", tenantName)
	}

	bundleJSON, err := h.store.LoadTenantSecrets(tenantName)
	if err != nil {
		return "", err
	}
	if bundleJSON == nil {
		return "", fmt.Errorf("no secrets bundle for tenant")
	}

	machineType := talosconfig.DetermineMachineType(m.Status.Role, false)
	patches, err := patch.ResolvePatches(h.store, tenantName, m.Status.Role)
	if err != nil {
		return "", err
	}

	result, err := talosconfig.GenerateConfig(talosconfig.ConfigRequest{
		ClusterName:       tenantName,
		ClusterEndpoint:   tenant.Spec.ControlPlaneEndpoint,
		KubernetesVersion: tenant.Spec.KubernetesVersion,
		TalosVersion:      tenant.Spec.TalosVersion,
		MachineType:       machineType,
		SecretsBundle:     bundleJSON,
		ConfigPatches:     patches,
		MachineID:         machineID,
	})
	if err != nil {
		return "", err
	}
	return result.MachineConfig, nil
}

// findKernelArgsPatch returns the existing kernel-args patch for the cluster,
// or empty strings if none exists.
func (h *Handler) findKernelArgsPatch(tenantName string) (string, string) {
	opts := state.ListOptions{
		LabelSelector: "rezuscloud.io/tenant=" + tenantName,
	}
	metas, specs, _, _, err := h.store.ListResources("configpatch", opts)
	if err != nil {
		return "", ""
	}
	for i, md := range metas {
		if md.Labels["rezuscloud.io/kind"] != "kernel-args" {
			continue
		}
		var ps patch.PatchSpec
		_ = json.Unmarshal(specs[i], &ps)
		return ps.Patch, md.Name
	}
	return "", ""
}

// isValidKernelArg checks if a kernel arg starts with an allowed prefix.
func isValidKernelArg(arg string) bool {
	allowed := []string{"talos.", "siderolink.", "console=", "reboot=", "mitigations=", "ip="}
	for _, p := range allowed {
		if strings.HasPrefix(arg, p) {
			return true
		}
	}
	return false
}

// buildKernelArgsPatch produces the strategic-merge YAML that injects the
// given kernel args into the Talos machine.install.extraKernelArgs field.
func buildKernelArgsPatch(args []string) string {
	var b strings.Builder
	b.WriteString("machine:\n  install:\n    extraKernelArgs:\n")
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		b.WriteString("      - ")
		b.WriteString(a)
		b.WriteString("\n")
	}
	return b.String()
}

// MachineRestart handles POST /machines/{id}/restart.
// Stub — real implementation will come in W7+ when machine actions are wired.
func (h *Handler) MachineRestart(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if _, err := h.store.GetMachine(id); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if m, _ := h.store.GetMachine(id); m == nil {
		http.NotFound(w, r)
		return
	}
	// TODO(W7): issue restart via machine link.
	http.Redirect(w, r, "/machines/"+id+"?toast=restart+queued&toast-type=success", http.StatusSeeOther)
}

// MachineShutdown handles POST /machines/{id}/shutdown.
func (h *Handler) MachineShutdown(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if m, _ := h.store.GetMachine(id); m == nil {
		http.NotFound(w, r)
		return
	}
	// TODO(W7): issue shutdown via machine link.
	http.Redirect(w, r, "/machines/"+id+"?toast=shutdown+queued&toast-type=success", http.StatusSeeOther)
}

// MachineApprove handles POST /machines/{id}/approve.
// For now, approving a machine is a no-op (machines auto-join via token).
func (h *Handler) MachineApprove(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if m, _ := h.store.GetMachine(id); m == nil {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/machines/"+id+"?toast=approved&toast-type=success", http.StatusSeeOther)
}

// MachineDelete handles DELETE /machines/{id}.
func (h *Handler) MachineDelete(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if m, _ := h.store.GetMachine(id); m == nil {
		http.NotFound(w, r)
		return
	}
	if err := h.store.DeleteMachine(id); err != nil {
		http.Error(w, "Failed to delete machine: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/machines?toast=machine+removed&toast-type=success", http.StatusSeeOther)
}

// JoinTokensList handles GET /machines/jointokens.
func (h *Handler) JoinTokensList(w http.ResponseWriter, r *http.Request) {
	tokens, _, err := h.store.ListJoinTokens()
	if err != nil {
		http.Error(w, "Failed to list tokens: "+err.Error(), http.StatusInternalServerError)
		return
	}

	rows := make([]pages.JoinTokenRow, 0, len(tokens))
	for _, jt := range tokens {
		rows = append(rows, joinTokenRow(jt))
	}

	data := pages.JoinTokensListData{
		Tokens:       rows,
		ClusterNames: h.clusterNames(),
		CanMutate:    h.canMutate(r),
	}

	// Flash a previously-created token (via query param — set by JoinTokenCreate on redirect).
	if tok := r.URL.Query().Get("new_token"); tok != "" {
		jt, _ := h.store.GetJoinToken(tok)
		if jt != nil {
			data.NewToken = tok
			data.NewTokenExp = jt.Spec.ExpiresAt
			data.NewTokenCluster = jt.Metadata.Labels["rezuscloud.io/tenant"]
			data.NewTokenArgs = kernelArgsPreview(tok, h.machineLinkEndpoint())
		}
	}

	h.render(w, r, layout.BaseProps{
		Title:   "Join Tokens",
		Page:    "jointokens",
		Content: pages.JoinTokensList(data),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Machines", URL: "/machines"},
			{Name: "Join Tokens", Current: true},
		},
	})
}

// JoinTokenCreate handles POST /machines/jointokens.
func (h *Handler) JoinTokenCreate(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form: "+err.Error(), http.StatusBadRequest)
		return
	}

	cluster := strings.TrimSpace(r.FormValue("cluster"))
	nodeGroup := strings.TrimSpace(r.FormValue("nodegroup"))
	ttlStr := strings.TrimSpace(r.FormValue("ttl"))
	singleUse := r.FormValue("single_use") == "true"

	if cluster == "" || nodeGroup == "" {
		http.Error(w, "cluster and nodegroup are required", http.StatusBadRequest)
		return
	}

	// Verify cluster exists.
	t, _ := h.store.GetTenant(cluster)
	if t == nil {
		http.Error(w, "cluster not found", http.StatusNotFound)
		return
	}

	// Parse TTL.
	ttl := 24 * time.Hour
	if ttlStr != "" && ttlStr != "0" {
		if parsed, err := time.ParseDuration(ttlStr); err == nil {
			ttl = parsed
		}
	}

	// Generate token + spec.
	token, err := generateJoinTokenValue()
	if err != nil {
		http.Error(w, "token generation failed", http.StatusInternalServerError)
		return
	}

	spec := state.JoinTokenSpec{
		SingleUse: singleUse,
		NodeGroup: nodeGroup,
	}
	if ttl > 0 {
		spec.ExpiresAt = time.Now().UTC().Add(ttl)
	}

	if _, err := h.store.CreateJoinToken(token, spec, cluster, nodeGroup); err != nil {
		http.Error(w, "failed to create token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect to list with the new token highlighted.
	http.Redirect(w, r, "/machines/jointokens?new_token="+url.QueryEscape(token), http.StatusSeeOther)
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

// machineLinkEndpoint returns the configured endpoint string for kernel args preview.
// TODO: make this configurable. For now, returns a placeholder.
func (h *Handler) machineLinkEndpoint() string {
	return "machinelink.rezus.cloud:50001"
}

// machineFleetRow converts a Machine to a fleet-table row.
func machineFleetRow(m *state.Machine) pages.MachineFleetRow {
	return pages.MachineFleetRow{
		ID:        m.Metadata.Name,
		Cluster:   m.Metadata.Labels["rezuscloud.io/tenant"],
		Role:      m.Status.Role,
		Stage:     string(m.Status.Stage),
		Connected: m.Spec.Connected,
		NodeGroup: m.Metadata.Labels["rezuscloud.io/node-group"],
		LastSeen:  formatAge(m.Metadata.UpdatedAt),
	}
}

// joinTokenRow converts a JoinToken to a list-table row.
func joinTokenRow(jt *state.JoinToken) pages.JoinTokenRow {
	status := "active"
	if jt.Status.Used {
		status = "used"
	} else if !jt.Spec.ExpiresAt.IsZero() && time.Now().UTC().After(jt.Spec.ExpiresAt) {
		status = "expired"
	}

	expires := "never"
	if !jt.Spec.ExpiresAt.IsZero() {
		expires = jt.Spec.ExpiresAt.Format("2006-01-02 15:04 MST")
	}

	return pages.JoinTokenRow{
		Token:     shortDisplayID(jt.Metadata.Name),
		Cluster:   jt.Metadata.Labels["rezuscloud.io/tenant"],
		NodeGroup: jt.Spec.NodeGroup,
		Status:    status,
		ExpiresAt: expires,
		CreatedAt: jt.Metadata.CreatedAt.Format("2006-01-02 15:04 MST"),
	}
}

// formatAge renders a human-readable "X ago" string from a timestamp.
func formatAge(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

// shortDisplayID returns the first 8 chars of a machine ID for display.
func shortDisplayID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// schematicID safely extracts the schematic ID.
func schematicID(s *state.SchematicInfo) string {
	if s == nil {
		return ""
	}
	return s.ID
}

// hardwareCPU renders a one-line description of the CPU.
func hardwareCPU(h *state.HardwareInfo) string {
	if len(h.Processors) == 0 {
		return "—"
	}
	p := h.Processors[0]
	if p.Description != "" {
		return p.Description
	}
	if p.CoreCount > 0 {
		return fmt.Sprintf("%d cores", p.CoreCount)
	}
	return "—"
}

// hardwareMemoryMB sums memory modules.
func hardwareMemoryMB(h *state.HardwareInfo) int {
	total := 0
	for _, m := range h.MemoryModules {
		total += m.SizeMB
	}
	return total
}

// hardwareDiskTotal sums block device sizes.
func hardwareDiskTotal(h *state.HardwareInfo) int64 {
	var total int64
	for _, d := range h.BlockDevices {
		total += d.Size
	}
	return total
}

// kernelArgsPreview renders the kernel args a machine should boot with.
func kernelArgsPreview(token, endpoint string) string {
	return fmt.Sprintf(
		"siderolink.api=https://%s?jointoken=%s\n"+
			"talos.platform=metal\n"+
			"talos.config=.siderolink",
		endpoint, token,
	)
}

// generateJoinTokenValue returns a 32-byte hex token.
func generateJoinTokenValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// machineStages is the list of known machine stages for the filter dropdown.
var machineStages = []string{
	string(state.StageInitializing),
	string(state.StageInstalling),
	string(state.StageConfiguring),
	string(state.StageReady),
	string(state.StageRestarting),
	string(state.StageStopping),
	string(state.StageOff),
	string(state.StageUpdating),
	string(state.StageRemoving),
}

// --- Users (W9) ---

// UsersPage renders /settings/users. Admins see create/edit/delete UI; non-admins
// see a read-only table.
func (h *Handler) UsersPage(w http.ResponseWriter, r *http.Request) {
	role := auth.RoleFromContext(r.Context())
	isAdmin := role == string(auth.RoleAdmin)

	users, err := h.store.ListUsers()
	if err != nil {
		http.Error(w, "list users failed", http.StatusInternalServerError)
		return
	}

	rows := make([]pages.UserRow, 0, len(users))
	for _, u := range users {
		row := pages.UserRow{
			Name: u.Metadata.Name,
			Role: u.Spec.Role,
		}
		if u.Status.LastLogin != nil {
			row.LastLogin = u.Status.LastLogin.Format(time.RFC3339)
		} else {
			row.LastLogin = "—"
		}
		rows = append(rows, row)
	}

	toast := h.popToast(r)
	h.render(w, r, layout.BaseProps{
		Title: "Users",
		Page:  "users",
		Content: pages.UsersPage(pages.UsersPageData{
			Users:     rows,
			CanMutate: isAdmin,
		}),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Settings", URL: "/settings"},
			{Name: "Users", Current: true},
		},
		Toast: toast,
	})
}

// UserCreate handles POST /settings/users. Admin-only; enforced by store + role.
func (h *Handler) UserCreate(w http.ResponseWriter, r *http.Request) {
	if !h.isAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.redirectAction(w, r, "/settings/users?toast=invalid+form&toast-type=error")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	role := strings.TrimSpace(r.FormValue("role"))
	password := r.FormValue("password")

	if name == "" || !auth.ValidRoles[role] || password == "" {
		h.redirectAction(w, r, "/settings/users?toast=name,+role,+password+required&toast-type=error")
		return
	}

	if existing, _ := h.store.GetUser(name); existing != nil {
		h.redirectAction(w, r, "/settings/users?toast=user+already+exists&toast-type=error")
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		h.redirectAction(w, r, "/settings/users?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	if _, err := h.store.CreateUser(name, state.UserSpec{Role: role, PasswordHash: hash}); err != nil {
		h.redirectAction(w, r, "/settings/users?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.redirectAction(w, r, "/settings/users?toast=user+created&toast-type=success")
}

// UserUpdate handles POST /settings/users/{name} (PUT-tunneled via _method).
func (h *Handler) UserUpdate(w http.ResponseWriter, r *http.Request) {
	if !h.isAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		h.redirectAction(w, r, "/settings/users?toast=invalid+form&toast-type=error")
		return
	}
	role := strings.TrimSpace(r.FormValue("role"))
	password := r.FormValue("password")
	if !auth.ValidRoles[role] {
		h.redirectAction(w, r, "/settings/users?toast=invalid+role&toast-type=error")
		return
	}

	existing, err := h.store.GetUser(name)
	if err != nil || existing == nil {
		h.redirectAction(w, r, "/settings/users?toast=user+not+found&toast-type=error")
		return
	}

	hash := existing.Spec.PasswordHash
	if password != "" {
		hash, err = auth.HashPassword(password)
		if err != nil {
			h.redirectAction(w, r, "/settings/users?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
			return
		}
	}
	if _, err := h.store.UpdateUser(name, existing.Metadata.ResourceVersion, state.UserSpec{Role: role, PasswordHash: hash}); err != nil {
		h.redirectAction(w, r, "/settings/users?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.redirectAction(w, r, "/settings/users?toast=user+updated&toast-type=success")
}

// UserDelete handles POST /settings/users/{name}/delete.
func (h *Handler) UserDelete(w http.ResponseWriter, r *http.Request) {
	if !h.isAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := r.PathValue("name")
	if name == auth.UserFromContext(r.Context()) {
		h.redirectAction(w, r, "/settings/users?toast=cannot+delete+current+user&toast-type=error")
		return
	}
	if err := h.store.DeleteUser(name); err != nil {
		h.redirectAction(w, r, "/settings/users?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.redirectAction(w, r, "/settings/users?toast=user+deleted&toast-type=success")
}

// --- API Tokens (W9) ---

// APITokensPage renders /settings/api-tokens. Admins see all tokens; everyone
// else sees only their own. On ?new=<id>, the page shows the one-time reveal
// card from a query-param flash (cleared client-side after copy).
func (h *Handler) APITokensPage(w http.ResponseWriter, r *http.Request) {
	caller := auth.UserFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	isAdmin := role == string(auth.RoleAdmin)

	userName := ""
	if !isAdmin {
		userName = caller
	}

	tokens, err := h.store.ListAPITokens(userName)
	if err != nil {
		http.Error(w, "list tokens failed", http.StatusInternalServerError)
		return
	}

	rows := make([]pages.APITokenRow, 0, len(tokens))
	now := time.Now().UTC()
	for _, t := range tokens {
		row := pages.APITokenRow{
			ID:        t.ID,
			UserName:  t.UserName,
			CreatedAt: t.CreatedAt.Format(time.RFC3339),
			LastUsed:  "—",
			ExpiresAt: "never",
			Status:    "active",
		}
		// Resolve current role for display.
		if u, _ := h.store.GetUser(t.UserName); u != nil {
			row.Role = u.Spec.Role
		}
		if t.LastUsed != nil {
			row.LastUsed = t.LastUsed.Format(time.RFC3339)
		}
		if t.ExpiresAt != nil {
			row.ExpiresAt = t.ExpiresAt.Format(time.RFC3339)
			if now.After(*t.ExpiresAt) {
				row.Status = "expired"
			}
		}
		rows = append(rows, row)
	}

	data := pages.APITokensPageData{
		Tokens:      rows,
		CanMutate:   h.canMutate(r),
		CurrentUser: caller,
	}

	// One-time reveal: token id and secret come from a short-lived flash cookie
	// set by APITokenCreate. The cookie is cleared after this render.
	if revealCookie, _ := r.Cookie("rezuscloud_token_reveal"); revealCookie != nil {
		// Cookie value is "<id>|<secret>[|<expires>]" URL-encoded.
		parts := strings.SplitN(revealCookie.Value, "|", 3)
		if len(parts) >= 2 {
			data.NewTokenID = parts[0]
			data.NewSecret = parts[1]
			if len(parts) == 3 {
				data.NewExpiresAt = parts[2]
			}
		}
		// Clear the cookie so it cannot be revealed again on refresh.
		http.SetCookie(w, &http.Cookie{
			Name: "rezuscloud_token_reveal", Value: "", Path: "/", HttpOnly: true,
			SameSite: http.SameSiteLaxMode, MaxAge: -1,
		})
	}

	toast := h.popToast(r)
	h.render(w, r, layout.BaseProps{
		Title:   "API Tokens",
		Page:    "api-tokens",
		Content: pages.APITokensPage(data),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Settings", URL: "/settings"},
			{Name: "API Tokens", Current: true},
		},
		Toast: toast,
	})
}

// APITokenCreate handles POST /settings/users/{name}/api-tokens. Issues a token
// for {name} (caller must be {name} or admin) and sets a one-time flash cookie
// so the next GET /settings/api-tokens shows the plaintext secret exactly once.
func (h *Handler) APITokenCreate(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	target := r.PathValue("name")
	caller := auth.UserFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	if role != string(auth.RoleAdmin) && caller != target {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	user, err := h.store.GetUser(target)
	if err != nil || user == nil {
		h.redirectAction(w, r, "/settings/api-tokens?toast=user+not+found&toast-type=error")
		return
	}
	if err := r.ParseForm(); err != nil {
		h.redirectAction(w, r, "/settings/api-tokens?toast=invalid+form&toast-type=error")
		return
	}
	days, _ := strconv.Atoi(r.FormValue("expiresInDays"))

	var expiresAt *time.Time
	if days > 0 {
		t := time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)
		expiresAt = &t
	}

	plaintext, id, hash, err := auth.GenerateAPIToken()
	if err != nil {
		h.redirectAction(w, r, "/settings/api-tokens?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	if _, err := h.store.CreateAPIToken(id, target, hash, expiresAt); err != nil {
		h.redirectAction(w, r, "/settings/api-tokens?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}

	expires := ""
	if expiresAt != nil {
		expires = expiresAt.Format(time.RFC3339)
	}
	// Flash cookie carries the plaintext once. MaxAge=300s is enough for the
	// redirect → GET round trip.
	http.SetCookie(w, &http.Cookie{
		Name: "rezuscloud_token_reveal", Value: id + "|" + plaintext + "|" + expires,
		Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 300,
	})
	h.redirectAction(w, r, "/settings/api-tokens?toast=token+created&toast-type=success")
}

// APITokenDelete handles POST /settings/api-tokens/{id}/delete.
func (h *Handler) APITokenDelete(w http.ResponseWriter, r *http.Request) {
	if !h.canMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	tok, err := h.store.GetAPIToken(id)
	if err != nil || tok == nil {
		h.redirectAction(w, r, "/settings/api-tokens?toast=token+not+found&toast-type=error")
		return
	}
	caller := auth.UserFromContext(r.Context())
	role := auth.RoleFromContext(r.Context())
	if role != string(auth.RoleAdmin) && caller != tok.UserName {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := h.store.DeleteAPIToken(id); err != nil {
		h.redirectAction(w, r, "/settings/api-tokens?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.redirectAction(w, r, "/settings/api-tokens?toast=token+revoked&toast-type=success")
}

// isAdmin reports whether the current user is an admin.
func (h *Handler) isAdmin(r *http.Request) bool {
	return auth.RoleFromContext(r.Context()) == string(auth.RoleAdmin)
}

// --- Audit (W10) ---

// AuditPage renders /settings/audit with filters + pagination.
func (h *Handler) AuditPage(w http.ResponseWriter, r *http.Request) {
	if h.auditStore == nil {
		http.Error(w, "audit store unavailable", http.StatusServiceUnavailable)
		return
	}

	// Parse the same filter surface the API uses.
	q := r.URL.Query()
	f := audit.Filter{
		User:     strings.TrimSpace(q.Get("user")),
		Resource: strings.TrimSpace(q.Get("resource")),
		Verb:     strings.TrimSpace(q.Get("verb")),
	}
	if raw := q.Get("since"); raw != "" {
		// Accept either datetime-local ("2026-06-06T12:00") or RFC3339.
		if t, err := time.Parse("2006-01-02T15:04", raw); err == nil {
			f.Since = t
		} else if t, err := time.Parse(time.RFC3339, raw); err == nil {
			f.Since = t
		}
	}
	if raw := q.Get("until"); raw != "" {
		if t, err := time.Parse("2006-01-02T15:04", raw); err == nil {
			f.Until = t
		} else if t, err := time.Parse(time.RFC3339, raw); err == nil {
			f.Until = t
		}
	}
	limit := 50
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	f.Limit = limit
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v >= 0 {
		f.Offset = v
	}

	events, err := h.auditStore.ListEvents(r.Context(), f)
	if err != nil {
		http.Error(w, "list audit failed", http.StatusInternalServerError)
		return
	}
	total, err := h.auditStore.CountEvents(r.Context(), f)
	if err != nil {
		http.Error(w, "count audit failed", http.StatusInternalServerError)
		return
	}

	rows := make([]pages.AuditRow, 0, len(events))
	for _, ev := range events {
		rows = append(rows, pages.AuditRow{
			ID: ev.ID, Timestamp: ev.Timestamp, UserName: ev.UserName, Role: ev.Role,
			Method: ev.Method, Path: ev.Path, Resource: ev.Resource, ResourceID: ev.ResourceID,
			Verb: ev.Verb, Status: ev.Status, RequestID: ev.RequestID, SourceIP: ev.SourceIP,
			Error: ev.Error,
		})
	}

	// Distinct users + resources seen in this page only (cheap).
	userSet := map[string]struct{}{}
	resSet := map[string]struct{}{}
	for _, ev := range events {
		if ev.UserName != "" {
			userSet[ev.UserName] = struct{}{}
		}
		if ev.Resource != "" {
			resSet[ev.Resource] = struct{}{}
		}
	}

	data := pages.AuditPageData{
		Events: rows,
		Filters: pages.AuditFilters{
			User: f.User, Resource: f.Resource, Verb: f.Verb,
			Since: q.Get("since"), Until: q.Get("until"),
		},
		Total:     total,
		Limit:     limit,
		Offset:    f.Offset,
		CanMutate: h.canMutate(r),
	}
	for u := range userSet {
		data.Users = append(data.Users, u)
	}
	for r := range resSet {
		data.Resources = append(data.Resources, r)
	}

	toast := h.popToast(r)
	h.render(w, r, layout.BaseProps{
		Title:   "Audit Log",
		Page:    "audit",
		Content: pages.AuditPage(data),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Settings", URL: "/settings"},
			{Name: "Audit", Current: true},
		},
		Toast: toast,
	})
}

// --- Providers + manual join (W11) ---

// ProvidersPage renders /providers with the live provider adapter table.
func (h *Handler) ProvidersPage(w http.ResponseWriter, r *http.Request) {
	providers, err := h.store.ListProviders()
	if err != nil {
		http.Error(w, "list providers failed", http.StatusInternalServerError)
		return
	}

	rows := make([]pages.ProviderRow, 0, len(providers))
	for _, p := range providers {
		row := pages.ProviderRow{
			Type:      p.Metadata.Name,
			Endpoint:  p.Spec.Endpoint,
			Connected: p.Status.Connected,
		}
		if !p.Status.LastHeartbeat.IsZero() {
			row.LastHeartbeat = p.Status.LastHeartbeat.Format(time.RFC3339)
		} else {
			row.LastHeartbeat = "—"
		}
		if p.Status.Schema != nil {
			row.MachineTypes = p.Status.Schema.MachineTypes
			row.Regions = p.Status.Schema.Regions
		}
		row.Error = p.Status.Error
		rows = append(rows, row)
	}

	toast := h.popToast(r)
	h.render(w, r, layout.BaseProps{
		Title:   "Providers",
		Page:    "providers",
		Content: pages.ProvidersPage(pages.ProvidersPageData{Providers: rows, Total: len(rows), CanMutate: h.canMutate(r)}),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Providers", Current: true},
		},
		Toast: toast,
	})
}

// ManualJoinPage renders /machines/join-manual with active join tokens and
// kernel args previews. Per ADR 17, installation media stays link-based.
func (h *Handler) ManualJoinPage(w http.ResponseWriter, r *http.Request) {
	endpoint := os.Getenv("REZUSCLOUD_MACHINELINK_PUBLIC_ENDPOINT")
	if endpoint == "" {
		endpoint = h.machineLinkEndpoint()
	}

	jtRecords, _, err := h.store.ListJoinTokens()
	if err != nil {
		http.Error(w, "list join tokens failed", http.StatusInternalServerError)
		return
	}

	rows := make([]pages.ManualJoinToken, 0, len(jtRecords))
	for _, jt := range jtRecords {
		if jt.Status.Used {
			continue
		}
		// Skip expired.
		if !jt.Spec.ExpiresAt.IsZero() && time.Now().UTC().After(jt.Spec.ExpiresAt) {
			continue
		}
		tokenPreview := jt.Metadata.Name
		if len(tokenPreview) > 8 {
			tokenPreview = tokenPreview[:8] + "…"
		}
		cluster := jt.Metadata.Labels["rezuscloud.io/tenant"]
		expires := ""
		if !jt.Spec.ExpiresAt.IsZero() {
			expires = jt.Spec.ExpiresAt.Format(time.RFC3339)
		}
		rows = append(rows, pages.ManualJoinToken{
			Token:      tokenPreview,
			Cluster:    cluster,
			NodeGroup:  jt.Spec.NodeGroup,
			KernelArgs: kernelArgsPreview(jt.Metadata.Name, endpoint),
			ExpiresAt:  expires,
		})
	}

	data := pages.ManualJoinPageData{
		ClusterNames: h.tenantNames(),
		JoinTokens:   rows,
		CanMutate:    h.canMutate(r),
	}

	// Image Factory link (ADR 17: link-only, no embedded wizard).
	if url := os.Getenv("REZUSCLOUD_IMAGE_FACTORY_URL"); url != "" {
		data.HelperURL = url
		data.HelperText = "Generate a Talos installation image that boots with your kernel args."
	} else {
		data.HelperURL = "https://factory.talos.dev/"
		data.HelperText = "Use Image Factory to generate a Talos ISO or raw image; boot it with the kernel args below."
	}

	toast := h.popToast(r)
	h.render(w, r, layout.BaseProps{
		Title:   "Manual Join",
		Page:    "machines",
		Content: pages.ManualJoinPage(data),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Machines", URL: "/machines"},
			{Name: "Manual Join", Current: true},
		},
		Toast: toast,
	})
}

// tenantNames returns the list of existing tenant names.
func (h *Handler) tenantNames() []string {
	tenants, _, _ := h.store.ListTenants()
	out := make([]string, 0, len(tenants))
	for _, t := range tenants {
		out = append(out, t.Metadata.Name)
	}
	return out
}

// --- W12: Machine monitor + events ---

// MachineMonitor renders /machines/{id}/monitor with stage/role stats + lifecycle events SSE stream.
func (h *Handler) MachineMonitor(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	m, err := h.store.GetMachine(id)
	if err != nil {
		http.Error(w, "load machine failed", http.StatusInternalServerError)
		return
	}
	if m == nil {
		http.NotFound(w, r)
		return
	}

	data := pages.MachineMonitorData{
		MachineID: id,
		Cluster:   m.Metadata.Labels["rezuscloud.io/tenant"],
		Stage:     string(m.Status.Stage),
		Role:      m.Status.Role,
		SSEURL:    "/machines/" + id + "/events",
	}

	h.render(w, r, layout.BaseProps{
		Title:   "Monitor — " + shortDisplayID(id),
		Page:    "machine",
		Content: pages.MachineMonitor(data),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Machines", URL: "/machines"},
			{Name: shortDisplayID(id), URL: "/machines/" + id},
			{Name: "Monitor", Current: true},
		},
	})
}

// MachineEvents streams lifecycle events for a specific machine via SSE.
// Filters the global watch bus to the machine's resource events.
func (h *Handler) MachineEvents(w http.ResponseWriter, r *http.Request) {
	if h.bus == nil {
		http.Error(w, "events bus unavailable", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}

	ch, cancel := h.bus.Subscribe("machine")
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, canFlush := w.(http.Flusher)
	if canFlush {
		flusher.Flush()
	}

	// Send an initial heartbeat so the client knows we're connected.
	fmt.Fprintf(w, "data: {\"type\":\"READY\",\"object\":{\"metadata\":{\"name\":%q}}}\n\n", id)
	if canFlush {
		flusher.Flush()
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			// Filter to this machine.
			obj, _ := ev.Object.(map[string]any)
			meta, _ := obj["metadata"].(map[string]any)
			name, _ := meta["name"].(string)
			if name != id {
				continue
			}
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", data)
			if canFlush {
				flusher.Flush()
			}
		case <-ticker.C:
			// Heartbeat keeps proxies from closing idle connections.
			fmt.Fprintf(w, ": heartbeat\n\n")
			if canFlush {
				flusher.Flush()
			}
		}
	}
}

// --- Settings index (W13) ---

// SettingsIndexPage renders /settings with section quick-links + a read-only
// operational config summary. Per ADR 17 this is minimal — no flag matrix.
func (h *Handler) SettingsIndexPage(w http.ResponseWriter, r *http.Request) {
	data := pages.SettingsIndexPageData{
		OperationalConfig: pages.OperationalConfig{
			JWTSessions:          envDefault("REZUSCLOUD_JWT_SESSIONS", "24h (default)"),
			BcryptCost:           envDefault("REZUSCLOUD_BCRYPT_COST", "12 (default)"),
			AuditRetentionDays:   envDefault("REZUSCLOUD_AUDIT_RETENTION_DAYS", "90 (default)"),
			BackupDirectory:      envDefault("REZUSCLOUD_BACKUP_DIR", "(tmpdir default)"),
			MachineLinkEndpoint:  envDefault("REZUSCLOUD_MACHINELINK_PUBLIC_ENDPOINT", "machinelink.rezus.cloud:50001"),
			ProviderGRPCEndpoint: envDefault("REZUSCLOUD_PROVIDER_PUBLIC_ENDPOINT", "provider.rezus.cloud:50190"),
		},
		ClusterSummary: pages.ClusterSummary{
			HTTPAddr:        envDefault("REZUSCLOUD_ADDR", ":8080"),
			MachineLinkAddr: envDefault("REZUSCLOUD_MACHINELINK_ADDR", ":50180"),
			ProviderAddr:    envDefault("REZUSCLOUD_PROVIDER_ADDR", ":50190"),
		},
		CanMutate: h.canMutate(r),
	}

	toast := h.popToast(r)
	h.render(w, r, layout.BaseProps{
		Title:   "Settings",
		Page:    "settings",
		Content: pages.SettingsIndex(data),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Settings", Current: true},
		},
		Toast: toast,
	})
}

// envDefault returns the env value if set + non-empty, else fallback.
func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
