package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/rezuscloud/rezuscloud/internal/cli/apiclient"
)

// API token response shapes (mirror internal/auth/apitokens.go).
type apiTokenResponse struct {
	ID        string `json:"id"`
	UserName  string `json:"userName"`
	Role      string `json:"role"`
	ExpiresAt *int64 `json:"expiresAt,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	LastUsed  *int64 `json:"lastUsed,omitempty"`
}

type apiTokenCreateResponse struct {
	apiTokenResponse
	Secret string `json:"secret"`
}

func newAPITokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apitoken",
		Short: "Manage API tokens (Bearer token auth)",
	}
	cmd.AddCommand(newAPITokenCreateCmd())
	cmd.AddCommand(newAPITokenListCmd())
	cmd.AddCommand(newAPITokenDeleteCmd())
	return cmd
}

func newAPITokenCreateCmd() *cobra.Command {
	var (
		user string
		days int
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an API token for a user (prints the plaintext secret exactly once)",
		RunE: func(c *cobra.Command, _ []string) error {
			client, err := newClientFromFlags()
			if err != nil {
				return err
			}

			body, _ := json.Marshal(map[string]any{"expiresInDays": days})
			path := fmt.Sprintf("api/v1/users/%s/api-tokens", user)
			raw, err := client.Create(c.Context(), path, &apiclient.Resource{
				Kind: "APIToken",
				Spec: map[string]any{"expiresInDays": days},
			})
			if err != nil {
				return fmt.Errorf("create api token: %w", err)
			}
			_ = body

			// Decode the apiclient.Resource → apitoken shape.
			secret, _ := raw.Spec.(map[string]any)["secret"].(string)
			if secret == "" {
				// Fallback: parse raw marshaled JSON.
				buf, _ := json.Marshal(raw.Spec)
				var t apiTokenCreateResponse
				if err := json.Unmarshal(buf, &t); err == nil {
					secret = t.Secret
				}
			}
			if secret == "" {
				return fmt.Errorf("token created but secret missing from response")
			}

			fmt.Println(secret)
			return nil
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "owner of the token (defaults to current user)")
	cmd.Flags().IntVar(&days, "expires-in-days", 30, "expiry in days; 0 = never expires")
	return cmd
}

func newAPITokenListCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List API tokens (admin sees all; --user filters to one user)",
		RunE: func(c *cobra.Command, _ []string) error {
			client, err := newClientFromFlags()
			if err != nil {
				return err
			}

			path := "api/v1/api-tokens"
			if user != "" {
				path = fmt.Sprintf("api/v1/users/%s/api-tokens", user)
			}
			list, err := client.List(c.Context(), path, listOptsFromFlags())
			if err != nil {
				return fmt.Errorf("list api tokens: %w", err)
			}

			if list.Total == 0 {
				fmt.Println("No API tokens found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "ID\tOWNER\tROLE\tCREATED\tEXPIRES\tLAST USED\n")
			for _, item := range list.Items {
				buf, _ := json.Marshal(item.Spec)
				var tok apiTokenResponse
				_ = json.Unmarshal(buf, &tok)
				if tok.ID == "" && item.Metadata != nil {
					// Some responses stash id under metadata; fallback.
					tok.ID = item.Metadata.Name
				}

				created := "-"
				if tok.CreatedAt > 0 {
					created = strconv.FormatInt(tok.CreatedAt, 10)
				}
				expires := "never"
				if tok.ExpiresAt != nil {
					expires = strconv.FormatInt(*tok.ExpiresAt, 10)
				}
				lastUsed := "—"
				if tok.LastUsed != nil {
					lastUsed = strconv.FormatInt(*tok.LastUsed, 10)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", tok.ID, tok.UserName, tok.Role, created, expires, lastUsed)
			}
			_ = w.Flush()
			return nil
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "filter to a single user")
	return cmd
}

func newAPITokenDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <token-id>",
		Short: "Revoke an API token by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			client, err := newClientFromFlags()
			if err != nil {
				return err
			}
			if _, err := client.Delete(c.Context(), "api/v1/api-tokens", args[0]); err != nil {
				return fmt.Errorf("revoke api token: %w", err)
			}
			fmt.Printf("Token %q revoked.\n", args[0])
			return nil
		},
	}
}
