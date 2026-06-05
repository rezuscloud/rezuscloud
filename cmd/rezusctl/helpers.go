package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/rezuscloud/rezuscloud/internal/cli/apiclient"
	"github.com/rezuscloud/rezuscloud/internal/cli/rezusconfig"
)

// newClientFromFlags builds an API client from persistent flags and config.
// Returns an error if no URL is configured.
func newClientFromFlags() (*apiclient.Client, error) {
	path := flagConfig
	if path == "" {
		p, err := rezusconfig.DefaultPath()
		if err != nil {
			return nil, fmt.Errorf("config path: %w", err)
		}
		path = p
	}

	cfg, err := rezusconfig.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	ctx := cfg.Current()
	if ctx == nil {
		return nil, fmt.Errorf("no current context — run: rezusctl config url <url>")
	}
	if ctx.URL == "" {
		return nil, fmt.Errorf("no URL configured — run: rezusctl config url <url>")
	}

	return apiclient.New(ctx.URL, ctx.Token), nil
}

// listOptsFromFlags builds ListOptions from persistent flags.
func listOptsFromFlags() apiclient.ListOptions {
	return apiclient.ListOptions{
		LabelSelector: flagSelector,
	}
}

// printResource prints a resource in the configured output format.
func printResource(r *apiclient.Resource) error {
	switch flagOutput {
	case "json":
		encoded, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return fmt.Errorf("encode: %w", err)
		}
		fmt.Println(string(encoded))
		return nil

	case "yaml":
		encoded, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return fmt.Errorf("encode: %w", err)
		}
		fmt.Println(string(encoded))
		return nil

	default:
		// Tabwriter key:value format.
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		if r.Metadata != nil {
			fmt.Fprintf(w, "Name:\t%s\n", r.Metadata.Name)
			fmt.Fprintf(w, "UID:\t%s\n", r.Metadata.UID)
			fmt.Fprintf(w, "Created:\t%s\n", r.Metadata.CreatedAt)
			if r.Metadata.DeletionTimestamp != nil {
				fmt.Fprintf(w, "Deleting:\t%s\n", *r.Metadata.DeletionTimestamp)
			}
			if len(r.Metadata.Labels) > 0 {
				fmt.Fprintf(w, "Labels:\t%v\n", r.Metadata.Labels)
			}
			if len(r.Metadata.Annotations) > 0 {
				fmt.Fprintf(w, "Annotations:\t%v\n", r.Metadata.Annotations)
			}
		}
		if r.Spec != nil {
			specJSON, _ := json.MarshalIndent(r.Spec, "", "  ")
			fmt.Fprintf(w, "Spec:\t%s\n", string(specJSON))
		}
		if r.Status != nil {
			statusJSON, _ := json.MarshalIndent(r.Status, "", "  ")
			fmt.Fprintf(w, "Status:\t%s\n", string(statusJSON))
		}
		_ = w.Flush()
		return nil
	}
}
