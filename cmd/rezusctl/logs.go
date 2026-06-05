package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
	var follow bool
	var tail int

	cmd := &cobra.Command{
		Use:   "logs <machine-id>",
		Short: "Stream machine logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			client, err := newClientFromFlags()
			if err != nil {
				return err
			}

			cluster := flagCluster
			if cluster == "" {
				return fmt.Errorf("--cluster/-c is required for logs")
			}

			path := "api/v1/tenants/" + cluster + "/machines/" + args[0] + "/logs"

			// Build query params.
			params := []string{}
			if follow {
				params = append(params, "follow=true")
			}
			if tail > 0 {
				params = append(params, fmt.Sprintf("tail=%d", tail))
			}
			if len(params) > 0 {
				path = path + "?" + strings.Join(params, "&")
			}

			body, err := client.StreamGet(c.Context(), path)
			if err != nil {
				return fmt.Errorf("stream logs: %w", err)
			}
			defer func() { _ = body.Close() }()

			scanner := bufio.NewScanner(body)
			for scanner.Scan() {
				line := scanner.Text()
				// SSE format: "data: {json}"
				if strings.HasPrefix(line, "data: ") {
					fmt.Println(strings.TrimPrefix(line, "data: "))
				}
			}

			if err := scanner.Err(); err != nil && err != io.EOF {
				return fmt.Errorf("read stream: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow log output")
	cmd.Flags().IntVar(&tail, "tail", 0, "show last N lines (0 = all)")

	return cmd
}
