// Package dashboard computes operational posture for the management plane
// root dashboard. The posture aggregates clusters, machines, providers,
// backups, and upgrades into a small set of cards.
//
// The module exists so that:
//   - The bug-prone "ComputeTenantStatus(t, nil, nil)" path that the original
//     inline implementation used (silently misclassifying phases when the
//     machine fleet was loaded) is structurally impossible. The Builder takes
//     a single snapshot of tenants + machines + node groups and uses it for
//     every posture card.
//   - Future callers (e.g. /api/v1/dashboard/posture for rezusctl or a mobile
//     view) can reuse the same aggregation without re-implementing it.
package dashboard

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// Posture is the set of cards rendered on the dashboard.
//
// The shape mirrors what the WebUI renders; it is also suitable for a
// JSON-serialized REST endpoint.
type Posture struct {
	Clusters  ClusterPosture
	Machines  MachinePosture
	Providers ProviderPosture
	Backups   BackupPosture
	Upgrades  UpgradePosture
}

// ClusterPosture summarizes the tenant fleet.
type ClusterPosture struct {
	Active   int      // Active phase
	Forming  int      // Forming / Shrinking / Progressing phases
	Removing int      // Removing phase
	Ready    int      // total machines with stage=ready across all tenants
	Expected int      // total machines expected across all tenants (sum of node group counts)
	Erroring []string // tenant names with a Degraded/Failed phase
}

// MachinePosture summarizes the machine fleet.
type MachinePosture struct {
	Connected int // spec.connected = true
	Pending   int // stage in {initializing, installing, configuring, updating}
	Failed    int // stage in {off, removing}
	Total     int
}

// ProviderPosture summarizes registered provider adapters.
type ProviderPosture struct {
	Connected    int
	Disconnected int
	Errors       int
	Total        int
}

// BackupPosture summarizes the most recent backup state.
type BackupPosture struct {
	LastSuccess string // RFC3339 timestamp or "never" or "unavailable"
	Failures    int
	RPOLabel    string // human label like "<1m", "5m", "2h", or "unknown"
}

// UpgradePosture summarizes in-flight upgrade runs across all tenants.
type UpgradePosture struct {
	ActiveRuns      int
	BlockedPrecheck bool   // true when a precheck run has Error != ""
	LatestTarget    string // target version of the most recent run
	LatestPhase     string // phase of the most recent run
}

// Deps is the set of optional dependencies Builder uses. Nil fields are
// skipped silently — their posture card reports zero/empty values.
type Deps struct {
	Store     state.StoreAPI
	Backup    BackupReader
	Upgrades  UpgradeReader
	NodeGroup NodeGroupReader // optional; if nil, Expected machine count falls back to per-tenant ListMachines
}

// BackupReader exposes the small surface of *backup.Service that posture
// needs. Define an interface so this package doesn't import backup.
type BackupReader interface {
	ListSnapshots() ([]BackupSnapshot, error)
}

// BackupSnapshot is the minimal shape of a backup.Service snapshot.
type BackupSnapshot struct {
	CreatedAt string
	Status    BackupSnapshotStatus
}

// BackupSnapshotStatus is the nested status of a snapshot.
type BackupSnapshotStatus struct {
	Status string // "success" | "failed"
}

// UpgradeReader exposes the small surface of *upgrade.Manager that posture
// needs.
type UpgradeReader interface {
	ListRuns(tenant string) ([]UpgradeRun, error)
}

// UpgradeRun is the minimal shape of an upgrade.Run that posture needs.
type UpgradeRun struct {
	Tenant  string
	Target  string
	Phase   string
	Error   string
	Started time.Time
}

// NodeGroupReader is an optional override for loading node groups.
// If nil, Builder falls back to ListResources on the store.
type NodeGroupReader interface {
	NodeGroupSummaries(tenant string) []state.NodeGroupSummary
}

// Builder computes Posture from a single snapshot of state.
type Builder struct {
	deps Deps
}

// NewBuilder creates a posture Builder with the given dependencies.
func NewBuilder(deps Deps) *Builder {
	return &Builder{deps: deps}
}

// Build aggregates all five posture cards in a single pass.
//
// Each tenant's phase is computed from its full machine fleet + node groups
// (never from tenant metadata alone). This is the bug fix for the prior
// inline implementation, which passed nil to ComputeTenantStatus and
// silently misclassified phases when the machine fleet disagreed with
// metadata.
func (b *Builder) Build(ctx context.Context) Posture {
	var p Posture
	if b.deps.Store == nil {
		return p
	}

	tenants, _, err := b.deps.Store.ListTenants()
	if err != nil {
		return p
	}
	machines, _, err := b.deps.Store.ListMachines()
	if err != nil {
		return p
	}
	providers, _ := b.deps.Store.ListProviders()

	// Index machines by tenant for fast lookup.
	byTenant := make(map[string][]*state.Machine)
	for _, m := range machines {
		tenant := m.Metadata.Labels["rezuscloud.io/tenant"]
		byTenant[tenant] = append(byTenant[tenant], m)
	}

	// --- Clusters (with real machine fleet + node groups) ---
	for _, t := range tenants {
		tenantMachines := byTenant[t.Metadata.Name]
		ngSums := b.nodeGroups(t.Metadata.Name)
		status := state.ComputeTenantStatus(t, tenantMachines, ngSums)
		phase := string(status.Phase)
		switch phase {
		case string(state.TenantActive):
			p.Clusters.Active++
		case string(state.TenantForming), string(state.TenantShrinking):
			p.Clusters.Forming++
		case string(state.TenantRemoving):
			p.Clusters.Removing++
		default:
			// Unknown / failed: treat as erroring so the card surfaces it.
			if phase == "failed" || phase == "error" || phase == "degraded" {
				p.Clusters.Erroring = append(p.Clusters.Erroring, t.Metadata.Name)
			} else {
				// Fall back to a neutral bucket; we don't have a phase for it.
				p.Clusters.Forming++
			}
		}
		p.Clusters.Expected += sumNodeGroupCounts(ngSums)
		for _, m := range tenantMachines {
			if m.Status.Stage == state.StageReady {
				p.Clusters.Ready++
			}
		}
	}

	// --- Machines ---
	p.Machines.Total = len(machines)
	for _, m := range machines {
		if m.Spec.Connected {
			p.Machines.Connected++
		}
		if isPendingStage(m.Status.Stage) {
			p.Machines.Pending++
		}
		if isFailedStage(m.Status.Stage) {
			p.Machines.Failed++
		}
	}

	// --- Providers ---
	p.Providers.Total = len(providers)
	for _, pr := range providers {
		if pr.Status.Connected {
			p.Providers.Connected++
		} else {
			p.Providers.Disconnected++
		}
		if pr.Status.Error != "" {
			p.Providers.Errors++
		}
	}

	// --- Backups ---
	p.Backups = b.backupPosture()

	// --- Upgrades ---
	p.Upgrades = b.upgradePosture(tenants)

	return p
}

func (b *Builder) nodeGroups(tenant string) []state.NodeGroupSummary {
	if b.deps.NodeGroup != nil {
		return b.deps.NodeGroup.NodeGroupSummaries(tenant)
	}
	// Fall back to a direct store read. This mirrors the prior
	// Handler.nodeGroupSummaries behaviour.
	opts := state.ListOptions{LabelSelector: "rezuscloud.io/tenant=" + tenant}
	mds, specs, _, _, err := b.deps.Store.ListResources("nodegroup", opts)
	if err != nil {
		return nil
	}
	out := make([]state.NodeGroupSummary, 0, len(mds))
	for i := range mds {
		var spec state.NodeGroupSpec
		_ = json.Unmarshal(specs[i], &spec)
		out = append(out, state.NodeGroupSummary{
			Name:  mds[i].Name,
			Count: spec.Count,
		})
	}
	return out
}

func (b *Builder) backupPosture() BackupPosture {
	if b.deps.Backup == nil {
		return BackupPosture{}
	}
	snaps, err := b.deps.Backup.ListSnapshots()
	if err != nil {
		return BackupPosture{LastSuccess: "unavailable"}
	}
	last := "never"
	failed := 0
	for _, s := range snaps {
		if s.Status.Status == "success" && last == "never" {
			last = s.CreatedAt
		}
		if s.Status.Status == "failed" {
			failed++
		}
	}
	return BackupPosture{LastSuccess: last, Failures: failed, RPOLabel: rpoEstimate(last)}
}

func (b *Builder) upgradePosture(tenants []*state.Tenant) UpgradePosture {
	if b.deps.Upgrades == nil {
		return UpgradePosture{}
	}
	active := 0
	blocked := false
	latestTarget := ""
	latestPhase := ""
	var latestTS time.Time
	for _, t := range tenants {
		runs, err := b.deps.Upgrades.ListRuns(t.Metadata.Name)
		if err != nil || len(runs) == 0 {
			continue
		}
		run := runs[0]
		if run.Phase == "running" || run.Phase == "precheck" {
			active++
		}
		if run.Phase == "precheck" && run.Error != "" {
			blocked = true
		}
		if run.Started.After(latestTS) {
			latestTS = run.Started
			latestTarget = run.Target
			latestPhase = run.Phase
		}
	}
	return UpgradePosture{
		ActiveRuns: active, BlockedPrecheck: blocked,
		LatestTarget: latestTarget, LatestPhase: latestPhase,
	}
}

func isPendingStage(s state.MachineStage) bool {
	switch s {
	case state.StageInitializing, state.StageInstalling, state.StageConfiguring, state.StageUpdating:
		return true
	}
	return false
}

func isFailedStage(s state.MachineStage) bool {
	switch s {
	case state.StageOff, state.StageRemoving:
		return true
	}
	return false
}

func sumNodeGroupCounts(groups []state.NodeGroupSummary) int {
	total := 0
	for _, g := range groups {
		total += g.Count
	}
	return total
}

func rpoEstimate(lastSuccess string) string {
	if lastSuccess == "" || lastSuccess == "never" || lastSuccess == "unavailable" {
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
