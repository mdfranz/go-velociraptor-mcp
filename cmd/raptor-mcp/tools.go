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
	"sort"
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
	"clients":                {},
	"list_artifacts":         {},
	"artifact_details":       {},
	"collect_artifact":       {},
	"inspect_collections":    {},
	"get_collection_results": {},
	"realtime_collect":       {},
	"server_health":          {},
	"hunts":                  {},
	"list_hunt_flows":        {},
	"get_hunt_results":       {},
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
	add(toolClients(), handleClients(client, cfg))
	add(toolListArtifacts(), handleListArtifacts(client, cfg))
	add(toolArtifactDetails(), handleArtifactDetails(client, cfg))
	add(toolCollectArtifact(), handleCollectArtifact(client, cfg))
	add(toolInspectCollections(), handleInspectCollections(client, cfg))
	add(toolGetCollectionResults(), handleGetCollectionResults(client, cfg))
	add(toolRealtimeCollect(), handleRealtimeCollect(client, cfg))
	add(toolServerHealth(), handleServerHealth(client, cfg))
	add(toolHunts(), handleHunts(client, cfg))
	add(toolListHuntFlows(), handleListHuntFlows(client, cfg))
	add(toolGetHuntResults(), handleGetHuntResults(client, cfg))

	add(toolRunVQL(), handleRunVQL(client, cfg))
	add(toolExportVQL(), handleExportVQL(client, cfg))
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

func getParams(args map[string]any) (map[string]any, error) {
	raw, ok := args["parameters"]
	if !ok {
		return nil, nil
	}
	params, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("parameters must be an object")
	}
	return params, nil
}

func decodeArgs(req *mcp.CallToolRequest) (map[string]any, error) {
	if req == nil || req.Params == nil {
		return nil, fmt.Errorf("missing tool request")
	}
	allowed, ok := allowedToolArgs[req.Params.Name]
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", req.Params.Name)
	}
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
	if out == nil {
		return nil, fmt.Errorf("arguments must be a JSON object")
	}
	var unknown []string
	for key := range out {
		if !allowed[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown argument(s): %s", strings.Join(unknown, ", "))
	}
	return out, nil
}

var allowedToolArgs = map[string]map[string]bool{
	"list_orgs": {},
	"clients": {
		"client_id": true, "search": true, "os_filter": true, "limit": true,
		"label": true, "online": true, "include_metadata": true,
		"search_all_orgs": true, "org_id": true,
	},
	"list_artifacts":   {"name_regex": true, "org_id": true},
	"artifact_details": {"artifact_name": true, "org_id": true},
	"collect_artifact": {
		"client_id": true, "artifact": true, "parameters": true, "timeout": true,
		"org_id": true,
	},
	"inspect_collections": {"client_id": true, "flow_id": true, "limit": true, "org_id": true},
	"get_collection_results": {
		"client_id": true, "flow_id": true, "artifact": true, "fields": true,
		"max_retries": true, "retry_delay": true, "org_id": true,
	},
	"realtime_collect": {
		"client_id": true, "artifact": true, "source": true, "parameters": true,
		"fields": true, "org_id": true,
	},
	"server_health": {},
	"hunts":         {"hunt_id": true, "summary": true, "limit": true, "org_id": true},
	"list_hunt_flows": {
		"hunt_id": true, "limit": true, "start_row": true, "full": true,
		"org_id": true,
	},
	"get_hunt_results": {
		"hunt_id": true, "artifact": true, "source": true, "fields": true,
		"limit": true, "org_id": true,
	},
	"run_vql": {"query": true, "org_id": true},
	"export_vql": {
		"query": true, "filepath": true, "max_file_mb": true, "org_id": true,
	},
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

func rowsResult(rows []map[string]any, cfg *raptor.Config) *mcp.CallToolResult {
	rows, dropped := truncateRows(rows, cfg.MaxResponseBytes)
	result := map[string]any{"ok": true, "data": rows}
	if dropped > 0 {
		result["truncated"] = dropped
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	return textResult(string(b))
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

// --- clients ---

func toolClients() *mcp.Tool {
	return &mcp.Tool{
		Name:        "clients",
		Description: toolDescription("clients"),
		InputSchema: schema(map[string]any{
			"client_id":        prop("string", "Exact client ID for a detailed lookup"),
			"search":           prop("string", "Regex filter on hostname, FQDN, or client_id"),
			"os_filter":        prop("string", "Regex filter on OS type (e.g. linux, windows)"),
			"limit":            prop("integer", "Max results (default 50)"),
			"label":            prop("string", "Filter using the server label index"),
			"online":           prop("boolean", "Only clients seen within the last 15 minutes"),
			"include_metadata": prop("boolean", "Include stored metadata; requires client_id"),
			"search_all_orgs":  prop("boolean", "Search across all organizations"),
			"org_id":           prop("string", "Org ID (optional)"),
		}),
	}
}

func handleClients(client *raptor.Client, cfg *raptor.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req)
		if err != nil {
			return toolErrMsg("invalid arguments: " + err.Error()), nil
		}
		clientID := getStr(args, "client_id")
		includeMetadata := getBool(args, "include_metadata")
		if includeMetadata && clientID == "" {
			return toolErrMsg("include_metadata requires client_id"), nil
		}
		orgID := client.OrgID(getStr(args, "org_id"))

		if clientID != "" {
			vql := fmt.Sprintf(`SELECT * FROM clients(client_id=%s) LIMIT 1`,
				raptor.VQLLiteral(clientID))
			rows, err := client.RunVQL(ctx, vql, orgID)
			if err != nil {
				return toolErr(err), nil
			}
			if includeMetadata && len(rows) > 0 {
				metadataVQL := fmt.Sprintf(
					`SELECT client_metadata(client_id=%s) AS metadata FROM scope()`,
					raptor.VQLLiteral(clientID))
				metadataRows, err := client.RunVQL(ctx, metadataVQL, orgID)
				if err != nil {
					return toolErr(err), nil
				}
				if len(metadataRows) > 0 {
					rows[0]["metadata"] = metadataRows[0]["metadata"]
				}
			}
			return textOK(map[string]any{"ok": true, "data": rows})
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
		source := "clients()"
		if label := getStr(args, "label"); label != "" {
			source = fmt.Sprintf("clients(search=%s)", raptor.VQLLiteral("label:"+label))
		}
		onlineWhere := ""
		if getBool(args, "online") {
			onlineWhere = "\n  AND (now() - last_seen_at / 1000000) < 900"
		}
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
	FROM %s
	WHERE (os_info.hostname =~ %s OR os_info.fqdn =~ %s OR client_id =~ %s)
	  AND os_info.system =~ %s%s
	ORDER BY LastSeen DESC LIMIT %d`,
			source,
			raptor.VQLLiteral(search), raptor.VQLLiteral(search),
			raptor.VQLLiteral(search), raptor.VQLLiteral(osFilter), onlineWhere, limit)
		var rows []map[string]any
		if getBool(args, "search_all_orgs") {
			orgs, err := client.RunVQL(ctx, `SELECT OrgId FROM orgs()`, "")
			if err != nil {
				return toolErr(err), nil
			}
			for _, org := range orgs {
				orgID, ok := org["OrgId"].(string)
				if !ok || orgID == "" {
					continue
				}
				orgRows, err := client.RunVQL(ctx, vql, orgID)
				if err != nil {
					return toolErr(err), nil
				}
				rows = append(rows, orgRows...)
			}
			sort.SliceStable(rows, func(i, j int) bool {
				left, _ := rows[i]["LastSeen"].(string)
				right, _ := rows[j]["LastSeen"].(string)
				return left > right
			})
			if len(rows) > limit {
				rows = rows[:limit]
			}
		} else {
			rows, err = client.RunVQL(ctx, vql, orgID)
			if err != nil {
				return toolErr(err), nil
			}
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
		params, err := getParams(args)
		if err != nil {
			return toolErr(err), nil
		}
		if len(params) > 0 {
			envDict, err := raptor.BuildEnvDict(params)
			if err != nil {
				return toolErr(err), nil
			}
			envPart = fmt.Sprintf(", env=dict(%s)", envDict)
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

// --- inspect_collections ---

func toolInspectCollections() *mcp.Tool {
	return &mcp.Tool{
		Name:        "inspect_collections",
		Description: toolDescription("inspect_collections"),
		InputSchema: schema(map[string]any{
			"client_id": prop("string", "Target client ID"),
			"flow_id":   prop("string", "Exact flow ID for detailed metadata"),
			"limit":     prop("integer", "Max results when listing (default 20)"),
			"org_id":    prop("string", "Org ID (optional)"),
		}, "client_id"),
	}
}

func handleInspectCollections(client *raptor.Client, cfg *raptor.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req)
		if err != nil {
			return toolErrMsg("invalid arguments: " + err.Error()), nil
		}
		clientID := getStr(args, "client_id")
		if clientID == "" {
			return toolErrMsg("client_id is required"), nil
		}
		orgID := client.OrgID(getStr(args, "org_id"))
		if flowID := getStr(args, "flow_id"); flowID != "" {
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
			rows, err := client.RunVQL(ctx, vql, orgID)
			if err != nil {
				return toolErr(err), nil
			}
			return rowsResult(rows, cfg), nil
		}
		limit := getInt(args, "limit", 20)
		if limit <= 0 || limit > 1000 {
			return toolErrMsg("limit must be between 1 and 1000"), nil
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
			"fields":      prop("string", "Comma-separated result fields (default *)"),
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
		fieldsArg := getStr(args, "fields")
		if fieldsArg == "" {
			fieldsArg = "*"
		}
		fields, err := raptor.ValidateFieldList(fieldsArg)
		if err != nil {
			return toolErr(err), nil
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
		resultsVQL := fmt.Sprintf(`SELECT %s FROM source(client_id=%s, flow_id=%s, artifact=%s)`,
			fields, raptor.VQLLiteral(clientID), raptor.VQLLiteral(flowID), raptor.VQLLiteral(artifact))
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
			"fields":     prop("string", "Comma-separated result fields (default *)"),
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
		fieldsArg := getStr(args, "fields")
		if fieldsArg == "" {
			fieldsArg = "*"
		}
		fields, err := raptor.ValidateFieldList(fieldsArg)
		if err != nil {
			return toolErr(err), nil
		}
		if clientID == "" {
			return toolErrMsg("client_id is required"), nil
		}
		if err := raptor.ValidateArtifactName(artifact); err != nil {
			return toolErr(err), nil
		}
		orgID := client.OrgID(getStr(args, "org_id"))

		envPart := ""
		params, err := getParams(args)
		if err != nil {
			return toolErr(err), nil
		}
		if len(params) > 0 {
			envDict, err := raptor.BuildEnvDict(params)
			if err != nil {
				return toolErr(err), nil
			}
			envPart = fmt.Sprintf(", env=dict(%s)", envDict)
		}

		resultArtifact := artifact
		if source != "" {
			resultArtifact = artifact + "/" + source
		}

		vql := fmt.Sprintf(`
LET collection <= collect_client(urgent='TRUE', client_id=%s, artifacts=%s%s)
LET get_monitoring = SELECT * FROM watch_monitoring(artifact='System.Flow.Completion') WHERE FlowId = collection.flow_id LIMIT 1
LET get_results = SELECT %s FROM source(client_id=collection.request.client_id, flow_id=collection.flow_id, artifact=%s)
SELECT * FROM foreach(row=get_monitoring, query=get_results)`,
			raptor.VQLLiteral(clientID), raptor.VQLLiteral(artifact), envPart,
			fields, raptor.VQLLiteral(resultArtifact))

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

// --- server_health ---

func toolServerHealth() *mcp.Tool {
	return &mcp.Tool{
		Name:        "server_health",
		Description: toolDescription("server_health"),
		InputSchema: schema(map[string]any{}),
	}
}

func handleServerHealth(client *raptor.Client, cfg *raptor.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		status, err := client.Health(ctx)
		if err != nil {
			return toolErr(err), nil
		}
		return textOK(map[string]any{"ok": true, "data": []map[string]any{{"status": status}}})
	}
}

// --- hunts ---

func toolHunts() *mcp.Tool {
	return &mcp.Tool{
		Name:        "hunts",
		Description: toolDescription("hunts"),
		InputSchema: schema(map[string]any{
			"hunt_id": prop("string", "Exact hunt ID for a detailed lookup"),
			"summary": prop("boolean", "Return summary metadata for an exact hunt lookup"),
			"limit":   prop("integer", "Max results when listing (default 50)"),
			"org_id":  prop("string", "Org ID (optional)"),
		}),
	}
}

func handleHunts(client *raptor.Client, cfg *raptor.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req)
		if err != nil {
			return toolErrMsg("invalid arguments: " + err.Error()), nil
		}
		orgID := client.OrgID(getStr(args, "org_id"))
		if huntID := getStr(args, "hunt_id"); huntID != "" {
			summary := "FALSE"
			if getBool(args, "summary") {
				summary = "TRUE"
			}
			vql := fmt.Sprintf(`SELECT * FROM hunts(hunt_id=%s, summary=%s) LIMIT 1`,
				raptor.VQLLiteral(huntID), summary)
			rows, err := client.RunVQL(ctx, vql, orgID)
			if err != nil {
				return toolErr(err), nil
			}
			return rowsResult(rows, cfg), nil
		}
		if getBool(args, "summary") {
			return toolErrMsg("summary requires hunt_id"), nil
		}
		limit := getInt(args, "limit", 50)
		if limit <= 0 || limit > 1000 {
			return toolErrMsg("limit must be between 1 and 1000"), nil
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
ORDER BY created DESC LIMIT %d`, limit)
		rows, err := client.RunVQL(ctx, vql, orgID)
		if err != nil {
			return toolErr(err), nil
		}
		return rowsResult(rows, cfg), nil
	}
}

// --- list_hunt_flows ---

func toolListHuntFlows() *mcp.Tool {
	return &mcp.Tool{
		Name:        "list_hunt_flows",
		Description: toolDescription("list_hunt_flows"),
		InputSchema: schema(map[string]any{
			"hunt_id":   prop("string", "Hunt ID"),
			"limit":     prop("integer", "Max results (default 50)"),
			"start_row": prop("integer", "Zero-based starting row (default 0)"),
			"full":      prop("boolean", "Include full flow details"),
			"org_id":    prop("string", "Org ID (optional)"),
		}, "hunt_id"),
	}
}

func handleListHuntFlows(client *raptor.Client, cfg *raptor.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req)
		if err != nil {
			return toolErrMsg("invalid arguments: " + err.Error()), nil
		}
		huntID := getStr(args, "hunt_id")
		if huntID == "" {
			return toolErrMsg("hunt_id is required"), nil
		}
		limit := getInt(args, "limit", 50)
		if limit <= 0 || limit > 1000 {
			return toolErrMsg("limit must be between 1 and 1000"), nil
		}
		startRow := getInt(args, "start_row", 0)
		if startRow < 0 {
			return toolErrMsg("start_row must be zero or greater"), nil
		}
		basicInfo := "TRUE"
		if getBool(args, "full") {
			basicInfo = "FALSE"
		}
		vql := fmt.Sprintf(
			`SELECT * FROM hunt_flows(hunt_id=%s, start_row=%d, basic_info=%s) LIMIT %d`,
			raptor.VQLLiteral(huntID), startRow, basicInfo, limit)
		rows, err := client.RunVQL(ctx, vql, client.OrgID(getStr(args, "org_id")))
		if err != nil {
			return toolErr(err), nil
		}
		return rowsResult(rows, cfg), nil
	}
}

// --- get_hunt_results ---

func toolGetHuntResults() *mcp.Tool {
	return &mcp.Tool{
		Name:        "get_hunt_results",
		Description: toolDescription("get_hunt_results"),
		InputSchema: schema(map[string]any{
			"hunt_id":  prop("string", "Hunt ID"),
			"artifact": prop("string", "Artifact name"),
			"source":   prop("string", "Source name for multi-source artifacts"),
			"fields":   prop("string", "Comma-separated result fields (default *)"),
			"limit":    prop("integer", "Max results (default 100)"),
			"org_id":   prop("string", "Org ID (optional)"),
		}, "hunt_id", "artifact"),
	}
}

func handleGetHuntResults(client *raptor.Client, cfg *raptor.Config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req)
		if err != nil {
			return toolErrMsg("invalid arguments: " + err.Error()), nil
		}
		huntID := getStr(args, "hunt_id")
		artifact := getStr(args, "artifact")
		if huntID == "" || artifact == "" {
			return toolErrMsg("hunt_id and artifact are required"), nil
		}
		if err := raptor.ValidateArtifactName(artifact); err != nil {
			return toolErr(err), nil
		}
		fieldsArg := getStr(args, "fields")
		if fieldsArg == "" {
			fieldsArg = "*"
		}
		fields, err := raptor.ValidateFieldList(fieldsArg)
		if err != nil {
			return toolErr(err), nil
		}
		limit := getInt(args, "limit", 100)
		if limit <= 0 || limit > 10000 {
			return toolErrMsg("limit must be between 1 and 10000"), nil
		}
		vql := fmt.Sprintf(
			`SELECT %s FROM hunt_results(hunt_id=%s, artifact=%s, source=%s) LIMIT %d`,
			fields, raptor.VQLLiteral(huntID), raptor.VQLLiteral(artifact),
			raptor.VQLLiteral(getStr(args, "source")), limit)
		rows, err := client.RunVQL(ctx, vql, client.OrgID(getStr(args, "org_id")))
		if err != nil {
			return toolErr(err), nil
		}
		return rowsResult(rows, cfg), nil
	}
}

// --- run_vql ---

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
		if err := raptor.ValidateReadOnlyVQL(query); err != nil {
			return toolErr(err), nil
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

// --- export_vql ---

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
		if err := raptor.ValidateReadOnlyVQL(query); err != nil {
			return toolErr(err), nil
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
