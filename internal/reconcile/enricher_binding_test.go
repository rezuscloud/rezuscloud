package reconcile

import (
	"context"
	"fmt"
	"testing"

	"github.com/rezuscloud/rezuscloud/internal/projection"
	"github.com/rezuscloud/rezuscloud/internal/provider"
	"github.com/rezuscloud/rezuscloud/internal/provider/metal"
)

// fixedSource serves one fixed state blob.
type fixedSource struct {
	blob []byte
}

func (s *fixedSource) State(_ context.Context, _ string) ([]byte, error) { return s.blob, nil }

// TestEnricher_AttachesBindingTokens is the ADR 0008 wiring proof: TF state
// carries terraform_data binding records (machine key → token); the enricher
// projects them onto machine records so the converge engine can map a
// connecting node (which presents its token at Provision) to its record.
func TestEnricher_AttachesBindingTokens(t *testing.T) {
	store := openTestStore(t)

	const (
		ip   = "2a01:e11:2440:2430:216:96ff:feec:93b6"
		tok  = "bind-tok-1"
		name = "edge"
	)

	stateJSON := fmt.Sprintf(`{
		"version": 4, "serial": 1, "lineage": "test",
		"resources": [
			{"mode": "managed", "type": "talos_machine_configuration_apply", "name": %q,
			 "instances": [{"index_key": %q, "attributes": {"node": %q}}]},
			{"mode": "managed", "type": "terraform_data", "name": %q,
			 "instances": [{"index_key": %q, "attributes": {"machine": %q, "token": %q}}]}
		]
	}`, name, ip, ip, name, ip, ip, tok)

	registry := provider.NewRegistry()
	registry.Register(metal.New())
	idx := projection.New(projection.StateSourceFunc((&fixedSource{blob: []byte(stateJSON)}).State), registry)
	idx.RegisterExtractor("Machine", func(tfType string, attrs map[string]interface{}) map[string]interface{} {
		// Minimal metal-shape Machine spec (mirrors main's machineExtractor).
		if tfType != "talos_machine_configuration_apply" || attrs == nil {
			return nil
		}
		if node, ok := attrs["node"].(string); ok {
			return map[string]interface{}{"address": node}
		}
		return nil
	})
	idx.RegisterExtractor("MachineBinding", projection.ExtractMachineBinding)
	if _, err := idx.Rebuild(context.Background(), "prod"); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	e := NewStoreEnricher(store, idx)
	e.enrich(context.Background(), "prod")

	m, err := store.GetMachine(name + "-" + ip)
	if err != nil || m == nil {
		t.Fatalf("machine record missing: err=%v nil=%v", err, m == nil)
	}
	if m.Spec.BindingToken != tok {
		t.Errorf("BindingToken = %q, want %q", m.Spec.BindingToken, tok)
	}
}
