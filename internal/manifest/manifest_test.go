package manifest

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/protocol"
)

func sampleSnapshot() Snapshot {
	return Snapshot{
		GeneratedAt: time.Date(2026, 4, 25, 19, 0, 0, 0, time.UTC),
		Servers: []protocol.ServerInfo{
			{Name: "notion", Alias: "notion", URL: "https://mcp.notion.com/mcp", Transport: "http", Status: "healthy", HasAuth: true},
			{Name: "linear", Alias: "linear", URL: "https://mcp.linear.app/mcp", Transport: "http", Status: "auth_required"},
			{Name: "fs", Alias: "fs", Transport: "stdio", Status: "healthy"},
			{Name: "broken", Alias: "broken", URL: "https://broken.example.com", Transport: "http", Status: "failed", LastError: "connection refused", AttemptCount: 3},
		},
		Tools: map[string][]protocol.ToolInfo{
			"notion": {
				{Server: "notion", Name: "search", Description: "Search the workspace.", Required: []string{"query"}, Properties: []string{"limit", "query"}},
				{Server: "notion", Name: "fetch", Description: "Fetch a page or block by id.\n\nDetails follow.", Required: []string{"id"}, Properties: []string{"id"}},
			},
			"fs": {
				{Server: "fs", Name: "read_file", Description: "Read a file's contents.", Required: []string{"path"}, Properties: []string{"path"}},
			},
		},
		ConfigBy: map[string]config.MCPServer{
			"notion": {Name: "notion", Transport: "http", URL: "https://mcp.notion.com/mcp", Headers: map[string]string{"Authorization": "Bearer x"}},
			"linear": {Name: "linear", Transport: "http", URL: "https://mcp.linear.app/mcp"},
			"fs":     {Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem", "/Users/me/projects"}, Env: map[string]string{"LOG_LEVEL": "info"}},
			"broken": {Name: "broken", Transport: "http", URL: "https://broken.example.com"},
		},
	}
}

func TestRenderEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, Snapshot{}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "MCPShim Live Manifest") {
		t.Errorf("missing title: %s", out)
	}
	if !strings.Contains(out, "No servers registered") {
		t.Errorf("expected empty-state hint, got: %s", out)
	}
}

func TestRenderHealthyHTTPServer(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, sampleSnapshot()); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"## notion",
		"`http`",
		"`healthy`",
		"`https://mcp.notion.com/mcp`",
		"Auth: pre-set Authorization header",
		"**search** — Search the workspace.",
		"required: `query`",
		"optional: `limit`",
		"**fetch** — Fetch a page or block by id.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestRenderAuthRequiredHint(t *testing.T) {
	var buf bytes.Buffer
	_ = Render(&buf, sampleSnapshot())
	out := buf.String()
	if !strings.Contains(out, "## linear") {
		t.Fatal("missing linear section")
	}
	if !strings.Contains(out, "mcpshim login --server linear") {
		t.Errorf("missing login hint: %s", out)
	}
	if !strings.Contains(out, "auth_required") {
		t.Errorf("expected auth_required status: %s", out)
	}
}

func TestRenderStdioServerShowsCommand(t *testing.T) {
	var buf bytes.Buffer
	_ = Render(&buf, sampleSnapshot())
	out := buf.String()
	for _, want := range []string{
		"## fs",
		"`stdio`",
		"npx -y @modelcontextprotocol/server-filesystem /Users/me/projects",
		"Env keys: `LOG_LEVEL`",
		"**read_file**",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestRenderFailedServerSurfacesError(t *testing.T) {
	var buf bytes.Buffer
	_ = Render(&buf, sampleSnapshot())
	out := buf.String()
	if !strings.Contains(out, "## broken") {
		t.Fatal("missing broken section")
	}
	if !strings.Contains(out, "Last refresh error: connection refused") {
		t.Errorf("missing last-error: %s", out)
	}
	if !strings.Contains(out, "tool list unavailable") {
		t.Errorf("missing unavailable hint: %s", out)
	}
}

func TestSummarizeStripsLeadingMarkup(t *testing.T) {
	cases := map[string]string{
		"":              "",
		"hello":         "hello",
		"<example>x":   "",
		"first\nsecond": "first",
	}
	for in, want := range cases {
		if got := summarize(in); got != want {
			t.Errorf("summarize(%q) = %q, want %q", in, got, want)
		}
	}
}
