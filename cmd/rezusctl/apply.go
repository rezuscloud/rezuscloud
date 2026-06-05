package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/rezuscloud/rezuscloud/internal/cli/apiclient"
	"github.com/rezuscloud/rezuscloud/internal/cli/registry"
)

func newApplyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply -f <file>",
		Short: "Apply a configuration to a resource by file",
		RunE:  runApply,
	}

	cmd.Flags().StringP("filename", "f", "", "filename to apply")
	_ = cmd.MarkFlagRequired("filename")

	return cmd
}

func runApply(cmd *cobra.Command, _ []string) error {
	filename, _ := cmd.Flags().GetString("filename")

	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	// Try JSON first, then YAML.
	var resource apiclient.Resource
	if jsonErr := json.Unmarshal(data, &resource); jsonErr != nil {
		if yamlErr := yaml.Unmarshal(data, &resource); yamlErr != nil {
			return fmt.Errorf("parse resource: not valid JSON or YAML")
		}
	}

	if resource.Metadata == nil || resource.Metadata.Name == "" {
		return fmt.Errorf("resource must have metadata.name")
	}

	kind := resource.Kind
	if kind == "" {
		return fmt.Errorf("resource must have a kind")
	}

	// Map kind to resource type.
	reg := registry.New()
	var rt *registry.ResourceType
	for _, t := range reg.All() {
		if t.Kind == kind {
			rt = &t
			break
		}
	}
	if rt == nil {
		return fmt.Errorf("unknown resource kind %q", kind)
	}

	path, err := rt.APIPath(flagCluster)
	if err != nil {
		return err
	}

	client, err := getClient()
	if err != nil {
		return err
	}

	// Try to get existing resource first (update), or create if not found.
	existing, err := client.Get(cmd.Context(), path, resource.Metadata.Name)
	if err != nil {
		// Not found — create.
		created, createErr := client.Create(cmd.Context(), path, &resource)
		if createErr != nil {
			return createErr
		}
		fmt.Printf("%s %q created\n", rt.Kind, created.Metadata.Name)
		return nil
	}

	// Update — preserve resourceVersion.
	resource.Metadata.ResourceVersion = existing.Metadata.ResourceVersion
	updated, err := client.Update(cmd.Context(), path, resource.Metadata.Name, &resource)
	if err != nil {
		return err
	}
	fmt.Printf("%s %q configured\n", rt.Kind, updated.Metadata.Name)
	return nil
}
