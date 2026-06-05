package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/rezuscloud/rezuscloud/internal/cli/registry"
)

func newAPIResourcesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "api-resources",
		Short: "Print the supported API resource types",
		Long:  "List all resource types that rezusctl can manage, with their short names and scopes.",
		RunE:  runAPIResources,
	}
}

func runAPIResources(_ *cobra.Command, _ []string) error {
	reg := registry.New()
	types := reg.All()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSHORT NAMES\tKIND\tSCOPE\tVERBS")

	for _, rt := range types {
		scope := "cluster"
		switch rt.Scope {
		case 1:
			scope = "cluster-optional"
		case 2:
			scope = "cluster-required"
		}

		shortNames := rt.Names[1:]
		shortStr := ""
		if len(shortNames) > 0 {
			for i, n := range shortNames {
				if i > 0 {
					shortStr += ", "
				}
				shortStr += n
			}
		}

		verbs := ""
		for i, v := range rt.Verbs {
			if i > 0 {
				verbs += ", "
			}
			verbs += v
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", rt.Names[0], shortStr, rt.Kind, scope, verbs)
	}

	return w.Flush()
}
