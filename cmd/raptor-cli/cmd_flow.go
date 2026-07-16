package main

import (
	"fmt"

	"github.com/mdfranz/go-velociraptor-mcp/internal/raptor"
	"github.com/spf13/cobra"
)

var (
	flagFlowListClientID string
	flagFlowListLimit    int
	flagFlowGetClientID  string
	flagFlowGetID        string
	flagFlowLogsClientID string
	flagFlowLogsID       string
	flagFlowLogsMatch    string
	flagFlowLogsLimit    int
)

func init() {
	rootCmd.AddCommand(flowCmd)
	flowCmd.AddCommand(flowListCmd, flowGetCmd, flowLogsCmd)

	flowListCmd.Flags().StringVar(&flagFlowListClientID, "client", "", "client_id (required)")
	flowListCmd.Flags().IntVar(&flagFlowListLimit, "limit", 20, "max results")
	_ = flowListCmd.MarkFlagRequired("client")

	flowGetCmd.Flags().StringVar(&flagFlowGetClientID, "client", "", "client_id (required)")
	flowGetCmd.Flags().StringVar(&flagFlowGetID, "flow", "", "flow_id (required)")
	_ = flowGetCmd.MarkFlagRequired("client")
	_ = flowGetCmd.MarkFlagRequired("flow")

	flowLogsCmd.Flags().StringVar(&flagFlowLogsClientID, "client", "", "client_id (required)")
	flowLogsCmd.Flags().StringVar(&flagFlowLogsID, "flow", "", "flow_id (required)")
	flowLogsCmd.Flags().StringVar(&flagFlowLogsMatch, "match", ".", "regex to filter log messages")
	flowLogsCmd.Flags().IntVar(&flagFlowLogsLimit, "limit", 200, "max results")
	_ = flowLogsCmd.MarkFlagRequired("client")
	_ = flowLogsCmd.MarkFlagRequired("flow")
}

var flowCmd = &cobra.Command{
	Use:   "flow",
	Short: "Flow management",
}

var flowListCmd = &cobra.Command{
	Use:   "list",
	Short: "List past and in-progress flows for a client",
	RunE: func(cmd *cobra.Command, args []string) error {
		rows, err := listFlows(flagFlowListClientID, flagFlowListLimit)
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}

var flowGetCmd = &cobra.Command{
	Use:     "describe",
	Aliases: []string{"get"},
	Short:   "Describe flow metadata",
	RunE: func(cmd *cobra.Command, args []string) error {
		rows, err := getFlow(flagFlowGetClientID, flagFlowGetID)
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}

var flowLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Read logs for a flow",
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagFlowLogsLimit <= 0 || flagFlowLogsLimit > 10000 {
			return fmt.Errorf("limit must be between 1 and 10000")
		}
		vql := fmt.Sprintf(`
SELECT * FROM flow_logs(client_id=%s, flow_id=%s)
WHERE message =~ %s
LIMIT %d`,
			raptor.VQLLiteral(flagFlowLogsClientID),
			raptor.VQLLiteral(flagFlowLogsID),
			raptor.VQLLiteral(flagFlowLogsMatch),
			flagFlowLogsLimit)
		rows, err := client.RunVQL(ctx(), vql, orgID())
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}

func listFlows(clientID string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("limit must be between 1 and 1000")
	}
	vql := fmt.Sprintf(`
SELECT session_id AS flow_id,
       request.artifacts AS artifacts,
       state,
       timestamp(epoch=create_time) AS created,
       total_collected_rows AS rows,
       total_uploaded_files AS uploaded_files,
       request.creator AS creator
FROM flows(client_id=%s)
ORDER BY created DESC LIMIT %d`,
		raptor.VQLLiteral(clientID), limit)
	return client.RunVQL(ctx(), vql, orgID())
}

func getFlow(clientID, flowID string) ([]map[string]any, error) {
	vql := fmt.Sprintf(`
SELECT session_id AS flow_id,
       request.artifacts AS artifacts,
       state,
       timestamp(epoch=create_time) AS created,
       timestamp(epoch=start_time) AS started,
       timestamp(epoch=active_time) AS active,
       timestamp(epoch=completion_time) AS completed,
       timestamp(epoch=expiry_time) AS expires,
       total_collected_rows AS rows,
       total_uploaded_files AS uploaded_files,
       request.creator AS creator
FROM flows(client_id=%s)
WHERE session_id=%s
LIMIT 1`,
		raptor.VQLLiteral(clientID), raptor.VQLLiteral(flowID))
	return client.RunVQL(ctx(), vql, orgID())
}
