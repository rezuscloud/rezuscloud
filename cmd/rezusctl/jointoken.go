package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/rezuscloud/rezuscloud/internal/cli/apiclient"
)

func newJointokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jointoken",
		Short: "Manage join tokens",
	}

	cmd.AddCommand(newJointokenCreateCmd())
	cmd.AddCommand(newJointokenListCmd())
	cmd.AddCommand(newJointokenDeleteCmd())

	return cmd
}

func newJointokenCreateCmd() *cobra.Command {
	var nodeGroup string

	cmd := &cobra.Command{
		Use:   "create -c <cluster>",
		Short: "Create a join token",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			client, err := newClientFromFlags()
			if err != nil {
				return err
			}

			if flagCluster == "" {
				return fmt.Errorf("--cluster/-c is required")
			}
			if nodeGroup == "" {
				return fmt.Errorf("--node-group is required")
			}

			path := "api/v1/tenants/" + flagCluster + "/join-tokens"
			created, err := client.Create(c.Context(), path, &apiclient.Resource{
				Spec: map[string]any{
					"nodeGroup": nodeGroup,
				},
			})
			if err != nil {
				return fmt.Errorf("create jointoken: %w", err)
			}

			// The API returns {token: "...", expiresAt: "...", spec: {...}}.
			// Extract the token from the raw spec response.
			if spec, ok := created.Spec.(map[string]any); ok {
				if token, ok := spec["token"].(string); ok {
					fmt.Println(token)
					return nil
				}
			}

			// Fallback: the token might be in a top-level field we couldn't unmarshal.
			fmt.Printf("Token created. Use 'rezusctl jointoken list -c %s' to view.\n", flagCluster)
			return nil
		},
	}

	cmd.Flags().StringVar(&nodeGroup, "node-group", "", "node group to join")

	return cmd
}

func newJointokenListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list -c <cluster>",
		Short: "List join tokens",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			client, err := newClientFromFlags()
			if err != nil {
				return err
			}

			if flagCluster == "" {
				return fmt.Errorf("--cluster/-c is required")
			}

			path := "api/v1/tenants/" + flagCluster + "/join-tokens"
			list, err := client.List(c.Context(), path, listOptsFromFlags())
			if err != nil {
				return fmt.Errorf("list jointokens: %w", err)
			}

			if list.Total == 0 {
				fmt.Println("No join tokens found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "NAME\tNODE GROUP\tEXPIRES\tUSED\n")
			for _, item := range list.Items {
				name := "-"
				if item.Metadata != nil {
					name = item.Metadata.Name
				}
				ng := "-"
				expires := "-"
				used := "-"
				if spec, ok := item.Spec.(map[string]any); ok {
					if v, ok := spec["nodeGroup"].(string); ok {
						ng = v
					}
					if v, ok := spec["expiresAt"].(string); ok {
						expires = v
					}
					if v, ok := spec["used"].(bool); ok && v {
						used = "yes"
					}
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, ng, expires, used)
			}
			_ = w.Flush()
			return nil
		},
	}
}

func newJointokenDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name> -c <cluster>",
		Short: "Delete a join token",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			client, err := newClientFromFlags()
			if err != nil {
				return err
			}

			if flagCluster == "" {
				return fmt.Errorf("--cluster/-c is required")
			}

			path := "api/v1/tenants/" + flagCluster + "/join-tokens"
			_, err = client.Delete(c.Context(), path, args[0])
			if err != nil {
				return fmt.Errorf("delete jointoken: %w", err)
			}

			fmt.Printf("Join token %q deleted.\n", args[0])
			return nil
		},
	}
}
