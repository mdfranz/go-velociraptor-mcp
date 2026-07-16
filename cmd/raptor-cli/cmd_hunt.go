package main

import (
	"fmt"

	"github.com/mdfranz/go-velociraptor-mcp/internal/raptor"
	"github.com/spf13/cobra"
)

var (
	flagHuntListLimit     int
	flagHuntDescribeID    string
	flagHuntSummary       bool
	flagHuntFlowsID       string
	flagHuntFlowsLimit    int
	flagHuntFlowsStart    int
	flagHuntFlowsFull     bool
	flagHuntResultsID     string
	flagHuntResultsArt    string
	flagHuntResultsSource string
	flagHuntResultsFields string
	flagHuntResultsLimit  int
)

func init() {
	rootCmd.AddCommand(huntCmd)
	huntCmd.AddCommand(huntListCmd, huntDescribeCmd, huntFlowsCmd, huntResultsCmd)

	huntListCmd.Flags().IntVar(&flagHuntListLimit, "limit", 50, "max results")

	huntDescribeCmd.Flags().StringVar(&flagHuntDescribeID, "hunt", "", "hunt_id (required)")
	huntDescribeCmd.Flags().BoolVar(&flagHuntSummary, "summary", false, "return summary metadata only")
	_ = huntDescribeCmd.MarkFlagRequired("hunt")

	huntFlowsCmd.Flags().StringVar(&flagHuntFlowsID, "hunt", "", "hunt_id (required)")
	huntFlowsCmd.Flags().IntVar(&flagHuntFlowsLimit, "limit", 50, "max results")
	huntFlowsCmd.Flags().IntVar(&flagHuntFlowsStart, "start", 0, "zero-based starting row")
	huntFlowsCmd.Flags().BoolVar(&flagHuntFlowsFull, "full", false, "include full flow details")
	_ = huntFlowsCmd.MarkFlagRequired("hunt")

	huntResultsCmd.Flags().StringVar(&flagHuntResultsID, "hunt", "", "hunt_id (required)")
	huntResultsCmd.Flags().StringVar(&flagHuntResultsArt, "artifact", "", "artifact name (required)")
	huntResultsCmd.Flags().StringVar(&flagHuntResultsSource, "source", "", "source name for multi-source artifacts")
	huntResultsCmd.Flags().StringVar(&flagHuntResultsFields, "fields", "*", "fields to return")
	huntResultsCmd.Flags().IntVar(&flagHuntResultsLimit, "limit", 100, "max results")
	_ = huntResultsCmd.MarkFlagRequired("hunt")
	_ = huntResultsCmd.MarkFlagRequired("artifact")
}

var huntCmd = &cobra.Command{
	Use:   "hunt",
	Short: "Hunt management",
}

var huntListCmd = &cobra.Command{
	Use:   "list",
	Short: "List hunts",
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagHuntListLimit <= 0 || flagHuntListLimit > 1000 {
			return fmt.Errorf("limit must be between 1 and 1000")
		}
		vql := fmt.Sprintf(`
SELECT hunt_id,
       hunt_description,
       state,
       timestamp(epoch=create_time) AS created,
       timestamp(epoch=start_time) AS started,
       timestamp(epoch=expires) AS expires,
       stats.total_clients_scheduled AS clients_scheduled,
       stats.total_clients_with_results AS clients_with_results,
       stats.total_clients_without_results AS clients_without_results,
       stats.total_clients_with_errors AS clients_with_errors,
       stats.total_collected_rows AS rows,
       stats.total_uploaded_bytes AS uploaded_bytes,
       creator
FROM hunts()
ORDER BY created DESC LIMIT %d`, flagHuntListLimit)
		rows, err := client.RunVQL(ctx(), vql, orgID())
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}

var huntDescribeCmd = &cobra.Command{
	Use:   "describe",
	Short: "Describe a hunt",
	RunE: func(cmd *cobra.Command, args []string) error {
		vql := fmt.Sprintf(
			`SELECT * FROM hunts(hunt_id=%s, summary=%s) LIMIT 1`,
			quote(flagHuntDescribeID), boolLiteral(flagHuntSummary))
		rows, err := client.RunVQL(ctx(), vql, orgID())
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}

var huntFlowsCmd = &cobra.Command{
	Use:   "flows",
	Short: "List flows launched by a hunt",
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagHuntFlowsLimit <= 0 || flagHuntFlowsLimit > 1000 {
			return fmt.Errorf("limit must be between 1 and 1000")
		}
		if flagHuntFlowsStart < 0 {
			return fmt.Errorf("start must be zero or greater")
		}
		vql := fmt.Sprintf(
			`SELECT * FROM hunt_flows(hunt_id=%s, start_row=%d, basic_info=%s) LIMIT %d`,
			quote(flagHuntFlowsID), flagHuntFlowsStart,
			boolLiteral(!flagHuntFlowsFull), flagHuntFlowsLimit)
		rows, err := client.RunVQL(ctx(), vql, orgID())
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}

var huntResultsCmd = &cobra.Command{
	Use:   "results",
	Short: "Retrieve results from a hunt",
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagHuntResultsLimit <= 0 || flagHuntResultsLimit > 10000 {
			return fmt.Errorf("limit must be between 1 and 10000")
		}
		fields, err := raptor.ValidateFieldList(flagHuntResultsFields)
		if err != nil {
			return err
		}
		vql := fmt.Sprintf(
			`SELECT %s FROM hunt_results(hunt_id=%s, artifact=%s, source=%s) LIMIT %d`,
			fields, quote(flagHuntResultsID), quote(flagHuntResultsArt),
			quote(flagHuntResultsSource), flagHuntResultsLimit)
		rows, err := client.RunVQL(ctx(), vql, orgID())
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}

func boolLiteral(value bool) string {
	if value {
		return "TRUE"
	}
	return "FALSE"
}
