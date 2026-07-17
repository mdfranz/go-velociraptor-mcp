package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mdfranz/go-velociraptor-mcp/internal/raptor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDecodeArgsRejectsUnknownArguments(t *testing.T) {
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "clients",
			Arguments: json.RawMessage(`{"search":".","unexpected":true}`),
		},
	}

	_, err := decodeArgs(req)
	if err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("decodeArgs() error = %v, want unknown argument error", err)
	}
}

func TestDecodeArgsAcceptsDeclaredArguments(t *testing.T) {
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name: "get_hunt_results",
			Arguments: json.RawMessage(`{
				"hunt_id":"H.123",
				"artifact":"Generic.Client.Info",
				"fields":"Hostname,ClientId",
				"limit":10
			}`),
		},
	}

	args, err := decodeArgs(req)
	if err != nil {
		t.Fatalf("decodeArgs() error = %v", err)
	}
	if got := getStr(args, "fields"); got != "Hostname,ClientId" {
		t.Fatalf("fields = %q, want %q", got, "Hostname,ClientId")
	}
}

func TestToolDescriptionsMatchToolCatalog(t *testing.T) {
	tools := []*mcp.Tool{
		toolListOrgs(),
		toolClients(),
		toolListArtifacts(),
		toolArtifactDetails(),
		toolCollectArtifact(),
		toolInspectCollections(),
		toolGetCollectionResults(),
		toolRealtimeCollect(),
		toolServerHealth(),
		toolHunts(),
		toolListHuntFlows(),
		toolGetHuntResults(),
		toolRunVQL(),
		toolExportVQL(),
	}

	if len(tools) != len(expectedToolDescriptions) {
		t.Fatalf("tool count = %d, expected descriptions = %d", len(tools), len(expectedToolDescriptions))
	}
	for _, tool := range tools {
		if tool.Description == "" {
			t.Errorf("%s has no description", tool.Name)
		}
		if _, ok := allowedToolArgs[tool.Name]; !ok {
			t.Errorf("%s has no strict argument allowlist", tool.Name)
		}
		var schemaMap map[string]any
		rawSchema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Errorf("%s schema cannot be marshaled: %v", tool.Name, err)
			continue
		}
		if err := json.Unmarshal(rawSchema, &schemaMap); err != nil {
			t.Errorf("%s schema is invalid JSON: %v", tool.Name, err)
		}
		if schemaMap["additionalProperties"] != false {
			t.Errorf("%s schema must reject additional properties", tool.Name)
		}
		properties, ok := schemaMap["properties"].(map[string]any)
		if !ok {
			t.Errorf("%s schema has no properties object", tool.Name)
			continue
		}
		for name := range properties {
			if !allowedToolArgs[tool.Name][name] {
				t.Errorf("%s schema property %q is missing from its allowlist", tool.Name, name)
			}
		}
		for name := range allowedToolArgs[tool.Name] {
			if _, ok := properties[name]; !ok {
				t.Errorf("%s allowlist property %q is missing from its schema", tool.Name, name)
			}
		}
	}
}

func TestGetParamsRejectsNonObject(t *testing.T) {
	_, err := getParams(map[string]any{"parameters": "not-an-object"})
	if err == nil || !strings.Contains(err.Error(), "object") {
		t.Fatalf("getParams() error = %v, want object type error", err)
	}
}

func TestRowsResultTruncatesWholeRows(t *testing.T) {
	cfg := &raptor.Config{MaxResponseBytes: 15}
	result := rowsResult([]map[string]any{
		{"id": 1},
		{"id": 2},
		{"id": 3},
	}, cfg)

	if result.IsError || len(result.Content) == 0 {
		t.Fatalf("rowsResult returned an unexpected error/empty result")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("result content type = %T, want *mcp.TextContent", result.Content[0])
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(text.Text), &envelope); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if _, ok := envelope["truncated"]; !ok {
		t.Fatalf("result = %s, want truncated row count", text.Text)
	}
}
