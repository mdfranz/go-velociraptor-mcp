package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mdfranz/go-velociraptor-mcp/internal/raptor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerTools(srv *mcp.Server, client *raptor.Client, cfg *raptor.Config) {
	disabled := make(map[string]bool, len(cfg.DisabledTools))
	for _, t := range cfg.DisabledTools {
		disabled[t] = true
	}

	add := func(t *mcp.Tool, h mcp.ToolHandler) {
		if !disabled[t.Name] {
			wrapped := func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				start := time.Now()
				slog.Info("tool execution started", "name", t.Name)
				res, err := h(ctx, req)
				duration := time.Since(start)
				if err != nil {
					slog.Error("tool execution failed (protocol error)", "name", t.Name, "duration_ms", duration.Milliseconds(), "error", err)
				} else if res.IsError {
					var errMsg string
					if len(res.Content) > 0 {
						if tc, ok := res.Content[0].(*mcp.TextContent); ok {
							errMsg = tc.Text
						}
					}
					slog.Warn("tool execution returned tool-level error", "name", t.Name, "duration_ms", duration.Milliseconds(), "error", errMsg)
				} else {
					slog.Info("tool execution succeeded", "name", t.Name, "duration_ms", duration.Milliseconds())
				}
				return res, err
			}
			srv.AddTool(t, wrapped)
		}
	}

	add(toolListOrgs(), handleListOrgs(client, cfg))
	add(toolClientInfo(), handleClientInfo(client, cfg))
	add(toolListClients(), handleListClients(client, cfg))
	add(toolListArtifacts(), handleListArtifacts(client, cfg))
	add(toolArtifactDetails(), handleArtifactDetails(client, cfg))
	add(toolCollectArtifact(), handleCollectArtifact(client, cfg))
	add(toolGetCollectionResults(), handleGetCollectionResults(client, cfg))
	add(toolRealtimeCollect(), handleRealtimeCollect(client, cfg))

	if cfg.EnableDangerousTools {
		add(toolRunVQL(), handleRunVQL(client, cfg))
	}
}

// --- helpers ---

func schema(properties map[string]any, required ...string) json.RawMessage {
	s := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return b
}

func prop(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

func textOK(data any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return toolErr(err), nil
	}
	return textResult(string(b)), nil
}

func toolErr(err error) *mcp.CallToolResult {
	b, _ := json.Marshal(map[string]any{"ok": false, "error": err.Error()})
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
	}
}

func toolErrMsg(msg string) *mcp.CallToolResult {
	return toolErr(fmt.Errorf("%s", msg))
}

func getStr(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

func getBool(args map[string]any, key string) bool {
	v, _ := args[key].(bool)
	return v
}

func getInt(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}

func decodeArgs(req *mcp.CallToolRequest) (map[string]any, error) {
	if req.Params.Arguments == nil {
		return map[string]any{}, nil
	}
	raw, err := json.Marshal(req.Params.Arguments)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n\n[... truncated %d bytes ...]", len(s)-max)
}

// --- list_orgs ---

func toolListOrgs() *mcp.Tool {
	return &mcp.Tool{
		Name:        "list_orgs",
		Description: "List all Velociraptor organizations (tenants). Call this first if the org is unknown.",
		InputSchema: schema(map[string]any{}),
	}
}

func handleListOrgs(client *raptor.Client, cfg *raptor.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		rows, err := client.RunVQL(ctx, `SELECT OrgId, Name, Nonce FROM orgs()`, "")
		if err != nil {
			return toolErr(err), nil
		}
		return textOK(map[string]any{"ok": true, "data": rows})
	}
}

// --- client_info ---

func toolClientInfo() *mcp.Tool {
	return &mcp.Tool{
		Name:        "client_info",
		Description: "Find a client by hostname or FQDN. Call before collection tools when you only know the hostname.",
		InputSchema: schema(map[string]any{
			"hostname":       prop("string", "Hostname or FQDN to search for (regex)"),
			"org_id":         prop("string", "Org ID (optional)"),
			"search_all_orgs": prop("boolean", "Search across all orgs"),
		}, "hostname"),
	}
}

func handleClientInfo(client *raptor.Client, cfg *raptor.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req)
		if err != nil {
			return toolErrMsg("invalid arguments: " + err.Error()), nil
		}
		hostname := getStr(args, "hostname")
		if hostname == "" {
			return toolErrMsg("hostname is required"), nil
		}
		orgID := client.OrgID(getStr(args, "org_id"))
		vql := fmt.Sprintf(`
SELECT client_id,
       timestamp(epoch=first_seen_at) AS FirstSeen,
       timestamp(epoch=last_seen_at) AS LastSeen,
       os_info.hostname AS Hostname,
       os_info.fqdn AS Fqdn,
       os_info.system AS OSType,
       os_info.release AS OS,
       os_info.machine AS Machine,
       agent_information.version AS AgentVersion
FROM clients()
WHERE os_info.hostname =~ %s OR os_info.fqdn =~ %s
ORDER BY LastSeen DESC LIMIT 1`,
			raptor.VQLLiteral(hostname), raptor.VQLLiteral(hostname))
		rows, err := client.RunVQL(ctx, vql, orgID)
		if err != nil {
			return toolErr(err), nil
		}
		return textOK(map[string]any{"ok": true, "data": rows})
	}
}

// --- list_clients ---

func toolListClients() *mcp.Tool {
	return &mcp.Tool{
		Name:        "list_clients",
		Description: "List clients, optionally filtered by hostname/FQDN pattern or OS type.",
		InputSchema: schema(map[string]any{
			"search":    prop("string", "Regex filter on hostname, FQDN, or client_id"),
			"os_filter": prop("string", "Regex filter on OS type (e.g. linux, windows)"),
			"limit":     prop("integer", "Max results (default 50)"),
			"org_id":    prop("string", "Org ID (optional)"),
		}),
	}
}

func handleListClients(client *raptor.Client, cfg *raptor.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req)
		if err != nil {
			return toolErrMsg("invalid arguments: " + err.Error()), nil
		}
		search := getStr(args, "search")
		if search == "" {
			search = "."
		}
		osFilter := getStr(args, "os_filter")
		if osFilter == "" {
			osFilter = "."
		}
		limit := getInt(args, "limit", 50)
		if limit <= 0 || limit > 1000 {
			return toolErrMsg("limit must be between 1 and 1000"), nil
		}
		orgID := client.OrgID(getStr(args, "org_id"))
		vql := fmt.Sprintf(`
SELECT client_id,
       timestamp(epoch=first_seen_at) AS FirstSeen,
       timestamp(epoch=last_seen_at) AS LastSeen,
       os_info.hostname AS Hostname,
       os_info.fqdn AS Fqdn,
       os_info.system AS OSType,
       os_info.release AS OS,
       os_info.machine AS Machine,
       agent_information.version AS AgentVersion,
       last_ip AS LastIP
FROM clients()
WHERE (os_info.hostname =~ %s OR os_info.fqdn =~ %s OR client_id =~ %s)
  AND os_info.system =~ %s
ORDER BY LastSeen DESC LIMIT %d`,
			raptor.VQLLiteral(search), raptor.VQLLiteral(search),
			raptor.VQLLiteral(search), raptor.VQLLiteral(osFilter), limit)
		rows, err := client.RunVQL(ctx, vql, orgID)
		if err != nil {
			return toolErr(err), nil
		}
		return textOK(map[string]any{"ok": true, "data": rows})
	}
}

// --- list_artifacts ---

func toolListArtifacts() *mcp.Tool {
	return &mcp.Tool{
		Name:        "list_artifacts",
		Description: "List available artifact definitions. Use name_regex to filter. Call before collect_artifact to find the correct artifact name.",
		InputSchema: schema(map[string]any{
			"name_regex": prop("string", "Regex to filter artifact names (default: all)"),
			"org_id":     prop("string", "Org ID (optional)"),
		}),
	}
}

func handleListArtifacts(client *raptor.Client, cfg *raptor.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req)
		if err != nil {
			return toolErrMsg("invalid arguments: " + err.Error()), nil
		}
		nameRegex := getStr(args, "name_regex")
		if nameRegex == "" {
			nameRegex = "."
		}
		orgID := client.OrgID(getStr(args, "org_id"))
		vql := fmt.Sprintf(`
SELECT name, description, type
FROM artifact_definitions()
WHERE name =~ %s`, raptor.VQLLiteral(nameRegex))
		rows, err := client.RunVQL(ctx, vql, orgID)
		if err != nil {
			return toolErr(err), nil
		}
		b, _ := json.MarshalIndent(map[string]any{"ok": true, "data": rows}, "", "  ")
		return textResult(truncate(string(b), cfg.MaxResponseBytes)), nil
	}
}

// --- artifact_details ---

func toolArtifactDetails() *mcp.Tool {
	return &mcp.Tool{
		Name:        "artifact_details",
		Description: "Get full artifact metadata including parameters and source names. Call before get_collection_results for multi-source artifacts.",
		InputSchema: schema(map[string]any{
			"artifact_name": prop("string", "Exact artifact name"),
			"org_id":        prop("string", "Org ID (optional)"),
		}, "artifact_name"),
	}
}

func handleArtifactDetails(client *raptor.Client, cfg *raptor.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req)
		if err != nil {
			return toolErrMsg("invalid arguments: " + err.Error()), nil
		}
		name := getStr(args, "artifact_name")
		if err := raptor.ValidateArtifactName(name); err != nil {
			return toolErr(err), nil
		}
		orgID := client.OrgID(getStr(args, "org_id"))
		vql := fmt.Sprintf(`
SELECT name, description, parameters, sources
FROM artifact_definitions(names=[%s])`, raptor.VQLLiteral(name))
		rows, err := client.RunVQL(ctx, vql, orgID)
		if err != nil {
			return toolErr(err), nil
		}
		return textOK(map[string]any{"ok": true, "data": rows})
	}
}

// --- collect_artifact ---

func toolCollectArtifact() *mcp.Tool {
	return &mcp.Tool{
		Name:        "collect_artifact",
		Description: "Start an async artifact collection on a client. Returns a flow_id. Follow up with get_collection_results to retrieve results.",
		InputSchema: schema(map[string]any{
			"client_id":  prop("string", "Target client ID"),
			"artifact":   prop("string", "Artifact name to collect"),
			"parameters": map[string]any{"type": "object", "description": "Optional artifact parameters as key/value pairs"},
			"timeout":    prop("integer", "Collection timeout in seconds (default 600)"),
			"org_id":     prop("string", "Org ID (optional)"),
		}, "client_id", "artifact"),
	}
}

func handleCollectArtifact(client *raptor.Client, cfg *raptor.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req)
		if err != nil {
			return toolErrMsg("invalid arguments: " + err.Error()), nil
		}
		clientID := getStr(args, "client_id")
		if clientID == "" {
			return toolErrMsg("client_id is required"), nil
		}
		artifact := getStr(args, "artifact")
		if err := raptor.ValidateArtifactName(artifact); err != nil {
			return toolErr(err), nil
		}
		timeout := getInt(args, "timeout", 600)
		orgID := client.OrgID(getStr(args, "org_id"))

		envPart := ""
		if rawParams, ok := args["parameters"]; ok {
			if params, ok := rawParams.(map[string]any); ok && len(params) > 0 {
				envDict, err := raptor.BuildEnvDict(params)
				if err != nil {
					return toolErr(err), nil
				}
				envPart = fmt.Sprintf(", env=dict(%s)", envDict)
			}
		}

		vql := fmt.Sprintf(`
LET collection <= collect_client(urgent='TRUE', client_id=%s, artifacts=%s, timeout=%d%s)
SELECT flow_id, request.artifacts AS artifacts, request.timeout AS timeout
FROM foreach(row=collection)`,
			raptor.VQLLiteral(clientID), raptor.VQLLiteral(artifact), timeout, envPart)

		rows, err := client.RunVQL(ctx, vql, orgID)
		if err != nil {
			return toolErr(err), nil
		}
		return textOK(map[string]any{"ok": true, "data": rows})
	}
}

// --- get_collection_results ---

func toolGetCollectionResults() *mcp.Tool {
	return &mcp.Tool{
		Name:        "get_collection_results",
		Description: "Poll a flow for completion and retrieve results. Use artifact_details first to check for multiple sources. For multi-source artifacts pass the source name as 'artifact/source'.",
		InputSchema: schema(map[string]any{
			"client_id":   prop("string", "Target client ID"),
			"flow_id":     prop("string", "Flow ID from collect_artifact"),
			"artifact":    prop("string", "Artifact name (or artifact/source for multi-source)"),
			"max_retries": prop("integer", "Max poll attempts (default 20)"),
			"retry_delay": prop("integer", "Seconds between poll attempts (default 5)"),
			"org_id":      prop("string", "Org ID (optional)"),
		}, "client_id", "flow_id", "artifact"),
	}
}

func handleGetCollectionResults(client *raptor.Client, cfg *raptor.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req)
		if err != nil {
			return toolErrMsg("invalid arguments: " + err.Error()), nil
		}
		clientID := getStr(args, "client_id")
		flowID := getStr(args, "flow_id")
		artifact := getStr(args, "artifact")
		if clientID == "" || flowID == "" {
			return toolErrMsg("client_id and flow_id are required"), nil
		}
		if err := raptor.ValidateArtifactName(strings.Split(artifact, "/")[0]); err != nil {
			return toolErr(err), nil
		}
		maxRetries := getInt(args, "max_retries", 20)
		retryDelay := getInt(args, "retry_delay", 5)
		orgID := client.OrgID(getStr(args, "org_id"))

		// poll for completion
		baseArtifact := strings.Split(artifact, "/")[0]
		donePattern := fmt.Sprintf("^Collection %s", baseArtifact)
		pollVQL := fmt.Sprintf(`
SELECT message FROM flow_logs(client_id=%s, flow_id=%s)
WHERE message =~ %s LIMIT 1`,
			raptor.VQLLiteral(clientID), raptor.VQLLiteral(flowID),
			raptor.VQLLiteral(donePattern))

		for i := 0; i < maxRetries; i++ {
			if i > 0 {
				time.Sleep(time.Duration(retryDelay) * time.Second)
			}
			rows, err := client.RunVQL(ctx, pollVQL, orgID)
			if err != nil {
				return toolErr(err), nil
			}
			if len(rows) > 0 {
				break
			}
			if i == maxRetries-1 {
				return toolErrMsg(fmt.Sprintf("flow %s did not complete after %d retries", flowID, maxRetries)), nil
			}
		}

		// fetch results
		resultsVQL := fmt.Sprintf(`SELECT * FROM source(client_id=%s, flow_id=%s, artifact=%s)`,
			raptor.VQLLiteral(clientID), raptor.VQLLiteral(flowID), raptor.VQLLiteral(artifact))
		rows, err := client.RunVQL(ctx, resultsVQL, orgID)
		if err != nil {
			return toolErr(err), nil
		}
		b, _ := json.MarshalIndent(map[string]any{"ok": true, "data": rows}, "", "  ")
		return textResult(truncate(string(b), cfg.MaxResponseBytes)), nil
	}
}

// --- realtime_collect ---

func toolRealtimeCollect() *mcp.Tool {
	return &mcp.Tool{
		Name:        "realtime_collect",
		Description: "Blocking collection: starts a flow and waits for completion via watch_monitoring. Best for fast artifacts. Returns results directly.",
		InputSchema: schema(map[string]any{
			"client_id":  prop("string", "Target client ID"),
			"artifact":   prop("string", "Artifact name to collect"),
			"source":     prop("string", "Source name for multi-source artifacts (optional)"),
			"parameters": map[string]any{"type": "object", "description": "Optional artifact parameters as key/value pairs"},
			"org_id":     prop("string", "Org ID (optional)"),
		}, "client_id", "artifact"),
	}
}

func handleRealtimeCollect(client *raptor.Client, cfg *raptor.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req)
		if err != nil {
			return toolErrMsg("invalid arguments: " + err.Error()), nil
		}
		clientID := getStr(args, "client_id")
		artifact := getStr(args, "artifact")
		source := getStr(args, "source")
		if clientID == "" {
			return toolErrMsg("client_id is required"), nil
		}
		if err := raptor.ValidateArtifactName(artifact); err != nil {
			return toolErr(err), nil
		}
		orgID := client.OrgID(getStr(args, "org_id"))

		envPart := ""
		if rawParams, ok := args["parameters"]; ok {
			if params, ok := rawParams.(map[string]any); ok && len(params) > 0 {
				envDict, err := raptor.BuildEnvDict(params)
				if err != nil {
					return toolErr(err), nil
				}
				envPart = fmt.Sprintf(", env=dict(%s)", envDict)
			}
		}

		resultArtifact := artifact
		if source != "" {
			resultArtifact = artifact + "/" + source
		}

		vql := fmt.Sprintf(`
LET collection <= collect_client(urgent='TRUE', client_id=%s, artifacts=%s%s)
LET get_monitoring = SELECT * FROM watch_monitoring(artifact='System.Flow.Completion') WHERE FlowId = collection.flow_id LIMIT 1
LET get_results = SELECT * FROM source(client_id=collection.request.client_id, flow_id=collection.flow_id, artifact=%s)
SELECT * FROM foreach(row=get_monitoring, query=get_results)`,
			raptor.VQLLiteral(clientID), raptor.VQLLiteral(artifact), envPart,
			raptor.VQLLiteral(resultArtifact))

		rows, err := client.RunVQL(ctx, vql, orgID)
		if err != nil {
			return toolErr(err), nil
		}
		b, _ := json.MarshalIndent(map[string]any{"ok": true, "data": rows}, "", "  ")
		return textResult(truncate(string(b), cfg.MaxResponseBytes)), nil
	}
}

// --- run_vql (dangerous) ---

func toolRunVQL() *mcp.Tool {
	return &mcp.Tool{
		Name:        "run_vql",
		Description: "Execute a raw VQL query with no sanitization. Only available when dangerous tools are enabled.",
		InputSchema: schema(map[string]any{
			"query":  prop("string", "Raw VQL query string"),
			"org_id": prop("string", "Org ID (optional)"),
		}, "query"),
	}
}

func handleRunVQL(client *raptor.Client, cfg *raptor.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req)
		if err != nil {
			return toolErrMsg("invalid arguments: " + err.Error()), nil
		}
		query := getStr(args, "query")
		if query == "" {
			return toolErrMsg("query is required"), nil
		}
		orgID := client.OrgID(getStr(args, "org_id"))
		slog.Debug("run_vql query", "query", query, "org_id", orgID)
		rows, err := client.RunVQL(ctx, query, orgID)
		if err != nil {
			return toolErr(err), nil
		}
		b, _ := json.MarshalIndent(map[string]any{"ok": true, "data": rows}, "", "  ")
		return textResult(truncate(string(b), cfg.MaxResponseBytes)), nil
	}
}
