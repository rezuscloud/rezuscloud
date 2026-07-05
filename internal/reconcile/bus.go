package reconcile

import (
	"github.com/rezuscloud/rezuscloud/internal/logging"
	"github.com/rezuscloud/rezuscloud/internal/state"
)

// Enqueuer is the minimal interface EnqueueBus depends on. applyqueue.Queue
// satisfies it; tests use a fake.
type Enqueuer interface {
	Enqueue(tenant string)
}

// EnqueueBus is a state.EventBus that enqueues the affected tenant into the
// apply queue whenever a tenant or node group resource is mutated. Combined
// with state.MultiBus, it coexists with the watch SSE adapter bus.
//
// This is the reconciler's "translation layer": every store mutation
// (Create/Update/Delete on tenants or node groups) triggers exactly one
// queue.Enqueue(tenant). The queue's debounce coalesces rapid mutations into a
// single apply.
type EnqueueBus struct {
	queue Enqueuer
	logf  func(format string, args ...any)
}

// NewEnqueueBus returns an EnqueueBus driving the given queue.
func NewEnqueueBus(queue Enqueuer) *EnqueueBus {
	return &EnqueueBus{queue: queue, logf: logging.Logf}
}

// Publish implements state.EventBus. It extracts the tenant name from the event
// and enqueues it. Tenant resource mutations use the metadata name directly;
// node group (and other tenant-scoped) mutations carry the tenant in the
// rezuscloud.io/tenant label.
func (b *EnqueueBus) Publish(resourceType string, ev state.ResourceEvent) {
	// Reconciliation/status writes must NOT feed back into the queue, or the
	// queue will enqueue itself forever. Only create/spec/delete mutations are
	// drift signals for the spec plane.
	if ev.Mutation == state.MutationStatus {
		return
	}
	tenant := tenantOf(resourceType, ev)
	if tenant == "" {
		return
	}
	b.queue.Enqueue(tenant)
}

// tenantOf extracts the tenant name to enqueue for a given event. For tenant
// resources, the metadata name IS the tenant. For tenant-scoped resources
// (nodegroup, configpatch, …), the rezuscloud.io/tenant label identifies it.
func tenantOf(resourceType string, ev state.ResourceEvent) string {
	if resourceType == "tenant" {
		return ev.Metadata.Name
	}
	if tenant := ev.Metadata.Labels["rezuscloud.io/tenant"]; tenant != "" {
		return tenant
	}
	return ""
}
