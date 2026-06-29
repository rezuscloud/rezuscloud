package reconcile

import (
	"context"
	"log"

	"github.com/rezuscloud/rezuscloud/internal/applyqueue"
	"github.com/rezuscloud/rezuscloud/internal/projection"
)

// ProjectionListener returns an applyqueue.Listener that rebuilds the
// projection index after a successful apply. The rebuild runs in a goroutine
// because StatePull (tofu state pull) is a blocking subprocess call and the
// listener MUST NOT block the tenant's apply loop.
//
// On PhaseFailed, the index is left as-is (it still reflects the last good
// apply). The caller can manually Rebuild if desired.
func ProjectionListener(idx *projection.Index) applyqueue.Listener {
	return func(tenant string, phase applyqueue.Phase, err error) {
		if phase != applyqueue.PhaseApplied {
			return
		}
		go func() {
			n, err := idx.Rebuild(context.Background(), tenant)
			if err != nil {
				log.Printf("reconcile: projection rebuild for %q failed: %v", tenant, err)
				return
			}
			log.Printf("reconcile: projected %d resources for %q", n, tenant)
		}()
	}
}
