// Package k8s implements Kubernetes-specific upgrade logic.
// It validates kubelet/API server skew policies and provides
// K8s-aware machine upgrading.
package k8s

import (
	"context"
	"fmt"
)

// VersionPolicy validates Kubernetes version skew.
// Kubernetes allows:
// - kubelet may be up to 2 minor versions older than API server
// - API server must be the same version across all control planes
// - Minor version upgrades must be sequential (1.35 → 1.36, not 1.35 → 1.37)
type VersionPolicy struct{}

// ValidateUpgrade checks if the upgrade is valid per K8s skew policy.
func (VersionPolicy) ValidateUpgrade(current, target string) error {
	currentMinor, currentPatch, err := parseVersion(current)
	if err != nil {
		return fmt.Errorf("parse current version: %w", err)
	}

	targetMinor, targetPatch, err := parseVersion(target)
	if err != nil {
		return fmt.Errorf("parse target version: %w", err)
	}

	// No downgrade.
	if targetMinor < currentMinor || (targetMinor == currentMinor && targetPatch < currentPatch) {
		return fmt.Errorf("downgrade from %s to %s is not supported", current, target)
	}

	// Only one minor version jump at a time (skip if current is unknown).
	if currentMinor > 0 && targetMinor > currentMinor+1 {
		return fmt.Errorf("cannot skip minor versions: %s → %s (must upgrade one minor at a time)", current, target)
	}

	return nil
}

func parseVersion(v string) (minor, patch int, err error) {
	if v == "" {
		return 0, 0, nil
	}
	var major int
	n, err := fmt.Sscanf(v, "%d.%d.%d", &major, &minor, &patch)
	if n < 3 || err != nil {
		return 0, 0, fmt.Errorf("invalid version: %s", v)
	}
	return minor, patch, nil
}

// UpgradeOrder returns machines in the correct order for K8s upgrades:
// 1. Control plane machines (one at a time, must be sequential)
// 2. Worker machines (can be parallel in future, sequential for safety)
func UpgradeOrder(machines []MachineInfo) []string {
	var controlPlane, workers []string
	for _, m := range machines {
		if m.Role == "controlplane" {
			controlPlane = append(controlPlane, m.ID)
		} else {
			workers = append(workers, m.ID)
		}
	}
	return append(controlPlane, workers...)
}

// MachineInfo describes a machine for upgrade ordering.
type MachineInfo struct {
	ID   string
	Role string
}

// K8sUpgrader implements upgrade.MachineUpgrader for Kubernetes.
// In production, this will call the K8s API to cordon/drain/upgrade nodes.
type K8sUpgrader struct{}

// UpgradeMachine upgrades a single machine's Kubernetes version.
func (k *K8sUpgrader) UpgradeMachine(_ context.Context, _, _ string) error {
	// TODO: implement cordon → drain → upgrade kubelet → uncordon
	return nil
}

// CheckMachineHealth checks if a machine's kubelet is healthy after upgrade.
func (k *K8sUpgrader) CheckMachineHealth(_ context.Context, _ string) error {
	// TODO: implement via K8s API (node status check)
	return nil
}

// RollbackMachine rolls back a machine to the previous K8s version.
func (k *K8sUpgrader) RollbackMachine(_ context.Context, _, _ string) error {
	// TODO: implement rollback
	return nil
}

// PreCheck performs K8s-specific pre-flight checks.
func PreCheck(currentK8s, targetK8s, talosVersion string) []string {
	var warnings []string

	policy := VersionPolicy{}
	if err := policy.ValidateUpgrade(currentK8s, targetK8s); err != nil {
		warnings = append(warnings, err.Error())
	}

	// Check Talos compatibility.
	if talosVersion == "" {
		warnings = append(warnings, "Talos version unknown, cannot verify K8s compatibility")
	}

	return warnings
}

// Ensure K8sUpgrader implements MachineUpgrader.
// var _ upgrade.MachineUpgrader = (*K8sUpgrader)(nil)
