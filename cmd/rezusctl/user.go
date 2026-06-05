package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/rezuscloud/rezuscloud/internal/cli/apiclient"
)

func newUserCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage users (admin only)",
	}

	cmd.AddCommand(newUserCreateCmd())
	cmd.AddCommand(newUserListCmd())
	cmd.AddCommand(newUserDeleteCmd())

	return cmd
}

func newUserCreateCmd() *cobra.Command {
	var role string
	var password string

	cmd := &cobra.Command{
		Use:   "create <username>",
		Short: "Create a new user",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			client, err := newClientFromFlags()
			if err != nil {
				return err
			}

			if role == "" {
				return fmt.Errorf("--role is required (view, edit, admin)")
			}
			if password == "" {
				return fmt.Errorf("--password is required")
			}

			created, err := client.Create(c.Context(), "api/v1/users", &apiclient.Resource{
				Kind: "User",
				Metadata: &apiclient.ObjectMeta{
					Name: args[0],
				},
				Spec: map[string]any{
					"role":     role,
					"password": password,
				},
			})
			if err != nil {
				return fmt.Errorf("create user: %w", err)
			}

			fmt.Printf("User %q created with role %q.\n", created.Metadata.Name, role)
			return nil
		},
	}

	cmd.Flags().StringVar(&role, "role", "", "user role (view, edit, admin)")
	cmd.Flags().StringVar(&password, "password", "", "user password")

	return cmd
}

func newUserListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List users",
		RunE: func(c *cobra.Command, _ []string) error {
			client, err := newClientFromFlags()
			if err != nil {
				return err
			}

			list, err := client.List(c.Context(), "api/v1/users", listOptsFromFlags())
			if err != nil {
				return fmt.Errorf("list users: %w", err)
			}

			if list.Total == 0 {
				fmt.Println("No users found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "NAME\tROLE\tCREATED\n")
			for _, item := range list.Items {
				name := "-"
				created := "-"
				if item.Metadata != nil {
					name = item.Metadata.Name
					created = item.Metadata.CreatedAt
				}
				role := "-"
				if spec, ok := item.Spec.(map[string]any); ok {
					if v, ok := spec["role"].(string); ok {
						role = v
					}
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", name, role, created)
			}
			_ = w.Flush()
			return nil
		},
	}
}

func newUserDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <username>",
		Short: "Delete a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			client, err := newClientFromFlags()
			if err != nil {
				return err
			}

			_, err = client.Delete(c.Context(), "api/v1/users", args[0])
			if err != nil {
				return fmt.Errorf("delete user: %w", err)
			}

			fmt.Printf("User %q deleted.\n", args[0])
			return nil
		},
	}
}
