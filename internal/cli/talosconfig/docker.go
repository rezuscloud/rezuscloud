package talosconfig

import (
	"fmt"

	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/generate"
)

// DockerGenOptions returns generate.Options specific to the Docker platform.
// These match the options talosctl applies when creating Docker clusters.
func DockerGenOptions(versionContract *config.VersionContract) []generate.Option {
	var opts []generate.Option

	if versionContract.HostDNSEnabled() {
		opts = append(opts, generate.WithHostDNSForwardKubeDNSToHost(true))
	}

	return opts
}

// DockerPlatformPatch returns a function that applies Docker-specific
// modifications to a generated machine config.
// Docker containers don't need install disk configuration.
func DockerPlatformPatch() func(config.Provider) error {
	return func(cfg config.Provider) error {
		v1alpha1 := cfg.RawV1Alpha1()
		if v1alpha1 == nil {
			return fmt.Errorf("config has no v1alpha1 document")
		}

		if v1alpha1.MachineConfig != nil && v1alpha1.MachineConfig.MachineInstall != nil {
			v1alpha1.MachineConfig.MachineInstall.InstallDisk = ""
		}

		return nil
	}
}
