package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newMachineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "machine",
		Short: "Manage machines",
	}

	cmd.AddCommand(newMachineListCmd())
	cmd.AddCommand(newMachineGetCmd())

	return cmd
}

func newMachineListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List machines",
		RunE: func(c *cobra.Command, _ []string) error {
			client, err := newClientFromFlags()
			if err != nil {
				return err
			}

			var path string
			if flagCluster != "" {
				path = "api/v1/tenants/" + flagCluster + "/machines"
			} else {
				path = "api/v1/machines"
			}

			list, err := client.List(c.Context(), path, listOptsFromFlags())
			if err != nil {
				return fmt.Errorf("list machines: %w", err)
			}

			if list.Total == 0 {
				fmt.Println("No machines found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "ID\tSTAGE\tCONNECTED\tCLUSTER\n")
			for _, item := range list.Items {
				id := ""
				if item.Metadata != nil {
					id = item.Metadata.Name
				}
				stage := "-"
				connected := "-"
				cluster := "-"
				if spec, ok := item.Spec.(map[string]any); ok {
					if v, ok := spec["stage"].(string); ok {
						stage = v
					}
					if v, ok := spec["connected"].(bool); ok {
						if v {
							connected = "yes"
						} else {
							connected = "no"
						}
					}
				}
				if item.Metadata != nil && item.Metadata.Labels != nil {
					if v, ok := item.Metadata.Labels["rezuscloud.io/tenant"]; ok {
						cluster = v
					}
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", id, stage, connected, cluster)
			}
			_ = w.Flush()
			return nil
		},
	}
}

func newMachineGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Show machine details",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			client, err := newClientFromFlags()
			if err != nil {
				return err
			}

			machine, err := client.Get(c.Context(), "api/v1/machines", args[0])
			if err != nil {
				return fmt.Errorf("get machine: %w", err)
			}

			return printResource(machine)
		},
	}
}
