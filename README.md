# go-velociraptor-mcp

## Environment Variables

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `VELOCIRAPTOR_API_CONFIG` | No | discovery chain | explicit YAML path |
| `VELOCIRAPTOR_ORG_ID` | No | empty | default org injection |
| `ENABLE_DANGEROUS_TOOLS` | No | `false` | register unrestricted tools |
| `VELOCIRAPTOR_DISABLED_TOOLS` | No | empty | comma-separated tool denylist |
| `RAPTOR_MAX_RESPONSE_BYTES` | No | `32000` | max returned payload size |
| `RAPTOR_TIMEOUT_SECONDS` | No | `300` | base tool timeout |
| `RAPTOR_LOG_FILE` | No | `raptor-mcp.log` | logfile path, `"off"` disables |
| `RAPTOR_LOCK_FILE` | No | `raptor-mcp.lock` | PID lockfile, `"off"` disables |
| `LOG_LEVEL` | No | `debug` | `debug`, `info`, `warn`, `error` |

## Related Projects

- [socfortress/velociraptor-mcp-server](https://github.com/socfortress/velociraptor-mcp-server) — MCP server for Velociraptor
- [mgreen27/mcp-velociraptor](https://github.com/mgreen27/mcp-velociraptor) — MCP integration for Velociraptor
- [Velocidex/velociraptor](https://github.com/Velocidex/velociraptor) — Velociraptor source
