package api

import (
	"fmt"
	"strings"

	"github.com/rezuscloud/rezuscloud/internal/state"
)

// validateTenantSpec checks a tenant spec before persistence (#175).
func validateTenantSpec(spec any) error {
	s, ok := spec.(state.TenantSpec)
	if !ok {
		return fmt.Errorf("expected TenantSpec, got %T", spec)
	}
	if strings.TrimSpace(s.KubernetesVersion) == "" {
		return fmt.Errorf("spec.kubernetesVersion must not be empty")
	}
	return nil
}
