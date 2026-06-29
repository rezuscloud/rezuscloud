// Package dashboard implements the WebUI dashboard section.
//
// Extracted from the root web.Handler as part of issue #53 (WebUI Handler
// god-module split follow-up). Owns:
//
//   - GET /              — dashboard with posture cards + recent audit
//   - GET /events/stream — multiplexed SSE stream of all resource events
//
// The dashboard depends on a few cross-cutting helpers (tenant summaries,
// toast handling, audit lookup) which are passed in via the Host interface.
package dashboard

import (
	"encoding/json"
	"net/http"

	"context"

	"github.com/rezuscloud/rezuscloud/internal/audit"
	"github.com/rezuscloud/rezuscloud/internal/dashboard" // posture computation
	"github.com/rezuscloud/rezuscloud/internal/metrics"
	"github.com/rezuscloud/rezuscloud/internal/state"
	"github.com/rezuscloud/rezuscloud/internal/watch"
	"github.com/rezuscloud/rezuscloud/internal/web/layout"
	"github.com/rezuscloud/rezuscloud/internal/web/pages"
)

// Host is the subset of the root web.Handler that the dashboard needs.
// Kept narrow so other sections can implement it independently.
type Host interface {
	// Render draws the layout.Base page.
	Render(w http.ResponseWriter, r *http.Request, props layout.BaseProps)
	// TenantSummaries returns the cluster list with computed phase + machine counts.
	TenantSummaries() []pages.TenantSummary
	// PopToast reads + clears a flash toast from a query-string param.
	PopToast(r *http.Request) layout.ToastData
	// AuthRequired wraps a handler with the WebUI auth middleware.
	AuthRequired(next http.HandlerFunc) http.HandlerFunc
}

// BackupReader is the small interface the dashboard needs to list snapshots.
// Aliased from the dashboard package to keep the seam explicit.
type BackupReader = dashboard.BackupReader

// UpgradeReader is the small interface the dashboard needs to list upgrade runs.
type UpgradeReader = dashboard.UpgradeReader

// MetricsAggregator is the interface for fetching cluster resource metrics.
// When nil, the dashboard omits the resource pressure section.
type MetricsAggregator interface {
	ClusterSummary(ctx context.Context) (*metrics.ClusterResourceSummary, error)
}

// Handler serves / and /events/stream.
type Handler struct {
	store      state.StoreAPI
	bus        *watch.Bus
	auditStore audit.Store
	backup     BackupReader
	upgrades   UpgradeReader
	metrics    MetricsAggregator
	host       Host
}

// New creates a dashboard Handler. bus, auditStore, backup, upgrades, and
// metrics may be nil — the dashboard degrades gracefully when those subsystems
// are not configured.
func New(store state.StoreAPI, bus *watch.Bus, auditStore audit.Store, backup BackupReader, upgrades UpgradeReader, metricsAgg MetricsAggregator, host Host) *Handler {
	return &Handler{
		store:      store,
		bus:        bus,
		auditStore: auditStore,
		backup:     backup,
		upgrades:   upgrades,
		metrics:    metricsAgg,
		host:       host,
	}
}

// RegisterRoutes registers GET / and GET /events/stream. The routes are
// gated by Host.AuthRequired so unauthenticated requests redirect to /login.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /", h.host.AuthRequired(h.Dashboard))
	mux.HandleFunc("GET /events/stream", h.host.AuthRequired(h.EventsStream))
}

// Dashboard renders the main dashboard page: tenant/machine/provider counts,
// tenant summaries with computed phases, posture cards (W14), and the most
// recent audit events.
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

	data.Tenants = h.host.TenantSummaries()

	// SSE hint: signal to the template that live updates are available.
	data.LiveStream = h.bus != nil

	// W14 posture cards — computed via the dedicated dashboard module so that
	// phase classification uses the real machine fleet, not tenant metadata.
	builder := dashboard.NewBuilder(dashboard.Deps{
		Store:    h.store,
		Backup:   h.backup,
		Upgrades: h.upgrades,
	})
	posture := builder.Build(r.Context())
	data.Posture = pages.DashboardPosture{
		Clusters: pages.ClusterPosture{
			Active: posture.Clusters.Active, Forming: posture.Clusters.Forming,
			Removing: posture.Clusters.Removing, Ready: posture.Clusters.Ready,
			Expected: posture.Clusters.Expected, Erroring: posture.Clusters.Erroring,
		},
		Machines: pages.MachinePosture{
			Connected: posture.Machines.Connected, Pending: posture.Machines.Pending,
			Failed: posture.Machines.Failed, Total: posture.Machines.Total,
		},
		Providers: pages.ProviderPosture{
			Connected: posture.Providers.Connected, Disconnected: posture.Providers.Disconnected,
			Errors: posture.Providers.Errors, Total: posture.Providers.Total,
		},
		Backups: pages.BackupPosture{
			LastSuccess: posture.Backups.LastSuccess, Failures: posture.Backups.Failures,
			RPOLabel: posture.Backups.RPOLabel,
		},
		Upgrades: pages.UpgradePosture{
			ActiveRuns: posture.Upgrades.ActiveRuns, BlockedPrecheck: posture.Upgrades.BlockedPrecheck,
			LatestTarget: posture.Upgrades.LatestTarget, LatestPhase: posture.Upgrades.LatestPhase,
		},
	}
	if h.auditStore != nil {
		events, _ := h.auditStore.ListEvents(r.Context(), audit.Filter{Limit: 8})
		data.RecentAudit = make([]pages.AuditRow, 0, len(events))
		for _, ev := range events {
			data.RecentAudit = append(data.RecentAudit, pages.AuditRow{
				ID: ev.ID, Timestamp: ev.Timestamp, UserName: ev.UserName, Role: ev.Role,
				Method: ev.Method, Path: ev.Path, Resource: ev.Resource, ResourceID: ev.ResourceID,
				Verb: ev.Verb, Status: ev.Status, RequestID: ev.RequestID, SourceIP: ev.SourceIP,
				Error: ev.Error,
			})
		}
	}

	// Resource pressure from metrics aggregator.
	if h.metrics != nil {
		if summary, err := h.metrics.ClusterSummary(r.Context()); err == nil && summary != nil {
			data.ResourcePressure = &pages.ResourcePressureData{
				CPUUsage:    summary.CPU.Usage,
				CPUCapacity: summary.CPU.Capacity,
				MemUsage:    summary.Memory.Usage,
				MemCapacity: summary.Memory.Capacity,
				PodRunning:  summary.Pods.Running,
				PodCapacity: summary.Pods.Capacity,
				Nodes:       summary.Nodes,
				NodeDetails: make([]pages.NodePressureCard, 0, len(summary.NodeDetails)),
			}
			for _, n := range summary.NodeDetails {
				data.ResourcePressure.NodeDetails = append(data.ResourcePressure.NodeDetails, pages.NodePressureCard{
					Name:      n.Name,
					Role:      n.Role,
					Status:    n.Status,
					CPUPct:    metrics.Percent(n.CPU.Usage.CPU, n.CPU.Allocatable.CPU),
					MemPct:    metrics.Percent(n.Memory.Usage.Memory, n.Memory.Allocatable.Memory),
					PodPct:    metrics.Percent(int64(n.Pods.Running), int64(n.Pods.Allocatable)),
					DiskPct:   metrics.Percent(n.Disk.UsedBytes, n.Disk.TotalBytes),
					CPUUsed:   n.CPU.Usage.CPU,
					CPUAlloc:  n.CPU.Allocatable.CPU,
					MemUsed:   n.Memory.Usage.Memory,
					MemAlloc:  n.Memory.Allocatable.Memory,
					PodCount:  n.Pods.Running,
					PodAlloc:  n.Pods.Allocatable,
					DiskUsed:  n.Disk.UsedBytes,
					DiskTotal: n.Disk.TotalBytes,
					Conditions: pages.Conditions{
						Ready:          string(n.Conditions.Ready),
						MemoryPressure: string(n.Conditions.MemoryPressure),
						DiskPressure:   string(n.Conditions.DiskPressure),
						PIDPressure:    string(n.Conditions.PIDPressure),
					},
				})
			}
		}
	}

	h.host.Render(w, r, layout.BaseProps{
		Title:   "Dashboard",
		Page:    "dashboard",
		Content: pages.Dashboard(data),
		Toast:   h.host.PopToast(r),
	})
}

// EventsStream multiplexes all resource type events into a single SSE stream.
// Used by the dashboard for live updates via HTMX sse extension. Returns 404
// when the watch bus is not configured.
func (h *Handler) EventsStream(w http.ResponseWriter, r *http.Request) {
	if h.bus == nil {
		http.NotFound(w, r)
		return
	}

	resourceTypes := []string{"tenant", "machine", "nodegroup", "provider", "configpatch"}
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
