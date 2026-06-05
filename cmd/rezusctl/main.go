package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/rezuscloud/rezuscloud/cmd/rezusctl/boot"
	"github.com/rezuscloud/rezuscloud/internal/cli/version"
)

var (
	Version   = version.Version
	GitCommit = version.GitCommit
	BuildTime = version.BuildTime
)

func main() {
	root := &cobra.Command{
		Use:   "rezusctl",
		Short: "CLI for RezusCloud management plane",
		Long:  "A kubectl-style CLI for managing RezusCloud clusters, machines, and infrastructure.",
	}

	root.PersistentFlags().StringVarP(&flagCluster, "cluster", "c", "", "tenant cluster name (scopes resource operations)")
	root.PersistentFlags().StringVarP(&flagOutput, "output", "o", "table", "output format (table, wide, yaml, json)")
	root.PersistentFlags().StringVarP(&flagSelector, "selector", "l", "", "label selector (key=value)")
	root.PersistentFlags().StringVar(&flagConfig, "rezusconfig", "", "path to config file (default: ~/.rezuscloud/config)")

	// Generic resource verbs.
	root.AddCommand(newGetCmd())
	root.AddCommand(newDeleteCmd())
	root.AddCommand(newApplyCmd())
	root.AddCommand(newCreateCmd())
	root.AddCommand(newDescribeCmd())

	// Specialized commands.
	root.AddCommand(newClusterCmd())
	root.AddCommand(newMachineCmd())
	root.AddCommand(newLogsCmd())
	root.AddCommand(newKubeconfigCmd())
	root.AddCommand(newTalosconfigCmd())
	root.AddCommand(newJointokenCmd())
	root.AddCommand(newUserCmd())

	// Config management.
	root.AddCommand(newConfigCmd())

	// API discovery.
	root.AddCommand(newAPIResourcesCmd())

	// Version.
	root.AddCommand(newVersionCmd())

	// Boot (standalone — no API needed).
	root.AddCommand(newBootCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func newBootCmd() *cobra.Command {
	opts := boot.DefaultOptions()

	cmd := &cobra.Command{
		Use:   "boot",
		Short: "Bootstrap a new RezusCloud management cluster",
		Long:  "Create a management cluster and deploy the RezusCloud management plane. This is the only command that does not require a running management plane.",
		RunE: func(c *cobra.Command, _ []string) error {
			if err := opts.Complete(); err != nil {
				return err
			}
			if err := opts.Validate(); err != nil {
				return err
			}
			return opts.Run(c.Context())
		},
	}

	cmd.Flags().StringVar(&opts.ClusterName, "name", "rezuscloud", "cluster name")
	cmd.Flags().StringVar(&opts.Platform, "platform", "docker", "platform (docker, qemu)")
	cmd.Flags().IntVar(&opts.ControlPlanes, "control-planes", 1, "number of control plane nodes")
	cmd.Flags().IntVar(&opts.Workers, "workers", 0, "number of worker nodes")
	cmd.Flags().StringVar(&opts.TalosVersion, "talos-version", "latest", "Talos version")
	cmd.Flags().StringVar(&opts.CiliumVersion, "cilium-version", "1.19.3", "Cilium version")
	cmd.Flags().StringVar(&opts.StateDir, "state-dir", ".rezusctl", "state directory")

	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("rezusctl %s\ncommit: %s\nbuilt: %s\n", Version, GitCommit, BuildTime)
		},
	}
}

// Persistent flags accessible by all commands.
var (
	flagCluster  string
	flagOutput   string
	flagSelector string
	flagConfig   string
)
