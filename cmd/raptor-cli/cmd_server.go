package main

import "github.com/spf13/cobra"

func init() {
	rootCmd.AddCommand(serverCmd)
	serverCmd.AddCommand(serverHealthCmd)
}

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Server operations",
}

var serverHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Check server health",
	RunE: func(cmd *cobra.Command, args []string) error {
		status, err := client.Health(ctx())
		if err != nil {
			return err
		}
		printRows([]map[string]any{{"status": status}})
		return nil
	},
}
