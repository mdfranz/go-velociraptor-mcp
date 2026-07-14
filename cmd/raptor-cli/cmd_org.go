package main

import (
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(orgCmd)
	orgCmd.AddCommand(orgListCmd)
}

var orgCmd = &cobra.Command{
	Use:   "org",
	Short: "Org management",
}

var orgListCmd = &cobra.Command{
	Use:   "list",
	Short: "List organizations",
	RunE: func(cmd *cobra.Command, args []string) error {
		rows, err := client.RunVQL(ctx(), `SELECT OrgId, Name, Nonce FROM orgs()`, "")
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}
