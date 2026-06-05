package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/rezuscloud/rezuscloud/internal/cli/apiclient"
)

func newClusterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage RezusCloud clusters",
	}

	cmd.AddCommand(newClusterStatusCmd())
	cmd.AddCommand(newClusterDeleteCmd())

	return cmd
}

func newClusterStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <name>",
		Short: "Show cluster status and health",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			client, err := newClientFromFlags()
			if err != nil {
				return err
			}

			tenant, err := client.Get(c.Context(), "api/v1/tenants", args[0])
			if err != nil {
				return fmt.Errorf("get cluster: %w", err)
			}

			return printClusterStatus(tenant)
		},
	}
}

func newClusterDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			client, err := newClientFromFlags()
			if err != nil {
				return err
			}

			deleted, err := client.Delete(c.Context(), "api/v1/tenants", args[0])
			if err != nil {
				return fmt.Errorf("delete cluster: %w", err)
			}

			if deleted != nil && deleted.Metadata != nil && deleted.Metadata.DeletionTimestamp != nil {
				fmt.Printf("Cluster %q marked for deletion (graceful).\n", args[0])
			} else {
				fmt.Printf("Cluster %q deleted.\n", args[0])
			}
			return nil
		},
	}
}

func printClusterStatus(r *apiclient.Resource) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	meta := r.Metadata
	if meta != nil {
		fmt.Fprintf(w, "Name:\t%s\n", meta.Name)
		fmt.Fprintf(w, "UID:\t%s\n", meta.UID)
		fmt.Fprintf(w, "Created:\t%s\n", meta.CreatedAt)
		if meta.DeletionTimestamp != nil {
			fmt.Fprintf(w, "Deleting:\t%s\n", *meta.DeletionTimestamp)
		}
		if meta.Labels != nil {
			fmt.Fprintf(w, "Labels:\t%v\n", meta.Labels)
		}
	}

	// Spec.
	if spec, ok := r.Spec.(map[string]any); ok {
		if v, ok := spec["kubernetesVersion"].(string); ok {
			fmt.Fprintf(w, "Kubernetes:\t%s\n", v)
		}
		if v, ok := spec["talosVersion"].(string); ok {
			fmt.Fprintf(w, "Talos:\t%s\n", v)
		}
		if v, ok := spec["locked"].(bool); ok && v {
			fmt.Fprintf(w, "Locked:\ttrue\n")
		}
	}

	// Status.
	if status, ok := r.Status.(map[string]any); ok {
		if v, ok := status["phase"].(string); ok {
			fmt.Fprintf(w, "Phase:\t%s\n", v)
		}
		if v, ok := status["available"].(bool); ok {
			fmt.Fprintf(w, "Available:\t%t\n", v)
		}
		if v, ok := status["ready"].(bool); ok {
			fmt.Fprintf(w, "Ready:\t%t\n", v)
		}
		if v, ok := status["machineCount"].(float64); ok {
			fmt.Fprintf(w, "Machines:\t%.0f\n", v)
		}
	}

	_ = w.Flush()

	// Print full JSON for wide/json output.
	if flagOutput == "wide" || flagOutput == "json" {
		encoded, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(encoded))
	}

	return nil
}
