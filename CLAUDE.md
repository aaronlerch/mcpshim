# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is MCPShim

MCPShim is a Go project with two binaries: `mcpshimd` (daemon) and `mcpshim` (CLI client). The daemon manages connections to remote MCP servers and exposes their tools as CLI commands over a local Unix socket using a JSON request/response protocol. The CLI translates user commands into socket messages and renders results.

## Build & Development Commands

```bash
make build          # Build both mcpshim and mcpshimd (CGO_ENABLED=0)
make test           # Run all tests (go test ./... -count=1)
make fmt            # Format code
make vet            # Run go vet
make lint           # Vet + lint check
go test ./internal/client/ -count=1 -run TestFoo  # Run a single test
```

CI also runs `go test ./... -count=1 -race` and `govulncheck ./...`.

## Architecture

**IPC flow:** CLI (`cmd/mcpshim`) → Unix socket → Daemon (`cmd/mcpshimd`) → remote MCP servers

Key internal packages:
- **`internal/protocol`** — Shared `Request`/`Response` types used by both client and server. The `Action` field on `Request` drives all routing.
- **`internal/server`** — Daemon runtime. Listens on Unix socket, dispatches actions via a single `handle(req)` switch statement. Manages config reload, server CRUD, and tool call history recording.
- **`internal/client`** — CLI-side IPC client. Sends JSON requests to the socket, receives responses.
- **`internal/mcp`** — MCP transport layer using `mcp-go` library. `Registry` holds live MCP client sessions, handles tool discovery (`Refresh`), tool calls, and OAuth flows. `oauth.go` and `token_store.go` handle OAuth token persistence in SQLite.
- **`internal/config`** — YAML config loading/saving with XDG path defaults. Environment variable expansion in URLs and headers. Atomic save via tmp-file-then-rename.
- **`internal/store`** — SQLite persistence for call history and OAuth tokens.

## Key Design Details

- Config uses strict YAML parsing (`KnownFields(true)`) — unknown fields cause load errors.
- Transport values normalize to `"http"` (default) or `"sse"`.
- The daemon refreshes MCP server tool lists every 2 minutes via `registry.Refresh`.
- Config saves are atomic: write to `.tmp`, validate by re-loading, then rename.
- All paths follow XDG conventions with env var overrides (`MCPSHIM_CONFIG`, `XDG_RUNTIME_DIR`, `XDG_DATA_HOME`).
