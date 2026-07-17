# go-velociraptor-mcp

A Go-based implementation of a Velociraptor command-line interface (`raptor-cli`) and a Model Context Protocol (MCP) server (`raptor-mcp`) for remote endpoint visibility, forensics, and orchestration via Velociraptor's gRPC/mTLS API.

---

## Architecture Overview

```
                      +-----------------------------+
                      |   Velociraptor API Server   |
                      +--------------+--------------+
                                     ^
                                     | gRPC / mTLS
                                     v
                      +--------------+--------------+
                      |   internal/raptor Library   |
                      +-------+--------------+------+
                              |              |
         +--------------------+              +--------------------+
         |                                                        |
         v                                                        v
+--------+--------+                                      +--------+--------+
|   raptor-cli    |                                      |   raptor-mcp    |
+-----------------+                                      +-----------------+
  Command-Line                                             Model Context
  User / Scripts                                           Protocol Server
                                                           (e.g., Claude)
```

Both binaries are powered by a shared backend package (`internal/raptor`) that manages mTLS configuration parsing, connection pooling, raw VQL queries, parameter marshaling, and safety validation.

---

## Installation & Building

Ensure you have the required version of Go installed (Go 1.26.3+, per `go.mod`).

### Build Binaries

To build both `raptor-cli` and `raptor-mcp` into the root directory:

```bash
# Build the CLI tool
go build -o raptor-cli ./cmd/raptor-cli

# Build the MCP server
go build -o raptor-mcp ./cmd/raptor-mcp
```

---
## 0. Generate a Client Config Yaml

```
$ sudo -u velociraptor velociraptor --config /etc/velociraptor/server.config.yaml config api_client --name "ExternalToolName" --role administrator /tmp/mcp_client.yaml
Creating API client file on /tmp/mcp_client.yaml.
```


## 1. Raptor CLI (`raptor-cli`)

`raptor-cli` is a flexible CLI client to interact with your Velociraptor instance. It outputs structured tables, JSON, or YAML.

### Global Flags

| Flag | Description | Environment Variable Override |
|---|---|---|
| `--config <path>` | Path to your `api_client.yaml`. Autodetects at `./api_client.yaml`, inside `$XDG_CONFIG_HOME`, or `~/.config/velociraptor/`. | `VELOCIRAPTOR_API_CONFIG` |
| `--org <id>` | The default organization ID to scope queries to (e.g., `root` or tenant ID). | `VELOCIRAPTOR_ORG_ID` |
| `-o`, `--output <format>` | Output format: `table`, `json`, `yaml`. (Default: `table`) | None |

### CLI Subcommands

#### Organizations
* **List Orgs**:
  ```bash
  ./raptor-cli org list
  ```

#### Hunts
* **List Hunts**:
  List recent fleet hunts.
  ```bash
  ./raptor-cli hunt list --limit 20
  ```
* **Describe Hunt**:
  Show hunt metadata and statistics.
  ```bash
  ./raptor-cli hunt describe --hunt "H.12345"
  ```
* **List Hunt Flows**:
  List the client flows launched by a hunt.
  ```bash
  ./raptor-cli hunt flows --hunt "H.12345" --limit 50
  ```
* **Read Hunt Results**:
  Read results for one artifact collected by a hunt.
  ```bash
  ./raptor-cli hunt results --hunt "H.12345" --artifact "Linux.Sys.Pslist" --limit 100
  ```

#### Flows
* **List Flows**:
  List recent and in-progress flows for a client.
  ```bash
  ./raptor-cli flow list --client "C.12345" --limit 20
  ```
* **Describe Flow**:
  Show metadata for one flow.
  ```bash
  ./raptor-cli flow describe --client "C.12345" --flow "F.67890"
  ```
* **Read Flow Logs**:
  Read diagnostic logs for a flow, optionally filtered by message regex.
  ```bash
  ./raptor-cli flow logs --client "C.12345" --flow "F.67890" --match "error"
  ```

#### Client Discovery
* **List Clients**:
  List and filter active endpoints.
  ```bash
  ./raptor-cli client list --search "win-10" --os "windows" --limit 10
  ```
  Use `--online` to restrict results to clients seen within the last 15 minutes, or `--label` to use the server label index.
* **Client Info**:
  Lookup a specific client's system profile by hostname/FQDN.
  ```bash
  ./raptor-cli client info "win-10"
  ```
* **Describe Client**:
  Show the complete client record by client ID.
  ```bash
  ./raptor-cli client describe --client "C.12345"
  ```
* **Client Metadata**:
  Read free-form metadata stored for a client.
  ```bash
  ./raptor-cli client metadata --client "C.12345"
  ```

#### Server
* **Health**:
  Check the Velociraptor API server health status.
  ```bash
  ./raptor-cli server health
  ```

#### Artifact Definitions
* **List Artifacts**:
  List available forensic artifact signatures.
  ```bash
  ./raptor-cli artifact list --filter "System.Flow"
  ```
* **Artifact Details**:
  Display full parameters, metadata, and sources for an artifact in JSON.
  ```bash
  ./raptor-cli artifact details "Generic.Client.Info"
  ```

#### Artifact Collections
* **Run Async Collection**:
  Dispatch an asynchronous artifact collection flow onto a client and get the flow ID.
  ```bash
  ./raptor-cli collect run --client "C.12345" --artifact "Generic.Client.Info" --param Key=Value
  ```
* **List Collections**:
  List past and in-progress flows for a client, ordered most-recent first.
  ```bash
  ./raptor-cli collect list --client "C.12345" --limit 20
  ```
* **Retrieve Results**:
  Poll and fetch results for an active or completed collection flow.
  ```bash
  ./raptor-cli collect results --client "C.12345" --flow "F.67890" --artifact "Generic.Client.Info"
  ```
* **Realtime Collection**:
  Blocking collection that dispatches a flow, waits for execution to complete, and displays results.
  ```bash
  ./raptor-cli collect realtime --client "C.12345" --artifact "Generic.Client.Info"
  ```

#### Read-only VQL
* **Run Query**:
  Execute one SELECT query directly against the Velociraptor API.
  ```bash
  ./raptor-cli vql run "SELECT * FROM info()"
  ```
* **Export to JSONL**:
  Stream SELECT results to a timestamped JSONL file on disk. Rolls to a new file when the size limit is reached. Progress and file paths are printed to stderr.
  ```bash
  ./raptor-cli vql export "SELECT * FROM clients()" --out /tmp/clients.jsonl --max-mb 50
  ```

---

## 2. Raptor MCP Server (`raptor-mcp`)

`raptor-mcp` is a Model Context Protocol (MCP) server that exposes your Velociraptor deployment to AI tools, agents, and desktop applications.

### Configuration Environment Variables

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `VELOCIRAPTOR_API_CONFIG` | No | discovery chain | Explicit yaml file path to `api_client.yaml` |
| `VELOCIRAPTOR_ORG_ID` | No | empty | Default organization ID scope |
| `VELOCIRAPTOR_DISABLED_TOOLS`| No | empty | Comma-separated list of tool names to hide from the client |
| `RAPTOR_MAX_RESPONSE_BYTES` | No | `512000` | Truncation limit for tool response size in bytes |
| `RAPTOR_TIMEOUT_SECONDS` | No | `300` | Tool timeout duration in seconds |
| `RAPTOR_LOG_FILE` | No | `raptor-mcp.log`| Output logfile path. Use `"off"` to disable logging to disk |
| `RAPTOR_LOCK_FILE` | No | `raptor-mcp.lock`| Exclusivity lockfile path. Use `"off"` to disable |
| `RAPTOR_DATA_PATH` | No | `data` | Default directory for `export_vql` output, relative to the MCP server's working directory |
| `LOG_LEVEL` | No | `debug` | Structured log verbosity: `debug`, `info`, `warn`, `error` |

### Operational Defaults

If unset, runtime defaults are applied:

| Setting | Default |
|---|---|
| API request timeout | 300 seconds |
| Max MCP response payload | 512000 bytes |
| Pinned server name | `VelociraptorServer` |
| Log level | `debug` |

### Registering with Claude Desktop

Add the server to your Claude Desktop configuration (typically `~/Library/Application Support/Claude/claude_desktop_config.json` on macOS or `%APPDATA%\Claude\claude_desktop_config.json` on Windows):

```json
{
  "mcpServers": {
    "raptor-mcp": {
      "command": "/Users/matthew/Code/raptor-mcp/go-velociraptor-mcp/raptor-mcp",
      "args": [],
      "env": {
        "VELOCIRAPTOR_API_CONFIG": "/path/to/api_client.yaml",
        "VELOCIRAPTOR_ORG_ID": "root",
        "LOG_LEVEL": "debug"
      }
    }
  }
}
```

### Exposed MCP Tools

1. **`list_orgs`**: List all Velociraptor organizations (tenants).
2. **`clients`**: Find, list, or inspect clients. Use `client_id` for exact details, or search and filters for discovery.
3. **`list_artifacts`**: Query and filter forensic artifact signatures.
4. **`artifact_details`**: Retrieve schemas, parameters, and sources for a forensic artifact.
5. **`collect_artifact`**: Trigger an asynchronous endpoint collection flow.
6. **`inspect_collections`**: List a client's collections or inspect one flow's metadata with `flow_id`.
7. **`get_collection_results`**: Poll, wait, and retrieve selected fields for a completed collection flow.
8. **`realtime_collect`**: Dispatch a collection flow, block until complete, and yield selected fields directly.
9. **`server_health`**: Check the Velociraptor API server health status.
10. **`hunts`**: List recent fleet hunts or inspect one hunt's metadata with `hunt_id`.
11. **`list_hunt_flows`**: List flows launched by a fleet hunt.
12. **`get_hunt_results`**: Retrieve selected fields from one artifact in a fleet hunt.
13. **`run_vql`**: Execute one read-only SELECT VQL query.
14. **`export_vql`**: Execute one read-only SELECT VQL query and stream results to a JSONL file.

### Customization

Top-level MCP tool descriptions are maintained in [`cmd/raptor-mcp/tools.yaml`](cmd/raptor-mcp/tools.yaml), keyed by tool name:

```yaml
inspect_collections: >-
  List collections, or inspect one flow when flow_id is provided.
```

The file is embedded into the server binary at build time. It is not read dynamically at runtime, so rebuild and restart `raptor-mcp` after making changes:

```bash
make
```

Input schemas and parameter-level descriptions remain in `cmd/raptor-mcp/tools.go`.


### Robust Rotating Logger

The server implements custom structured file logging (`log/slog`) with a safety rotating writer.
* **Auto-rotation**: When `raptor-mcp.log` grows beyond **10 MiB**, it is automatically rotated to `raptor-mcp.log.1` to prevent disk depletion.
* **Stderr redirection**: Standard error output is redirected into structured log events to ensure protocol standard stream sanity (standard streams must remain purely JSON-RPC for MCP).

---

## Security & Parameter Validation

To prevent VQL injection attacks during automated execution:
* **Artifact Validation**: Artifact names are strictly sanitized and checked against RFC-compliant patterns (`^[a-zA-Z0-9_\.]+$`).
* **Parameter Validation**: Dict parameters are strictly restricted to safe alphanumerics and underscoring for key identifiers.
* **SQL Escaping**: VQL arguments are safely quoted using backslash-escaped literal routines.

---

## Integration Testing

### CLI End-to-End Tests (`tools/test_vql.sh`)

A bash test suite validates all `raptor-cli` commands against a live Velociraptor instance. Tests cover client discovery, artifact listing, collection flows, VQL execution, JSONL export, and all output formats.

**Prerequisites**: a valid `api_client.yaml` reachable via `VELOCIRAPTOR_API_CONFIG` (or the default discovery chain), and the `raptor-cli` binary built.

```bash
# Build the CLI
go build -o raptor-cli ./cmd/raptor-cli

# Run with the binary in the current directory
CLI=./raptor-cli bash tools/test_vql.sh

# Run with verbose output (shows first 5 lines of each result)
VERBOSE=1 CLI=./raptor-cli bash tools/test_vql.sh
```

The script uses two known client IDs (`CLIENT_A` / `CLIENT_B`) and pre-existing flow IDs for `source()` tests. Update these variables at the top of the script if your environment differs.

### MCP Integration Tests (`tools/test_mcp.py`)

An AI-driven end-to-end test harness that spins up `raptor-mcp` as a subprocess and runs a set of DFIR tasks through a Gemini model via pydantic-ai. Results and tool call traces are written to `tools/raptor_mcp_test.log`.

**Prerequisites:**
- `raptor-mcp` binary built and on PATH (or at the repo root)
- `GOOGLE_API_KEY` or `GEMINI_API_KEY` set in the environment
- `VELOCIRAPTOR_API_CONFIG` set or `api_client.yaml` present in the default discovery path
- [uv](https://github.com/astral-sh/uv) installed

```bash
# Run with the default model (gemini-3.5-flash)
uv run tools/test_mcp.py

# Run with a specific model
uv run tools/test_mcp.py gemini-2.0-flash
```

---

## Related Projects

- [socfortress/velociraptor-mcp-server](https://github.com/socfortress/velociraptor-mcp-server) — Python-based MCP server for Velociraptor
- [mgreen27/mcp-velociraptor](https://github.com/mgreen27/mcp-velociraptor) — Alternate Python-based MCP server implementation
- [Velocidex/velociraptor](https://github.com/Velocidex/velociraptor) — Main Velociraptor endpoint monitoring framework
