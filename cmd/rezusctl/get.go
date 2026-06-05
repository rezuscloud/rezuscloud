package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/rezuscloud/rezuscloud/internal/cli/apiclient"
	"github.com/rezuscloud/rezuscloud/internal/cli/registry"
	"github.com/rezuscloud/rezuscloud/internal/cli/rezusconfig"
)

func newGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <type> [<name>]",
		Short: "Display one or many resources",
		Long:  "Get a specific resource by name or list resources of a given type.\nSupports output formats: -o table (default), -o wide, -o yaml, -o json.",
		Args:  cobra.RangeArgs(1, 2),
		RunE:  runGet,
	}
}

func runGet(cmd *cobra.Command, args []string) error {
	typeName := args[0]

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

	// Single resource by name.
	if len(args) == 2 {
		resource, err := client.Get(cmd.Context(), path, args[1])
		if err != nil {
			return err
		}
		return printResource(resource)
	}

	// List resources.
	opts := apiclient.ListOptions{
		LabelSelector: flagSelector,
	}
	list, err := client.List(cmd.Context(), path, opts)
	if err != nil {
		return err
	}

	return printList(list)
}

func getClient() (*apiclient.Client, error) {
	cfgPath := flagConfig
	if cfgPath == "" {
		var err error
		cfgPath, err = rezusconfig.DefaultPath()
		if err != nil {
			return nil, err
		}
	}

	cfg, err := rezusconfig.Load(cfgPath)
	if err != nil {
		return nil, err
	}

	ctx := cfg.Current()
	if ctx == nil || ctx.URL == "" {
		return nil, fmt.Errorf("no management plane configured. Run: rezusctl config url <url>")
	}

	return apiclient.New(ctx.URL, ctx.Token), nil
}

func printList(list *apiclient.ListResponse) error {
	switch flagOutput {
	case "yaml":
		data, err := yaml.Marshal(list.Items)
		if err != nil {
			return err
		}
		fmt.Print(string(data))
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(list.Items)
	default:
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tKIND")
		for _, item := range list.Items {
			name := ""
			if item.Metadata != nil {
				name = item.Metadata.Name
			}
			fmt.Fprintf(w, "%s\t%s\n", name, item.Kind)
		}
		_ = w.Flush()
	}
	return nil
}
