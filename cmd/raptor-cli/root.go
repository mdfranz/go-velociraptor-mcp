package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mdfranz/go-velociraptor-mcp/internal/raptor"
	"github.com/spf13/cobra"
)

var (
	flagConfig string
	flagOrgID  string
	flagOutput string

	client        *raptor.Client
	cfg           *raptor.Config
	commandCtx    = context.Background()
	commandCancel context.CancelFunc
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
		baseCtx := cmd.Context()
		if baseCtx == nil {
			baseCtx = context.Background()
		}
		commandCtx, commandCancel = context.WithTimeout(baseCtx, cfg.DefaultTimeout)
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
}

func orgID() string {
	return client.OrgID(flagOrgID)
}

func ctx() context.Context {
	return commandCtx
}

func printErr(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
}

func closeRuntime() {
	if client != nil {
		client.Close()
		client = nil
	}
	if commandCancel != nil {
		commandCancel()
		commandCancel = nil
	}
	commandCtx = context.Background()
}
