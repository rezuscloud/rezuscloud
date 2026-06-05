package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rezuscloud/rezuscloud/internal/cli/registry"
)

func newDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <type> <name>",
		Short: "Delete a resource by name",
		Args:  cobra.ExactArgs(2),
		RunE:  runDelete,
	}
}

func runDelete(cmd *cobra.Command, args []string) error {
	typeName := args[0]
	name := args[1]

	reg := registry.New()
	rt, err := reg.Resolve(typeName)
	if err != nil {
		return err
	}

	if !rt.SupportsVerb("delete") {
		return fmt.Errorf("resource type %q does not support delete", rt.Kind)
	}

	path, err := rt.APIPath(flagCluster)
	if err != nil {
		return err
	}

	client, err := getClient()
	if err != nil {
		return err
	}

	_, err = client.Delete(cmd.Context(), path, name)
	if err != nil {
		return err
	}

	fmt.Printf("%s %q deleted\n", rt.Kind, name)
	return nil
}
