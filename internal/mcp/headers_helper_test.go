package mcp

import (
	"strings"
	"testing"

	"github.com/mcpshim/mcpshim/internal/config"
)

func TestResolveHeadersStaticOnly(t *testing.T) {
	s := config.MCPServer{
		Name: "remote",
		URL:  "https://example.com",
		Headers: map[string]string{
			"Authorization": "Bearer abc",
			"X-Other":       "value",
		},
	}
	got, err := resolveHeaders(s)
	if err != nil {
		t.Fatalf("resolveHeaders: %v", err)
	}
	if got["Authorization"] != "Bearer abc" || got["X-Other"] != "value" {
		t.Errorf("unexpected headers: %v", got)
	}
}

func TestResolveHeadersHelperWins(t *testing.T) {
	s := config.MCPServer{
		Name:          "dyn",
		URL:           "https://example.com",
		Headers:       map[string]string{"Authorization": "Bearer static", "X-Static": "kept"},
		HeadersHelper: `echo '{"Authorization":"Bearer dynamic","X-Dyn":"new"}'`,
	}
	got, err := resolveHeaders(s)
	if err != nil {
		t.Fatalf("resolveHeaders: %v", err)
	}
	if got["Authorization"] != "Bearer dynamic" {
		t.Errorf("helper should win on conflict, got %q", got["Authorization"])
	}
	if got["X-Static"] != "kept" {
		t.Errorf("non-conflicting static header should remain, got %q", got["X-Static"])
	}
	if got["X-Dyn"] != "new" {
		t.Errorf("helper-only header missing, got %q", got["X-Dyn"])
	}
}

func TestHeadersHelperReceivesEnvVars(t *testing.T) {
	s := config.MCPServer{
		Name:          "named",
		URL:           "https://api.example.com/path",
		HeadersHelper: `printf '{"X-Server":"%s","X-Url":"%s"}' "$MCPSHIM_SERVER_NAME" "$MCPSHIM_SERVER_URL"`,
	}
	got, err := resolveHeaders(s)
	if err != nil {
		t.Fatalf("resolveHeaders: %v", err)
	}
	if got["X-Server"] != "named" {
		t.Errorf("MCPSHIM_SERVER_NAME not passed: %q", got["X-Server"])
	}
	if got["X-Url"] != "https://api.example.com/path" {
		t.Errorf("MCPSHIM_SERVER_URL not passed: %q", got["X-Url"])
	}
}

func TestHeadersHelperBadJSON(t *testing.T) {
	s := config.MCPServer{
		Name:          "bad",
		HeadersHelper: `echo 'not json'`,
	}
	_, err := resolveHeaders(s)
	if err == nil || !strings.Contains(err.Error(), "not a JSON object") {
		t.Fatalf("expected JSON error, got %v", err)
	}
}

func TestHeadersHelperEmptyOutput(t *testing.T) {
	s := config.MCPServer{
		Name:          "blank",
		HeadersHelper: `true`,
	}
	_, err := resolveHeaders(s)
	if err == nil || !strings.Contains(err.Error(), "empty output") {
		t.Fatalf("expected empty-output error, got %v", err)
	}
}

func TestHeadersHelperFailureSurfaces(t *testing.T) {
	s := config.MCPServer{
		Name:          "boom",
		HeadersHelper: `echo "bad creds" >&2; exit 7`,
	}
	_, err := resolveHeaders(s)
	if err == nil || !strings.Contains(err.Error(), "bad creds") {
		t.Fatalf("expected stderr surfaced in error, got %v", err)
	}
}
