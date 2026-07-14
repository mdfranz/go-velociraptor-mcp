package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mdfranz/go-velociraptor-mcp/internal/raptor"
	"github.com/spf13/cobra"
)

var (
	flagCollectClientID string
	flagCollectArtifact string
	flagCollectParams   []string
	flagCollectTimeout  int
	flagFlowID          string
	flagCollectRetries  int
	flagCollectDelay    int
	flagCollectFields   string
	flagCollectSource   string
)

func init() {
	rootCmd.AddCommand(collectCmd)
	collectCmd.AddCommand(collectRunCmd, collectResultsCmd, collectRealtimeCmd)

	collectRunCmd.Flags().StringVar(&flagCollectClientID, "client", "", "client_id (required)")
	collectRunCmd.Flags().StringVar(&flagCollectArtifact, "artifact", "", "artifact name (required)")
	collectRunCmd.Flags().StringArrayVar(&flagCollectParams, "param", nil, "Key=Value parameter (repeatable)")
	collectRunCmd.Flags().IntVar(&flagCollectTimeout, "timeout", 600, "collection timeout in seconds")
	_ = collectRunCmd.MarkFlagRequired("client")
	_ = collectRunCmd.MarkFlagRequired("artifact")

	collectResultsCmd.Flags().StringVar(&flagCollectClientID, "client", "", "client_id (required)")
	collectResultsCmd.Flags().StringVar(&flagFlowID, "flow", "", "flow_id (required)")
	collectResultsCmd.Flags().StringVar(&flagCollectArtifact, "artifact", "", "artifact name (required)")
	collectResultsCmd.Flags().StringVar(&flagCollectSource, "source", "", "source name for multi-source artifacts")
	collectResultsCmd.Flags().StringVar(&flagCollectFields, "fields", "*", "fields to return")
	collectResultsCmd.Flags().IntVar(&flagCollectRetries, "retries", 20, "max poll retries")
	collectResultsCmd.Flags().IntVar(&flagCollectDelay, "delay", 5, "seconds between poll attempts")
	_ = collectResultsCmd.MarkFlagRequired("client")
	_ = collectResultsCmd.MarkFlagRequired("flow")
	_ = collectResultsCmd.MarkFlagRequired("artifact")

	collectRealtimeCmd.Flags().StringVar(&flagCollectClientID, "client", "", "client_id (required)")
	collectRealtimeCmd.Flags().StringVar(&flagCollectArtifact, "artifact", "", "artifact name (required)")
	collectRealtimeCmd.Flags().StringArrayVar(&flagCollectParams, "param", nil, "Key=Value parameter (repeatable)")
	collectRealtimeCmd.Flags().StringVar(&flagCollectSource, "source", "", "source name for multi-source artifacts")
	collectRealtimeCmd.Flags().StringVar(&flagCollectFields, "fields", "*", "fields to return")
	_ = collectRealtimeCmd.MarkFlagRequired("client")
	_ = collectRealtimeCmd.MarkFlagRequired("artifact")
}

var collectCmd = &cobra.Command{
	Use:   "collect",
	Short: "Artifact collection",
}

var collectRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Start async artifact collection on a client",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := raptor.ValidateArtifactName(flagCollectArtifact); err != nil {
			return err
		}
		params, err := parseParams(flagCollectParams)
		if err != nil {
			return err
		}
		vql, err := buildCollectVQL(flagCollectClientID, flagCollectArtifact, params, flagCollectTimeout)
		if err != nil {
			return err
		}
		rows, err := client.RunVQL(ctx(), vql, orgID())
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}

var collectResultsCmd = &cobra.Command{
	Use:   "results",
	Short: "Poll and retrieve results of a collection flow",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := raptor.ValidateArtifactName(flagCollectArtifact); err != nil {
			return err
		}
		if err := pollFlowCompletion(flagCollectClientID, flagFlowID, flagCollectArtifact, flagCollectRetries, flagCollectDelay); err != nil {
			return err
		}
		rows, err := fetchFlowResults(flagCollectClientID, flagFlowID, flagCollectArtifact, flagCollectSource, flagCollectFields)
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}

var collectRealtimeCmd = &cobra.Command{
	Use:   "realtime",
	Short: "Blocking collection — waits for flow completion and returns results",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := raptor.ValidateArtifactName(flagCollectArtifact); err != nil {
			return err
		}
		params, err := parseParams(flagCollectParams)
		if err != nil {
			return err
		}
		vql, err := buildRealtimeVQL(flagCollectClientID, flagCollectArtifact, flagCollectSource, flagCollectFields, params)
		if err != nil {
			return err
		}
		rows, err := client.RunVQL(ctx(), vql, orgID())
		if err != nil {
			return err
		}
		printRows(rows)
		return nil
	},
}

func parseParams(rawParams []string) (map[string]any, error) {
	result := make(map[string]any, len(rawParams))
	for _, p := range rawParams {
		parts := strings.SplitN(p, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid param %q: expected Key=Value", p)
		}
		if err := raptor.ValidateParamName(parts[0]); err != nil {
			return nil, err
		}
		result[parts[0]] = parts[1]
	}
	return result, nil
}

func buildCollectVQL(clientID, artifact string, params map[string]any, timeoutSecs int) (string, error) {
	envPart := ""
	if len(params) > 0 {
		envDict, err := raptor.BuildEnvDict(params)
		if err != nil {
			return "", err
		}
		envPart = fmt.Sprintf(", env=dict(%s)", envDict)
	}
	return fmt.Sprintf(`
LET collection <= collect_client(urgent='TRUE', client_id=%s, artifacts=%s, timeout=%d%s)
SELECT flow_id, request.artifacts AS artifacts, request.timeout AS timeout
FROM foreach(row=collection)`,
		raptor.VQLLiteral(clientID),
		raptor.VQLLiteral(artifact),
		timeoutSecs,
		envPart,
	), nil
}

func buildRealtimeVQL(clientID, artifact, source, fields string, params map[string]any) (string, error) {
	envPart := ""
	if len(params) > 0 {
		envDict, err := raptor.BuildEnvDict(params)
		if err != nil {
			return "", err
		}
		envPart = fmt.Sprintf(", env=dict(%s)", envDict)
	}
	resultArtifact := artifact
	if source != "" {
		resultArtifact = artifact + "/" + source
	}
	return fmt.Sprintf(`
LET collection <= collect_client(urgent='TRUE', client_id=%s, artifacts=%s%s)
LET get_monitoring = SELECT * FROM watch_monitoring(artifact='System.Flow.Completion') WHERE FlowId = collection.flow_id LIMIT 1
LET get_results = SELECT %s FROM source(client_id=collection.request.client_id, flow_id=collection.flow_id, artifact=%s)
SELECT * FROM foreach(row=get_monitoring, query=get_results)`,
		raptor.VQLLiteral(clientID),
		raptor.VQLLiteral(artifact),
		envPart,
		fields,
		raptor.VQLLiteral(resultArtifact),
	), nil
}

func pollFlowCompletion(clientID, flowID, artifact string, maxRetries, delaySeconds int) error {
	donePattern := fmt.Sprintf("^Collection %s", artifact)
	vql := fmt.Sprintf(`
SELECT message FROM flow_logs(client_id=%s, flow_id=%s)
WHERE message =~ %s
LIMIT 1`,
		raptor.VQLLiteral(clientID),
		raptor.VQLLiteral(flowID),
		raptor.VQLLiteral(donePattern),
	)
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			fmt.Fprintf(os.Stderr, "waiting for flow %s (%d/%d)...\n", flowID, i, maxRetries)
			time.Sleep(time.Duration(delaySeconds) * time.Second)
		}
		rows, err := client.RunVQL(ctx(), vql, orgID())
		if err != nil {
			return fmt.Errorf("poll error: %w", err)
		}
		if len(rows) > 0 {
			return nil
		}
	}
	return fmt.Errorf("flow %s did not complete after %d retries", flowID, maxRetries)
}

func fetchFlowResults(clientID, flowID, artifact, source, fields string) ([]map[string]any, error) {
	sourceName := artifact
	if source != "" {
		sourceName = artifact + "/" + source
	}
	vql := fmt.Sprintf(`
SELECT %s FROM source(client_id=%s, flow_id=%s, artifact=%s)`,
		fields,
		raptor.VQLLiteral(clientID),
		raptor.VQLLiteral(flowID),
		raptor.VQLLiteral(sourceName),
	)
	return client.RunVQL(ctx(), vql, orgID())
}
