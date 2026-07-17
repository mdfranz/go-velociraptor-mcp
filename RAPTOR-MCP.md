# raptor-mcp - Go Implementation Design Guide

This guide updates the Velociraptor MCP server design to match the July 2026 MCP server standards while keeping the implementation specific to gRPC/mTLS, VQL execution, and DFIR workflows.

The server is a thin MCP adapter over the Velociraptor API. It should not become a second product with its own competing config, logging, or execution model.

Derived from:
- `MCP-SERVER-JULY-2026.md`
- Velociraptor gRPC/VQL interfaces (`api/proto/api.proto`, `actions/proto/vql.proto`)
- prior Python reference servers (`mcp-velociraptor`, `velociraptor-mcp-server`)

---

## 1. Principles and SDK Choice

Key principles:
- Protocol adapter, not platform: keep MCP transport concerns separate from Velociraptor business logic.
- Stdio discipline: `stdout` is reserved for MCP JSON-RPC only. Logs and diagnostics go to a logfile or `stderr`.
- Deterministic logging target: choose a default logfile and support explicit override or disable.
- Fail-fast configuration: validate config and required files before starting the MCP loop.
- Clean MCP responses: operational failures return MCP tool errors (`IsError: true`), not raw Go handler errors.
- Reuse one long-lived gRPC client: connect once at startup and share it across tool calls.
- Validate before dispatch: reject malformed artifact names, fields, parameter maps, and non-SELECT VQL before opening a stream.

Recommended SDK dependency:

```go
github.com/modelcontextprotocol/go-sdk v1.4.1
```

Use the raw registration API (`server.AddTool`) with explicit JSON Schema. That is the current standard because it supports stricter schemas, `additionalProperties: false`, and typed decoding without framework-specific helper constraints.

---

## 2. Recommended Project Layout

Prefer a multi-binary layout with shared core logic under `internal/raptor/`.

```text
internal/raptor/
  config.go       # YAML loading, env overrides, validation
  client.go       # gRPC/mTLS client, query execution, retries/timeouts
  schemas.go      # typed input structs with Validate()
  sanitize.go     # VQL escaping and identifier validation
  results.go      # response shaping, truncation, compaction helpers
  errors.go       # API/tool error helpers

cmd/raptor-mcp/
  main.go         # bootstrap, logger, lockfile, stdio loop
  tools.go        # tool schemas, registration, handlers
  version.go      # build-time version injection

cmd/raptor-cli/
  main.go         # cobra root, global flags, config bootstrap
  cmd_client.go   # client discovery and inspection
  cmd_artifact.go # list_artifacts, artifact_details
  cmd_collect.go  # collect_artifact, get_collection_results, realtime_collect
  cmd_hunt.go     # hunts, list_hunt_flows, get_hunt_results
  cmd_org.go      # list_orgs
  cmd_vql.go      # read-only VQL operations
  output.go       # table/JSON/YAML output formatting
  version.go      # build-time version injection

tools/
  test_mcp.py     # optional end-to-end MCP test harness

go.mod
go.sum
Makefile
```

Why this layout:
- `internal/raptor/` is the single source of truth for config, the gRPC client, sanitization, and result shaping — both binaries draw from it with no duplication;
- `cmd/raptor-mcp/` stays a thin MCP transport adapter;
- `cmd/raptor-cli/` is a thin cobra command adapter over the same shared core;
- validation structs, timeout tiers, and VQL sanitization are tested once and exercised by both.

If the project remains small, a single package under `cmd/raptor-mcp/` is still acceptable. The rule is to keep transport concerns thin, not to force an early package split.

### 2.1 Implementation Progression

Build in phases:

1. Minimal working server
   - `main() -> run() int`
   - config load and validation
   - one gRPC client
   - one or two tools
   - stdio transport
2. Production reliability
   - deterministic logger
   - lockfile
   - graceful shutdown
   - strict argument decoding
3. LLM ergonomics
   - prompt-engineered tool descriptions
   - typed input structs
   - reusable schema builders
   - precise tool errors
4. Observability
   - startup config logging
   - build-time version
   - structured log fields
5. Performance
   - response truncation
   - optional result compaction
   - optional short-lived caching for reference data

---

## 3. Core MCP Adapter

### 3.1 Bootstrap Pattern

Use `main() -> os.Exit(run())` so deferred cleanup always runs.

```go
func main() {
    os.Exit(run())
}

func run() int {
    cfg, err := raptor.LoadConfig()
    if err != nil {
        fmt.Fprintln(os.Stderr, err.Error())
        return 1
    }

    logCloser, err := initLogger(cfg)
    if err != nil {
        fmt.Fprintf(os.Stderr, "logger init error: %v\n", err)
        return 1
    }
    if logCloser != nil {
        defer logCloser.Close()
    }

    cleanupLock, err := acquireLock(cfg)
    if err != nil {
        fmt.Fprintf(os.Stderr, "lock error: %v\n", err)
        return 1
    }
    defer cleanupLock()

    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    client, err := raptor.NewClient(cfg)
    if err != nil {
        slog.Error("client init failed", "error", err)
        fmt.Fprintf(os.Stderr, "client init error: %v\n", err)
        return 1
    }
    defer client.Close()

    srv := mcp.NewServer(&mcp.Implementation{
        Name:    "raptor-mcp",
        Version: version,
    }, nil)

    registerTools(srv, client, cfg)

    slog.Info("server starting", "version", version)
    if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && err != context.Canceled {
        slog.Error("server stopped with error", "error", err)
        fmt.Fprintf(os.Stderr, "server error: %v\n", err)
        return 1
    }
    slog.Info("server stopped")
    return 0
}
```

Rules:
- never call `os.Exit` from the middle of startup logic;
- never write logs to `stdout`;
- treat startup validation failure as fatal.

### 3.2 Configuration

The Velociraptor side already has a config contract: `api_client.yaml`. Reuse it rather than inventing a parallel MCP-specific auth format.

> [!TIP]
> Developers are highly encouraged to deserialize `api_client.yaml` directly into the official Velociraptor protobuf configuration struct:
> `www.velocidex.com/golang/velociraptor/config/proto.ApiClientConfig`
> using `yaml.UnmarshalStrict` from `gopkg.in/yaml.v3`. This guarantees compatibility and avoids manual duplication of config structures.

If a custom configuration parser is defined, it should match this structure:

```go
type Config struct {
    CACertificate       string `yaml:"ca_certificate"`
    ClientCert          string `yaml:"client_cert"`
    ClientPrivateKey    string `yaml:"client_private_key"`
    APIConnectionString string `yaml:"api_connection_string"`
    PinnedServerName    string `yaml:"pinned_server_name"`
    MaxGRPCRecvSize     int    `yaml:"max_grpc_recv_size"`

    OrgID            string
    DisabledTools    []string
    LogLevel         string
    LogFile           string
    LockFile          string
    MaxResponseBytes  int
    DefaultTimeout    time.Duration
}
```

Config resolution order:

1. `--config`
2. `VELOCIRAPTOR_API_CONFIG`
3. `./api_client.yaml`
4. `${XDG_CONFIG_HOME}/velociraptor/api_client.yaml`
5. `~/.config/velociraptor/api_client.yaml`

Environment variables should only control MCP-specific behavior plus safe runtime defaults:

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `VELOCIRAPTOR_API_CONFIG` | No | discovery chain | explicit YAML path |
| `VELOCIRAPTOR_ORG_ID` | No | empty | default org injection |
| `VELOCIRAPTOR_DISABLED_TOOLS` | No | empty | comma-separated tool denylist |
| `RAPTOR_MAX_RESPONSE_BYTES` | No | `32000` | max returned payload size |
| `RAPTOR_TIMEOUT_SECONDS` | No | `300` | base tool timeout |
| `RAPTOR_LOG_FILE` | No | `raptor-mcp.log` | logfile path, `"off"` disables |
| `RAPTOR_LOCK_FILE` | No | `raptor-mcp.lock` | PID lockfile, `"off"` disables |
| `LOG_LEVEL` | No | `debug` | `debug`, `info`, `warn`, `error` |

Validation:
- fail if `CACertificate`, `ClientCert`, `ClientPrivateKey`, or `APIConnectionString` is empty;
- default `PinnedServerName` to `VelociraptorServer` when omitted;
- reject non-positive timeout and truncation values;
- normalize disabled-tool names during load.

### 3.3 Logger Initialization

Use a deterministic log target by default:

```text
raptor-mcp.log
```

Requirements:
- default to a size-rotating logfile;
- support `RAPTOR_LOG_FILE=off`;
- do not mirror structured logs to `stdout`;
- log startup config except secrets.

Recommended startup fields:
- `version`
- `api_connection_string`
- `pinned_server_name`
- `org_id`
- `max_response_bytes`
- `timeout`
  - `disabled_tools`
- `log_file`
- `lock_file`

Never log:
- PEM contents
- client private key
- full certificate bodies

### 3.4 Single-Instance Lock

MCP clients may spawn duplicate subprocesses. Add a PID lockfile:

- default path: `raptor-mcp.lock`
- disable with `RAPTOR_LOCK_FILE=off`
- remove stale lockfiles automatically
- clean up on normal exit

This prevents duplicate servers from racing over transport or consuming unnecessary Velociraptor/API resources.

### 3.5 Error Helpers

Keep application failures inside MCP tool results.

```go
func textResult(text string) (*mcp.CallToolResult, error) {
    return &mcp.CallToolResult{
        Content: []mcp.Content{&mcp.TextContent{Text: text}},
    }, nil
}

func errResult(err error) *mcp.CallToolResult {
    return &mcp.CallToolResult{
        Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
        IsError: true,
    }
}

func jsonOK(data any) (*mcp.CallToolResult, error) {
    b, err := json.Marshal(map[string]any{
        "ok":   true,
        "data": data,
    })
    if err != nil {
        return errResult(err), nil
    }
    return textResult(string(b))
}

func jsonErr(msg string) (*mcp.CallToolResult, error) {
    b, _ := json.Marshal(map[string]any{
        "ok":    false,
        "error": msg,
    })
    return &mcp.CallToolResult{
        Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
        IsError: true,
    }, nil
}
```

Rule:
- operational failure -> `errResult(...)` or `jsonErr(...)`, `nil`
- actual MCP/protocol failure -> non-nil Go error

### 3.6 Tool Registration and Handler Standards

Current standard:
- explicit JSON Schema;
- `additionalProperties: false`;
- strict JSON decoding;
- typed input structs with `Validate()`;
- prompt-engineered descriptions that help the LLM choose correct call order.

#### Strict argument decoding

```go
func decodeArgs[T any](raw json.RawMessage) (T, error) {
    var out T
    if len(raw) == 0 {
        return out, nil
    }
    dec := json.NewDecoder(bytes.NewReader(raw))
    dec.DisallowUnknownFields()
    if err := dec.Decode(&out); err != nil {
        return out, err
    }
    if err := dec.Decode(&struct{}{}); err != io.EOF {
        return out, fmt.Errorf("unexpected trailing data")
    }
    return out, nil
}
```

Why this matters:
- catches hallucinated fields immediately;
- prevents silent ignores;
- makes tool errors actionable for the LLM.

#### Typed input pattern

```go
type Validatable interface {
    Validate() error
}

type ClientInfoInput struct {
    Hostname       string `json:"hostname"`
    OrgID          string `json:"org_id,omitempty"`
    SearchAllOrgs  bool   `json:"search_all_orgs,omitempty"`
}

func (in ClientInfoInput) Validate() error {
    if strings.TrimSpace(in.Hostname) == "" {
        return fmt.Errorf("hostname is required")
    }
    return nil
}
```

#### Generic handler pattern

```go
func handleTool[T Validatable](
    name string,
    fn func(context.Context, T) (any, error),
) mcp.ToolHandler {
    return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        in, err := decodeArgs[T](req.Params.Arguments)
        if err != nil {
            return jsonErr(fmt.Sprintf("invalid arguments: %v", err))
        }
        if err := in.Validate(); err != nil {
            return jsonErr(err.Error())
        }
        out, err := fn(ctx, in)
        if err != nil {
            return jsonErr(err.Error())
        }
        return jsonOK(out)
    }
}
```

#### Schema builder helper

```go
func buildSchema(properties map[string]any, required ...string) json.RawMessage {
    schema := map[string]any{
        "type":                 "object",
        "additionalProperties": false,
        "properties":           properties,
    }
    if len(required) > 0 {
        schema["required"] = required
    }
    b, err := json.Marshal(schema)
    if err != nil {
        panic(err)
    }
    return b
}
```

#### Registration rules

- register the read-only VQL tools by default;
- apply disabled-tool filtering centrally;
- keep tool names stable once published;
- use descriptions to tell the model when to call discovery tools first.

---

## 4. Velociraptor Client Layer

### 4.1 Dependencies

```go
github.com/modelcontextprotocol/go-sdk           // MCP server + stdio transport
google.golang.org/grpc                           // gRPC runtime
google.golang.org/grpc/credentials               // TLS credentials
gopkg.in/yaml.v3                                 // api_client.yaml parsing
www.velocidex.com/golang/velociraptor/...        // generated proto packages only
```

Only import Velociraptor generated protobuf/gRPC packages:

```go
api_proto "www.velocidex.com/golang/velociraptor/api/proto"
vql_proto "www.velocidex.com/golang/velociraptor/actions/proto"
```

Do not pull in broader server-side Velociraptor packages unless the implementation truly needs them.

### 4.2 gRPC Client

```go
type Client struct {
    conn *grpc.ClientConn
    stub api_proto.APIClient
    cfg  Config
}
```

Connection rules:
- create once at startup;
- use mTLS with cert, key, and CA from `api_client.yaml`;
- default `PinnedServerName` to `VelociraptorServer`;
- optionally apply `grpc.MaxCallRecvMsgSize` when configured;
- keep the connection open and let gRPC handle reconnect behavior.

Reference connection setup:

```go
cert, err := tls.X509KeyPair([]byte(cfg.ClientCert), []byte(cfg.ClientPrivateKey))
if err != nil {
    return nil, fmt.Errorf("parse client keypair: %w", err)
}

pool := x509.NewCertPool()
if !pool.AppendCertsFromPEM([]byte(cfg.CACertificate)) {
    return nil, fmt.Errorf("parse ca certificate")
}

serverName := cfg.PinnedServerName
if serverName == "" {
    serverName = "VelociraptorServer"
}

creds := credentials.NewTLS(&tls.Config{
    Certificates: []tls.Certificate{cert},
    RootCAs:      pool,
    ServerName:   serverName,
})

opts := []grpc.DialOption{grpc.WithTransportCredentials(creds)}
if cfg.MaxGRPCRecvSize > 0 {
    opts = append(opts, grpc.WithDefaultCallOptions(
        grpc.MaxCallRecvMsgSize(cfg.MaxGRPCRecvSize),
    ))
}

conn, err := grpc.NewClient(cfg.APIConnectionString, opts...)
```

### 4.3 Query Execution

Core method:

```go
func (c *Client) RunVQL(ctx context.Context, vql string, orgID string) ([]map[string]any, error) {
    args := &vql_proto.VQLCollectorArgs{
        Query: []*vql_proto.VQLRequest{{VQL: vql}},
    }
    if orgID != "" {
        args.OrgId = orgID
    }

    stream, err := c.stub.Query(ctx, args)
    if err != nil {
        return nil, err
    }

    var rows []map[string]any
    for {
        resp, err := stream.Recv()
        if err == io.EOF {
            break
        }
        if err != nil {
            return nil, err
        }
        if resp.Error != "" {
            return nil, fmt.Errorf("%s", resp.Error)
        }
        chunk, err := decodeResponse(resp)
        if err != nil {
            return nil, err
        }
        rows = append(rows, chunk...)
    }
    return rows, nil
}
```

Guidance:
- keep transport-level errors as Go errors in the shared client;
- convert them into MCP tool results at the handler boundary;
- use per-tool contexts with bounded timeouts;
- allow longer timeouts for blocking collection/hunt flows than for metadata lookup.

### 4.4 Response Decoding

Check `VQLResponse` in this order:

1. compressed JSONL
2. plain JSONL
3. legacy JSON array

```go
func decodeResponse(resp *vql_proto.VQLResponse) ([]map[string]any, error) {
    if resp.UncompressedSize > 0 && len(resp.CompressedJsonResponse) > 0 {
        data, err := decompressZlib(resp.CompressedJsonResponse)
        if err != nil {
            return nil, err
        }
        return parseJSONL(data)
    }
    if resp.JSONLResponse != "" {
        return parseJSONL([]byte(resp.JSONLResponse))
    }
    if resp.Response != "" {
        var rows []map[string]any
        if err := json.Unmarshal([]byte(resp.Response), &rows); err != nil {
            return nil, err
        }
        return rows, nil
    }
    return nil, nil
}
```

Do not assume one response shape globally. Velociraptor can emit all three.

### 4.5 Default Scope Injection

Apply the default org centrally:

- tool arg `org_id`
- else config `VELOCIRAPTOR_ORG_ID`
- else empty string

This avoids repeating org resolution across handlers and reduces LLM parameter burden.

### 4.6 Timeout Strategy

Use timeout tiers instead of one global value:

| Tool type | Suggested timeout |
|---|---|
| metadata lookup | 30-60s |
| standard VQL query | 120s |
| blocking realtime collection | 300s |
| hunt result retrieval | 300s |

If a tool accepts `max_retries` / `retry_delay`, validate them strictly to avoid effectively infinite agent loops.

---

## 5. Input Sanitization and Guardrails

Every user value that reaches a VQL string must be validated or escaped first.

### 5.1 Scalar Escaping

```go
func vqlLiteral(v any) string {
    if v == nil {
        return "null"
    }
    switch val := v.(type) {
    case bool:
        if val {
            return "TRUE"
        }
        return "FALSE"
    case int, int64, float64:
        return fmt.Sprintf("%v", val)
    case string:
        escaped := strings.ReplaceAll(val, `\`, `\\`)
        escaped = strings.ReplaceAll(escaped, `'`, `\'`)
        return "'" + escaped + "'"
    default:
        b, _ := json.Marshal(val)
        return vqlLiteral(string(b))
    }
}
```

### 5.2 Identifier Validation

```go
var (
    validParamName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
    validFieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
    validArtifact  = regexp.MustCompile(`^[A-Za-z0-9_.:/-]+$`)
)
```

Rules:
- artifact names must pass `validateArtifactName()` before interpolation;
- `fields` is a whitelist problem, not an escaping problem;
- parameter names inside `env=dict(...)` must be validated;
- accept only a single SELECT VQL statement for ad hoc query tools.

### 5.3 Safe Environment Dict Assembly

```go
func buildEnvDict(params map[string]any) (string, error) {
    if len(params) == 0 {
        return "", nil
    }
    parts := make([]string, 0, len(params))
    for key, value := range params {
        if err := validateParamName(key); err != nil {
            return "", err
        }
        parts = append(parts, key+"="+vqlLiteral(value))
    }
    sort.Strings(parts)
    return strings.Join(parts, ", "), nil
}
```

### 5.4 Query Guardrails

Add pre-flight limits for heavy tools:
- cap `limit` on list-style tools;
- reject empty `artifact` or `client_id`;
- require bounded polling arguments for collection retrieval;
- reject non-SELECT and multi-statement ad hoc VQL before dispatch;
- log validation failures at `WARN`, not `ERROR`, because the remote operation never started.

---

## 6. Tool Catalog

### 6.0 Generic-Primitive Tool Design Principle

Older reference implementations often registered dozens of platform-specific, hardcoded tools (e.g., `linux_pslist`, `macos_pslist`, `windows_pslist`, `windows_shellbags`).
This Go implementation **strongly rejects** that pattern in favor of a **Generic-Primitive Tool Design**.

The Go adapter registers a unified, consolidated catalog of core discovery and collection primitives:
- `list_artifacts`
- `artifact_details`
- `collect_artifact`
- `get_collection_results`
- `realtime_collect`

#### Rationale for Generic-Primitive Design:
1. **Model Cognitive Load**: Registering dozens of platform-specific tools bloats the tool definitions sent to the LLM. It reduces the context window and increases the likelihood of model confusion or incorrect tool selection.
2. **Upstream Maintenance**: Upstream Velociraptor adds and updates artifact definitions frequently. If tools are hardcoded in the Go adapter, the adapter must be updated and redeployed to support new artifacts. With generic primitives, the model dynamically queries the server's current artifact definitions.
3. **User Customization**: Users can write or upload custom artifacts to their Velociraptor deployment. Hardcoded adapters cannot target these custom artifacts, whereas generic-primitive tools can.

---

All tools should return a consistent JSON envelope and use prompt-engineered descriptions. Where useful, descriptions should tell the model which discovery tool to call first.

Base argument rules:
- `org_id` is optional on multi-tenant tools;
- `fields` defaults to `"*"` only where projection is truly supported;
- large result tools should expose `limit` or polling bounds.

---

### 6.1 Client Discovery

`clients`
- **Args**: optional `client_id`, `search`, `os_filter`, `label`, `online`, `limit`, `include_metadata`, `search_all_orgs`, and `org_id`.
- **Behavior**: Lists matching client summaries when searching, or returns the complete record for an exact `client_id`.
- **Metadata**: `include_metadata` is available for exact lookups.

---

### 6.2 Artifact Discovery

`list_artifacts`
- **Args**: `scope`, `name_regex`, `include_parameter_details`, `org_id`
- **Returns**: Artifact list plus parameter names or details.
- **Guidance**: Preferred replacement for separate Windows/Linux/macOS listing tools.
- **VQL Template**:
  ```sql
  SELECT name, description, type, metadata.basic as basic, metadata.hidden as hidden
  FROM artifact_definitions()
  WHERE name =~ NameRegexPattern
  ```

`artifact_details`
- **Args**: `artifact_name`, `org_id`
- **Returns**: Full artifact metadata including parameter definitions and source names.
- **Guidance**: Must be called before `get_collection_results` when an artifact has multiple sources to ensure all individual sources are polled.
- **VQL Template**:
  ```sql
  SELECT name, description, parameters, sources.name as source_names
  FROM artifact_definitions(names=[ArtifactName])
  ```

---

### 6.3 Collection

`collect_artifact`
- **Args**: `client_id`, `artifact`, `parameters`, `timeout`, `org_id`
- **Behavior**: Starts an asynchronous collection flow on the client and returns flow metadata.
- **VQL Template**:
  ```sql
  LET collection <= collect_client(urgent='TRUE', client_id=ClientID, artifacts=ArtifactName, env=dict(Params))
  SELECT flow_id, request.artifacts as artifacts, request.timeout as timeout, request.specs[0] as specs
  FROM foreach(row=collection)
  ```

`get_collection_results`
- **Args**: `client_id`, `flow_id`, `artifact`, `fields`, `max_retries`, `retry_delay`, `org_id`
- **Behavior**: Polls the flow completion status and retrieves the collection results.
- **Multi-Source Handling**:
  1. Retrieve artifact details first to check if the artifact defines multiple internal sources (e.g., `Windows.Forensics.Pslist` might have `Pslist`, `Threads`, etc.).
  2. If multiple sources are present, poll and fetch results for each source independently as `ArtifactName/SourceName` (e.g., `Windows.Forensics.Pslist/Pslist`).
  3. If no sub-sources exist, query the `ArtifactName` directly.
- **Polling VQL**:
  ```sql
  SELECT * FROM flow_logs(client_id=ClientID, flow_id=FlowID)
  WHERE message =~ DonePattern
  LIMIT 100
  ```
  *(Where `DonePattern` is `^Collection {ArtifactName} is done after`)*
- **Results Retrieval VQL**:
  ```sql
  SELECT Fields FROM source(client_id=ClientID, flow_id=FlowID, artifact=SourceName)
  ```
- **Partial Results**: If some sources complete but the polling timeout is hit (retries exhausted), return the available records in a success envelope flagged with `"status": "partial_results"` along with a list of the incomplete source names.

`realtime_collect`
- **Args**: `client_id`, `artifact`, `parameters`, `fields`, `result_scope`, `org_id`
- **Behavior**: A blocking collection primitive for quick, low-latency artifacts.
- **VQL Template**:
  ```sql
  LET collection <= collect_client(urgent='TRUE', client_id=ClientID, artifacts=ArtifactName, env=dict(Params))
  LET get_monitoring = SELECT * FROM watch_monitoring(artifact='System.Flow.Completion') WHERE FlowId = collection.flow_id LIMIT 1
  LET get_results = SELECT * FROM source(client_id=collection.request.client_id, flow_id=collection.flow_id, artifact=ResultArtifactName)
  SELECT Fields FROM foreach(row=get_monitoring, query=get_results)
  ```

---

### 6.4 Hunts

`hunts`
- **Args**: optional `hunt_id`, `summary`, `limit`, and `org_id`.
- **Behavior**: Lists recent hunt summaries without `hunt_id`, or returns detailed metadata for an exact hunt lookup.

`get_hunt_results`
- **Args**: `hunt_id`, `artifact`, `fields`, `limit`, `org_id`
- **Behavior**: Retrieves results from a fleet hunt.
- **VQL Template**:
  ```sql
  SELECT Fields
  FROM hunt_results(hunt_id=HuntID, artifact=ArtifactName)
  LIMIT Limit
  ```

`list_hunt_flows`
- **Args**: `hunt_id`, `limit`, `start_row`, `full`, and `org_id`.
- **Behavior**: Lists the client flows launched by a hunt with pagination and optional full details.

---

### 6.5 Multi-Tenancy

`list_orgs`
- **Args**: None.
- **Returns**: Array of organization metadata.
- **VQL Template**:
  ```sql
  SELECT OrgId, Name, Nonce FROM orgs()
  ```

---

### 6.6 Read-only VQL Tools

`run_vql`
- **Args**: `query`, `org_id`
- **Behavior**: Runs one SELECT statement after syntax validation. Multiple statements and non-SELECT queries are rejected.

`export_vql`
- **Args**: `query`, `filepath`, `max_file_mb`, `org_id`
- **Behavior**: Runs one validated SELECT statement and streams rows to rolling JSONL files.

The disabled-tool denylist still applies to these tools.

### 6.7 Recommended Implementation and Call Order

Implement tools in dependency order, not by perceived user importance. Discovery primitives come first because later action tools depend on them for safe, predictable inputs.

Recommended implementation order:

1. `list_orgs`
2. `clients`
3. `list_artifacts`
4. `artifact_details`
5. `collect_artifact`
6. `get_collection_results`
7. `realtime_collect`
8. `hunts`
9. `list_hunt_flows`
10. `get_hunt_results`
11. `run_vql`
12. `export_vql`

Rationale:
- `list_orgs` is the simplest multi-tenant primitive and validates org scoping before more complex tools exist.
- `clients` establishes the endpoint discovery and exact-lookup layer needed to obtain valid `client_id` values.
- `list_artifacts` and `artifact_details` establish the artifact discovery layer needed to obtain valid artifact names, parameters, and source names.
- `collect_artifact` is the first core action tool and should not be built before both discovery layers are stable.
- `get_collection_results` depends on collection behavior and is more complex because it must handle polling, multi-source artifacts, and partial results.
- `realtime_collect` is a convenience path for curated fast artifacts, not the baseline collection flow.
- hunt tools come after single-endpoint collection because they are broader in blast radius and more complex operationally.
- read-only VQL tools come last because they are intentionally broader than the structured tools.

Shared helper milestones should be completed in parallel with the above order:

1. config load and default-org resolution
2. gRPC client and response decoding
3. result helpers and truncation
4. strict argument decoding
5. VQL sanitization and validators
6. schema builder helpers
7. polling helpers for flows
8. hunt helper routines
9. read-only VQL validation

Intended LLM call order for common workflows:

Standard endpoint collection:

1. `list_orgs` if tenant is unknown
2. `clients`
3. `list_artifacts` or `artifact_details`
4. `collect_artifact`
5. `get_collection_results`

Fast curated artifact collection:

1. `list_orgs` if tenant is unknown
2. `clients`
3. `artifact_details`
4. `realtime_collect`

Fleet-wide hunt workflow:

1. `list_orgs` if tenant is unknown
2. `list_artifacts`
3. `artifact_details`
4. `hunts`
5. `get_hunt_results`

Design rule:
- discovery tools first;
- execution tools second;
- broad read-only VQL tools last.

---

## 7. Response Shaping

### 7.1 Truncation

Always enforce a byte cap before returning large result payloads:

```go
func truncate(s string, maxBytes int) string {
    if len(s) <= maxBytes {
        return s
    }
    return s[:maxBytes] + fmt.Sprintf("\n\n[... truncated %d bytes ...]", len(s)-maxBytes)
}
```

Default:

```text
RAPTOR_MAX_RESPONSE_BYTES=32000
```

### 7.2 Compaction

Velociraptor can return very large rows. Current standards favor optional compaction before the truncation stage:

- keep artifact discovery and client discovery outputs compact and typed;
- for row-heavy tools, honor `fields` whenever safe;
- flatten obviously verbose nested structures when the tool is intended for LLM consumption, not raw export;
- never compact error payloads.

### 7.3 Envelope

Preferred tool payload shape:

```json
{"ok": true, "data": ...}
```

or

```json
{"ok": false, "error": "..."}
```

For partial polling results, add explicit status fields:

```json
{"ok": true, "data": {"status": "partial_results", "...": "..."}}
```

---

## 8. Testing and Verification

### 8.1 Minimum Verification

Before calling the implementation done, verify:
- config load failure paths;
- stdio startup with no stdout contamination;
- strict argument decoding rejects unknown fields;
- at least one happy-path tool call against a real or mocked Velociraptor endpoint;
- truncation works on oversized payloads.

### 8.2 End-to-End MCP Harness

Add a simple programmatic MCP client under `tools/test_mcp.py`.

```python
import asyncio
import os
from pydantic_ai import Agent
from pydantic_ai.mcp import MCPServerStdio

async def main():
    env = os.environ.copy()
    env.setdefault("VELOCIRAPTOR_API_CONFIG", "./api_client.yaml")

    server = MCPServerStdio(
        "go",
        ["run", "./cmd/raptor-mcp"],
        env=env,
        cwd=os.path.abspath(os.path.join(os.path.dirname(__file__), "..")),
    )

    agent = Agent("google-gla:gemini-3-flash-preview", toolsets=[server])

    async with server:
        result = await agent.run("What tools are available?")
        print(result.output)

if __name__ == "__main__":
    asyncio.run(main())
```

### 8.3 Makefile Targets

```makefile
.PHONY: build build-mcp build-cli test test-e2e fmt vet

GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
LDFLAGS    := -X main.version=$(GIT_COMMIT)

build: build-mcp build-cli

build-mcp:
	go build -ldflags "$(LDFLAGS)" -o raptor-mcp ./cmd/raptor-mcp

build-cli:
	go build -ldflags "$(LDFLAGS)" -o raptor-cli ./cmd/raptor-cli

test:
	go test ./...

test-e2e:
	uv run tools/test_mcp.py

fmt:
	go fmt ./...

vet:
	go vet ./...
```

---

## 9. Logging Guidance

Use structured logs with stable fields.

### 9.1 Level Usage

| Level | Use |
|---|---|
| `DEBUG` | high-volume internals, decoded chunk counts, retry detail |
| `INFO` | startup, shutdown, tool entry, completed calls |
| `WARN` | validation failures, partial results, slow operations |
| `ERROR` | startup failure, connection failure, unrecoverable execution failure |

### 9.2 Common Fields

Recommended fields:
- `name`
- `org_id`
- `client_id`
- `artifact`
- `flow_id`
- `hunt_id`
- `duration_ms`
- `rows`
- `response_bytes`
- `status`

### 9.3 Startup Logging

Log resolved non-secret config once at startup. This is the fastest way to diagnose misconfiguration in real MCP client launches.

### 9.4 Build-Time Version Injection

Use:

```go
var version = "dev"
```

and override it via `-ldflags` during builds.

---

## 10. Security Model

### 10.1 VQL Injection Prevention

Three input surfaces need separate handling:

| Surface | Defense |
|---|---|
| scalar values | `vqlLiteral()` |
| artifact names | `validateArtifactName()` plus `vqlLiteral()` |
| projected fields | `validateFields()` |

Never treat escaping as sufficient for field selectors or artifact identifiers.

### 10.2 Read-only VQL Validation

Ad hoc VQL tools accept one `SELECT` statement after leading whitespace and comments.
They reject empty input, non-SELECT statements, and multiple statements before dispatch.
This is a syntax-level guardrail; plugin behavior inside a SELECT is still governed by
Velociraptor.

`VELOCIRAPTOR_DISABLED_TOOLS=...` remains available for selectively hiding tools.

### 10.3 mTLS

The server authenticates the client certificate, and the client validates the server against the configured CA and pinned server name.

`PinnedServerName` is a certificate identity check, not a DNS hostname selector.

### 10.4 Stdio Transport Discipline

The server is a subprocess transport adapter. It should not:
- open an HTTP listener;
- emit casual logs to stdout;
- write banners, prompts, or debug prints during startup.

---

## 11. Error Handling Matrix

| Condition | Behavior |
|---|---|
| missing config file | fatal at startup |
| invalid YAML | fatal at startup |
| invalid PEM/keypair | fatal at startup |
| gRPC connect failure | fatal at startup |
| stream receive error | tool-level error result |
| `resp.Error != ""` | tool-level error result |
| invalid tool arguments | tool-level error result |
| artifact/client not found | tool-level error result |
| polling exhausted, no results | tool-level error result |
| polling exhausted, partial results | success envelope with partial status |

Rule:
- Velociraptor-level failures should be visible to the LLM as tool errors;
- only protocol/runtime failures should escape as non-nil Go errors.

---

## 12. Best Practices and Anti-Patterns

### Must Do

- Use `run() int` and `os.Exit(run())`.
- Keep logs off `stdout`.
- Reuse `api_client.yaml` rather than inventing another auth contract.
- Use strict JSON argument decoding with `DisallowUnknownFields`.
- Set `"additionalProperties": false` on every input schema.
- Use typed input structs with `Validate()`.
- Centralize disabled-tool filtering and read-only VQL validation.
- Enforce truncation on every large tool response.
- Log resolved non-secret config at startup.
- Keep the gRPC client shared and long-lived.

### Anti-Patterns

- Writing any non-protocol data to `stdout`.
- Returning raw Go errors from normal tool failures.
- Silently ignoring unknown tool parameters.
- Building VQL with raw string interpolation.
- Accepting unrestricted VQL without validation.
- Treating all tools as having the same timeout.
- Dumping huge raw result sets without truncation or compaction.

---

## 13. Implementation Checklist

### Shared core (`internal/raptor/`)
- [ ] `config.go` - YAML discovery, env overrides, validation
- [ ] `client.go` - gRPC/mTLS connection and `RunVQL`
- [ ] `sanitize.go` - `vqlLiteral`, validators, env dict builder
- [ ] `results.go` - truncation and optional compaction helpers
- [ ] `errors.go` - result helpers
- [ ] `schemas.go` - typed inputs with `Validate()`

### MCP server (`cmd/raptor-mcp/`)
- [ ] `main.go` - bootstrap, logger, lockfile, stdio loop
- [ ] `tools.go` - schemas, registration, handlers
- [ ] `version.go` - build-time version string
- [ ] all logs routed to logfile or `stderr`, never `stdout`
- [ ] strict argument decoding enabled for every tool
- [ ] read-only VQL validation and denylist-aware registration
- [ ] truncation enforced on all large responses

### CLI (`cmd/raptor-cli/`)
- [ ] `main.go` - cobra root, `--config`/`--org`/`--output` global flags
- [ ] `cmd_client.go` - `client info`, `client list`
- [ ] `cmd_artifact.go` - `artifact list`, `artifact details`
- [ ] `cmd_collect.go` - `collect`, `collect results`, `collect realtime`
- [ ] `cmd_hunt.go` - `hunt run`, `hunt results`
- [ ] `cmd_org.go` - `org list`
- [ ] `cmd_vql.go` - read-only `vql run` and `vql export`
- [ ] `output.go` - table (default), JSON (`--output json`), YAML (`--output yaml`)
- [ ] `version.go` - build-time version string

### Project
- [ ] `tools/test_mcp.py` - end-to-end stdio harness
- [ ] `Makefile` - `build`, `build-mcp`, `build-cli`, `test`, `fmt`, `vet`, `test-e2e`
