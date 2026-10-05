<p align="center">
	<img src="https://mcpshim.dev/icon.svg" alt="MCPShim" width="80" height="80" />
</p>

<h1 align="center">MCPShim</h1>

<p align="center">
	<strong>Use any MCP server or HTTP API as a standard CLI command.</strong><br/>
	A lightweight daemon + CLI that turns remote MCP tools and configured HTTP endpoints into native shell commands your agent or script can call directly.
</p>

<p align="center">
	<a href="https://mcpshim.dev">Website</a> · <a href="https://github.com/mcpshim/mcpshim">Repository</a> · <a href="#quick-start">Quick Start</a> · <a href="#core-commands">Core Commands</a>
</p>

---

## The Problem

Remote MCP servers and HTTP APIs are powerful, but each service has its own auth flow, transport expectations, and invocation patterns. Wiring all of that directly into every script or agent loop creates brittle command workflows.

For LLM agents, there is also context pressure: dumping raw MCP schemas for every connected server can consume prompt budget before useful work begins.

## The Solution

`mcpshimd` centralizes MCP registration and OAuth alongside configured HTTP
service bindings, discovery, call execution, and history behind one local
socket.

`mcpshim` exposes every remote MCP tool and configured HTTP operation as a
standard CLI command. Flags map to tool parameters and output comes back as
structured JSON. No SDKs or libraries are required by the caller.

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
		Daemon --> HTTP["Configured HTTP API"]
		Daemon --> MCPN["..."]
```

## Why MCPShim

|                          | Without MCPShim               | With MCPShim                        |
| ------------------------ | ----------------------------- | ----------------------------------- |
| **Tool integration**     | Custom per-service wiring     | One daemon + one CLI                |
| **Auth handling**        | Per-script OAuth/header logic | Centralized in `mcpshimd`           |
| **Tool invocation**      | Provider-specific conventions | `mcpshim call --server --tool ...`  |
| **HTTP API binding**     | Hand-written client code       | Typed or constrained raw tools     |
| **Agent context budget** | Large MCP schemas in prompt   | Alias-based local command workflows |
| **Operational history**  | Ad-hoc logging                | Built-in call history in SQLite     |

---

## Architecture

| Component  | Role                                                              |
| ---------- | ----------------------------------------------------------------- |
| `mcpshimd` | Local daemon for MCP/HTTP registry, discovery, auth, calls, and IPC |
| `mcpshim`  | CLI client for config, discovery, tool calls, history, and script |

All client calls go through a Unix socket and JSON request/response protocol.

## Quick Start

### 1. Install from source

```bash
go install github.com/mcpshim/mcpshim/cmd/mcpshimd@latest
go install github.com/mcpshim/mcpshim/cmd/mcpshim@latest
```

### 2. Configure

```bash
mkdir -p ~/.config/mcpshim
cat > ~/.config/mcpshim/config.yaml <<'YAML'
servers:
  - name: notion
    alias: notion
    transport: http
    url: https://mcp.notion.com/mcp
YAML
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

### Autostart (Linux, systemd user service)

A user-level systemd unit is provided at `configs/mcpshim.service`. It runs `mcpshimd` as your user, restarts on failure, and logs to journald. It assumes the binary is at `~/.local/bin/mcpshimd` (adjust `ExecStart=` if you installed elsewhere).

```bash
# Install the unit
mkdir -p ~/.config/systemd/user
cp configs/mcpshim.service ~/.config/systemd/user/

# Enable and start
systemctl --user daemon-reload
systemctl --user enable --now mcpshim.service

# Verify
systemctl --user status mcpshim.service
mcpshim servers
journalctl --user -u mcpshim.service -f   # tail logs
```

For the service to start at boot (before you log in), enable lingering once:

```bash
loginctl enable-linger "$USER"
```

The daemon already removes any stale `mcpshim.sock` before binding, so no `ExecStartPre` cleanup is needed.

### Autostart (macOS, launchd)

Not bundled — file an issue or PR if you want a `launchd` plist. A minimal plist runs `~/.local/bin/mcpshimd` with `RunAtLoad=true` and `KeepAlive=true`, loaded via `launchctl load ~/Library/LaunchAgents/dev.mcpshim.daemon.plist`.

---

## Core Commands

| Command                                               | Description                      |
| ----------------------------------------------------- | -------------------------------- |
| `mcpshim servers`                                     | List registered MCP/HTTP services |
| `mcpshim tools [--server name] [--full]`              | List tools for all or one server |
| `mcpshim inspect --server s --tool t`                 | Show tool schema/details         |
| `mcpshim call --server s --tool t --arg value`        | Execute a tool call              |
| `mcpshim add --name s --url ... [--alias a]`          | Register a new MCP endpoint (opt-in) |
| `mcpshim set auth --server s --header K=V`            | Set auth headers for a server (opt-in) |
| `mcpshim remove --name s`                             | Remove a registered server (opt-in) |
| `mcpshim reload`                                      | Reload daemon configuration      |
| `mcpshim validate [--config path]`                    | Validate config file             |
| `mcpshim login --server s [--manual]`                 | Complete OAuth login flow        |
| `mcpshim logout --server s [--full]`                  | Clear stored OAuth token (--full also clears client creds) |
| `mcpshim history [--server s] [--tool t] [--limit n]` | Show persisted call history      |
| `mcpshim resources [--server s]`                      | List MCP resources               |
| `mcpshim read --server s --uri 'protocol://path'`     | Read a single resource           |
| `mcpshim prompts [--server s]`                        | List MCP prompts                 |
| `mcpshim get-prompt --server s --name p [--arg K=V]`  | Render a prompt with arguments   |
| `mcpshim refresh [--server s]`                        | Force-refresh tools/state now    |
| `mcpshim manifest [--path]`                           | Print live markdown manifest (or its file path) |
| `mcpshim history --clear [--server s] [--tool t]`     | Clear scoped call history        |
| `mcpshim history --clear --all`                       | Clear all call history           |
| `mcpshim script [--install] [--dir ~/.local/bin]`     | Generate/install alias wrappers  |

### Register MCP servers

The config file is the source of truth. Edit it and reload:

```bash
$EDITOR ~/.config/mcpshim/config.yaml
mcpshim reload
```

`add`, `set auth`, and `remove` do the same thing over the socket, and are
**refused by default**. Those actions rewrite the config file, so leaving them
enabled makes socket access equivalent to config write access -- and here a
config write can add a `headers_helper` or a stdio `command`, both of which the
daemon executes. Turn them on only when runtime registration is worth that:

```yaml
server:
  allow_registry_writes: true
```

With that set:

```bash
# Remote HTTP server with static auth (single quotes keep the reference
# unexpanded, so the token itself is never written to the config file)
mcpshim add --name notion --alias notion --transport http --url https://example.com/mcp
mcpshim set auth --server notion --header 'Authorization=Bearer ${NOTION_MCP_TOKEN}'

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

Config values support `${VAR}` and `${VAR:-default}` expansion in URLs, headers,
command, args, and env. References are kept as written in the config and
expanded each time a connection is made, so resolved secrets are never written
back to disk. An unset variable without a default expands to empty.

Credentials are never readable back through the socket either way: `servers`
reports `has_auth` as a boolean and never returns header values.

### Dynamic flags

Tool flags are converted automatically to MCP arguments:

```bash
mcpshim call --server notion --tool search --query "projects" --limit 10 --archived false
```

> Tip: JSON output is automatic when stdout is not a terminal. Put the global
> `--json` before the command to force JSON output in a terminal. On `call`,
> `--json` after the tool selector parses JSON-like text fields returned by the
> remote tool.

Objects, arrays, and null can be passed as JSON values. MCPShim uses the
discovered tool schema to keep string properties as strings:

```bash
mcpshim call --server notion --tool search \
  --filter '{"status":"open"}' \
  --ids '[1,2,3]' \
  --cursor null
```

---

## HTTP Services

Ordinary HTTP APIs can be exposed without implementing an MCP server. A service
owns its base URL, credentials, and policy, then publishes typed tools, a
constrained raw request tool, or both:

```yaml
http_services:
  - name: deployment-api
    alias: deploy
    base_url: https://deploy.example.com/api
    headers:
      Authorization: Bearer ${DEPLOY_API_TOKEN}
    policy:
      redirects: same-origin
      allowed_request_content_types: [application/json]
      max_response_bytes: 2097152
    raw_tool:
      name: request
      methods: [GET, POST, PATCH]
      paths: [/v1/deployments/**]
    tools:
      - name: get_deployment
        request:
          method: GET
          path: /v1/deployments/{deployment_id}
        inputs:
          deployment_id:
            type: string
            required: true
```

```bash
mcpshim call --server deploy --tool get_deployment --deployment_id dep_123
mcpshim call --server deploy --tool request \
  --method PATCH \
  --path /v1/deployments/dep_123 \
  --body '{"desired_state":"running"}'
```

Typed request bodies, queries, and headers support deeply nested structural
templates with `$arg`, `$default`, `$omit_if_missing`, and `$format`. The raw
tool remains limited to configured methods and wildcard paths and cannot
override authentication or other protected headers.

See the [complete HTTP service guide](docs/http-services.md) and
[`configs/http-services.example.yaml`](configs/http-services.example.yaml).

---

## Elicitation

When an upstream MCP server invokes `elicitation/create` mid-call (e.g. "are you sure you want to delete this?"), the daemon relays the question to the calling `mcpshim` process, which prompts the user on stderr and sends the answer back over the same socket connection.

Two modes are supported:

- **Form** — the server sends a JSON schema; the user replies with a JSON object on one line, or types `decline`/`cancel`. Invalid JSON automatically declines.
- **URL** — the server sends a URL; the user is asked `[y/N/cancel]`. Anything that isn't `y`/`yes` declines.

When stdin is not a TTY (programmatic invocation), elicitation is automatically declined so calls don't hang waiting for input.

---

## Live Manifest

The daemon writes a markdown manifest of every registered server and its currently-cached tools to `${XDG_DATA_HOME:-~/.local/share}/mcpshim/manifest.md` (override via `server.manifest_path` in config). It regenerates automatically after every successful refresh and after every config-mutating action (`add`, `remove`, `set auth`, `reload`, `refresh`).

Point an AI agent at this file at session start and it gets the full server-and-tool inventory up front — no discovery round-trips through `servers` / `tools` / `inspect`. Auth-required servers are flagged with the exact `mcpshim login` command needed to recover; failed servers surface their last error inline.

```bash
mcpshim manifest          # print current manifest
mcpshim manifest --path   # just the on-disk path
```

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

When a request receives `401` and no `Authorization` header is configured,
MCPShim checks its SQLite token store. If authorization is still required, the
command tells you to run an explicit login instead of starting an interactive
browser flow inside the daemon.

Log in from a terminal (the browser flow runs in the CLI process, and the
daemon is told to re-probe the server once the token is saved):

```bash
mcpshim login --server notion
mcpshim login --server notion --manual
```

If your MCP server requires pre-registered OAuth clients (no dynamic registration), pass them at registration time or via `set auth` (both need `server.allow_registry_writes: true`):

```bash
mcpshim add --name acme --transport http --url https://mcp.acme.com/mcp \
  --client-id $ACME_CLIENT_ID --client-secret $ACME_CLIENT_SECRET
# or for an already-registered server:
mcpshim set auth --server acme --client-id $ACME_CLIENT_ID --client-secret $ACME_CLIENT_SECRET
```

Tokens are stored per server name *and* endpoint URL, so re-pointing a server
at a different URL never sends it the old endpoint's token. Re-pointing (with
`add` or by editing the config and running `mcpshim reload`) and `remove` also
drop the server's stored client credentials, which belong to the old
endpoint's authorization server. To revoke a stored
token (the next call then asks for `mcpshim login`):

```bash
mcpshim logout --server notion          # token only
mcpshim logout --server notion --full   # token + client credentials
```

`--manual` supports cross-device auth by printing a URL and accepting pasted callback URL/code.

Every authorization request and token request (code exchange and refresh)
carries an RFC 8707 `resource` parameter naming the MCP server, as the MCP
authorization spec requires. Without it, an authorization server shared by
several MCP servers cannot tell which one a token is for and may issue it with
the wrong audience. The value is the `resource` from the server's
protected-resource metadata (RFC 9728) when it publishes one for its own
origin, otherwise the configured URL with a lowercase scheme and host and no
fragment.

---

## Call History

Every `mcpshim call` is recorded by `mcpshimd` with timestamp, server/tool, args, status, and duration.

```bash
mcpshim history
mcpshim history --server notion --limit 20
mcpshim history --server notion --tool search --limit 100
mcpshim history --server notion --clear
mcpshim history --clear --all
```

History is stored locally in SQLite (`call_history` table). The daemon retains
the newest 1,000 calls by default; set `server.history_size` in the YAML config
to choose another positive limit. Clearing requires at least one filter or an
explicit `--all`.

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
{"action":"clear_history","server":"notion"}
{"action":"clear_history","all":true}
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
