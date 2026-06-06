// Package clusters implements the WebUI clusters (tenants) section.
//
// Extracted from the root web.Handler as part of issue #55 (WebUI Handler
// god-module split follow-up). Owns:
//
//   - GET    /clusters                                  — clusters list
//   - GET    /clusters/create                           — create form
//   - POST   /clusters/create                           — create submit
//   - GET    /clusters/{name}                           — cluster detail
//   - GET    /clusters/{name}/{tab}                     — cluster detail tab
//   - DELETE /clusters/{name}                           — delete cluster
//   - POST   /clusters/{name}/nodegroups/{ng}/scale     — scale node group
//   - GET    /clusters/{name}/kubeconfig                — download kubeconfig
//   - GET    /clusters/{name}/talosconfig               — download talosconfig
//   - POST   /clusters/{name}/upgrade/start             — start upgrade
//   - POST   /clusters/{name}/upgrade/{id}/cancel       — cancel upgrade
//   - POST   /clusters/{name}/patches/create            — create patch
//   - GET    /clusters/{name}/patches/{patch}           — edit patch
//   - POST   /clusters/{name}/patches/{patch}/save      — save patch
//   - POST   /clusters/{name}/patches/{patch}/delete    — delete patch
//   - POST   /clusters/{name}/patches/{patch}/toggle    — toggle patch
//   - GET    /clusters/{name}/patches/preview           — preview effective patch
//
// Also owns the /tenants aliases for backward compatibility.
package clusters

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/rezuscloud/rezuscloud/internal/api/patch"
	"github.com/rezuscloud/rezuscloud/internal/auth"
	"github.com/rezuscloud/rezuscloud/internal/credentials"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/statemachine"
	"github.com/rezuscloud/rezuscloud/internal/upgrade"
	"github.com/rezuscloud/rezuscloud/internal/web/layout"
	"github.com/rezuscloud/rezuscloud/internal/web/pages"
	"sigs.k8s.io/yaml"
)

// Host is the subset of the root web.Handler that the clusters section needs.
type Host interface {
	Render(w http.ResponseWriter, r *http.Request, props layout.BaseProps)
	PopToast(r *http.Request) layout.ToastData
	AuthRequired(next http.HandlerFunc) http.HandlerFunc
	CanMutate(r *http.Request) bool
	RedirectAction(w http.ResponseWriter, r *http.Request, target string)
	TenantSummaries() []pages.TenantSummary
	NodeGroupSummaries(tenantName string) []statemachine.NodeGroupSummary
	// BusPresent reports whether the watch bus is configured (used to toggle
	// the "live updates" hint on the clusters list).
	BusPresent() bool
}

// Handler serves the clusters routes.
type Handler struct {
	store      *state.Store
	upgradeMgr *upgrade.Manager // optional
	host       Host
}

// New creates a clusters Handler. upgradeMgr may be nil.
func New(store *state.Store, upgradeMgr *upgrade.Manager, host Host) *Handler {
	return &Handler{store: store, upgradeMgr: upgradeMgr, host: host}
}

// RegisterRoutes registers all cluster routes, gated by Host.AuthRequired.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	auth := h.host.AuthRequired

	mux.HandleFunc("GET /clusters", auth(h.TenantsList))
	mux.HandleFunc("GET /clusters/create", auth(h.ClusterCreatePage))
	mux.HandleFunc("POST /clusters/create", auth(h.ClusterCreateSubmit))
	mux.HandleFunc("GET /clusters/{name}", auth(h.TenantDetail))
	mux.HandleFunc("GET /clusters/{name}/{tab}", auth(h.TenantDetail))
	mux.HandleFunc("DELETE /clusters/{name}", auth(h.ClusterDelete))
	mux.HandleFunc("POST /clusters/{name}/nodegroups/{ng}/scale", auth(h.NodeGroupScale))
	mux.HandleFunc("GET /clusters/{name}/kubeconfig", auth(h.ClusterKubeconfig))
	mux.HandleFunc("GET /clusters/{name}/talosconfig", auth(h.ClusterTalosconfig))
	mux.HandleFunc("POST /clusters/{name}/upgrade/start", auth(h.ClusterUpgradeStart))
	mux.HandleFunc("POST /clusters/{name}/upgrade/{id}/cancel", auth(h.ClusterUpgradeCancel))
	mux.HandleFunc("POST /clusters/{name}/patches/create", auth(h.ClusterPatchCreate))
	mux.HandleFunc("GET /clusters/{name}/patches/{patch}", auth(h.ClusterPatchEditPage))
	mux.HandleFunc("POST /clusters/{name}/patches/{patch}/save", auth(h.ClusterPatchSave))
	mux.HandleFunc("POST /clusters/{name}/patches/{patch}/delete", auth(h.ClusterPatchDelete))
	mux.HandleFunc("POST /clusters/{name}/patches/{patch}/toggle", auth(h.ClusterPatchToggle))
	mux.HandleFunc("GET /clusters/{name}/patches/preview", auth(h.ClusterPatchesPreview))

	// /tenants aliases (legacy URL space).
	mux.HandleFunc("GET /tenants", auth(h.TenantsList))
	mux.HandleFunc("GET /tenants/{name}", auth(h.TenantDetail))
}

// --- Cluster list ---

func (h *Handler) TenantsList(w http.ResponseWriter, r *http.Request) {
	h.host.Render(w, r, layout.BaseProps{
		Title: "Clusters",
		Page:  "clusters",
		Content: pages.TenantsList(pages.TenantListData{
			Tenants:    h.host.TenantSummaries(),
			LiveStream: h.host.BusPresent(),
		}),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Clusters", Current: true},
		},
	})
}

// --- Cluster detail ---

func (h *Handler) TenantDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var spec state.TenantSpec
	meta, err := h.store.GetResource("tenant", name, &spec, nil)
	if err != nil || meta.Name == "" {
		http.NotFound(w, r)
		return
	}

	machines, _, _ := h.store.ListMachinesByTenant(name)
	nodeGroups := h.host.NodeGroupSummaries(name)

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
		CanMutate:        h.host.CanMutate(r),
		UpgradeComponent: r.URL.Query().Get("component"),
		UpgradeTarget:    r.URL.Query().Get("version"),
	}

	// Node groups.
	data.NodeGroups, _, _ = state.ListTypedByTenant(h.store, "nodegroup", name,
		func(meta state.Metadata, specRaw, _ json.RawMessage) (pages.NodeGroupRow, error) {
			var ngSpec struct {
				Name  string `json:"name"`
				Role  string `json:"role"`
				Count int    `json:"count"`
			}
			_ = json.Unmarshal(specRaw, &ngSpec)
			return pages.NodeGroupRow{Name: meta.Name, Role: ngSpec.Role, Count: ngSpec.Count}, nil
		})

	// Machines.
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
	data.UpgradeRuns = []pages.UpgradeRunRow{}
	if h.upgradeMgr != nil {
		runs, _ := h.upgradeMgr.ListRuns(name)
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
	}

	// Patches.
	data.Patches, _, _ = state.ListTypedByTenant(h.store, "configpatch", name,
		func(meta state.Metadata, specRaw, _ json.RawMessage) (pages.PatchRow, error) {
			var ps struct {
				Format     string `json:"format"`
				TargetRole string `json:"targetRole"`
				Enabled    bool   `json:"enabled"`
			}
			_ = json.Unmarshal(specRaw, &ps)
			tr := ps.TargetRole
			if tr == "" {
				tr = "all"
			}
			return pages.PatchRow{
				Name:       meta.Name,
				Format:     ps.Format,
				TargetRole: tr,
				Enabled:    ps.Enabled,
				UpdatedAt:  meta.UpdatedAt.Format("2006-01-02 15:04"),
			}, nil
		})

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

	h.host.Render(w, r, layout.BaseProps{
		Title:   name,
		Page:    "cluster",
		Content: pages.TenantDetail(data),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Clusters", URL: "/clusters"},
			{Name: name, Current: true},
		},
		Toast: h.host.PopToast(r),
	})
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

// --- Cluster create ---

func (h *Handler) ClusterCreatePage(w http.ResponseWriter, r *http.Request) {
	h.host.Render(w, r, layout.BaseProps{
		Title:   "Create Cluster",
		Page:    "clusters",
		Content: pages.ClusterCreate(pages.ClusterCreateData{}),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Clusters", URL: "/clusters"},
			{Name: "Create", Current: true},
		},
	})
}

func (h *Handler) ClusterCreateSubmit(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	data := pages.ClusterCreateData{
		Name:         r.FormValue("name"),
		K8sVersion:   r.FormValue("kubernetesVersion"),
		TalosVersion: r.FormValue("talosVersion"),
	}
	if data.Name == "" {
		data.NameError = "Name is required."
	} else if !validClusterName(data.Name) {
		data.NameError = "Name must match ^[a-z][a-z0-9-]{1,62}$ (lowercase, start with a letter, 2–63 chars)."
	}
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

	spec := state.TenantSpec{
		KubernetesVersion: data.K8sVersion,
		TalosVersion:      data.TalosVersion,
	}
	_, err := h.store.CreateTenant(data.Name, spec, nil, nil)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			data.FormError = "A cluster with this name already exists."
		} else {
			data.FormError = "Failed to create cluster: " + err.Error()
		}
		h.renderClusterCreateForm(w, r, data)
		return
	}

	bundle, err := credentials.GenerateSecretsBundle(spec.TalosVersion)
	if err == nil {
		bundleJSON, err := credentials.SecretsBundleJSON(bundle)
		if err == nil {
			_ = h.store.SaveTenantSecrets(data.Name, bundleJSON)
		}
	}

	w.Header().Set("HX-Redirect", "/clusters/"+data.Name+"?toast="+url.QueryEscape("Cluster "+data.Name+" created")+"&toast-type=success")
	w.WriteHeader(http.StatusNoContent)
}

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

// --- Cluster delete + node group scale ---

func (h *Handler) ClusterDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.NotFound(w, r)
		return
	}
	role := auth.RoleFromContext(r.Context())
	if role != string(auth.RoleAdmin) && role != string(auth.RoleEdit) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	_ = h.store.DeleteTenant(name)
	_ = h.store.RemoveTenantSecrets(name)

	w.Header().Set("HX-Redirect", "/clusters?toast="+url.QueryEscape("Cluster "+name+" deleted")+"&toast-type=success")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) NodeGroupScale(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("name")
	ngName := r.PathValue("ng")
	if tenant == "" || ngName == "" {
		http.NotFound(w, r)
		return
	}
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
	w.Header().Set("HX-Redirect", "/clusters/"+tenant+"?toast="+url.QueryEscape("Node group "+ngName+" scaled to "+countStr)+"&toast-type=success")
	w.WriteHeader(http.StatusNoContent)
}

// --- Credential download (kubeconfig / talosconfig) ---

func (h *Handler) ClusterKubeconfig(w http.ResponseWriter, r *http.Request) {
	h.credentialDownload(w, r, "kubeconfig")
}

func (h *Handler) ClusterTalosconfig(w http.ResponseWriter, r *http.Request) {
	h.credentialDownload(w, r, "talosconfig")
}

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

// --- Upgrade ---

func (h *Handler) ClusterUpgradeStart(w http.ResponseWriter, r *http.Request) {
	if !h.host.CanMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		h.host.RedirectAction(w, r, "/clusters/"+name+"/upgrade?toast="+url.QueryEscape("Invalid form")+"&toast-type=error")
		return
	}
	component := strings.TrimSpace(r.FormValue("component"))
	version := strings.TrimSpace(r.FormValue("version"))
	user := auth.UserFromContext(r.Context())
	if user == "" {
		user = "web"
	}
	if h.upgradeMgr == nil {
		h.host.RedirectAction(w, r, "/clusters/"+name+"/upgrade?component="+url.QueryEscape(component)+"&version="+url.QueryEscape(version)+"&toast="+url.QueryEscape("upgrade manager not configured")+"&toast-type=error")
		return
	}
	_, err := h.upgradeMgr.StartRun(name, component, version, user)
	if err != nil {
		h.host.RedirectAction(w, r, "/clusters/"+name+"/upgrade?component="+url.QueryEscape(component)+"&version="+url.QueryEscape(version)+"&toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.host.RedirectAction(w, r, "/clusters/"+name+"/upgrade?toast="+url.QueryEscape("Upgrade run started")+"&toast-type=success")
}

func (h *Handler) ClusterUpgradeCancel(w http.ResponseWriter, r *http.Request) {
	if !h.host.CanMutate(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := r.PathValue("name")
	runID := r.PathValue("id")
	if h.upgradeMgr == nil {
		h.host.RedirectAction(w, r, "/clusters/"+name+"/upgrade?toast="+url.QueryEscape("upgrade manager not configured")+"&toast-type=error")
		return
	}
	if err := h.upgradeMgr.CancelRun(runID); err != nil {
		h.host.RedirectAction(w, r, "/clusters/"+name+"/upgrade?toast="+url.QueryEscape(err.Error())+"&toast-type=error")
		return
	}
	h.host.RedirectAction(w, r, "/clusters/"+name+"/upgrade?toast="+url.QueryEscape("Upgrade canceled")+"&toast-type=success")
}

// --- ConfigPatch (W6) ---

func (h *Handler) ClusterPatchCreate(w http.ResponseWriter, r *http.Request) {
	if !h.host.CanMutate(r) {
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
	h.host.Render(w, r, layout.BaseProps{
		Title: "Patch " + patchName,
		Page:  "cluster",
		Content: pages.PatchEdit(pages.PatchEditData{
			Cluster:    cluster,
			Name:       patchName,
			Format:     spec.Format,
			TargetRole: tr,
			Enabled:    spec.Enabled,
			Patch:      spec.Patch,
			CanMutate:  h.host.CanMutate(r),
		}),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Clusters", URL: "/clusters"},
			{Name: cluster, URL: "/clusters/" + cluster + "/patches"},
			{Name: patchName, Current: true},
		},
		Toast: h.host.PopToast(r),
	})
}

func (h *Handler) ClusterPatchSave(w http.ResponseWriter, r *http.Request) {
	if !h.host.CanMutate(r) {
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
	if !h.host.CanMutate(r) {
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
	if !h.host.CanMutate(r) {
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

// validatePatchInput validates patch format/target/body before saving.
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
