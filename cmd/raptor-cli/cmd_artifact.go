package main

import (
	"fmt"

	"github.com/mdfranz/go-velociraptor-mcp/internal/raptor"
	"github.com/spf13/cobra"
)

var (
	flagArtifactRegex string
)

func init() {
	rootCmd.AddCommand(artifactCmd)
	artifactCmd.AddCommand(artifactListCmd, artifactDetailsCmd)

	artifactListCmd.Flags().StringVar(&flagArtifactRegex, "filter", ".", "name regex filter")
}

var artifactCmd = &cobra.Command{
	Use:   "artifact",
	Short: "Artifact discovery",
}

var artifactListCmd = &cobra.Command{
	Use:   "list",
	Short: "List artifact definitions",
	RunE: func(cmd *cobra.Command, args []string) error {
		vql := fmt.Sprintf(`
SELECT name, description, type
FROM artifact_definitions()
WHERE name =~ %s`, quote(flagArtifactRegex))
		rows, err := client.RunVQL(ctx(), vql, orgID())
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}

var artifactDetailsCmd = &cobra.Command{
	Use:   "details <artifact-name>",
	Short: "Show full artifact metadata including parameters and sources",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := raptor.ValidateArtifactName(name); err != nil {
			return err
		}
		vql := fmt.Sprintf(`
SELECT name, description, parameters, sources
FROM artifact_definitions(names=[%s])`, quote(name))
		rows, err := client.RunVQL(ctx(), vql, orgID())
		if err != nil {
			return err
		}
		// details are complex — always JSON
		orig := flagOutput
		flagOutput = "json"
		printRows(rows)
		flagOutput = orig
		return nil
	},
}
