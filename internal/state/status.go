// Status derivation: computes tenant phase/status from machine states.
// The phase is derived (not stored manually) — it's always recomputed from
// the current machine states whenever the status is requested.
package state

// NodeGroupSummary is a minimal view of a node group for phase computation.
type NodeGroupSummary struct {
	Name  string
	Count int
}

// ComputeTenantPhase determines the tenant phase from machine states and node groups.
func ComputeTenantPhase(tenant *Tenant, machines []*Machine, nodeGroups []NodeGroupSummary) TenantPhase {
	// If deletion timestamp is set, tenant is removing.
	if tenant.Metadata.DeletionTimestamp != nil {
		return TenantRemoving
	}

	// No machines yet → forming.
	if len(machines) == 0 {
		return TenantForming
	}

	// Count ready vs total machines.
	readyCount := 0
	for _, m := range machines {
		if m.Status.Ready && m.Status.Stage == StageReady {
			readyCount++
		}
	}

	// Check if we have enough machines for all node groups.
	expectedCount := 0
	for _, ng := range nodeGroups {
		expectedCount += ng.Count
	}

	totalMachines := len(machines)

	// If we have more machines than expected → shrinking.
	if totalMachines > expectedCount {
		return TenantShrinking
	}

	// If we have fewer machines than expected → forming.
	if totalMachines < expectedCount {
		return TenantForming
	}

	// Exact count. Are all ready?
	if readyCount == totalMachines {
		return TenantActive
	}

	// Right count but some not ready → forming (still converging).
	return TenantForming
}

// ComputeTenantStatus builds the full TenantStatus from machine states.
func ComputeTenantStatus(tenant *Tenant, machines []*Machine, nodeGroups []NodeGroupSummary) TenantStatus {
	phase := ComputeTenantPhase(tenant, machines, nodeGroups)

	readyCount := 0
	connectedCount := 0
	controlPlaneReady := false

	for _, m := range machines {
		if m.Status.Ready {
			readyCount++
		}
		if m.Spec.Connected {
			connectedCount++
		}
		// Check if any control plane machine is ready.
		if m.Status.Ready && m.Metadata.Labels["rezuscloud.io/role"] == "controlplane" {
			controlPlaneReady = true
		}
	}

	totalMachines := len(machines)
	expectedCount := 0
	for _, ng := range nodeGroups {
		expectedCount += ng.Count
	}

	available := phase == TenantActive || (phase == TenantForming && controlPlaneReady)
	ready := phase == TenantActive
	apiReady := controlPlaneReady && readyCount > 0

	return TenantStatus{
		Phase:             phase,
		Available:         available,
		Ready:             ready,
		APIReady:          apiReady,
		ControlPlaneReady: controlPlaneReady,
		Machines: MachineCounts{
			Total:     totalMachines,
			Healthy:   readyCount,
			Connected: connectedCount,
		},
		KubernetesVersion: tenant.Spec.KubernetesVersion,
		TalosVersion:      tenant.Spec.TalosVersion,
	}
}
