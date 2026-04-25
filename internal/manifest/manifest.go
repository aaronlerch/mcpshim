// Package manifest renders a markdown summary of every registered MCP server
// and its currently-cached tools. The daemon writes this file after every
// successful refresh and after every config-mutating action, giving AI agents
// a stable, up-to-date pointer to "what's available right now" so they can
// skip the discover-by-trial-and-error round trips.
package manifest

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/protocol"
)

// Snapshot is the data needed to render a manifest. Capturing it as a
// concrete struct keeps the renderer pure and easy to test.
type Snapshot struct {
	GeneratedAt time.Time
	Servers     []protocol.ServerInfo
	Tools       map[string][]protocol.ToolInfo // keyed by ServerInfo.Name
	ConfigBy    map[string]config.MCPServer    // keyed by ServerInfo.Name
}

// Render writes the markdown manifest to w. It never returns an error from
// the writer in practice (callers typically pass a *bytes.Buffer or *os.File
// known to be writable) but the signature reserves that option.
func Render(w io.Writer, snap Snapshot) error {
	ts := snap.GeneratedAt
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	bw := &errWriter{w: w}
	bw.line("# MCPShim Live Manifest")
	bw.blank()
	bw.linef("_Generated %s by mcpshimd. Source of truth for available MCP servers and tools — read this before invoking any `mcpshim` command. Tool details are cached from the upstream servers and refresh automatically; auth-required servers are flagged in the section header._", ts.UTC().Format(time.RFC3339))
	bw.blank()
	bw.line("## Quick reference")
	bw.line("- List servers: `mcpshim servers`")
	bw.line("- List a server's tools: `mcpshim tools --server <name>`")
	bw.line("- Show full schema for one tool: `mcpshim inspect --server <name> --tool <tool>`")
	bw.line("- Invoke a tool: `mcpshim call --server <name> --tool <tool> --<arg> <value>`")
	bw.line("- Force-refresh state: `mcpshim refresh [--server <name>]`")
	bw.blank()

	if len(snap.Servers) == 0 {
		bw.line("_No servers registered. Run `mcpshim add --name X --transport http --url ...` to register one._")
		return bw.err
	}

	bw.linef("Servers: **%d**", len(snap.Servers))
	bw.blank()

	for _, s := range snap.Servers {
		renderServer(bw, s, snap.Tools[s.Name], snap.ConfigBy[s.Name])
	}
	return bw.err
}

func renderServer(bw *errWriter, s protocol.ServerInfo, tools []protocol.ToolInfo, mcp config.MCPServer) {
	status := s.Status
	if status == "" {
		status = "unknown"
	}
	bw.linef("## %s", s.Name)
	alias := s.Alias
	if alias == "" {
		alias = s.Name
	}
	bw.linef("_alias: `%s` · transport: `%s` · status: `%s` · tools: %d_", alias, s.Transport, status, len(tools))
	bw.blank()

	switch s.Transport {
	case "stdio":
		if mcp.Command != "" {
			cmd := mcp.Command
			for _, a := range mcp.Args {
				cmd += " " + shellSafe(a)
			}
			bw.linef("Command: `%s`", cmd)
			if len(mcp.Env) > 0 {
				keys := sortedKeys(mcp.Env)
				bw.linef("Env keys: %s", joinBackticked(keys))
			}
			bw.blank()
		}
	default:
		if s.URL != "" {
			bw.linef("Endpoint: `%s`", s.URL)
			if s.HasAuth {
				bw.line("Auth: pre-set Authorization header")
			} else if mcp.HeadersHelper != "" {
				bw.line("Auth: dynamic via headers_helper")
			}
			bw.blank()
		}
	}

	switch status {
	case "auth_required":
		bw.linef("> ⚠ Auth required. Run `mcpshim login --server %s` (add `--manual` for cross-device).", s.Name)
		bw.blank()
	case "failed", "degraded":
		if s.LastError != "" {
			bw.linef("> ⚠ Last refresh error: %s", oneLine(s.LastError))
			bw.blank()
		}
	}

	if len(tools) == 0 {
		if status == "healthy" {
			bw.line("_(server reports no tools)_")
		} else {
			bw.line("_(tool list unavailable — server is not healthy; cached state cleared)_")
		}
		bw.blank()
		return
	}

	bw.line("Tools:")
	for _, t := range tools {
		summary := summarize(t.Description)
		if summary != "" {
			bw.linef("- **%s** — %s", t.Name, summary)
		} else {
			bw.linef("- **%s**", t.Name)
		}
		if len(t.Required) > 0 {
			bw.linef("  - required: %s", joinBackticked(t.Required))
		}
		if extras := optionals(t.Properties, t.Required); len(extras) > 0 {
			bw.linef("  - optional: %s", joinBackticked(extras))
		}
	}
	bw.blank()

	bw.linef("Invoke any tool above with `mcpshim call --server %s --tool <name> --<arg> <value>` (the alias `%s` is also a shorthand: `mcpshim %s <name> --<arg> <value>`).", s.Name, alias, alias)
	bw.blank()
}

// summarize compresses a possibly-multiline tool description into a one-liner.
func summarize(desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return ""
	}
	for _, line := range strings.Split(desc, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "<") || strings.HasPrefix(line, "{") {
			continue
		}
		if len(line) > 140 {
			return line[:137] + "…"
		}
		return line
	}
	return ""
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		return s[:197] + "…"
	}
	return s
}

func optionals(all, required []string) []string {
	req := map[string]bool{}
	for _, r := range required {
		req[r] = true
	}
	out := []string{}
	for _, p := range all {
		if !req[p] {
			out = append(out, p)
		}
	}
	return out
}

func joinBackticked(items []string) string {
	if len(items) == 0 {
		return ""
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, "`"+it+"`")
	}
	return strings.Join(out, ", ")
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Manual insertion sort — keeps the package free of "sort" import for a
	// trivial input size and stays deterministic.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func shellSafe(s string) string {
	if strings.ContainsAny(s, " \t\"'$&|;<>") {
		return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
	}
	return s
}

type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) line(s string) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintln(e.w, s)
}

func (e *errWriter) linef(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format+"\n", args...)
}

func (e *errWriter) blank() {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintln(e.w)
}
