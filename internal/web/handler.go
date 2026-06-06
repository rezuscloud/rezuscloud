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
	"strconv"
	"strings"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/credentials"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/statemachine"
	"github.com/rezuscloud/rezuscloud/internal/watch"
	"github.com/rezuscloud/rezuscloud/internal/web/layout"
	"github.com/rezuscloud/rezuscloud/internal/web/pages"
)

// Handler serves the WebUI.
type Handler struct {
	store      *state.Store
	jwtManager *auth.JWTManager
	bus        *watch.Bus // optional — enables /events/stream
}

// NewHandler creates a WebUI handler.
// jwtManager is required for login and cookie validation.
// bus is optional — pass nil to disable the /events/stream endpoint.
func NewHandler(store *state.Store, jwtManager *auth.JWTManager, bus *watch.Bus) *Handler {
	return &Handler{store: store, jwtManager: jwtManager, bus: bus}
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
	mux.HandleFunc("DELETE /clusters/{name}", h.AuthRequired(h.ClusterDelete))
	mux.HandleFunc("POST /clusters/{name}/nodegroups/{ng}/scale", h.AuthRequired(h.NodeGroupScale))
	mux.HandleFunc("GET /clusters/{name}/kubeconfig", h.AuthRequired(h.ClusterKubeconfig))
	mux.HandleFunc("GET /clusters/{name}/talosconfig", h.AuthRequired(h.ClusterTalosconfig))

	// Machines (W4).
	mux.HandleFunc("GET /machines", h.AuthRequired(h.MachinesList))
	mux.HandleFunc("GET /machines/jointokens", h.AuthRequired(h.JoinTokensList))
	mux.HandleFunc("POST /machines/jointokens", h.AuthRequired(h.JoinTokenCreate))
	mux.HandleFunc("GET /machines/pending", h.AuthRequired(h.MachinesPending))
	mux.HandleFunc("GET /machines/{id}", h.AuthRequired(h.MachineDetail))
	mux.HandleFunc("POST /machines/{id}/restart", h.AuthRequired(h.MachineRestart))
	mux.HandleFunc("POST /machines/{id}/shutdown", h.AuthRequired(h.MachineShutdown))
	mux.HandleFunc("POST /machines/{id}/approve", h.AuthRequired(h.MachineApprove))
	mux.HandleFunc("DELETE /machines/{id}", h.AuthRequired(h.MachineDelete))

	// Legacy /tenants aliases (kept for backward compatibility; /clusters is the
	// user-facing name per W2). New code should use /clusters/*.
	mux.HandleFunc("GET /tenants", h.AuthRequired(h.TenantsList))
	mux.HandleFunc("GET /tenants/{name}", h.AuthRequired(h.TenantDetail))

	// SSE stream — optional, only when bus is configured.
	if h.bus != nil {
		mux.HandleFunc("GET /events/stream", h.AuthRequired(h.EventsStream))
	}
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
		Name:         name,
		Phase:        string(status.Phase),
		K8sVersion:   spec.KubernetesVersion,
		TalosVersion: spec.TalosVersion,
		CurrentTab:   currentTab(r),
		CanMutate:    h.canMutate(r),
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
