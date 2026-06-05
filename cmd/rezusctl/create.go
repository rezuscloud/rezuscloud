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

func newCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create -f <file>",
		Short: "Create a resource from a file",
		RunE:  runCreate,
	}

	cmd.Flags().StringP("filename", "f", "", "filename to create from")
	_ = cmd.MarkFlagRequired("filename")

	return cmd
}

func runCreate(cmd *cobra.Command, _ []string) error {
	filename, _ := cmd.Flags().GetString("filename")

	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

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

	if !rt.SupportsVerb("create") {
		return fmt.Errorf("resource type %q does not support create", rt.Kind)
	}

	path, err := rt.APIPath(flagCluster)
	if err != nil {
		return err
	}

	client, err := getClient()
	if err != nil {
		return err
	}

	created, err := client.Create(cmd.Context(), path, &resource)
	if err != nil {
		return err
	}

	fmt.Printf("%s %q created\n", rt.Kind, created.Metadata.Name)
	return nil
}
