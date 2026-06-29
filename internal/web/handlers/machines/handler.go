// Package machines implements the WebUI machines section.
//
// Extracted from the root web.Handler as part of issue #56 (WebUI Handler
// god-module split follow-up). Owns:
//
//   - GET    /machines                       — machines fleet
//   - GET    /machines/pending               — pending machines
//   - GET    /machines/{id}                  — machine detail
//   - GET    /machines/{id}/logs             — logs viewer
//   - GET    /machines/{id}/logs/poll        — HTMX logs partial
//   - GET    /machines/{id}/monitor          — monitor view
//   - GET    /machines/{id}/events           — SSE event stream
//   - GET    /machines/{id}/config           — Talos config preview
//   - GET    /machines/{id}/kernel-args      — kernel args editor
//   - POST   /machines/{id}/kernel-args      — save kernel args
//   - POST   /machines/{id}/restart          — restart machine
//   - POST   /machines/{id}/shutdown         — shutdown machine
//   - POST   /machines/{id}/approve          — approve machine
//   - DELETE /machines/{id}                  — delete machine
package machines

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/api/patch"
	"github.com/rezuscloud/rezuscloud/internal/configrender"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/watch"
	"github.com/rezuscloud/rezuscloud/internal/web/layout"
	"github.com/rezuscloud/rezuscloud/internal/web/pages"
)

// Host is the subset of the root web.Handler that the machines section needs.
type Host interface {
	Render(w http.ResponseWriter, r *http.Request, props layout.BaseProps)
	PopToast(r *http.Request) layout.ToastData
	AuthRequired(next http.HandlerFunc) http.HandlerFunc
	CanMutate(r *http.Request) bool
	ClusterNames() []string
	BusPresent() bool
}

// Handler serves the machines routes.
type Handler struct {
	store state.StoreAPI
	bus   *watch.Bus // optional — enables /machines/{id}/events SSE
	host  Host
}

// New creates a machines Handler. bus may be nil — the events endpoint
// returns 503 in that case.
func New(store state.StoreAPI, bus *watch.Bus, host Host) *Handler {
	return &Handler{store: store, bus: bus, host: host}
}

// RegisterRoutes registers all machine routes, gated by Host.AuthRequired.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	auth := h.host.AuthRequired

	mux.HandleFunc("GET /machines", auth(h.MachinesList))
	mux.HandleFunc("GET /machines/pending", auth(h.MachinesPending))
	mux.HandleFunc("GET /machines/{id}", auth(h.MachineDetail))
	mux.HandleFunc("GET /machines/{id}/logs", auth(h.MachineLogs))
	mux.HandleFunc("GET /machines/{id}/logs/poll", auth(h.MachineLogsPoll))
	mux.HandleFunc("GET /machines/{id}/monitor", auth(h.MachineMonitor))
	mux.HandleFunc("GET /machines/{id}/events", auth(h.MachineEvents))
	mux.HandleFunc("GET /machines/{id}/config", auth(h.MachineConfig))
	mux.HandleFunc("GET /machines/{id}/kernel-args", auth(h.MachineKernelArgs))
	mux.HandleFunc("POST /machines/{id}/kernel-args", auth(h.MachineKernelArgsSave))
	mux.HandleFunc("POST /machines/{id}/restart", auth(h.MachineRestart))
	mux.HandleFunc("POST /machines/{id}/shutdown", auth(h.MachineShutdown))
	mux.HandleFunc("POST /machines/{id}/approve", auth(h.MachineApprove))
	mux.HandleFunc("DELETE /machines/{id}", auth(h.MachineDelete))
}

// --- Machine list ---

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

	h.host.Render(w, r, layout.BaseProps{
		Title: "Machines",
		Page:  "machines",
		Content: pages.MachinesList(pages.MachinesListData{
			Machines:        rows,
			FilterCluster:   cluster,
			FilterStage:     stage,
			FilterConnected: connectedOnly,
			ClusterNames:    h.host.ClusterNames(),
			Stages:          machineStages,
			LiveStream:      h.host.BusPresent(),
		}),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Machines", Current: true},
		},
	})
}

// MachinesPending renders /machines/pending.
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

	h.host.Render(w, r, layout.BaseProps{
		Title: "Pending Machines",
		Page:  "machines",
		Content: pages.MachinesList(pages.MachinesListData{
			Machines:     rows,
			ClusterNames: h.host.ClusterNames(),
			Stages:       machineStages,
			LiveStream:   h.host.BusPresent(),
		}),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Machines", URL: "/machines"},
			{Name: "Pending", Current: true},
		},
	})
}

// --- Machine detail ---

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
		CanMutate:  h.host.CanMutate(r),
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

	h.host.Render(w, r, layout.BaseProps{
		Title:   "Machine " + shortDisplayID(id),
		Page:    "machine",
		Content: pages.MachineDetail(data),
		Breadcrumb: []layout.BreadcrumbItem{
			{Name: "Machines", URL: "/machines"},
			{Name: shortDisplayID(id), Current: true},
		},
	})
}

// --- Logs ---

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
	if r.Header.Get("HX-Request") == "true" && r.URL.Query().Get("partial") == "1" {
		logPartial(w, data.Lines)
		return
	}
	h.host.Render(w, r, layout.BaseProps{
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

func (h *Handler) MachineLogsPoll(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	logPartial(w, h.recentLogs(id))
}

// logPartial writes just the log lines div for HTMX swaps.
func logPartial(w http.ResponseWriter, lines []pages.LogLine) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for _, line := range lines {
		fmt.Fprintf(w, `<div class="ds-logs-line"><span class="ds-logs-time">%s</span>`,
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

// recentLogs returns synthetic log lines for v1. TODO(W7+): wire real log provider.
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

// --- Monitor + events ---

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
	h.host.Render(w, r, layout.BaseProps{
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
			fmt.Fprintf(w, ": heartbeat\n\n")
			if canFlush {
				flusher.Flush()
			}
		}
	}
}

// --- Config (Talos machine config preview) ---

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

	config, err := h.generateMachineConfig(cluster, id)
	if err != nil {
		http.Error(w, "Failed to generate config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.host.Render(w, r, layout.BaseProps{
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

// generateMachineConfig produces the Talos machine config YAML for display
// using the shared configrender module (PR #51).
func (h *Handler) generateMachineConfig(tenantName, machineID string) (string, error) {
	result, err := configrender.GenerateMachineConfig(context.Background(), h.store, h.store, patch.ResolvePatches,
		configrender.MachineConfigRequest{TenantName: tenantName, MachineID: machineID})
	if err != nil {
		return "", err
	}
	return result.YAML, nil
}

// --- Kernel args editor ---

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
	existing, existingName := h.findKernelArgsPatch(cluster)

	h.host.Render(w, r, layout.BaseProps{
		Title: "Kernel args — " + shortDisplayID(id),
		Page:  "machine",
		Content: pages.KernelArgs(pages.KernelArgsData{
			MachineID:         id,
			Cluster:           cluster,
			Existing:          existing,
			ExistingPatchName: existingName,
			FormValue:         existing,
			CanMutate:         h.host.CanMutate(r),
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
	if !h.host.CanMutate(r) {
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

	patchYAML := buildKernelArgsPatch(lines)
	_, existingName := h.findKernelArgsPatch(cluster)

	if existingName != "" {
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
		name := "kernel-args-" + cluster
		ps := patch.PatchSpec{Patch: patchYAML, Format: "strategic", Enabled: true}
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

// findKernelArgsPatch returns the existing kernel-args patch for a cluster.
func (h *Handler) findKernelArgsPatch(tenantName string) (string, string) {
	items, _, _ := state.ListTypedByTenant(h.store, "configpatch", tenantName,
		func(meta state.Metadata, specRaw, _ json.RawMessage) (patchWithMeta, error) {
			var ps patch.PatchSpec
			_ = json.Unmarshal(specRaw, &ps)
			return patchWithMeta{Metadata: meta, Spec: ps}, nil
		})
	for _, item := range items {
		if item.Metadata.Labels["rezuscloud.io/kind"] != "kernel-args" {
			continue
		}
		return item.Spec.Patch, item.Metadata.Name
	}
	return "", ""
}

type patchWithMeta struct {
	Metadata state.Metadata
	Spec     patch.PatchSpec
}

func isValidKernelArg(arg string) bool {
	allowed := []string{"talos.", "console=", "reboot=", "mitigations=", "ip="}
	for _, p := range allowed {
		if strings.HasPrefix(arg, p) {
			return true
		}
	}
	return false
}

// buildKernelArgsPatch produces the strategic-merge YAML for extraKernelArgs.
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

// --- Machine actions ---

func (h *Handler) MachineRestart(w http.ResponseWriter, r *http.Request) {
	if !h.host.CanMutate(r) {
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
	// TODO(W7): issue restart via machine link.
	http.Redirect(w, r, "/machines/"+id+"?toast=restart+queued&toast-type=success", http.StatusSeeOther)
}

func (h *Handler) MachineShutdown(w http.ResponseWriter, r *http.Request) {
	if !h.host.CanMutate(r) {
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
	http.Redirect(w, r, "/machines/"+id+"?toast=shutdown+queued&toast-type=success", http.StatusSeeOther)
}

func (h *Handler) MachineApprove(w http.ResponseWriter, r *http.Request) {
	if !h.host.CanMutate(r) {
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

func (h *Handler) MachineDelete(w http.ResponseWriter, r *http.Request) {
	if !h.host.CanMutate(r) {
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

// --- helpers ---

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

// formatAge renders a human-readable "X ago" string.
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

// shortDisplayID returns the first 8 chars of an ID for display.
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
