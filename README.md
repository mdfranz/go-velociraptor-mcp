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

Ensure you have a recent version of Go installed (Go 1.26+ is recommended).

### Build Binaries

To build both `raptor-cli` and `raptor-mcp` into the root directory:

```bash
# Build the CLI tool
go build -o raptor-cli ./cmd/raptor-cli

# Build the MCP server
go build -o raptor-mcp ./cmd/raptor-mcp
```

---

## 1. Raptor CLI (`raptor-cli`)

`raptor-cli` is a flexible CLI client to interact with your Velociraptor instance. It outputs structured tables, JSON, or YAML.

### Global Flags

| Flag | Description | Environment Variable Override |
|---|---|---|
| `--config <path>` | Path to your `api_client.yaml`. Autodetects at `./api_client.yaml`, inside `$XDG_CONFIG_HOME`, or `~/.config/velociraptor/`. | `VELOCIRAPTOR_API_CONFIG` |
| `--org <id>` | The default organization ID to scope queries to (e.g., `root` or tenant ID). | `VELOCIRAPTOR_ORG_ID` |
| `-o`, `--output <format>` | Output format: `table`, `json`, `yaml`. (Default: `table`) | None |
| `--dangerous` | Enables dangerous features such as raw VQL query execution. | `ENABLE_DANGEROUS_TOOLS=true` |

### CLI Subcommands

#### Organizations
* **List Orgs**:
  ```bash
  ./raptor-cli org list
  ```

#### Client Discovery
* **List Clients**:
  List and filter active endpoints.
  ```bash
  ./raptor-cli client list --search "win-10" --os "windows" --limit 10
  ```
* **Client Info**:
  Lookup a specific client's system profile by hostname/FQDN.
  ```bash
  ./raptor-cli client info "win-10"
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

#### Raw VQL Execution (Dangerous)
* **Run Query**:
  Execute custom VQL queries directly against the Velociraptor API.
  ```bash
  ./raptor-cli vql run "SELECT * FROM info()" --dangerous
  ```

---

## 2. Raptor MCP Server (`raptor-mcp`)

`raptor-mcp` is a Model Context Protocol (MCP) server that exposes your Velociraptor deployment to AI tools, agents, and desktop applications.

### Configuration Environment Variables

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `VELOCIRAPTOR_API_CONFIG` | No | discovery chain | Explicit yaml file path to `api_client.yaml` |
| `VELOCIRAPTOR_ORG_ID` | No | empty | Default organization ID scope |
| `ENABLE_DANGEROUS_TOOLS` | No | `false` | Must be set to `true` to register raw VQL execution tools (`run_vql`) |
| `VELOCIRAPTOR_DISABLED_TOOLS`| No | empty | Comma-separated list of tool names to hide from the client |
| `RAPTOR_MAX_RESPONSE_BYTES` | No | `32000` | Truncation limit for tool response size in bytes |
| `RAPTOR_TIMEOUT_SECONDS` | No | `300` | Tool timeout duration in seconds |
| `RAPTOR_LOG_FILE` | No | `raptor-mcp.log`| Output logfile path. Use `"off"` to disable logging to disk |
| `RAPTOR_LOCK_FILE` | No | `raptor-mcp.lock`| Exclusivity lockfile path. Use `"off"` to disable |
| `LOG_LEVEL` | No | `debug` | Structured log verbosity: `debug`, `info`, `warn`, `error` |

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
        "ENABLE_DANGEROUS_TOOLS": "true",
        "LOG_LEVEL": "debug"
      }
    }
  }
}
```

### Exposed MCP Tools

1. **`list_orgs`**: List all Velociraptor organizations (tenants).
2. **`client_info`**: Identify client ID and specs using a hostname or FQDN regex.
3. **`list_clients`**: Filter, list, and profile registered endpoints.
4. **`list_artifacts`**: Query and filter forensic artifact signatures.
5. **`artifact_details`**: Retrieve schemas, parameters, and sources for a forensic artifact.
6. **`collect_artifact`**: Trigger an asynchronous endpoint collection flow.
7. **`list_collections`**: List past and in-progress artifact collections (flows) for a client, ordered by most recent first.
8. **`get_collection_results`**: Poll, wait, and retrieve results for a completed artifact collection flow.
9. **`realtime_collect`**: Dispatch an artifact collection flow, block until complete, and yield structured results.
10. **`run_vql`**: Run raw VQL query directly (only registered when `ENABLE_DANGEROUS_TOOLS="true"`).
11. **`export_vql`**: Execute a VQL query and stream all results to a JSONL file on the server. Handles arbitrarily large result sets without hitting response size limits (only registered when `ENABLE_DANGEROUS_TOOLS="true"`).


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

We use Python-based end-to-end integration tests to validate the MCP protocol handler.

### Run Tests

Ensure you have [uv](https://github.com/astral-sh/uv) or `pip` installed:

```bash
# Enter tools directory
cd tools

# Run test harness
uv run pytest -v test_mcp.py
```

---

## Related Projects

- [socfortress/velociraptor-mcp-server](https://github.com/socfortress/velociraptor-mcp-server) — Python-based MCP server for Velociraptor
- [mgreen27/mcp-velociraptor](https://github.com/mgreen27/mcp-velociraptor) — Alternate Python-based MCP server implementation
- [Velocidex/velociraptor](https://github.com/Velocidex/velociraptor) — Main Velociraptor endpoint monitoring framework
