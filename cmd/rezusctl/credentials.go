package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newKubeconfigCmd() *cobra.Command {
	var outputPath string

	cmd := &cobra.Command{
		Use:   "kubeconfig <cluster>",
		Short: "Fetch kubeconfig for a cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			client, err := newClientFromFlags()
			if err != nil {
				return err
			}

			path := "api/v1/tenants/" + args[0] + "/kubeconfig"
			data, err := client.RawGet(c.Context(), path)
			if err != nil {
				return fmt.Errorf("fetch kubeconfig: %w", err)
			}

			return writeCredential(data, outputPath, "kubeconfig")
		},
	}

	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "output file path (default: stdout)")

	return cmd
}

func newTalosconfigCmd() *cobra.Command {
	var outputPath string

	cmd := &cobra.Command{
		Use:   "talosconfig <cluster>",
		Short: "Fetch talosconfig for a cluster",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			client, err := newClientFromFlags()
			if err != nil {
				return err
			}

			path := "api/v1/tenants/" + args[0] + "/talosconfig"
			data, err := client.RawGet(c.Context(), path)
			if err != nil {
				return fmt.Errorf("fetch talosconfig: %w", err)
			}

			return writeCredential(data, outputPath, "talosconfig")
		},
	}

	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "output file path (default: stdout)")

	return cmd
}

// writeCredential writes credential data to a file or stdout.
func writeCredential(data []byte, outputPath, kind string) error {
	if outputPath == "" {
		fmt.Print(string(data))
		return nil
	}

	if err := os.WriteFile(outputPath, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", kind, err)
	}

	fmt.Printf("%s written to %s\n", kind, outputPath)
	return nil
}
