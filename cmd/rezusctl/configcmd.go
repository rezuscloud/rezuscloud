package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rezuscloud/rezuscloud/internal/cli/rezusconfig"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage rezusctl configuration",
	}

	cmd.AddCommand(newConfigURLCmd())
	cmd.AddCommand(newConfigContextCmd())
	cmd.AddCommand(newConfigContextsCmd())
	cmd.AddCommand(newConfigInfoCmd())

	return cmd
}

func newConfigURLCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "url <url>",
		Short: "Set the management plane URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, path, err := loadConfig()
			if err != nil {
				return err
			}

			cfg.SetURL(args[0])
			if err := cfg.Save(path); err != nil {
				return err
			}

			fmt.Printf("Set management plane URL to %q\n", args[0])
			return nil
		},
	}
}

func newConfigContextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "context <name>",
		Short: "Switch to a different context",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, path, err := loadConfig()
			if err != nil {
				return err
			}

			if err := cfg.SwitchContext(args[0]); err != nil {
				return err
			}
			if err := cfg.Save(path); err != nil {
				return err
			}

			fmt.Printf("Switched to context %q\n", args[0])
			return nil
		},
	}
}

func newConfigContextsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "contexts",
		Short: "List available contexts",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, _, err := loadConfig()
			if err != nil {
				return err
			}

			if len(cfg.Contexts) == 0 {
				fmt.Println("No contexts configured. Run: rezusctl config url <url>")
				return nil
			}

			for _, ctx := range cfg.Contexts {
				marker := " "
				if ctx.Name == cfg.CurrentContext {
					marker = "*"
				}
				fmt.Printf("%s %s\t%s\n", marker, ctx.Name, ctx.URL)
			}
			return nil
		},
	}
}

func newConfigInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show current context info",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, _, err := loadConfig()
			if err != nil {
				return err
			}

			ctx := cfg.Current()
			if ctx == nil {
				fmt.Println("No context configured. Run: rezusctl config url <url>")
				return nil
			}

			fmt.Printf("Context: %s\n", ctx.Name)
			fmt.Printf("URL:     %s\n", ctx.URL)
			if ctx.Token != "" {
				fmt.Printf("Token:   ****%s\n", lastN(ctx.Token, 8))
			}
			return nil
		},
	}
}

func loadConfig() (*rezusconfig.Config, string, error) {
	cfgPath := flagConfig
	if cfgPath == "" {
		var err error
		cfgPath, err = rezusconfig.DefaultPath()
		if err != nil {
			return nil, "", err
		}
	}

	cfg, err := rezusconfig.Load(cfgPath)
	if err != nil {
		return nil, "", err
	}
	return cfg, cfgPath, nil
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
