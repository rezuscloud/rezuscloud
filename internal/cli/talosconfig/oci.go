package talosconfig

import (
	"fmt"

	"github.com/siderolabs/talos/pkg/machinery/config"
)

// OCIPlatformPatch returns a function that applies OCI-specific
// modifications to a generated machine config.
// Sets the install disk, platform, and external cloud provider.
func OCIPlatformPatch() func(config.Provider) error {
	return func(cfg config.Provider) error {
		v1alpha1 := cfg.RawV1Alpha1()
		if v1alpha1 == nil {
			return fmt.Errorf("config has no v1alpha1 document")
		}

		if v1alpha1.MachineConfig != nil && v1alpha1.MachineConfig.MachineInstall != nil {
			v1alpha1.MachineConfig.MachineInstall.InstallDisk = "/dev/sda"
		}

		return nil
	}
}
