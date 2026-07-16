package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mdfranz/go-velociraptor-mcp/internal/raptor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

const logQueryMaxLen = 120

//go:embed tools.yaml
var toolDescriptionsFile embed.FS

var toolDescriptions = loadToolDescriptions()

var expectedToolDescriptions = map[string]struct{}{
	"list_orgs":              {},
	"client_info":            {},
	"list_clients":           {},
	"list_artifacts":         {},
	"artifact_details":       {},
	"collect_artifact":       {},
	"list_collections":       {},
	"get_collection_results": {},
	"realtime_collect":       {},
	"run_vql":                {},
	"export_vql":             {},
}

func loadToolDescriptions() map[string]string {
	data, err := toolDescriptionsFile.ReadFile("tools.yaml")
	if err != nil {
		panic(fmt.Sprintf("read embedded tool descriptions: %v", err))
	}
	var descriptions map[string]string
	if err := yaml.Unmarshal(data, &descriptions); err != nil {
		panic(fmt.Sprintf("parse embedded tool descriptions: %v", err))
	}
	for name := range expectedToolDescriptions {
		if descriptions[name] == "" {
			panic(fmt.Sprintf("missing description for tool %q", name))
		}
	}
	for name := range descriptions {
		if _, ok := expectedToolDescriptions[name]; !ok {
			panic(fmt.Sprintf("unknown tool description %q", name))
		}
	}
	return descriptions
}

func toolDescription(name string) string {
	description, ok := toolDescriptions[name]
	if !ok || description == "" {
		panic(fmt.Sprintf("missing description for tool %q", name))
	}
	return description
}

func truncateLogStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// toolAttrs builds slog key-value pairs for a tool call, including all scalar
// string/int/bool arguments so logs show identifiers like client_id, flow_id, etc.
func toolAttrs(name string, req *mcp.CallToolRequest) []any {
	attrs := []any{"name", name}
	if req.Params.Arguments == nil {
		return attrs
	}
	raw, err := json.Marshal(req.Params.Arguments)
	if err != nil {
		return attrs
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return attrs
	}
	// log scalar args only; skip objects/arrays (e.g. parameters) to keep lines readable
	keys := []string{"client_id", "flow_id", "artifact", "artifact_name", "hostname", "search", "os_filter", "org_id", "query"}
	for _, k := range keys {
		if v, ok := args[k]; ok {
			switch s := v.(type) {
			case string:
				if k == "query" {
					attrs = append(attrs, k, truncateLogStr(s, logQueryMaxLen))
				} else {
					attrs = append(attrs, k, s)
				}
			case float64, bool:
				attrs = append(attrs, k, v)
			}
		}
	}
	return attrs
}

func registerTools(srv *mcp.Server, client *raptor.Client, cfg *raptor.Config) {
	disabled := make(map[string]bool, len(cfg.DisabledTools))
	for _, t := range cfg.DisabledTools {
		disabled[t] = true
	}

	add := func(t *mcp.Tool, h mcp.ToolHandler) {
		if !disabled[t.Name] {
			wrapped := func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				if cfg.DefaultTimeout > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, cfg.DefaultTimeout)
					defer cancel()
				}
				start := time.Now()
				attrs := toolAttrs(t.Name, req)
				slog.Info("tool execution started", attrs...)
				res, err := h(ctx, req)
				duration := time.Since(start)
				dattrs := append(attrs, "duration_ms", duration.Milliseconds())
				if err != nil {
					slog.Error("tool execution failed (protocol error)", append(dattrs, "error", err)...)
				} else if res.IsError {
					var errMsg string
					if len(res.Content) > 0 {
						if tc, ok := res.Content[0].(*mcp.TextContent); ok {
							errMsg = tc.Text
						}
					}
					slog.Warn("tool execution returned tool-level error", append(dattrs, "error", errMsg)...)
				} else {
					slog.Info("tool execution succeeded", dattrs...)
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
	add(toolListCollections(), handleListCollections(client, cfg))
	add(toolGetCollectionResults(), handleGetCollectionResults(client, cfg))
	add(toolRealtimeCollect(), handleRealtimeCollect(client, cfg))

	if cfg.EnableDangerousTools {
		add(toolRunVQL(), handleRunVQL(client, cfg))
		add(toolExportVQL(), handleExportVQL(client, cfg))
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

// truncateRows trims a row slice so the marshaled JSON stays under maxBytes,
// returning the trimmed slice and the number of dropped rows.
func truncateRows(rows []map[string]any, maxBytes int) ([]map[string]any, int) {
	if rows == nil {
		return rows, 0
	}
	size := 0
	for i, row := range rows {
		b, _ := json.Marshal(row)
		size += len(b) + 2 // 2 for comma+space
		if size > maxBytes {
			return rows[:i], len(rows) - i
		}
	}
	return rows, 0
}

// --- list_orgs ---

func toolListOrgs() *mcp.Tool {
	return &mcp.Tool{
		Name:        "list_orgs",
		Description: toolDescription("list_orgs"),
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
		Description: toolDescription("client_info"),
		InputSchema: schema(map[string]any{
			"hostname":        prop("string", "Hostname or FQDN to search for (regex)"),
			"org_id":          prop("string", "Org ID (optional)"),
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
		Description: toolDescription("list_clients"),
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
		Description: toolDescription("list_artifacts"),
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
		rows, dropped := truncateRows(rows, cfg.MaxResponseBytes)
		result := map[string]any{"ok": true, "data": rows}
		if dropped > 0 {
			result["truncated"] = dropped
		}
		b, _ := json.MarshalIndent(result, "", "  ")
		return textResult(string(b)), nil
	}
}

// --- artifact_details ---

func toolArtifactDetails() *mcp.Tool {
	return &mcp.Tool{
		Name:        "artifact_details",
		Description: toolDescription("artifact_details"),
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
		Description: toolDescription("collect_artifact"),
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

// --- list_collections ---

func toolListCollections() *mcp.Tool {
	return &mcp.Tool{
		Name:        "list_collections",
		Description: toolDescription("list_collections"),
		InputSchema: schema(map[string]any{
			"client_id": prop("string", "Target client ID"),
			"limit":     prop("integer", "Max results (default 20)"),
			"org_id":    prop("string", "Org ID (optional)"),
		}, "client_id"),
	}
}

func handleListCollections(client *raptor.Client, cfg *raptor.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req)
		if err != nil {
			return toolErrMsg("invalid arguments: " + err.Error()), nil
		}
		clientID := getStr(args, "client_id")
		if clientID == "" {
			return toolErrMsg("client_id is required"), nil
		}
		limit := getInt(args, "limit", 20)
		if limit <= 0 || limit > 1000 {
			return toolErrMsg("limit must be between 1 and 1000"), nil
		}
		orgID := client.OrgID(getStr(args, "org_id"))
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
		Description: toolDescription("get_collection_results"),
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
		if maxRetries <= 0 || maxRetries > 1000 {
			return toolErrMsg("max_retries must be between 1 and 1000"), nil
		}
		if retryDelay < 0 || retryDelay > 300 {
			return toolErrMsg("retry_delay must be between 0 and 300 seconds"), nil
		}
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
				slog.Debug("get_collection_results polling", "flow_id", flowID, "attempt", i+1, "max", maxRetries)
				timer := time.NewTimer(time.Duration(retryDelay) * time.Second)
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					return toolErr(ctx.Err()), nil
				case <-timer.C:
				}
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
		rows, dropped := truncateRows(rows, cfg.MaxResponseBytes)
		if dropped > 0 {
			slog.Warn("get_collection_results truncated", "flow_id", flowID, "rows_returned", len(rows), "rows_dropped", dropped)
		} else {
			slog.Debug("get_collection_results rows", "flow_id", flowID, "rows_returned", len(rows))
		}
		result := map[string]any{"ok": true, "data": rows}
		if dropped > 0 {
			result["truncated"] = dropped
		}
		b, _ := json.MarshalIndent(result, "", "  ")
		return textResult(string(b)), nil
	}
}

// --- realtime_collect ---

func toolRealtimeCollect() *mcp.Tool {
	return &mcp.Tool{
		Name:        "realtime_collect",
		Description: toolDescription("realtime_collect"),
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
		rows, dropped := truncateRows(rows, cfg.MaxResponseBytes)
		result := map[string]any{"ok": true, "data": rows}
		if dropped > 0 {
			result["truncated"] = dropped
		}
		b, _ := json.MarshalIndent(result, "", "  ")
		return textResult(string(b)), nil
	}
}

// --- run_vql (dangerous) ---

func toolRunVQL() *mcp.Tool {
	return &mcp.Tool{
		Name:        "run_vql",
		Description: toolDescription("run_vql"),
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
		rows, err := client.RunVQL(ctx, query, orgID)
		if err != nil {
			return toolErr(err), nil
		}
		rows, dropped := truncateRows(rows, cfg.MaxResponseBytes)
		if dropped > 0 {
			slog.Warn("run_vql truncated", "rows_returned", len(rows), "rows_dropped", dropped)
		} else {
			slog.Debug("run_vql rows", "rows_returned", len(rows))
		}
		result := map[string]any{"ok": true, "data": rows}
		if dropped > 0 {
			result["truncated"] = dropped
		}
		b, _ := json.MarshalIndent(result, "", "  ")
		return textResult(string(b)), nil
	}
}

// --- export_vql (dangerous) ---

func toolExportVQL() *mcp.Tool {
	return &mcp.Tool{
		Name:        "export_vql",
		Description: toolDescription("export_vql"),
		InputSchema: schema(map[string]any{
			"query":       prop("string", "VQL query to execute"),
			"filepath":    prop("string", "Optional output file path on the MCP server host (e.g. /tmp/results.jsonl). Defaults to the RAPTOR_DATA_PATH directory. Files are named with a timestamp suffix and sequence number if they roll over."),
			"max_file_mb": prop("integer", "Max file size in MB before rolling to a new file (default 100, max 1000)"),
			"org_id":      prop("string", "Org ID (optional)"),
		}, "query"),
	}
}

func handleExportVQL(client *raptor.Client, cfg *raptor.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req)
		if err != nil {
			return toolErrMsg("invalid arguments: " + err.Error()), nil
		}
		query := getStr(args, "query")
		if query == "" {
			return toolErrMsg("query is required"), nil
		}
		basePath := getStr(args, "filepath")
		if basePath == "" {
			basePath = filepath.Join(cfg.DataDir, "export.jsonl")
		}
		maxFileMB := getInt(args, "max_file_mb", 100)
		if maxFileMB <= 0 {
			maxFileMB = 100
		}
		if maxFileMB > 1000 {
			maxFileMB = 1000
		}
		maxFileBytes := int64(maxFileMB) * 1024 * 1024
		orgID := client.OrgID(getStr(args, "org_id"))

		// Build output path: insert timestamp before extension
		baseDir := filepath.Dir(basePath)
		baseName := filepath.Base(basePath)
		ext := filepath.Ext(baseName)
		stem := strings.TrimSuffix(baseName, ext)
		if ext == "" {
			ext = ".jsonl"
		}
		ts := time.Now().UTC().Format("20060102T150405Z")

		if baseDir != "" && baseDir != "." {
			if err := os.MkdirAll(baseDir, 0o755); err != nil {
				return toolErr(fmt.Errorf("create output dir: %w", err)), nil
			}
		}

		fileIndex := 1
		var exportedFiles []string
		totalRows := int64(0)

		newFile := func() (*os.File, *bufio.Writer, string, error) {
			name := filepath.Join(baseDir, fmt.Sprintf("%s_%s_%03d%s", stem, ts, fileIndex, ext))
			f, err := os.Create(name)
			if err != nil {
				return nil, nil, "", fmt.Errorf("create %s: %w", name, err)
			}
			exportedFiles = append(exportedFiles, name)
			return f, bufio.NewWriterSize(f, 256*1024), name, nil
		}

		currentFile, currentWriter, currentName, err := newFile()
		if err != nil {
			return toolErr(err), nil
		}
		var currentBytes int64

		closeCurrentFile := func() error {
			if err := currentWriter.Flush(); err != nil {
				return err
			}
			return currentFile.Close()
		}

		slog.Info("export_vql started", "query", query, "filepath", basePath, "max_file_mb", maxFileMB)
		start := time.Now()

		streamErr := client.StreamVQL(ctx, query, orgID, func(rows []map[string]any) error {
			for _, row := range rows {
				data, err := json.Marshal(row)
				if err != nil {
					slog.Warn("export_vql: marshal error, skipping row", "error", err)
					continue
				}

				// Roll to a new file if this row would push us over the limit
				if currentBytes > 0 && currentBytes+int64(len(data))+1 > maxFileBytes {
					if err := closeCurrentFile(); err != nil {
						return fmt.Errorf("flush %s: %w", currentName, err)
					}
					fileIndex++
					currentBytes = 0
					currentFile, currentWriter, currentName, err = newFile()
					if err != nil {
						return err
					}
				}

				currentWriter.Write(data)
				currentWriter.WriteByte('\n')
				currentBytes += int64(len(data)) + 1
				totalRows++
			}
			return nil
		})

		// Always flush+close the last file, even on error
		if ferr := closeCurrentFile(); ferr != nil && streamErr == nil {
			streamErr = fmt.Errorf("flush final file: %w", ferr)
		}

		if streamErr != nil {
			slog.Error("export_vql failed", "error", streamErr, "rows_so_far", totalRows)
			return toolErr(streamErr), nil
		}

		elapsed := time.Since(start)
		slog.Info("export_vql completed", "total_rows", totalRows, "total_files", len(exportedFiles), "elapsed_ms", elapsed.Milliseconds())

		result := map[string]any{
			"ok":          true,
			"total_rows":  totalRows,
			"total_files": len(exportedFiles),
			"files":       exportedFiles,
			"elapsed_ms":  elapsed.Milliseconds(),
		}
		b, _ := json.MarshalIndent(result, "", "  ")
		return textResult(string(b)), nil
	}
}
