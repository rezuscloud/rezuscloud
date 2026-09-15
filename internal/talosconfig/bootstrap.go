package talosconfig

import (
	"fmt"
	"strings"
)

// BootstrapParams describes the minimal bootstrap config — the one unavoidable
// push (ADR 0008). It carries no cluster secrets: the node boots, joins the
// management link, and pulls its full config from the platform (converge
// engine). A node booted with only this config sits in Talos maintenance mode
// while the SideroLink controller runs from the kernel args.
type BootstrapParams struct {
	// InstallDisk is the Talos install disk (e.g. "/dev/nvme0n1"). Metal only —
	// cloud images are preinstalled and never run the installer.
	InstallDisk string
	// ExtraKernelArgs are appended to the kernel command line at install time.
	// This is where the management link is wired:
	//   siderolink.api=grpc://<endpoint>?jointoken=<binding-token>
	// NOTE: effective only when the installer runs (metal). Cloud VMs boot a
	// preinstalled image — their node images must carry the management-link
	// kernel args via an Image Factory schematic (tenant-scoped token).
	ExtraKernelArgs []string
}

// BootstrapConfig renders the minimal bootstrap machine config YAML.
func BootstrapConfig(p BootstrapParams) (string, error) {
	var b strings.Builder
	b.WriteString("machine:\n")
	b.WriteString("    install:\n")
	if p.InstallDisk != "" {
		fmt.Fprintf(&b, "        disk: %s\n", p.InstallDisk)
	}
	if len(p.ExtraKernelArgs) > 0 {
		b.WriteString("        extraKernelArgs:\n")
		for _, arg := range p.ExtraKernelArgs {
			if strings.ContainsAny(arg, "\n\"") {
				return "", fmt.Errorf("invalid kernel arg %q", arg)
			}
			fmt.Fprintf(&b, "            - %q\n", arg)
		}
	}
	return b.String(), nil
}

// SiderolinkKernelArg renders the management-link kernel argument with the
// machine's binding token.
func SiderolinkKernelArg(endpoint, bindingToken string) string {
	return fmt.Sprintf("siderolink.api=grpc://%s?jointoken=%s", endpoint, bindingToken)
}
