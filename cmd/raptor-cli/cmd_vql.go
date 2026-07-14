package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(vqlCmd)
	vqlCmd.AddCommand(vqlRunCmd)
}

var vqlCmd = &cobra.Command{
	Use:   "vql",
	Short: "Raw VQL execution (requires --dangerous)",
}

var vqlRunCmd = &cobra.Command{
	Use:   "run <query>",
	Short: "Execute a raw VQL query",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !cfg.EnableDangerousTools {
			return fmt.Errorf("vql run requires --dangerous flag")
		}
		rows, err := client.RunVQL(ctx(), args[0], orgID())
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}
