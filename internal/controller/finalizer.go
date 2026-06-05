// Package controller implements teardown controllers that process
// K8s-style finalizers when resources are marked for deletion.
package controller

import (
	"log"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// FinalizerController processes resources marked for deletion.
// It runs cleanup tasks and removes finalizers, allowing the
// resource to be permanently deleted when all finalizers clear.
type FinalizerController struct {
	store *state.Store
}

// NewFinalizerController creates a new finalizer controller.
func NewFinalizerController(store *state.Store) *FinalizerController {
	return &FinalizerController{store: store}
}

// ReconcileTenant processes a tenant marked for deletion.
// Cleanup order: revoke tokens → deprovision machines → remove secrets.
func (c *FinalizerController) ReconcileTenant(name string) error {
	tenant, err := c.store.GetTenant(name)
	if err != nil {
		return err
	}
	if tenant == nil || tenant.Metadata.DeletionTimestamp == nil {
		return nil // nothing to do
	}

	log.Printf("reconciling tenant %q deletion", name)

	// Step 1: Revoke all join tokens.
	if hasFinalizer(tenant.Metadata, "rezuscloud.io/tokens") {
		if err := c.cleanupTenantTokens(name); err != nil {
			return err
		}
		_, _ = c.store.RemoveFinalizer("tenant", name, "rezuscloud.io/tokens")
	}

	// Step 2: Deprovision all machines.
	if hasFinalizer(tenant.Metadata, "rezuscloud.io/machines") {
		if err := c.cleanupTenantMachines(name); err != nil {
			return err
		}
		_, _ = c.store.RemoveFinalizer("tenant", name, "rezuscloud.io/machines")
	}

	// Step 3: Remove secrets bundle.
	if hasFinalizer(tenant.Metadata, "rezuscloud.io/secrets") {
		// Secrets cleanup is a no-op for now (secrets stored in resource table).
		_, _ = c.store.RemoveFinalizer("tenant", name, "rezuscloud.io/secrets")
	}

	return nil
}

// ReconcileMachine processes a machine marked for deletion.
// Cleanup order: disconnect link → clean up config.
func (c *FinalizerController) ReconcileMachine(id string) error {
	machine, err := c.store.GetMachine(id)
	if err != nil {
		return err
	}
	if machine == nil || machine.Metadata.DeletionTimestamp == nil {
		return nil
	}

	log.Printf("reconciling machine %q deletion", id)

	// Step 1: Disconnect MachineLink.
	if hasFinalizer(machine.Metadata, "rezuscloud.io/link") {
		// MachineLink disconnect is a no-op for now (connection will time out).
		_, _ = c.store.RemoveFinalizer("machine", id, "rezuscloud.io/link")
	}

	// Step 2: Clean up config.
	if hasFinalizer(machine.Metadata, "rezuscloud.io/config") {
		// Config cleanup is a no-op for now (config is generated on demand).
		_, _ = c.store.RemoveFinalizer("machine", id, "rezuscloud.io/config")
	}

	return nil
}

// ReconcileNodeGroup processes a node group marked for deletion.
// Unlinks all machines from the group.
func (c *FinalizerController) ReconcileNodeGroup(tenant, name string) error {
	// Read node group as generic resource.
	var spec state.NodeGroupSpec
	md, err := c.store.GetResource("nodegroup", name, &spec, nil)
	if err != nil {
		return err
	}
	if md.DeletionTimestamp == nil {
		return nil
	}

	log.Printf("reconciling node group %q deletion", name)

	// Unlink machines from this node group.
	if hasFinalizer(md, "rezuscloud.io/machines") {
		machines, _, err := c.store.ListMachinesByTenant(tenant)
		if err != nil {
			return err
		}
		for _, m := range machines {
			if m.Metadata.Labels["rezuscloud.io/node-group"] == name {
				// Remove node group label.
				delete(m.Metadata.Labels, "rezuscloud.io/node-group")
				delete(m.Metadata.Labels, "rezuscloud.io/tenant")
				// Just mark for deletion — machine controller will clean up.
				_ = c.store.DeleteMachine(m.Metadata.Name)
			}
		}
		_, _ = c.store.RemoveFinalizer("nodegroup", name, "rezuscloud.io/machines")
	}

	return nil
}

// cleanupTenantTokens revokes all join tokens for a tenant.
func (c *FinalizerController) cleanupTenantTokens(tenant string) error {
	opts := state.ListOptions{
		LabelSelector: "rezuscloud.io/tenant=" + tenant,
	}
	metas, _, _, _, err := c.store.ListResources("jointoken", opts)
	if err != nil {
		return err
	}
	for _, md := range metas {
		_ = c.store.RemoveResource("jointoken", md.Name)
	}
	return nil
}

// cleanupTenantMachines removes all tenant machines permanently.
func (c *FinalizerController) cleanupTenantMachines(tenant string) error {
	machines, _, err := c.store.ListMachinesByTenant(tenant)
	if err != nil {
		return err
	}
	for _, m := range machines {
		// Remove finalizers so the resource can be permanently deleted.
		for _, f := range m.Metadata.Finalizers {
			_, _ = c.store.RemoveFinalizer("machine", m.Metadata.Name, f)
		}
		// Now permanently delete.
		_ = c.store.RemoveResource("machine", m.Metadata.Name)
	}
	return nil
}

// hasFinalizer checks if a resource has a specific finalizer.
func hasFinalizer(md state.Metadata, finalizer string) bool {
	for _, f := range md.Finalizers {
		if f == finalizer {
			return true
		}
	}
	return false
}

// DefaultTenantFinalizers returns the standard finalizers for a tenant.
func DefaultTenantFinalizers() []string {
	return []string{"rezuscloud.io/machines", "rezuscloud.io/secrets", "rezuscloud.io/tokens"}
}

// DefaultMachineFinalizers returns the standard finalizers for a machine.
func DefaultMachineFinalizers() []string {
	return []string{"rezuscloud.io/config", "rezuscloud.io/link"}
}

// DefaultNodeGroupFinalizers returns the standard finalizers for a node group.
func DefaultNodeGroupFinalizers() []string {
	return []string{"rezuscloud.io/machines"}
}
