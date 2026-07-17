# Project Evolution: go-velociraptor-mcp

## Overview

go-velociraptor-mcp is a Go implementation of two complementary tools for interacting with Velociraptor DFIR deployments via gRPC/mTLS: a CLI (`raptor-cli`) for scripting and direct investigation, and an MCP server (`raptor-mcp`) that exposes Velociraptor's artifact collection and VQL capabilities to AI assistants. Both tools are built on a shared internal library, ensuring consistent behavior, validation, and connection management across the two interfaces.

---

## Phase 1: Design & Architecture (Jul 14 2026; Commits `c6036f9` → `a5f7690`)

**Theme:** Spec before code

The project opened with two design documents rather than any Go source. `MCP-SERVER-JULY-2026.md` established general principles for Go MCP servers — stdio discipline (stdout reserved exclusively for JSON-RPC traffic), the raw `server.AddTool` API over generic helpers, multi-binary layout with a shared `internal/` core, and end-to-end testing via pydantic-ai. `RAPTOR-MCP.md` adapted that playbook specifically to Velociraptor, prescribing the exact package layout (`internal/raptor/`, `cmd/raptor-mcp/`, `cmd/raptor-cli/`), build phases, and the principle of reusing one long-lived gRPC connection shared across all tool calls.

### Technical Decisions
- **Spec-first discipline**: Committing architecture decisions before implementation created a reference that held through all subsequent commits — the final layout matches the design almost exactly.
- **Shared core from the start**: The design explicitly called for `internal/raptor/` to serve both binaries, preventing the divergence that typically develops when a CLI and a server evolve in parallel.
- **Raw `server.AddTool` API**: Chosen over framework helpers to allow explicit JSON Schema with `additionalProperties: false`, giving the LLM cleaner tool definitions.

---

## Phase 2: Shared Library & CLI Foundation (Jul 14 2026; Commits `004f3bf` → `e56e113`)

**Theme:** Working CLI and live test suite before the MCP server

1,100+ lines of Go landed in a single commit: the entire shared library and CLI binary. `internal/raptor/` delivered mTLS config loading with environment override support, a gRPC client with `RunVQL()` and compressed (zlib/JSONL) response decoding, VQL injection safety via `VQLLiteral()` and strict artifact/parameter name validation, and response shaping helpers. `cmd/raptor-cli/` wrapped it in a Cobra CLI with global flags (`--config`, `--org`, `--output`), commands for org listing, client discovery, artifact browsing, and three collection modes: async flow dispatch, async result polling, and blocking realtime collect via `watch_monitoring`. Table and JSON output were included from the start; YAML was declared but not yet wired.

`vql run` followed one commit later as a separate read-only query path. Alongside it came `tools/test_vql.sh` — written before the MCP server existed — establishing a live-system test harness with 31 tests covering org listing, client discovery, artifact listing, SELECT validation, server-side queries, client-side `source()` results, and netstat analysis.

### Technical Decisions
- **Live-system tests, not mocks**: The test suite pins real client IDs and flow IDs from the actual Velociraptor deployment. This catches real API behavior that mocks would miss, at the cost of requiring a live instance to run.
- **Read-only VQL validation**: Ad hoc VQL is available by default but must be a single SELECT statement before dispatch.
- **`VQLLiteral()` in shared library**: Centralizing VQL escaping in `internal/raptor/sanitize.go` rather than in each command ensures consistent injection protection. Any new command that forgets to use it is immediately visible in review.

---

## Phase 3: MCP Server & Integration Testing (Jul 14 2026; Commit `45d6171`)

**Theme:** Full MCP server mirroring the CLI tool set

The MCP server, rotating logger, Python test harness, and Claude Desktop config landed together in a single 3,200-line commit. `cmd/raptor-mcp/` followed the `run() int` pattern prescribed in the design docs: signal handling, MCP stdio transport, and a `registerTools()` wrapper that times every call and emits structured `started`/`succeeded`/`failed` log lines with duration. All nine initial tools (`list_orgs` through `run_vql`) were implemented at launch, mirroring the CLI's capabilities exactly since both draw from `internal/raptor`.

A rotating slog handler was included from day one — the MCP server cannot write to stdout, so file logging with guaranteed rotation (10 MiB threshold, rolls to `raptor-mcp.log.1`) was non-negotiable. The Python test harness (`tools/test_mcp.py`) uses pydantic-ai to run seven DFIR tasks through the live server — org discovery, artifact listing, process collection, network analysis, raw VQL queries — using `gemini-3.5-flash` as the default model.

### Technical Decisions
- **Cohesive drop over incremental commits**: The MCP server launched feature-complete against the CLI rather than iterating from a stub. This was practical given the shared library already encoded all the business logic.
- **Structured logging on every tool call**: The `registerTools()` wrapper approach means logging is guaranteed for every tool without trusting individual handlers to remember it. Duration, tool name, and key identifiers are always captured.
- **AI-driven integration tests**: Using an LLM agent to exercise the server tests realistic multi-step workflows (resolve hostname → collect artifact → poll for results) rather than individual tool calls in isolation.

---

## Phase 4: Operational Hardening & Streaming Export (Jul 15 2026; Commits `04b9f23` → `698d13b`)

**Theme:** Fixing truncation bugs and handling large result sets discovered during live investigations

Two bugs surfaced during real use. The original `truncate()` cut response payloads at byte boundaries, producing invalid mid-JSON that broke tool results. This was replaced with `truncateRows()`, which measures and drops whole rows and reports the count as `"truncated": N` in the response. The 32KB response cap proved too small for realistic artifact outputs — package lists, process tables, bash history — and was raised to 512KB.

The new `export_vql` tool addressed result sets too large for any in-memory response. A new `StreamVQL()` method on the gRPC client accepts a row callback rather than accumulating all rows in memory, writes to timestamped rolling JSONL files (`stem_20260715T123243Z_001.jsonl`), and rolls to a new file when a configurable size threshold is reached. Structured logging was tightened with a `toolAttrs()` helper that extracts scalar arguments (`client_id`, `flow_id`, `artifact`, `query`) from every tool call into structured log fields, making individual operations traceable in `raptor-mcp.log`.

### Technical Decisions
- **Row-level truncation over byte-level**: Byte truncation was simpler to implement but produced unusable output. Row-level truncation preserves valid JSON at the cost of slightly more measurement work — the right trade-off for a tool feeding an LLM.
- **Streaming callback over buffering**: `StreamVQL()` processes gRPC response batches as they arrive, keeping memory flat regardless of result set size. This is essential for large artifact collections like full package lists or extensive bash histories.
- **`export_vql` for large read results**: The tool streams validated SELECT results to rolling JSONL files without loading the full result set into memory.

---

## Phase 5: CLI Feature Parity (Jul 15 2026; Commit `1392509`)

**Theme:** Bringing the CLI up to the MCP server's feature level

A focused parity pass closed the gap between the two binaries. `collect list` mirrors `list_collections`, listing past and in-progress flows for a client ordered most-recent first. `vql export` mirrors `export_vql`, reusing the same `StreamVQL()` callback, file rolling logic, and timestamp naming convention, with progress printed to stderr. YAML output had been declared as a valid `--output` value since Phase 2 but silently fell through to table — fixed with a `yaml.Marshal` branch in `output.go`.

A latent injection bug was also fixed: `cmd_client.go`'s `quote()` helper had used raw single-quote wrapping rather than `raptor.VQLLiteral()`. A hostname containing a single quote would have produced broken VQL with no error. The fix makes `quote()` a thin wrapper around the shared sanitizer, restoring consistency across all commands. Ten new tests in `test_vql.sh` covered all of the above, including a JSONL validity check using `python3` and SELECT validation for `vql export`. The suite reached 35 tests.

### Technical Decisions
- **Parity as an explicit goal**: Keeping CLI and MCP at the same feature level prevents the MCP server from becoming the "real" interface while the CLI stagnates. Both are supported paths for different operators.
- **Bug fixed incidentally**: The `quote()` injection risk was discovered during the parity review rather than in production. This argues for the value of cross-cutting reviews even when the primary goal is feature addition.
- **Inline JSONL validation in bash tests**: Using `python3 -c` to parse every line of the export output tests the actual byte stream produced by `StreamVQL`, not just that the file exists. This would have caught the original byte-truncation bug immediately.
