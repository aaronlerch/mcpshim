<p align="center">
	<img src="https://mcpshim.dev/icon.svg" alt="MCPShim" width="80" height="80" />
</p>

<h1 align="center">MCPShim</h1>

<p align="center">
	<strong>Use any MCP server as a standard CLI command.</strong><br/>
	A lightweight daemon + CLI that turns remote MCP tools into native shell commands your agent or script can call directly.
</p>

<p align="center">
	<a href="https://mcpshim.dev">Website</a> · <a href="https://github.com/mcpshim/mcpshim">Repository</a> · <a href="#quick-start">Quick Start</a> · <a href="#core-commands">Core Commands</a>
</p>

---

## The Problem

Remote MCP servers are powerful, but each service has its own auth flow, transport expectations, and invocation patterns. Wiring all of that directly into every script or agent loop creates brittle command workflows.

For LLM agents, there is also context pressure: dumping raw MCP schemas for every connected server can consume prompt budget before useful work begins.

## The Solution

`mcpshimd` handles MCP lifecycle concerns in one place: session management, discovery, retries, and OAuth flow.

`mcpshim` exposes every remote MCP tool as a standard CLI command - flags map to tool parameters, output comes back as structured JSON. No SDKs, no libraries, just shell commands that work with any language or agent.

```mermaid
graph TD
		Agent["Your AI Agent / Script"]
		Agent -->|call| CLI["mcpshim CLI"]
		Agent -->|JSON request| Socket["Unix Socket"]
		CLI --> Socket
		Socket --> Daemon["mcpshimd"]
		Daemon --> MCP1["MCP Server: Notion"]
		Daemon --> MCP2["MCP Server: GitHub"]
		Daemon --> MCP3["MCP Server: Linear"]
		Daemon --> MCPN["..."]
```

## Why MCPShim

|                          | Without MCPShim               | With MCPShim                        |
| ------------------------ | ----------------------------- | ----------------------------------- |
| **MCP integration**      | Custom per-server wiring      | One daemon + one CLI                |
| **Auth handling**        | Per-script OAuth/header logic | Centralized in `mcpshimd`           |
| **Tool invocation**      | Provider-specific conventions | `mcpshim call --server --tool ...`  |
| **Agent context budget** | Large MCP schemas in prompt   | Alias-based local command workflows |
| **Operational history**  | Ad-hoc logging                | Built-in call history in SQLite     |

---

## Architecture

| Component  | Role                                                              |
| ---------- | ----------------------------------------------------------------- |
| `mcpshimd` | Local daemon for MCP registry, sessions, auth, retries, and IPC   |
| `mcpshim`  | CLI client for config, discovery, tool calls, history, and script |

All client calls go through a Unix socket and JSON request/response protocol.

## Source Layout

```
cmd/
	mcpshimd/             # Daemon entry point
	mcpshim/              # CLI entry point
configs/
	mcpshim.example.yaml  # Example configuration
internal/
	client/               # CLI command handling and IPC client logic
	config/               # Config loading and defaults
	mcp/                  # MCP transport + OAuth handling
	protocol/             # Request/response protocol types
	server/               # Daemon runtime and routing
	store/                # SQLite persistence
```

## Quick Start

### 1. Install from source

```bash
go install github.com/mcpshim/mcpshim/cmd/mcpshimd@latest
go install github.com/mcpshim/mcpshim/cmd/mcpshim@latest
```

### 2. Configure

```bash
mkdir -p ~/.config/mcpshim
cp configs/mcpshim.example.yaml ~/.config/mcpshim/config.yaml
```

### 3. Start daemon and inspect

```bash
mcpshimd
mcpshim status
mcpshim servers
mcpshim tools
```

### Path Defaults

| Resource | Default Location                    | Override                        |
| -------- | ----------------------------------- | ------------------------------- |
| Config   | `~/.config/mcpshim/config.yaml`     | `--config`, `$MCPSHIM_CONFIG`   |
| Socket   | `$XDG_RUNTIME_DIR/mcpshim.sock`     | `mcpshimd --socket ...`         |
| Database | `~/.local/share/mcpshim/mcpshim.db` | `server.db_path` in YAML config |

All paths follow XDG defaults where applicable.

### Daemon flags

| Flag        | Description               |
| ----------- | ------------------------- |
| `--config`  | Path to config YAML       |
| `--socket`  | Override unix socket path |
| `--debug`   | Enable debug logging      |
| `--version` | Print version and exit    |

---

## Core Commands

| Command                                               | Description                      |
| ----------------------------------------------------- | -------------------------------- |
| `mcpshim servers`                                     | List registered MCP servers      |
| `mcpshim tools [--server name] [--full]`              | List tools for all or one server |
| `mcpshim inspect --server s --tool t`                 | Show tool schema/details         |
| `mcpshim call --server s --tool t --arg value`        | Execute a tool call              |
| `mcpshim add --name s --url ... [--alias a]`          | Register a new MCP endpoint      |
| `mcpshim set auth --server s --header K=V`            | Set auth headers for a server    |
| `mcpshim remove --name s`                             | Remove a registered server       |
| `mcpshim reload`                                      | Reload daemon configuration      |
| `mcpshim validate [--config path]`                    | Validate config file             |
| `mcpshim login --server s [--manual]`                 | Complete OAuth login flow        |
| `mcpshim history [--server s] [--tool t] [--limit n]` | Show persisted call history      |
| `mcpshim resources [--server s]`                      | List MCP resources               |
| `mcpshim read --server s --uri 'protocol://path'`     | Read a single resource           |
| `mcpshim prompts [--server s]`                        | List MCP prompts                 |
| `mcpshim get-prompt --server s --name p [--arg K=V]`  | Render a prompt with arguments   |
| `mcpshim refresh [--server s]`                        | Force-refresh tools/state now    |
| `mcpshim script [--install] [--dir ~/.local/bin]`     | Generate/install alias wrappers  |

### Register MCP servers

```bash
# Remote HTTP server with static auth
mcpshim add --name notion --alias notion --transport http --url https://example.com/mcp
mcpshim set auth --server notion --header "Authorization=Bearer $NOTION_MCP_TOKEN"

# Remote server with a dynamic-auth helper (matches Claude Code's headersHelper).
# The helper is run before each connect; stdout must be a JSON {key:value} of headers.
# It receives MCPSHIM_SERVER_NAME and MCPSHIM_SERVER_URL in env. 10s timeout, no caching.
mcpshim add --name internal --transport http --url https://mcp.internal.example.com \
  --headers-helper /opt/bin/get-mcp-auth-headers.sh

# Local stdio MCP server (subprocess)
mcpshim add --name filesystem --alias fs --transport stdio \
  --command npx --arg -y --arg @modelcontextprotocol/server-filesystem --arg /Users/me/projects \
  --env LOG_LEVEL=info

mcpshim reload
```

Config values support `${VAR}` and `${VAR:-default}` expansion in URLs, headers, command, args, and env.

### Dynamic flags

Tool flags are converted automatically to MCP arguments:

```bash
mcpshim call --server notion --tool search --query "projects" --limit 10 --archived false
```

> Tip: JSON output is automatic when stdout is not a terminal. Use `--json` to force JSON parsing behavior in interactive sessions.

---

## Server Status & Resilience

Every registered server carries a status that you can see via `mcpshim servers`:

| Status          | Meaning                                                      |
| --------------- | ------------------------------------------------------------ |
| `healthy`       | Last refresh succeeded.                                      |
| `degraded`      | First failure observed; auto-retry pending.                  |
| `failed`        | Multiple consecutive failures; backing off.                  |
| `auth_required` | Server returned 401; run `mcpshim login --server <name>`.    |
| `unknown`       | No refresh has been attempted yet.                           |

Failed refreshes are retried in the background with exponential backoff (5s → 15s → 30s → 60s → 2m → 5m, then capped). Auth-required servers do **not** auto-retry; complete the login flow first. Use `mcpshim refresh [--server name]` to force an immediate refresh and reset backoff.

---

## OAuth Flow

For OAuth-capable MCP servers, you can configure URL-only registration:

```bash
mcpshim add --name notion --alias notion --transport http --url https://mcp.notion.com/mcp
```

When a request receives `401` and no `Authorization` header is configured, `mcpshimd` can initiate OAuth login, store tokens in SQLite (`oauth_tokens`), and retry automatically.

You can also pre-authorize:

```bash
mcpshim login --server notion
mcpshim login --server notion --manual
```

`--manual` supports cross-device auth by printing a URL and accepting pasted callback URL/code.

---

## Call History

Every `mcpshim call` is recorded by `mcpshimd` with timestamp, server/tool, args, status, and duration.

```bash
mcpshim history
mcpshim history --server notion --limit 20
mcpshim history --server notion --tool search --limit 100
```

History is stored locally in SQLite (`call_history` table).

---

## IPC Protocol

`mcpshim` communicates with `mcpshimd` over a Unix socket using JSON messages with an `action` field.

```json
{"action":"status"}
{"action":"servers"}
{"action":"tools","server":"notion"}
{"action":"inspect","server":"notion","tool":"search"}
{"action":"call","server":"notion","tool":"search","args":{"query":"roadmap"}}
{"action":"history","server":"notion","limit":20}
{"action":"add_server","name":"notion","alias":"notion","url":"https://mcp.notion.com/mcp","transport":"http"}
{"action":"add_server","name":"fs","transport":"stdio","command":"npx","cmd_args":["-y","@modelcontextprotocol/server-filesystem","/tmp"],"env":{"LOG_LEVEL":"info"}}
{"action":"add_server","name":"internal","transport":"http","url":"https://mcp.internal.example.com","headers_helper":"/opt/bin/get-mcp-auth-headers.sh"}
{"action":"resources","server":"fs"}
{"action":"read_resource","server":"fs","uri":"file:///tmp/notes.md"}
{"action":"prompts","server":"github"}
{"action":"get_prompt","server":"github","name":"summarize_pr","prompt_args":{"pr":"123"}}
{"action":"set_auth","name":"notion","headers":{"Authorization":"Bearer ..."}}
{"action":"reload"}
```

---

## Lightweight Aliases

Generate shell functions:

```bash
eval "$(mcpshim script)"
notion search --query "projects" --limit 10
```

If a server name/alias contains shell-incompatible characters (spaces, dashes, punctuation) MCPShim automatically normalizes it to a safe function name (for example, `my-server` becomes `my_server`).

Install executable wrappers instead:

```bash
mcpshim script --install --dir ~/.local/bin
notion search --query "projects" --limit 10
```

---

## See Also

**[Pantalk](https://github.com/pantalk/pantalk)** - Give your AI agent a voice on every chat platform. MCPShim gives your agent tools; Pantalk gives it a voice across Slack, Discord, Telegram, and more. Together they form a complete agent infrastructure stack.
