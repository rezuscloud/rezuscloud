package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/rezuscloud/rezuscloud/internal/cli/registry"
)

func newDescribeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "describe <type> <name>",
		Short: "Show details of a specific resource",
		Args:  cobra.ExactArgs(2),
		RunE:  runDescribe,
	}
}

func runDescribe(cmd *cobra.Command, args []string) error {
	typeName := args[0]
	name := args[1]

	reg := registry.New()
	rt, err := reg.Resolve(typeName)
	if err != nil {
		return err
	}

	path, err := rt.APIPath(flagCluster)
	if err != nil {
		return err
	}

	client, err := getClient()
	if err != nil {
		return err
	}

	resource, err := client.Get(cmd.Context(), path, name)
	if err != nil {
		return err
	}

	// Print metadata.
	fmt.Fprintf(os.Stdout, "Kind: %s\n", resource.Kind)
	if resource.Metadata != nil {
		fmt.Fprintf(os.Stdout, "Name: %s\n", resource.Metadata.Name)
		if resource.Metadata.UID != "" {
			fmt.Fprintf(os.Stdout, "UID: %s\n", resource.Metadata.UID)
		}
		if resource.Metadata.ResourceVersion > 0 {
			fmt.Fprintf(os.Stdout, "Resource Version: %d\n", resource.Metadata.ResourceVersion)
		}
		if resource.Metadata.CreatedAt != "" {
			fmt.Fprintf(os.Stdout, "Created: %s\n", resource.Metadata.CreatedAt)
		}
		if resource.Metadata.UpdatedAt != "" {
			fmt.Fprintf(os.Stdout, "Updated: %s\n", resource.Metadata.UpdatedAt)
		}
		if resource.Metadata.DeletionTimestamp != nil {
			fmt.Fprintf(os.Stdout, "Deletion: %s\n", *resource.Metadata.DeletionTimestamp)
		}
		if len(resource.Metadata.Finalizers) > 0 {
			fmt.Fprintf(os.Stdout, "Finalizers: %v\n", resource.Metadata.Finalizers)
		}
		if len(resource.Metadata.Labels) > 0 {
			fmt.Fprintln(os.Stdout, "Labels:")
			for k, v := range resource.Metadata.Labels {
				fmt.Fprintf(os.Stdout, "  %s=%s\n", k, v)
			}
		}
	}

	// Print spec.
	if resource.Spec != nil {
		fmt.Fprintln(os.Stdout, "Spec:")
		specData, _ := json.MarshalIndent(resource.Spec, "  ", "  ")
		fmt.Fprintf(os.Stdout, "  %s\n", string(specData))
	}

	// Print status.
	if resource.Status != nil {
		fmt.Fprintln(os.Stdout, "Status:")
		statusData, _ := json.MarshalIndent(resource.Status, "  ", "  ")
		fmt.Fprintf(os.Stdout, "  %s\n", string(statusData))
	}

	return nil
}
