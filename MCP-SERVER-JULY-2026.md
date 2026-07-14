# MCP Server Implementation Guide (July 2026)

This guide provides a production-ready playbook for building Model Context Protocol (MCP) servers in Go. It separates **MCP adapter concerns** (always required) from **HTTP client concerns** (bring your own if you already have one).

## 1. Principles & SDK Choice

**Key Principles:**
*   **Protocol Adapter:** The MCP server is a thin adapter layer over your existing API or CLI, not a separate product.
*   **Stdio Discipline:** `stdout` is exclusively for MCP protocol traffic. Never write logs or debug information to `stdout`.
*   **Deterministic Logging Target:** Choose a default logfile target (for this implementation: `sentinelone-sdl-mcp.log`) and allow explicit override/disable via environment variables.
*   **Fail-Fast Configuration:** Validate all environment variables and configuration early in the startup process. Exit immediately if requirements are missing.
*   **Clean Protocol Responses:** Return MCP error results (`IsError: true`) instead of bubbling raw Go errors. Pass API JSON responses as raw text back to the LLM.

**SDK Dependency:**
```go
github.com/modelcontextprotocol/go-sdk v1.4.1
```

**Tool Registration API:**
Prefer the raw API (`server.AddTool`) which accepts `*mcp.Tool` and `json.RawMessage` schemas. This provides the necessary flexibility for complex JSON schemas and parameter parsing, unlike the generic `mcp.AddTool`.

## 2. Recommended Project Layout

Keep server code in a single package under `cmd/<app-name>/` until growth justifies splitting. `client.go` is optional — omit it if you are wrapping an existing SDK or CLI.

```text
cmd/<app-name>/
  main.go      # config, logging, lockfile, server creation, run loop
  client.go    # (optional) HTTP/auth client — omit if wrapping an existing SDK
  tools.go     # tool schemas, registration, and handlers
  errors.go    # textResult/errResult helpers, custom error types
  cache.go     # (optional) token/result cache layer
tools/
  test_mcp.py  # end-to-end Python test client using pydantic-ai
go.mod
go.sum
Makefile
```

## 2.1 Shared Core Library Pattern (Multi-Binary Projects)

When the same API is consumed by both an MCP server and a CLI tool, extract all business logic into a shared internal package (`internal/<app>/`). The MCP server and CLI binary become thin transport adapters over a common core.

```text
internal/<app>/
  config.go       # env-var loading + validation
  client.go       # HTTP client, auth, rate-limit retry, singleflight
  schemas.go      # typed input structs with Validate() and QueryParams()
  cache.go        # TTL cache with disk persistence
  compact.go      # (optional) response field filtering
  errors.go       # APIError type

cmd/<app>-mcp/    # MCP server: stdio transport, tool registration
cmd/<app>-cli/    # CLI + TUI: cobra commands, output formatting
```

**Benefits:**
- Retry, auth, and caching logic live in one place — no drift between MCP and CLI behaviour.
- Input validation structs are tested once and reused by both binaries.
- Adding a new tool or CLI command requires only writing the schema and calling the shared client method.

#### 2.1.1 Facade Type-Aliasing Pattern (CMD adapters)
To keep your transport package clean (e.g., `cmd/<app>-mcp/main.go`) and avoid import name stuttering (`s1.Config`, `s1.Client`, `s1.ThreatsListInput`) on every other line, create a `shared_aliases.go` file inside your binary directory. This file uses Go's type-aliasing to define a clean local namespace package facade for your shared core library:

```go
// cmd/<app>-mcp/shared_aliases.go
package main

import "github.com/bespinus/sentinelone-mcp/internal/s1"

type Config = s1.Config
type Client = s1.Client
type Cache  = s1.Cache

type AgentsListInput = s1.AgentsListInput
// ... type-alias all relevant input structures for the handlers
```
This is a standard practice in enterprise Go monorepos to simplify main package logic and enhance developer ergonomics.

## 2.2 Recommended Implementation Progression (Option A — High-Velocity Baseline)

Build incrementally — each phase produces a working, deployable server. Stop when you have reached the capability level your deployment requires. This follows the **Option A (High-Velocity / Streamlined)** model, where local testing is introduced immediately in Phase 1, while stateful performance features like caching and payload compaction are deferred to Phase 5 to maximize initial development speed.

### Phase 1 — Minimal Working Server & Test Harness

Establish a server that connects to an MCP client, registers tools, and executes queries. Everything else builds on this foundation.

1. **`run() int` bootstrap** (§3.1) — set up `main()` → `run()` so deferred cleanups execute on all exit paths.
2. **Fail-fast config loading** (§3.2) — read env vars; exit immediately on missing required values.
3. **Programmatic Test Harness & Makefile** (§6) — add local testing scripts (`test_mcp.py` + `Makefile`) immediately so protocol frames can be validated in isolation without a real IDE client.
4. **Register at least one tool** (§3.6.5) — inline handler with a raw JSON schema; no generics needed yet.
5. **Wire to an existing client or build a minimal `Post()`** (§4.3 / §4.1) — the tool handler only needs `(ctx, payload) → (string, error)`.

*Checkpoint: `go run ./cmd/<app>-mcp` connects to your MCP client or programmatic test client and executes live queries.*

### Phase 2 — Production Reliability

Harden the server so it runs unattended without leaking resources or competing with itself.

6. **Logger with rotation** (§3.3) — deterministic log target, never `stdout`; 10 MiB cap with one backup.
7. **Single-instance lockfile** (§3.4) — prevent duplicate processes racing over stdin/stdout or burning API quota.
8. **Graceful shutdown** (§3.1) — `signal.NotifyContext` for `SIGINT`/`SIGTERM`; propagate the context to all HTTP calls.
9. **Pre-flight query validation** (§3.6) — reject syntactically invalid or hallucinated query expressions before the network call; log at `WARN`.

*Checkpoint: the server survives process restarts, signal kills, and malformed LLM-generated queries without leaving stale lock files or open handles.*

### Phase 3 — LLM Ergonomics

Reduce hallucinations and improve tool invocation accuracy so the model constructs well-formed inputs on the first try.

10. **Prompt-engineered schemas** (§3.6) — add instructions directly in `description` fields; cross-reference companion tools where call order matters.
11. **Offline data injection** (§3.6) — register a no-args tool that returns hardcoded schema or syntax metadata instantly, without a network round-trip.
12. **Strict argument decoding** (§3.6.2) — `DisallowUnknownFields` + trailing-data check surfaces misspelled parameters as immediate errors.
13. **Typed input structs with `Validate()`** (§3.6.3) — replace `map[string]any` handlers with typed structs; enforce business-rule bounds before dispatching.
14. **Generic handler factory** (§3.6.1) — once you have three or more tools, replace repetitive boilerplate with `handleTool[T Validatable]`.

*Checkpoint: the LLM constructs well-formed queries consistently and receives specific, actionable errors when it does not.*

### Phase 4 — Observability & Config Depth

Make the running server diagnosable without touching its code, and add configuration versatility.

15. **Build-time version injection** (§7.4) — inject the git commit hash via `-ldflags` so every log line identifies the deployed binary.
16. **Startup config logging** (§7.5) — log all resolved config values except secrets at startup so configuration errors are immediately visible.
17. **Structured field conventions** (§7.3) — consistent field names (`path`, `status_code`, `duration_ms`, `name`) across every log call site.
18. **Slow-request warnings** (§7.2) — emit `WARN` when a request exceeds 2× the configured per-endpoint timeout.

*Checkpoint: any production incident can be diagnosed from the log file alone, and configuration errors are easily surfaced.*

### Phase 5 — Performance & Optimization

Reduce latency, prevent LLM context bloating, and minimize API quota consumption.

19. **Response compaction** (§8) — whitelist fields before returning to the LLM; flatten verbose nested structures into flat primitives.
20. **Default scope injection** (§9) — auto-apply site, account, or tenant IDs from config to prevent cross-tenant queries and reduce required LLM parameters.
21. **Singleflight coalescing** (§4.1) — deduplicate simultaneous identical in-flight requests so only one network call fires.
22. **Result cache with TTL** (§5) — avoid redundant API calls for stable reference data across server restarts.
23. **Per-endpoint TTL overrides** (§5.2) — shorter TTLs for live event data, longer TTLs for static reference data.
24. **Negative caching** (§5.3) — cache deterministic 400/404 responses briefly to absorb retries on provably invalid requests.

*Checkpoint: the server operates efficiently under sustained load and survives API rate limits gracefully.*

---

## 3. Core MCP Adapter

### 3.1 Server Bootstrap (`main.go`)

The entrypoint manages configuration, logging, the single-instance lockfile, cache lifecycle, graceful shutdown, and the MCP stdio transport loop.

Use the `run() int` pattern so that all deferred cleanup (log close, lock removal, cache flush) runs even on early-exit paths. `os.Exit` in the middle of `main` skips any open defers; moving the exit code to `run` avoids this.

Use `signal.NotifyContext` (Go 1.16+) rather than a manual goroutine — it is shorter and handles the stop/reset automatically.

```go
package main

import (
    "context"
    "fmt"
    "io"
    "log/slog"
    "os"
    "os/signal"
    "syscall"

    "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
    os.Exit(run())
}

func run() int {
    cfg, err := loadConfig()
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

    client := NewClient(cfg)
    s := mcp.NewServer(&mcp.Implementation{Name: "my-mcp-server", Version: version}, nil)
    registerTools(s, client)

    slog.Info("server ready")
    if err := s.Run(ctx, &mcp.StdioTransport{}); err != nil && err != context.Canceled {
        slog.Error("server error", "error", err)
        fmt.Fprintf(os.Stderr, "server error: %v\n", err)
        return 1
    }
    slog.Info("server stopped")
    return 0
}
```

### 3.2 Configuration (`main.go`)

```go
type Config struct {
    BaseURL          string
    APIToken         string
    Scope            string
    Debug            bool
    LogFile          string
    LockFile         string
    Timeout          time.Duration
    CacheEnabled     bool
    CacheTTL         time.Duration
    CacheFile        string
    RetryMaxAttempts int
    RetryBaseBackoff time.Duration
    RetryMaxBackoff  time.Duration
}

func loadConfig() (Config, error) {
    // Re-use the existing client/CLI env contract directly for shared fields.
    // Do not fork auth/base-url variable names just for MCP.
    cfg := Config{
        BaseURL:  strings.TrimRight(strings.TrimSpace(os.Getenv("EXISTING_CLIENT_BASE_URL")), "/"),
        APIToken: strings.TrimSpace(os.Getenv("EXISTING_CLIENT_API_TOKEN")),
        Scope:    strings.TrimSpace(os.Getenv("EXISTING_CLIENT_SCOPE")),
        Debug:    strings.TrimSpace(os.Getenv("APP_DEBUG")) != "",
        LogFile:  strings.TrimSpace(os.Getenv("APP_LOGFILE")),
        LockFile: strings.TrimSpace(os.Getenv("APP_LOCKFILE")),
    }
    if cfg.BaseURL == "" {
        return Config{}, fmt.Errorf("EXISTING_CLIENT_BASE_URL is required")
    }
    if cfg.APIToken == "" {
        return Config{}, fmt.Errorf("EXISTING_CLIENT_API_TOKEN is required")
    }
    if cfg.LogFile == "" {
        cfg.LogFile = "my-mcp-server.log"
    }
    if cfg.LockFile == "" {
        cfg.LockFile = "my-mcp-server.lock"
    }

    timeoutSeconds := 30
    if raw := strings.TrimSpace(os.Getenv("APP_TIMEOUT_SECONDS")); raw != "" {
        n, err := strconv.Atoi(raw)
        if err != nil || n <= 0 {
            return Config{}, fmt.Errorf("APP_TIMEOUT_SECONDS must be a positive integer")
        }
        timeoutSeconds = n
    }
    cfg.Timeout = time.Duration(timeoutSeconds) * time.Second

    cacheEnabled := strings.ToLower(strings.TrimSpace(os.Getenv("APP_CACHE_ENABLED")))
    cfg.CacheEnabled = cacheEnabled == "" || cacheEnabled == "1" || cacheEnabled == "true" || cacheEnabled == "on"

    cacheTTLSeconds := 3600
    if raw := strings.TrimSpace(os.Getenv("APP_CACHE_TTL_SECONDS")); raw != "" {
        n, err := strconv.Atoi(raw)
        if err != nil || n <= 0 {
            return Config{}, fmt.Errorf("APP_CACHE_TTL_SECONDS must be a positive integer")
        }
        cacheTTLSeconds = n
    }
    cfg.CacheTTL = time.Duration(cacheTTLSeconds) * time.Second
    cfg.CacheFile = strings.TrimSpace(os.Getenv("APP_CACHE_FILE"))
    if cfg.CacheFile == "" {
        cfg.CacheFile = "my-mcp-server-cache.json"
    }

    cfg.RetryMaxAttempts = 2
    if raw := strings.TrimSpace(os.Getenv("APP_RETRY_MAX_ATTEMPTS")); raw != "" {
        n, err := strconv.Atoi(raw)
        if err != nil || n < 1 {
            return Config{}, fmt.Errorf("APP_RETRY_MAX_ATTEMPTS must be >= 1")
        }
        cfg.RetryMaxAttempts = n
    }
    cfg.RetryBaseBackoff = 400 * time.Millisecond
    if raw := strings.TrimSpace(os.Getenv("APP_RETRY_BASE_BACKOFF_MS")); raw != "" {
        n, err := strconv.Atoi(raw)
        if err != nil || n <= 0 {
            return Config{}, fmt.Errorf("APP_RETRY_BASE_BACKOFF_MS must be a positive integer")
        }
        cfg.RetryBaseBackoff = time.Duration(n) * time.Millisecond
    }
    cfg.RetryMaxBackoff = 3 * time.Second
    if raw := strings.TrimSpace(os.Getenv("APP_RETRY_MAX_BACKOFF_MS")); raw != "" {
        n, err := strconv.Atoi(raw)
        if err != nil || n <= 0 {
            return Config{}, fmt.Errorf("APP_RETRY_MAX_BACKOFF_MS must be a positive integer")
        }
        cfg.RetryMaxBackoff = time.Duration(n) * time.Millisecond
    }
    if cfg.RetryMaxBackoff < cfg.RetryBaseBackoff {
        return Config{}, fmt.Errorf("APP_RETRY_MAX_BACKOFF_MS must be >= APP_RETRY_BASE_BACKOFF_MS")
    }

    return cfg, nil
}
```

#### 3.2.1 Environment Variable Reference

| Variable | Required | Default | Notes |
|----------|----------|---------|-------|
| `EXISTING_CLIENT_BASE_URL` | Yes | none | Re-used directly from existing CLI/client tooling. |
| `EXISTING_CLIENT_API_TOKEN` | Yes | none | Re-used directly from existing CLI/client tooling; never log it. |
| `EXISTING_CLIENT_SCOPE` | No | empty | Re-used directly from existing CLI/client tooling. |
| `APP_TIMEOUT_SECONDS` | No | `30` | Base per-request timeout. |
| `APP_LOGFILE` | No | `my-mcp-server.log` | Use `"off"` to disable logs. |
| `APP_LOCKFILE` | No | `my-mcp-server.lock` | Use `"off"` to disable single-instance lock. |
| `APP_CACHE_ENABLED` | No | `on` | `on/true/1` enables response cache. |
| `APP_CACHE_TTL_SECONDS` | No | `3600` | TTL for cached responses. |
| `APP_CACHE_FILE` | No | `my-mcp-server-cache.json` | Snapshot path for persisted cache. |
| `APP_RETRY_MAX_ATTEMPTS` | No | `2` | Total attempts including first try. |
| `APP_RETRY_BASE_BACKOFF_MS` | No | `400` | Exponential backoff base delay. |
| `APP_RETRY_MAX_BACKOFF_MS` | No | `3000` | Backoff cap; must be `>=` base. |

#### 3.2.2 Honor Existing CLI/Client Environment Variables

When wrapping an existing SDK/CLI, MCP should **re-use the same environment variables** used by existing tooling for shared concerns (base URL, auth token, scope). The MCP process should feel like another consumer of the same config contract, not a parallel contract.

Conventions:
*   Keep one source of truth for shared variables (for example `EXISTING_CLIENT_BASE_URL`, `EXISTING_CLIENT_API_TOKEN`).
*   Reserve MCP-specific vars only for MCP-only behavior (logging target, lockfile, MCP cache policy).
*   Use the same validation rules and error messages as existing client tooling for shared fields.
*   Never duplicate secret variables across multiple names just to support MCP.

#### 3.2.3 Hierarchical Profile Config Resolution Pattern
When your server is co-deployed with standard CLI tools, unify configuration under a single file hierarchy (e.g. `~/.config/<app>/config.yaml`) using a strict fallback resolution chain:

```text
Priority Order: Command-line Flags > Environment Variables > Active profile in YAML file > Hardcoded Defaults
```

Store multiple configuration profiles in a single shared file:
```yaml
current_profile: production
profiles:
  default:
    base_url: "https://api.example.com"
    api_key: "dev-secret"
  production:
    base_url: "https://prod-api.example.com"
    api_key: "prod-secret"
```

**Reference implementation** — resolves flag > env > profile > default for each field, with explicit profile-not-found errors:

```go
type Profile struct {
    APIKey  string `yaml:"api_key"`
    BaseURL string `yaml:"base_url"`
}

type ConfigFile struct {
    CurrentProfile string             `yaml:"current_profile"`
    Profiles       map[string]Profile `yaml:"profiles"`
}

// Load resolves: CLI flags > env vars > active profile > hardcoded defaults.
func Load(flagProfile, flagAPIKey, flagBaseURL string) (*Config, error) {
    cfg := &Config{Timeout: 30 * time.Second}

    resolvedProfile := flagProfile
    if resolvedProfile == "" {
        resolvedProfile = os.Getenv("APP_PROFILE")
    }

    var file ConfigFile
    hasFile := false
    if path, ok := findConfigPath(); ok {
        if data, err := os.ReadFile(path); err == nil {
            if err := yaml.Unmarshal(data, &file); err == nil {
                hasFile = true
            }
        }
    }
    if resolvedProfile == "" && hasFile {
        resolvedProfile = file.CurrentProfile
    }

    var prof Profile
    if resolvedProfile != "" {
        if !hasFile {
            return nil, fmt.Errorf("profile %q requested but no config file found", resolvedProfile)
        }
        var exists bool
        prof, exists = file.Profiles[resolvedProfile]
        if !exists {
            return nil, fmt.Errorf("profile %q not found in config file", resolvedProfile)
        }
    }

    cfg.APIKey = firstNonEmpty(flagAPIKey, os.Getenv("APP_API_KEY"), prof.APIKey)
    cfg.BaseURL = firstNonEmpty(flagBaseURL, os.Getenv("APP_BASE_URL"), prof.BaseURL, "https://api.example.com")
    return cfg, nil
}

func findConfigPath() (string, bool) {
    // Check local, XDG, and home-dir locations in priority order.
    candidates := []string{"config.yaml"}
    if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
        candidates = append(candidates, filepath.Join(xdg, "myapp", "config.yaml"))
    }
    if home, err := os.UserHomeDir(); err == nil {
        candidates = append(candidates, filepath.Join(home, ".config", "myapp", "config.yaml"))
    }
    for _, p := range candidates {
        if _, err := os.Stat(p); err == nil {
            return p, true
        }
    }
    return "", false
}

func firstNonEmpty(vals ...string) string {
    for _, v := range vals {
        if v != "" {
            return v
        }
    }
    return ""
}
```

This ensures both CLI commands and background MCP operations draw from the same identity context seamlessly.

### 3.3 Logger Initialization

Default to a deterministic logfile target (e.g., `my-mcp-server.log`) when no explicit path is provided. The sentinel value `"off"` disables all logging.

**Log rotation:** MCP servers run indefinitely, so without rotation the log file grows until the disk is full. Implement a size-rotating writer that caps the active file (e.g. 10 MiB), renames it to `<path>.1` (one backup), and opens a fresh file. Wire it as the `io.Writer` passed to `slog.NewTextHandler`.

```go
// logger.go — size-rotating log file (10 MiB cap, one backup retained)
const maxLogSize = 10 * 1024 * 1024

type rotatingFile struct {
    path string
    mu   sync.Mutex
    f    *os.File
    size int64
}

func openRotating(path string) (*rotatingFile, error) {
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return nil, fmt.Errorf("create log dir: %w", err)
    }
    f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
    if err != nil {
        return nil, fmt.Errorf("open log file: %w", err)
    }
    info, _ := f.Stat()
    return &rotatingFile{path: path, f: f, size: info.Size()}, nil
}

func (l *rotatingFile) Write(p []byte) (int, error) {
    l.mu.Lock()
    defer l.mu.Unlock()
    if l.size+int64(len(p)) > maxLogSize {
        if err := l.rotate(); err != nil {
            return 0, err
        }
    }
    n, err := l.f.Write(p)
    l.size += int64(n)
    return n, err
}

func (l *rotatingFile) Close() error {
    l.mu.Lock()
    defer l.mu.Unlock()
    if l.f != nil {
        return l.f.Close()
    }
    return nil
}

func (l *rotatingFile) rotate() error {
    l.f.Close()
    backup := l.path + ".1"
    _ = os.Remove(backup)
    if err := os.Rename(l.path, backup); err != nil && !os.IsNotExist(err) {
        return err
    }
    f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
    if err != nil {
        return err
    }
    l.f, l.size = f, 0
    return nil
}
```

```go
func initLogger(cfg Config) (io.Closer, error) {
    level := slog.LevelInfo
    if cfg.Debug {
        level = slog.LevelDebug
    }

    if strings.EqualFold(cfg.LogFile, "off") {
        slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: level})))
        return nil, nil
    }

    if cfg.LogFile == "" {
        cfg.LogFile = "my-mcp-server.log"
    }

    lf, err := openRotating(cfg.LogFile)
    if err != nil {
        return nil, err
    }
    slog.SetDefault(slog.New(slog.NewTextHandler(lf, &slog.HandlerOptions{Level: level})))
    return lf, nil
}
```

### 3.3.1 `io.Writer` → slog Bridge

Some libraries (HTTP clients, SDKs) write trace/debug output to an `io.Writer`. Route this into the shared `slog` sink rather than letting it go to a separate file or stderr. A line-buffering writer accumulates bytes until a newline, then emits them as a single structured log record:

```go
type slogWriter struct {
    level slog.Level
    buf   strings.Builder
}

func newSlogWriter(level slog.Level) *slogWriter {
    return &slogWriter{level: level}
}

func (w *slogWriter) Write(p []byte) (int, error) {
    n := len(p)
    w.buf.Write(p)
    for {
        s := w.buf.String()
        idx := strings.IndexByte(s, '\n')
        if idx < 0 {
            break
        }
        slog.Log(context.Background(), w.level, strings.TrimSpace(s[:idx]))
        w.buf.Reset()
        w.buf.WriteString(s[idx+1:])
    }
    return n, nil
}
```

Usage:

```go
client := NewClient(cfg)
client.LogOut = newSlogWriter(slog.LevelDebug) // library trace → slog DEBUG
```

This keeps all logs in one file with a consistent format and respects the global log level — library traces are only emitted when debug mode is on.

### 3.4 Single-Instance PID Lock

MCP clients (like IDEs) may spawn your server multiple times. A PID lockfile prevents duplicate instances from competing for stdin/stdout or burning API rate limits.

*   Write PID to a file on startup. Check if the PID is alive (`os.FindProcess` and send Signal 0).
*   Remove on clean exit or if stale.
*   Allow disabling via `APP_LOCKFILE=off` for testing.

```go
func acquireLock(cfg Config) (func(), error) {
    if strings.EqualFold(cfg.LockFile, "off") {
        return func() {}, nil
    }

    if _, err := os.Stat(cfg.LockFile); err == nil {
        content, err := os.ReadFile(cfg.LockFile)
        if err == nil {
            pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
            if err == nil {
                process, err := os.FindProcess(pid)
                if err == nil {
                    if err = process.Signal(syscall.Signal(0)); err == nil {
                        return nil, fmt.Errorf("another instance is already running (PID: %d)", pid)
                    }
                }
            }
        }
        _ = os.Remove(cfg.LockFile)
    }

    f, err := os.OpenFile(cfg.LockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
    if err != nil {
        return nil, fmt.Errorf("could not create lock file: %w", err)
    }
    _, _ = fmt.Fprintf(f, "%d", os.Getpid())
    f.Close()

    return func() { _ = os.Remove(cfg.LockFile) }, nil
}
```

### 3.5 Error Types and Result Helpers (`errors.go`)

```go
package main

import (
    "fmt"
    "strings"

    "github.com/modelcontextprotocol/go-sdk/mcp"
)

type APIError struct {
    StatusCode int
    Body       string
}

func (e *APIError) Error() string {
    trimmed := strings.TrimSpace(e.Body)
    if len(trimmed) > 600 {
        trimmed = trimmed[:600] + "..."
    }
    return fmt.Sprintf("API error HTTP %d: %s", e.StatusCode, trimmed)
}

func textResult(text string) (*mcp.CallToolResult, error) {
    return &mcp.CallToolResult{
        Content: []mcp.Content{&mcp.TextContent{Text: text}},
    }, nil
}

// errResult returns only *mcp.CallToolResult (no error tuple).
// Tool handlers return (errResult(e), nil) — the Go error return from a tool handler
// signals an MCP protocol error, not an application error. Application errors
// belong inside CallToolResult with IsError: true. Keeping the helper single-return
// makes this distinction visible at every call site.
func errResult(err error) *mcp.CallToolResult {
    return &mcp.CallToolResult{
        Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
        IsError: true,
    }
}

func handleResponse(body string, err error) (*mcp.CallToolResult, error) {
    if err != nil {
        return errResult(err), nil
    }
    return textResult(body)
}
```

### 3.6 Tools Ecosystem (`tools.go`)

#### Design Goals & Principles

A production-grade tool ecosystem must integrate two primary layers:
1.  **Strict Input Validation & Safety**: Enforce field bounds, validate required inputs, and reject malformed structures *pre-flight* (before dispatching remote network calls).
2.  **LLM Ergonomics & Prompt Engineering**: Tailor tool names, schemas, and descriptions to instruct the model. This guides the LLM to query fields correctly, avoids hallucinated fields, and reduces resource waste.

**Key Practices:**
*   **Prompt-Engineered Descriptions**: Write instructions directly in schema `description` fields (e.g., advising the LLM on query structure or projection). Cross-reference related tools (e.g., *"Call `get_activity_types` to get valid IDs before calling this tool."*).
*   **Offline Data Injection**: For schema-heavy environments, register an "offline" metadata discovery tool. This returns hardcoded database metadata/syntax guides instantly, letting the LLM learn your structure without making costly remote HTTP requests.
*   **Safe Type Handling**: Explicitly assert types and bounds on `json.RawMessage` parameters (JSON-RPC numbers unmarshal into Go `float64`).
*   **Pre-flight Query Validation**: For tools that accept a query language expression, validate the expression in-process before dispatching the network call. This catches common LLM hallucinations (wrong operators, invalid syntax, unsupported functions) and returns a specific, actionable error immediately — faster and cheaper than a round-trip to the API.

```go
// validate.go — example for a custom query language
func Validate(query string) error {
    if strings.Contains(query, `"`) {
        return fmt.Errorf("string literals must use single quotes, not double quotes")
    }
    // ... additional rule checks
    return nil
}

// Hint enriches a validation error with a corrective suggestion.
func Hint(err error) string {
    // map error messages to actionable guidance
    return err.Error()
}
```

In the tool handler, call validate before building the request:

```go
if err := querylang.Validate(query); err != nil {
    slog.Warn("query validation failed", "error", err, "query", query)
    return errResult(fmt.Errorf("%s", querylang.Hint(err))), nil
}
```

Log at `WARN` (not `ERROR`) — the operation never started, so nothing failed on the server side. The corrected query on the next attempt will succeed.

#### 3.6.1 Generic Handler Factory Pattern

When you have many tools with a uniform decode → validate → execute → respond flow, use a generic handler factory rather than repeating boilerplate. Define a `Validatable` interface and implement it on each input struct.

```go
type Validatable interface {
    Validate() error
}

// handleTool returns a ToolHandler that decodes args into T, validates, then calls fn.
func handleTool[T Validatable](fn func(context.Context, T) (string, error)) mcp.ToolHandler {
    return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        slog.Info("tool called", "name", req.Params.Name)
        in, err := decodeArgs[T](req.Params.Arguments)
        if err != nil {
            slog.Error("tool argument decode failed", "tool", req.Params.Name, "error", err)
            return errResult(fmt.Errorf("invalid arguments: %w", err)), nil
        }
        if err := in.Validate(); err != nil {
            slog.Error("tool validation failed", "tool", req.Params.Name, "error", err)
            return errResult(err), nil
        }
        resp, err := fn(ctx, in)
        if err != nil {
            slog.Error("tool execution failed", "tool", req.Params.Name, "error", err)
            return errResult(err), nil
        }
        return textResult(resp)
    }
}
```

Registration then becomes a one-liner per tool:

```go
s.AddTool(&mcp.Tool{
    Name:        "app_list_items",
    Description: "List items with filter and pagination.",
    InputSchema: itemsListSchema,
}, handleTool(client.ListItems))
```

#### 3.6.2 Strict Argument Decoding

Use a streaming `json.Decoder` rather than `json.Unmarshal` to catch both unknown fields and trailing garbage:

```go
func decodeArgs[T any](raw json.RawMessage) (T, error) {
    var out T
    if len(raw) == 0 {
        return out, nil // some tools take no args
    }
    dec := json.NewDecoder(bytes.NewReader(raw))
    dec.DisallowUnknownFields() // reject fields not in the struct
    if err := dec.Decode(&out); err != nil {
        return out, err
    }
    if err := dec.Decode(&struct{}{}); err != io.EOF {
        return out, fmt.Errorf("unexpected trailing data")
    }
    return out, nil
}
```

**Why `DisallowUnknownFields` matters**: if a field is misspelled (e.g., `"limitt": 10`) it would silently be ignored without this flag. Rejecting unknown fields surfaces the LLM's mistake immediately as a tool error rather than returning wrong data.

#### 3.6.3 Typed Input Structs with Validate() and QueryParams()

Define a Go struct for each tool's input. Implement two methods:

*   `Validate() error` — pre-flight business rule checks (bounds, enums, required combos).
*   `QueryParams() url.Values` — converts the struct to API query parameters.

Use `*bool` for optional boolean flags (distinguishes "not set" from `false`):

```go
type ItemsListInput struct {
    Query     string   `json:"query,omitempty"`
    SiteIDs   []string `json:"site_ids,omitempty"`
    IsActive  *bool    `json:"is_active,omitempty"` // nil = omit; false = explicitly false
    Limit     int      `json:"limit,omitempty"`
    SortOrder string   `json:"sort_order,omitempty"`
    Cursor    string   `json:"cursor,omitempty"`
}

func (in ItemsListInput) Validate() error {
    if in.Limit != 0 && (in.Limit < 1 || in.Limit > 1000) {
        return fmt.Errorf("limit must be between 1 and 1000")
    }
    if in.SortOrder != "" {
        s := strings.ToLower(strings.TrimSpace(in.SortOrder))
        if s != "asc" && s != "desc" {
            return fmt.Errorf("sort_order must be asc or desc")
        }
    }
    return nil
}

func (in ItemsListInput) QueryParams() url.Values {
    q := url.Values{}
    addString(q, "query", in.Query)
    addCSV(q, "siteIds", in.SiteIDs)
    addBool(q, "isActive", in.IsActive)
    addInt(q, "limit", in.Limit)
    addString(q, "sortOrder", strings.ToLower(in.SortOrder))
    addString(q, "cursor", in.Cursor)
    return q
}
```

Helper functions keep `QueryParams()` concise and skip zero/empty values automatically:

```go
func addString(q url.Values, key, value string) {
    if v := strings.TrimSpace(value); v != "" {
        q.Set(key, v)
    }
}

func addInt(q url.Values, key string, value int) {
    if value > 0 {
        q.Set(key, fmt.Sprintf("%d", value))
    }
}

func addBool(q url.Values, key string, value *bool) {
    if value != nil {
        if *value {
            q.Set(key, "true")
        } else {
            q.Set(key, "false")
        }
    }
}

func addCSV(q url.Values, key string, values []string) {
    // trim and drop empty strings before joining
    cleaned := make([]string, 0, len(values))
    for _, v := range values {
        if t := strings.TrimSpace(v); t != "" {
            cleaned = append(cleaned, t)
        }
    }
    if len(cleaned) > 0 {
        q.Set(key, strings.Join(cleaned, ","))
    }
}
```

#### 3.6.4 Schema Construction Helpers

Rather than writing raw JSON strings for every schema, use a `buildSchema` helper that enforces `additionalProperties: false` and `type: object` consistently:

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
        panic(err) // schema construction is a programming error, not a runtime error
    }
    return b
}

// stringArraySchema creates the JSON Schema fragment for an array-of-strings field.
// Pass enum values to restrict to a known set.
func stringArraySchema(description string, enums ...string) map[string]any {
    items := map[string]any{"type": "string"}
    if len(enums) > 0 {
        items["enum"] = enums
    }
    return map[string]any{
        "type":        "array",
        "description": description,
        "items":       items,
    }
}
```

Example usage with enum constraints on known fields:

```go
var itemsListSchema = buildSchema(map[string]any{
    "query":      map[string]any{"type": "string", "description": "Free text search query."},
    "site_ids":   stringArraySchema("Site IDs to filter by."),
    "os_types":   stringArraySchema("OS types.", "macos", "windows", "linux"),
    "sort_order": map[string]any{"type": "string", "enum": []string{"asc", "desc"}},
    "limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 1000, "description": "Default is 10."},
    "cursor":     map[string]any{"type": "string", "description": "Pagination cursor from a previous call."},
})

var noArgsSchema = buildSchema(map[string]any{})
```

Using `"format": "date-time"` on timestamp fields lets the LLM know to pass RFC3339 strings:

```go
"created_at_gte": map[string]any{
    "type":        "string",
    "format":      "date-time",
    "description": "Start of creation time range (RFC3339).",
},
```

#### 3.6.5 Original `registerTools` / Handler Reference

For single-file projects without the generics pattern, the inline handler approach remains valid:

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"

    "github.com/modelcontextprotocol/go-sdk/mcp"
)

var querySchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "filter": {
            "type": "string",
            "description": "Filter expression. IMPORTANT: Do not use wildcard (*) selects; list columns explicitly."
        },
        "limit": {
            "type": "integer",
            "description": "Maximum number of rows to return. Default is 50.",
            "minimum": 1,
            "maximum": 1000
        }
    },
    "required": ["filter"],
    "additionalProperties": false
}`)

// Hardcoded schema metadata served as an offline discovery reference
const dbSchemaMetadata = `{
    "tables": {
        "events": {
            "columns": {
                "id": "UUID",
                "timestamp": "Timestamp in RFC3339 format",
                "message": "String",
                "severity": "String (INFO, WARN, ERROR)"
            }
        }
    }
}`

func registerTools(s *mcp.Server, client *Client) {
    // OFFLINE DATA INJECTION: static schema info without making API calls
    s.AddTool(&mcp.Tool{
        Name:        "get_schema_metadata",
        Description: "Returns static database table schemas and data types. Call this BEFORE writing a query if you need to discover available columns.",
        InputSchema: json.RawMessage(`{"type": "object", "additionalProperties": false}`),
    }, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        slog.Info("tool called", "name", req.Params.Name)
        return textResult(dbSchemaMetadata)
    })

    // VALIDATED REMOTE API QUERY TOOL
    s.AddTool(&mcp.Tool{
        Name:        "api_query",
        Description: "Search raw event payloads using SQL-like expressions.",
        InputSchema: querySchema,
    }, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        slog.Info("tool called", "name", req.Params.Name, "arguments", string(req.Params.Arguments))

        var args map[string]any
        if req.Params.Arguments != nil {
            if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
                return errResult(fmt.Errorf("invalid arguments JSON: %w", err)), nil
            }
        }

        filter, ok := args["filter"].(string)
        if !ok || filter == "" {
            return errResult(fmt.Errorf("argument 'filter' is required and must be a non-empty string")), nil
        }

        // JSON-RPC numbers arrive as float64 — cast carefully
        limit := 50
        if limitVal, ok := args["limit"].(float64); ok {
            limit = int(limitVal)
            if limit < 1 || limit > 1000 {
                return errResult(fmt.Errorf("argument 'limit' %d is out of range [1, 1000]", limit)), nil
            }
        }

        payload, err := json.Marshal(map[string]any{"filter": filter, "limit": limit})
        if err != nil {
            return errResult(fmt.Errorf("marshaling request: %w", err)), nil
        }

        body, err := client.Post(ctx, "/v1/query", payload)
        return handleResponse(body, err)
    })
}

#### 3.6.6 Client-Side Query Translation

LLMs are pre-trained on standardized querying languages like standard SQL, Kusto/KQL, or Splunk SPL. When interacting with proprietary query engines (like S1QL), they frequently output syntax dialect errors, such as using `between`, `startswith()`, `| filter`, or SQL `in (A, B, C)`.

Instead of returning `400 Bad Request` and forcing the LLM to try again, implement an inline client-side translation layer (`NormalizeQuery`) to rewrite these structures:

```go
// query_normalizer.go — Client-side translation layer for dialect adaptations
package main

import (
    "fmt"
    "regexp"
    "strings"
)

var (
    // Translate SQL 'BETWEEN' syntax: field BETWEEN 'a' AND 'b'
    betweenRegex = regexp.MustCompile("(?i)(\\w+)\\s+between\\s+['\"]([^'\"]+)['\"]\\s+and\\s+['\"]([^'\"]+)['\"]")
    // Translate SQL-like 'IN' string arrays: field in ('a', 'b')
    sqlInRegex   = regexp.MustCompile("(?i)(\\w+)\\s+in\\s+\\(([^)]+)\\)")
)

func NormalizeQuery(query string) string {
    q := query
    
    // 1. Translate BETWEEN into compound relational operators
    q = betweenRegex.ReplaceAllString(q, "$1 >= '$2' and $1 <= '$3'")
    
    // 2. Translate IN arrays into group OR operations
    q = sqlInRegex.ReplaceAllStringFunc(q, func(match string) string {
        matches := sqlInRegex.FindStringSubmatch(match)
        if len(matches) < 3 { return match }
        field := matches[1]
        rawItems := matches[2]
        
        items := strings.Split(rawItems, ",")
        var clauses []string
        for _, item := range items {
            trimmed := strings.TrimSpace(item)
            clauses = append(clauses, fmt.Sprintf("%s = %s", field, trimmed))
        }
        return "(" + strings.Join(clauses, " or ") + ")"
    })
    
    return q
}
```

#### 3.6.7 Resource & Temporal Guardrails

LLM agents are notorious for running analytical queries over extreme time bounds (e.g. searching all logs for the entire year) or omitting essential query filters/limits.

Establishing strict **Temporal and Operator Guardrails** ensures bad requests are blocked *pre-flight*.

```go
// tools.go — Guardrails for heavy data analytical queries
package main

import (
    "errors"
    "strings"
    "time"
)

type QueryGuardrailInput struct {
    Query     string    `json:"query"`
    FromDate  time.Time `json:"from_date"`
    ToDate    time.Time `json:"to_date"`
}

func (in QueryGuardrailInput) Validate() error {
    span := in.ToDate.Sub(in.FromDate)
    
    // Guardrail: Queries spanning >= 72h MUST contain structural limit/filter protections
    if span >= 72*time.Hour {
        lowerQuery := strings.ToLower(in.Query)
        
        // Ensure PowerQueries contain limit pipelines
        if strings.Contains(lowerQuery, "|") && !strings.Contains(lowerQuery, "limit") {
            return errors.New("heavy queries covering >= 72 hours require an explicit '| limit <N>' pipeline to protect memory bounds")
        }
        
        // Ensure Standard queries contain bounding predicates
        if !strings.Contains(lowerQuery, "site.id") && !strings.Contains(lowerQuery, "endpoint.id") {
            return errors.New("broad data-lake query spans (>= 72 hours) must be scoped with a specific site.id or endpoint.id predicate")
        }
    }
    
    return nil
}
```
```

## 4. Client Layer

> **If your project already has a working HTTP client, SDK wrapper, or CLI driver, use it directly.** The MCP adapter only needs a callable that accepts a `context.Context` and returns `(string, error)` — the raw API response as text plus any error. Wire your existing client into `registerTools` and skip this section.

The minimal contract the tool handlers depend on:

```go
// Anything satisfying this shape works — a method, a func var, a thin wrapper.
Post(ctx context.Context, path string, payload []byte) (string, error)
```

The reference implementation below is for projects that are building from scratch or have no existing HTTP client.

### 4.1 Reference HTTP Client (`client.go`)

Design goals:
*   Return raw JSON (`string`); do not deserialize just to re-serialize for the LLM.
*   Always apply a per-request timeout derived from config.
*   Centralize auth (Bearer token, API keys).
*   Retry on transient errors (5xx, 429) with exponential backoff + jitter.
*   Respect the upstream `Retry-After` header when rate-limited.
*   Coalesce duplicate in-flight requests with `singleflight` to avoid redundant network calls.
*   Log request metrics (duration, status, byte sizes) for observability. **Never log auth tokens.**

#### Retry-After Header Handling

APIs often include a `Retry-After` header on 429 responses. Parse it before falling back to the computed backoff. The header may be an integer (seconds) or an HTTP-date:

```go
func retryAfterDelay(retryAfter string, fallback time.Duration) time.Duration {
    retryAfter = strings.TrimSpace(retryAfter)
    if retryAfter == "" {
        return fallback
    }
    // Integer seconds form
    if seconds, err := strconv.Atoi(retryAfter); err == nil {
        if seconds <= 0 {
            return 0
        }
        return time.Duration(seconds) * time.Second
    }
    // HTTP-date form (RFC1123)
    if t, err := time.Parse(http.TimeFormat, retryAfter); err == nil {
        if d := time.Until(t); d > 0 {
            return d
        }
        return 0
    }
    return fallback
}
```

Use `sleepWithContext` so a context cancellation can interrupt the backoff sleep:

```go
func sleepWithContext(ctx context.Context, d time.Duration) error {
    if d <= 0 {
        return nil
    }
    timer := time.NewTimer(d)
    defer timer.Stop()
    select {
    case <-ctx.Done():
        return ctx.Err()
    case <-timer.C:
        return nil
    }
}
```

#### Only Retry Safe Methods

Retrying POST mutations risks double-application of side effects. Limit rate-limit retries to idempotent methods:

```go
func shouldRetryOnRateLimit(method string, statusCode int, attempt int) bool {
    return strings.EqualFold(method, http.MethodGet) &&
        statusCode == http.StatusTooManyRequests &&
        attempt < maxRateLimitRetries
}
```

#### Duration as float64 in Logs

Log `duration_ms` as `float64` (microseconds / 1000.0) rather than `int` — this preserves sub-millisecond precision on fast cache-hit paths:

```go
"duration_ms", float64(duration.Microseconds()) / 1000.0,
```

```go
package main

import (
    "bytes"
    "context"
    "errors"
    "fmt"
    "io"
    "log/slog"
    "math"
    "math/rand"
    "net"
    "net/http"
    "time"

    "golang.org/x/sync/singleflight"
)

type Client struct {
    httpClient       *http.Client
    baseURL          string
    token            string
    defaultTimeout   time.Duration
    retryMaxAttempts int
    retryBaseBackoff time.Duration
    retryMaxBackoff  time.Duration
    inflight         singleflight.Group
}

func NewClient(cfg Config) *Client {
    return &Client{
        httpClient:       &http.Client{},
        baseURL:          cfg.BaseURL,
        token:            cfg.APIToken,
        defaultTimeout:   cfg.Timeout,
        retryMaxAttempts: 2,
        retryBaseBackoff: 400 * time.Millisecond,
        retryMaxBackoff:  3 * time.Second,
    }
}

func (c *Client) Post(ctx context.Context, path string, payload []byte) (string, error) {
    res, err, _ := c.inflight.Do(path+string(payload), func() (any, error) {
        return c.doPostWithRetries(ctx, path, payload)
    })
    if err != nil {
        return "", err
    }
    return res.(string), nil
}

func (c *Client) doPostWithRetries(ctx context.Context, path string, payload []byte) (string, error) {
    var lastErr error
    for attempt := 1; attempt <= c.retryMaxAttempts; attempt++ {
        resp, err := c.doPost(ctx, path, payload)
        if err == nil {
            return resp, nil
        }
        lastErr = err
        if !c.shouldRetry(ctx, err, attempt) {
            return "", err
        }
        backoff := c.computeBackoff(attempt)
        slog.Warn("retrying request", "path", path, "attempt", attempt+1, "backoff_ms", backoff.Milliseconds(), "error", err)
        timer := time.NewTimer(backoff)
        select {
        case <-ctx.Done():
            timer.Stop()
            return "", ctx.Err()
        case <-timer.C:
        }
    }
    return "", lastErr
}

func (c *Client) shouldRetry(ctx context.Context, err error, attempt int) bool {
    if attempt >= c.retryMaxAttempts || ctx.Err() != nil {
        return false
    }
    if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
        return false
    }
    var netErr net.Error
    if errors.As(err, &netErr) && netErr.Timeout() {
        return false
    }
    var apiErr *APIError
    if errors.As(err, &apiErr) {
        return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= 500
    }
    return false
}

func (c *Client) computeBackoff(attempt int) time.Duration {
    backoff := float64(c.retryBaseBackoff) * math.Pow(2, float64(attempt-1))
    if time.Duration(backoff) > c.retryMaxBackoff {
        backoff = float64(c.retryMaxBackoff)
    }
    return time.Duration(backoff * (0.75 + rand.Float64()*0.5))
}

func (c *Client) doPost(ctx context.Context, path string, payload []byte) (string, error) {
    reqCtx, cancel := context.WithTimeout(ctx, c.defaultTimeout)
    defer cancel()

    req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
    if err != nil {
        return "", fmt.Errorf("creating request: %w", err)
    }
    req.Header.Set("Authorization", "Bearer "+c.token)
    req.Header.Set("Content-Type", "application/json")

    start := time.Now()
    resp, err := c.httpClient.Do(req)
    if err != nil {
        return "", fmt.Errorf("request failed: %w", err)
    }
    defer resp.Body.Close()

    body, err := io.ReadAll(resp.Body)
    if err != nil {
        return "", fmt.Errorf("reading response: %w", err)
    }

    slog.Info("api_request",
        "path", path,
        "status", resp.StatusCode,
        "duration_ms", time.Since(start).Milliseconds(),
        "request_bytes", len(payload),
        "response_bytes", len(body),
    )

    if resp.StatusCode != http.StatusOK {
        return string(body), &APIError{StatusCode: resp.StatusCode, Body: string(body)}
    }
    return string(body), nil
}
```

### 4.2 NDJSON Streaming Client Pattern

When an API streams results as newline-delimited JSON rather than returning a single response body, return a `*Stream` with `Next() / Close()` instead of accumulating the full body. This lets the caller process records incrementally and avoids holding the entire response in memory.

Two details matter here:

**`UseNumber()`** — decode with `dec.UseNumber()` rather than the default `float64` for JSON numbers. Large integer IDs (Snowflake IDs, nanosecond timestamps) lose precision when cast to `float64`. `json.Number` preserves the original string and can be parsed as `int64` without loss.

**`io.LimitReader` on error bodies** — bound memory when reading error responses. APIs can return megabyte-sized HTML error pages on 5xx; reading the full body then truncating the string is too late. Cap the read at the source:

```go
buf, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
```

Full streaming client pattern:

```go
type QueryStream struct {
    body    io.ReadCloser
    scanner *bufio.Scanner
    err     error
}

func (c *Client) QueryStream(ctx context.Context, path string, payload []byte) (*QueryStream, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
    if err != nil {
        return nil, fmt.Errorf("build request: %w", err)
    }
    req.Header.Set("Authorization", "Bearer "+c.token)
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Accept", "application/x-ndjson, application/json")

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return nil, fmt.Errorf("send request: %w", err)
    }

    if resp.StatusCode >= 400 {
        defer resp.Body.Close()
        buf, _ := io.ReadAll(io.LimitReader(resp.Body, 8192)) // cap before reading
        return nil, &APIError{StatusCode: resp.StatusCode, Body: string(buf)}
    }

    scanner := bufio.NewScanner(resp.Body)
    scanner.Buffer(make([]byte, 64*1024), 16*1024*1024) // allow large lines
    return &QueryStream{body: resp.Body, scanner: scanner}, nil
}

// Next returns the next decoded NDJSON record. Returns io.EOF when exhausted.
func (s *QueryStream) Next() (map[string]any, error) {
    if s.err != nil {
        return nil, s.err
    }
    for s.scanner.Scan() {
        line := bytes.TrimSpace(s.scanner.Bytes())
        if len(line) == 0 {
            continue
        }
        dec := json.NewDecoder(bytes.NewReader(line))
        dec.UseNumber() // preserves large integer precision — never use float64 for IDs
        var m map[string]any
        if err := dec.Decode(&m); err != nil {
            s.err = fmt.Errorf("decode response line: %w", err)
            return nil, s.err
        }
        return m, nil
    }
    if err := s.scanner.Err(); err != nil {
        s.err = err
        return nil, err
    }
    s.err = io.EOF
    return nil, io.EOF
}

func (s *QueryStream) Close() error {
    if s.body != nil {
        return s.body.Close()
    }
    return nil
}
```

Callers iterate with a simple loop:

```go
stream, err := c.QueryStream(ctx, "/v1/query", payload)
if err != nil {
    return errResult(err), nil
}
defer stream.Close()

var sb strings.Builder
enc := json.NewEncoder(&sb)
enc.SetEscapeHTML(false)
for {
    rec, err := stream.Next()
    if errors.Is(err, io.EOF) {
        break
    }
    if err != nil {
        return errResult(err), nil
    }
    _ = enc.Encode(rec)
}
return textResult(sb.String())
```

Also apply `UseNumber()` in the §3.6.2 `decodeArgs` helper when the tool input may contain large integer fields — the default `float64` silently truncates integers larger than 2^53.

### 4.3 Adapting an Existing Client

If you have an existing Go SDK or HTTP wrapper, create a thin shim so your existing client satisfies the contract expected by `registerTools`:

```go
// shim.go — bridge your existing client to the shape tools.go expects
type MySDKShim struct {
    sdk *mypackage.Client
}

func (s *MySDKShim) Post(ctx context.Context, path string, payload []byte) (string, error) {
    // Delegate to your existing client. Normalize errors to *APIError if needed.
    resp, err := s.sdk.DoRequest(ctx, path, payload)
    if err != nil {
        return "", err
    }
    return resp.RawJSON, nil
}
```

Pass `&MySDKShim{sdk: existingClient}` wherever `*Client` is expected. If the existing client already handles retries and auth, do not duplicate that logic.

### 4.4 Temporal-Aware Timeout Scaling

Static timeouts are an architectural anti-pattern for data-lake MCP servers. A 30-second timeout is perfect for standard operational queries covering the last 2 hours, but it will immediately fail on a PowerQuery map-reduce aggregation stretching back 30 days.

We must dynamically analyze the requested temporal window (or command type) in-flight and scale the context deadline accordingly.

```go
// client.go — Implementing dynamic range-aware timeout selection
package main

import (
    "context"
    "time"
)

type TimeRangeExtractor interface {
    GetTemporalRange() (time.Duration, bool)
}

func (c *Client) getTimeoutForRequest(req any, isPowerQuery bool) time.Duration {
    extractor, ok := req.(TimeRangeExtractor)
    if !ok {
        return c.defaultTimeout // Fallback to config timeout (e.g. 30s)
    }

    duration, ok := extractor.GetTemporalRange()
    if !ok {
        return c.defaultTimeout
    }

    // Determine timeout based on query window breadth
    switch {
    case isPowerQuery && duration >= 72*time.Hour:
        return 150 * time.Second // heavy analytical map-reduce
    case isPowerQuery:
        return 60 * time.Second  // moderate pipeline aggregation
    case duration >= 48*time.Hour:
        return 90 * time.Second  // deep data-lake sweeps
    default:
        return 30 * time.Second  // low-latency operational range
    }
}
```

## 5. Optional: Result Cache (`cache.go`)

When the upstream API is slow or rate-limited, a TTL cache with disk persistence avoids redundant calls across server restarts. Key design decisions:

*   Use a singleflight group on the client (section 4.1) to coalesce simultaneous duplicate requests — only one network call fires even if multiple tool invocations arrive at once.
*   **Double-check the cache inside the singleflight leader** to handle the race where a second identical request arrives after the cache miss is detected but before the network call completes.
*   Bucket relative time windows (e.g., round `"24h"` to a 5-minute boundary) so cache keys for the same logical query hit the same entry.
*   Persist to disk on shutdown; reload on startup, skipping expired entries.
*   Skip caching paginated responses (continuation tokens) — page 2 of a live query has no stable key.
*   Wire a save-on-shutdown hook via the graceful shutdown path in `main.go`.

```go
// In main.go, after initializing cache:
defer func() {
    if err := cache.SaveToDisk(cfg.CacheFile); err != nil {
        slog.Warn("failed to save cache snapshot", "error", err)
    }
    cache.Close()
}()
```

### 5.1 Caching Conventions (Implementation Synthesis)

When you keep a persistent cache, document the conventions explicitly so behavior is predictable:

*   **Key normalization:** normalize query/filter whitespace and bucket relative windows (for example, `"24h"` at 5-minute boundaries) before key hashing so semantically identical requests hit the same entry.
*   **Pagination safety:** never read/write cache entries for requests containing `cursor` (or any continuation token), and skip caching responses that contain a cursor in their payload.
*   **In-flight dedupe:** combine cache with `singleflight` so duplicate misses only execute one upstream request. Perform a second cache check *inside* the singleflight leader body to handle the concurrent-miss race.
*   **Snapshot integrity:** persist snapshots with a deterministic payload checksum (SHA-256 over sorted keys + values) and reject corrupted files on load.
*   **Atomic writes:** write snapshots to a temp file and rename atomically to avoid partial/truncated files on crash.
*   **Observed lifecycle metrics:** log load/save stats (`entries_in_file`, `loaded_entries`, `skipped_expired`, `integrity_verified`, bytes written) so operators can validate cache health.

#### 5.1.1 Relative & Absolute Timestamp Bucketing
LLMs frequently query with moving temporal expressions (such as `"24h"`, `"7d"`, or custom dates). Because "now" moves continuously, consecutive identical searches generate slightly different raw API queries, yielding cache misses. To make time-based queries cacheable, implement time normalization inside your key generator:

```go
func (c *Cache) normalizeStringForCacheKey(key, value string) string {
    lowerKey := strings.ToLower(key)
    trimmed := strings.TrimSpace(value)

    if strings.Contains(lowerKey, "time") || lowerKey == "createdat__gte" {
        // 1. Bucket absolute timestamps (RFC3339)
        if t, err := time.Parse(time.RFC3339, trimmed); err == nil {
            bucket := t.Truncate(5 * time.Minute).Format(time.RFC3339)
            return fmt.Sprintf("bucket:%s", bucket)
        }
        // 2. Recognize and bucket relative intervals (e.g. "1h", "24h")
        if isRelativeTime(trimmed) {
            nowBucket := time.Now().UTC().Truncate(5 * time.Minute).Format(time.RFC3339)
            return fmt.Sprintf("relative:%s@bucket:%s", strings.ToLower(trimmed), nowBucket)
        }
    }
    return strings.ToLower(trimmed)
}

func isRelativeTime(s string) bool {
    s = strings.ToLower(s)
    for _, suffix := range []string{"h", "m", "s", "d", "w"} {
        if strings.HasSuffix(s, suffix) {
            return true
        }
    }
    return false
}
```

#### 5.1.2 Deterministic Cache Snapshot Checksumming
Because Go maps are unordered, serializing snapshots directly to JSON produces arbitrary key orderings, corrupting simple file hashes. Ensure checksum predictability by sorting map keys before calculation:

```go
func computeStableChecksum(version int, entries map[string]SnapshotEntry) string {
    h := sha256.New()
    fmt.Fprintf(h, "version:%d|", version)

    keys := make([]string, 0, len(entries))
    for k := range entries {
        keys = append(keys, k)
    }
    sort.Strings(keys)

    for _, k := range keys {
        entry := entries[k]
        fmt.Fprintf(h, "k:%s|v:%s|e:%d|", k, entry.Value, entry.ExpiresAt.UnixNano())
    }
    return hex.EncodeToString(h.Sum(nil))
}
```

#### 5.1.3 Cache-Stabilizing Whitespace Collapse

When caching query-based endpoints, any extraneous spacing generated by the LLM (like newlines, tab indentation, or multi-space formatting) results in different SHA-256 cache keys, completely breaking caching efficiency. 

However, a naive space removal will corrupt string literals within quotes (e.g., `event.name = "My Crucial Incident"` becomes `event.name = "MyCrucialIncident"`). Implement a space-normalizer that ignores quoted literals:

```go
// cache.go — Space normalizer that ignores quoted literals
package main

import (
    "regexp"
    "strings"
)

var (
    // Matches either single/double quoted strings OR sequences of whitespace
    spaceCollapseRegex = regexp.MustCompile(`('[^']*'|"[^"]*")|\s+`)
)

func CollapseWhitespace(query string) string {
    result := spaceCollapseRegex.ReplaceAllStringFunc(query, func(match string) string {
        if strings.HasPrefix(match, "'") || strings.HasPrefix(match, "\"") {
            return match // preserve quoted literals exactly
        }
        return " " // collapse arbitrary spaces/tabs/newlines to a single space
    })
    return strings.TrimSpace(result)
}
```

### 5.2 Per-Endpoint TTL Overrides

Not all API responses age at the same rate. Define TTL constants by data volatility and dispatch them by path prefix:

```go
const (
    ttlStatic  = 6 * time.Hour    // reference data — sites, accounts, activity types
    ttlDynamic = 10 * time.Minute // live data — threats, recent activities
)

func ttlForPath(path string) time.Duration {
    switch {
    case strings.HasPrefix(path, "/v1/sites"):
        return ttlStatic
    case path == "/v1/activity-types":
        return 12 * time.Hour
    case strings.HasPrefix(path, "/v1/threats"),
        strings.HasPrefix(path, "/v1/activities"):
        return ttlDynamic
    default:
        return 0 // 0 signals "use the cache's defaultTTL"
    }
}
```

Pass the result to `cache.Set` after a successful response:

```go
ttl := ttlForPath(path)
if ttl == 0 {
    ttl = c.cache.defaultTTL
}
c.cache.Set(ctx, method, path, query, body, resp.StatusCode, string(respBody), ttl)
```

### 5.3 Negative Caching

Cache deterministic API errors (HTTP 400 and 404) with a short TTL to avoid re-hitting the upstream for provably invalid requests. Do **not** cache 5xx errors — those are transient and should not poison the cache.

```go
// After receiving a non-2xx response:
if resp.StatusCode >= 500 {
    // transient — do not cache
} else if resp.StatusCode >= 400 {
    // deterministic client error — cache briefly to absorb retries
    c.cache.Set(ctx, method, path, query, body, resp.StatusCode, string(respBody), 5*time.Minute)
}
return string(respBody), &APIError{StatusCode: resp.StatusCode, Body: string(respBody)}
```

## 6. Testing Framework

Use Python's `pydantic_ai` to test the binary end-to-end with a real LLM agent.

**`tools/test_mcp.py`:**
```python
import asyncio, os
from pydantic_ai import Agent
from pydantic_ai.mcp import MCPServerStdio

async def main():
    env = os.environ.copy()
    env.setdefault("EXISTING_CLIENT_API_TOKEN", "dummy_token")
    env.setdefault("EXISTING_CLIENT_BASE_URL", "https://api.example.com")

    server = MCPServerStdio(
        "go",
        ["run", "./cmd/my-mcp-server"],
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

**`Makefile`:**
```makefile
.PHONY: all build run clean test fmt vet install test-e2e

APP_NAME = my-mcp-server

all: fmt vet build

build:
	go build -o $(APP_NAME) ./cmd/$(APP_NAME)

test:
	go test -v ./...

test-e2e:
	uv run tools/test_mcp.py

fmt:
	go fmt ./...

vet:
	go vet ./...

install: build
	mkdir -p $(HOME)/.local/bin
	cp $(APP_NAME) $(HOME)/.local/bin/

clean:
	rm -f $(APP_NAME)
```

Run unit tests: `make test`  
Run end-to-end: `make test-e2e`

## 7. Logging Guidance

### 7.1 Level Usage

| Level | When to use | Examples from this implementation |
|-------|-------------|-----------------------------------|
| `DEBUG` | High-frequency, low-value events that would flood production logs | `cache miss`, `request coalesced`, lockfile cleanup |
| `INFO` | One-off lifecycle events and every completed request | server start/stop, cache snapshot load/save, `tool called`, `request completed` |
| `WARN` | Recoverable problems — the request still completed but something was wrong | non-200 responses (4xx, 5xx), retry attempts, slow requests, cache snapshot load failure |
| `ERROR` | Non-recoverable failures where the operation produced no result | `context deadline exceeded`, network errors, server startup failure |

**Rule of thumb:** if the operation eventually succeeded, it's at most `WARN`. If nothing was returned to the caller, it's `ERROR`.

Cache misses and singleflight coalescing are `DEBUG` — they fire on almost every request and contain no actionable signal in normal operation. Promote to `INFO` only if you need to audit cache effectiveness temporarily.

### 7.2 Slow-Request Warnings

Use per-endpoint slow-request thresholds rather than a single global one. A good default is 2× the configured timeout for that endpoint:

```go
const slowRequestThreshold = 2 * defaultTimeout  // or per-endpoint

elapsed := time.Since(start)
if resp.StatusCode == http.StatusOK && elapsed > slowRequestThreshold {
    slog.Warn("slow request", "path", path, "duration_ms", elapsed.Milliseconds(), "threshold_ms", slowRequestThreshold.Milliseconds())
} else if resp.StatusCode != http.StatusOK {
    slog.Warn("request completed with non-200 response", attrs...)
} else {
    slog.Info("request completed", attrs...)
}
```

If your server supports adaptive timeouts (scaling the timeout based on query time range), log the chosen timeout at `INFO` when it differs from the default — it explains why a request was allowed to run longer than usual.

### 7.3 Structured Field Conventions

Use consistent field names across all call sites so log lines can be aggregated and filtered without string parsing.

**Request/response fields** (always present on completed requests):
```
method          HTTP verb (GET, POST)
path            API path without base URL or query string
status_code     HTTP status integer
duration_ms     float — wall-clock milliseconds (not truncated to int, preserves sub-ms for fast paths)
request_bytes   int
response_bytes  int
```

**API-level fields** (when the upstream returns structured metadata):
```
api_status          status string from the response body (e.g. "success", "error/client/badParam")
cpu_usage_ms        server-side CPU reported by the upstream
matching_events     row/event count from the upstream
omitted_events      events dropped by the upstream due to limits
has_continuation_token  bool — whether the result is paginated
```

**Cache fields**:
```
entries_in_file     entries read from disk
loaded_entries      entries actually loaded (expired ones are skipped)
skipped_expired     entries dropped at load time
integrity_verified  bool — whether checksum matched
snapshot_age        duration since the snapshot was written
```

**Tool fields**:
```
name    tool name (matches MCP tool registration)
args    raw JSON arguments string — log at INFO on entry
```

Avoid logging `args` at `DEBUG` and promoting later; always log it at `INFO` on `tool called` so you can correlate a request with its inputs when investigating an error without needing debug mode enabled.

### 7.4 Version String & Build-Time Injection

Declare a package-level variable in `version.go` and overwrite it at build time via `-ldflags`. The default `"dev"` value is used during `go run` and local builds:

```go
// version.go
package main

var version = "dev"
```

Inject the short git commit hash at build time in the `Makefile`:

```makefile
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

build:
    go build -ldflags "-X main.version=$(GIT_COMMIT)" -o $(APP_NAME) ./cmd/$(APP_NAME)
```

This produces a binary where `version` is e.g. `"a3f9c12"`. If you also tag releases, combine both:

```makefile
GIT_TAG    := $(shell git describe --tags --exact-match 2>/dev/null)
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
VERSION    := $(if $(GIT_TAG),$(GIT_TAG),$(GIT_COMMIT))

build:
    go build -ldflags "-X main.version=$(VERSION)" -o $(APP_NAME) ./cmd/$(APP_NAME)
```

Pass `version` to the MCP server implementation and to the startup log:

```go
s := mcp.NewServer(&mcp.Implementation{Name: "my-mcp-server", Version: version}, nil)
```

### 7.5 What to Log at Startup

The startup `INFO` line is the single most useful line for diagnosing configuration problems. Log every resolved config value except secrets:

```go
slog.Info("Starting server",
    "version", version,
    "base_url", cfg.BaseURL,      // good — helps verify env is right
    // DO NOT log cfg.APIToken    // never log secrets
    "timeout", cfg.Timeout,
    "retry_max_attempts", cfg.RetryMaxAttempts,
    "retry_base_backoff", cfg.RetryBaseBackoff,
    "cache_enabled", cfg.CacheEnabled,
    "cache_ttl", cfg.CacheTTL,
    "log_file", cfg.LogFile,
    "lock_file", cfg.LockFile,
    "debug", cfg.Debug,
)
```

Log any config-derived guardrails or feature flags that affect runtime behavior separately, immediately after the main startup line, so they're visible without scanning the whole startup block.

### 7.6 Volume and Production Hygiene

In normal operation at `INFO`, expect roughly **3–5 log lines per tool invocation**: `tool called`, optionally a `cache miss` (if DEBUG is off this is absent), and `request completed`. If you see significantly more, check for accidental `DEBUG`-level promotion or loops in tool handlers.

At production scale:
- Keep `DEBUG` off by default — `cache miss` alone fires once per request and doubles log volume.
- `args` strings can be large (complex filter expressions). They're valuable for debugging but consider truncating at 500 chars if log storage is a concern.
- The `request_bytes` / `response_bytes` fields let you spot unexpectedly large payloads without storing the bodies themselves.
- `matching_events` and `omitted_events` from the upstream API are worth monitoring as metrics — a rising `omitted_events` count means queries are hitting row limits and returning incomplete results.

## 8. Response Compaction

APIs designed for dashboards often return 50–150 fields per record. Passing these raw to the LLM wastes context tokens and can obscure the signal in noise.

Define a field whitelist per endpoint and strip everything else before returning the response. For nested structures (e.g., network interfaces), flatten them into a simpler form:

#### 8.1 Struct Flattening & Array Compaction Recipe
If an allowed field contains verbose array structures (such as hardware lists or virtual interface bindings), do not dump the raw arrays. Write custom compactors to flatten arrays into basic primitive string slices before key filtering:

```go
func extractIPAddresses(networkInterfacesRaw json.RawMessage) []string {
    var interfaces []struct {
        Inet  []string `json:"inet"`
        Inet6 []string `json:"inet6"`
    }
    if err := json.Unmarshal(networkInterfacesRaw, &interfaces); err != nil {
        return nil
    }
    var ips []string
    for _, iface := range interfaces {
        for _, ip := range iface.Inet {
            if ip != "" && !strings.HasPrefix(ip, "127.") {
                ips = append(ips, ip)
            }
        }
    }
    return ips
}
```
Replacing a 150-line network-interface configuration tree with an `ipAddresses: ["192.168.1.50"]` slice preserves context window budget and makes the relevant signal immediately clear to the LLM.

```go
// agentKeepFields is the field allowlist for /agents responses.
var agentKeepFields = map[string]bool{
    "id": true, "computerName": true, "uuid": true,
    "osName": true, "osType": true, "isActive": true,
    "infected": true, "networkStatus": true,
    // ... add fields that matter for your use case
}

func compactAgents(raw string) string {
    // Unmarshal, filter keys, re-marshal — see compact.go for full implementation
}
```

Route compaction by path prefix from the shared client `Do()` method, so every tool automatically receives the compact view without per-tool boilerplate:

```go
return CompactResponse(path, res.(string)), nil
```

**When to skip compaction:** tools returning CSV exports or binary data, tools where the LLM is explicitly asked to inspect all fields, and error responses (always pass through verbatim).

**Maintain the field list actively.** Every upstream API version bump is an opportunity for new useful fields. Document which fields were removed and why so future maintainers don't re-add them.

## 9. Default Scope Injection

When the server is deployed for a specific tenant or site, inject a default scope automatically rather than requiring the LLM to supply it on every call. Load it from an environment variable and apply it in the client method when the caller doesn't override it:

```go
func (c *Client) ListItems(ctx context.Context, in ItemsListInput) (string, error) {
    if len(in.SiteIDs) == 0 && len(c.defaultSiteIDs) > 0 {
        in.SiteIDs = c.defaultSiteIDs
    }
    return c.Do(ctx, http.MethodGet, "/v1/items", in.QueryParams(), nil)
}
```

This prevents accidental cross-tenant queries and reduces the number of parameters the LLM must manage. Apply the pattern to every method that accepts a scope filter (site IDs, account IDs, tenant ID, etc.).

### 9.1 In-Query Scope Injection

Many analytical APIs require authorization checks to be embedded directly within the filter query itself, rather than appended as standard URL query arguments. For example, if an enterprise has `S1_SITE_IDS=123,456`, we must rewrite the LLM's raw S1QL query `event.type = 'indicator'` into `event.type = 'indicator' and site.id in ('123', '456')`.

```go
// client.go — Dynamic injection of tenant/site security scopes into raw query strings
package main

import (
    "fmt"
    "strings"
)

func InjectQueryScope(rawQuery string, siteIDs []string) string {
    if len(siteIDs) == 0 {
        return rawQuery
    }
    
    // Construct scope clause: (site.id = '123' or site.id = '456')
    var conditions []string
    for _, id := range siteIDs {
        conditions = append(conditions, fmt.Sprintf("site.id = '%s'", id))
    }
    scopeClause := "(" + strings.Join(conditions, " or ") + ")"
    
    trimmed := strings.TrimSpace(rawQuery)
    if trimmed == "" {
        return scopeClause
    }
    
    // Safely append scope conditions with operator precedence grouping
    return fmt.Sprintf("(%s) and %s", trimmed, scopeClause)
}
```

## 10. Best Practices & Anti-Patterns Checklist

### ✅ Must Do (MCP Adapter)
*   **Use a deterministic default log target.** Default to a known logfile path and support `APP_LOGFILE=off` to suppress all logging.
*   **Rotate log files.** Cap log files at a fixed size (e.g. 10 MiB) and retain one backup — servers run indefinitely and will fill the disk without rotation (section 3.3).
*   **Use `run() int` + `os.Exit(run())`.** Never call `os.Exit` inside `main` — it skips defers. Put all logic in `run()` so log close, lock removal, and cache flush always execute (section 3.1).
*   **Use `signal.NotifyContext`.** Prefer `signal.NotifyContext(context.Background(), ...)` over a manual goroutine — it is shorter and handles the stop/reset automatically (section 3.1).
*   **Graceful shutdown.** Catch `SIGINT`/`SIGTERM`, cancel the context, and flush any persistent state (cache, metrics) before exiting.
*   **Offline Data Injection:** Hardcode schemas or syntax guides as reference tools so the LLM doesn't waste API calls discovering data structures.
*   **Prompt Engineering in Schemas:** Use the tool `Description` field to give instructions directly to the LLM (e.g., *"IMPORTANT: Always project needed columns."*). Cross-reference companion tools when ordering matters (e.g., *"Call `get_activity_types` first to get valid IDs."*).
*   **Pre-flight query validation:** Validate query language expressions in-process before dispatching the API call (section 3.6). Return specific, actionable error messages; log at WARN.
*   **Client-Side Query Translation:** Normalize syntax hallucinations in-flight (Section 3.6.6) to proactively fix dialect mismatch and save LLM token retries.
*   **Temporal Query Guardrails:** Enforce limits/filters on queries covering massive dates (Section 3.6.7) to prevent database resource exhaustion.
*   **Generic handler factory:** Use `handleTool[T Validatable]` (section 3.6.1) for uniform decode → validate → execute → respond flow across all tools.
*   **Strict decoding:** Use `DisallowUnknownFields` + trailing-data check (section 3.6.2) to catch misspelled parameters immediately.
*   **Typed input structs:** Implement `Validate()` and `QueryParams()` on every input type (section 3.6.3) for reusable pre-flight validation.
*   **Temporal-Aware Timeout Scaling:** Dynamically scale context timeouts in-flight based on query complexity/temporal ranges (Section 4.4).
*   **Cache-Stabilizing Whitespace Collapse:** Normalize arbitrary whitespace outside of quoted literals (Section 5.1.3) to preserve high cache-hit ratios.
*   **Response compaction:** Strip unused fields before returning to the LLM (section 8) to preserve context window budget.
*   **Default scope injection:** Auto-apply default site/account/tenant IDs from config (section 9) to prevent cross-tenant queries.
*   **In-Query Scope Injection:** Rewrite query strings directly at the transport layer to inject mandatory multitenancy scope constraints (Section 9.1).
*   Pass raw Go errors through `errResult(err), nil`, never bubble them up unwrapped.

### ✅ Must Do (Client — if building from scratch)
*   **Retry with backoff + jitter:** Retry on 5xx and 429 with exponential backoff. Do not retry on timeouts or cancellations.
*   **Respect `Retry-After`:** Parse the response header (integer seconds or HTTP-date) before falling back to computed backoff (section 4.1).
*   **Only retry safe methods:** Do not retry POST/PUT/DELETE — limit rate-limit retries to GET.
*   **Singleflight coalescing:** Wrap outgoing requests in `singleflight.Group` so simultaneous identical calls fire only one network request.
*   **Per-endpoint TTL overrides:** Assign shorter TTLs to high-churn endpoints and longer TTLs to static reference data (section 5.2).
*   **Negative caching:** Cache 400/404 responses briefly to absorb repeat invalid requests; never cache 5xx (section 5.3).
*   **Redact secrets:** Never log API keys, tokens, or sensitive authorization headers.

### ❌ Anti-Patterns
*   Writing logs to `stdout` in stdio transport mode.
*   Using an implicit/unknown log destination. Set a deterministic default log target and make it explicitly configurable.
*   Not rotating log files — servers run indefinitely and will fill the disk.
*   Calling `os.Exit` inside `main` — skips defers and leaks resources (lock files, open log handles, unsaved cache).
*   Registering tools without `"additionalProperties": false` on the input schema.
*   Registering tools with weak or missing JSON schemas.
*   Passing raw Go errors out of the tool handler instead of wrapping them in `errResult(err), nil`.
*   Decoding JSON into `float64` without accounting for integer precision loss — use `dec.UseNumber()` for large IDs and timestamps.
*   Reading error response bodies without a size limit — use `io.LimitReader` to cap before `io.ReadAll`.
*   Leaving process duplication unchecked (no lockfile).
*   Retrying on context cancellation or deadline exceeded — the client already gave up.
*   Retrying POST mutations — only retry idempotent GETs.
*   A single global TTL for all cached endpoints — data volatility varies widely by endpoint.
*   Caching 5xx errors — transient failures should not poison the cache.
*   Not double-checking the cache inside the singleflight leader — wastes one extra network call per concurrent-miss pair.
*   Duplicating retry/auth logic when wrapping an existing SDK that already handles it.
