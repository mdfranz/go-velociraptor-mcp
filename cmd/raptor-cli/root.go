package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mdfranz/go-velociraptor-mcp/internal/raptor"
	"github.com/spf13/cobra"
)

var (
	flagConfig    string
	flagOrgID     string
	flagOutput    string
	flagDangerous bool

	client *raptor.Client
	cfg    *raptor.Config
)

var rootCmd = &cobra.Command{
	Use:           "raptor-cli",
	Short:         "Velociraptor CLI via gRPC/mTLS",
	Version:       version,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		var err error
		cfg, err = raptor.LoadConfig(flagConfig)
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		if flagOrgID != "" {
			cfg.OrgID = flagOrgID
		}
		if flagDangerous {
			cfg.EnableDangerousTools = true
		}
		client, err = raptor.NewClient(cfg)
		if err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagConfig, "config", "", "path to api_client.yaml")
	rootCmd.PersistentFlags().StringVar(&flagOrgID, "org", "", "default org ID")
	rootCmd.PersistentFlags().StringVarP(&flagOutput, "output", "o", "table", "output format: table, json, yaml")
	rootCmd.PersistentFlags().BoolVar(&flagDangerous, "dangerous", false, "enable dangerous tools")
}

func orgID() string {
	return client.OrgID(flagOrgID)
}

func ctx() context.Context {
	return context.Background()
}

func printErr(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
}
