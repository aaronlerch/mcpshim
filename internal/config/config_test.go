package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadStdioServer(t *testing.T) {
	path := writeConfig(t, `
servers:
  - name: local
    transport: stdio
    command: /usr/local/bin/myserver
    args:
      - --flag
      - --port
      - "1234"
    env:
      LEVEL: debug
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(cfg.Servers))
	}
	s := cfg.Servers[0]
	if s.Transport != "stdio" {
		t.Errorf("transport = %q, want stdio", s.Transport)
	}
	if s.Command != "/usr/local/bin/myserver" {
		t.Errorf("command = %q", s.Command)
	}
	if len(s.Args) != 3 || s.Args[0] != "--flag" || s.Args[2] != "1234" {
		t.Errorf("args = %v", s.Args)
	}
	if s.Env["LEVEL"] != "debug" {
		t.Errorf("env LEVEL = %q", s.Env["LEVEL"])
	}
}

func TestStdioRequiresCommand(t *testing.T) {
	path := writeConfig(t, `
servers:
  - name: local
    transport: stdio
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "command is required") {
		t.Fatalf("expected command-required error, got %v", err)
	}
}

func TestStdioRejectsURL(t *testing.T) {
	path := writeConfig(t, `
servers:
  - name: local
    transport: stdio
    command: bin
    url: https://example.com
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "url is not valid for stdio") {
		t.Fatalf("expected stdio-rejects-url error, got %v", err)
	}
}

func TestHTTPRejectsCommand(t *testing.T) {
	path := writeConfig(t, `
servers:
  - name: remote
    transport: http
    url: https://example.com
    command: bin
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "command/args/env are only valid for stdio") {
		t.Fatalf("expected http-rejects-command error, got %v", err)
	}
}

func TestHTTPRequiresURL(t *testing.T) {
	path := writeConfig(t, `
servers:
  - name: remote
    transport: http
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "url is required for http") {
		t.Fatalf("expected url-required error, got %v", err)
	}
}

func TestStdioRejectsHeadersHelper(t *testing.T) {
	path := writeConfig(t, `
servers:
  - name: local
    transport: stdio
    command: bin
    headers_helper: "echo {}"
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "headers/headers_helper are not valid for stdio") {
		t.Fatalf("expected stdio-rejects-headers-helper error, got %v", err)
	}
}

func TestUnsupportedTransport(t *testing.T) {
	path := writeConfig(t, `
servers:
  - name: ws
    transport: websocket
    url: wss://example.com
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unsupported transport") {
		t.Fatalf("expected unsupported-transport error, got %v", err)
	}
}

func TestEnvExpansionInStdioFields(t *testing.T) {
	t.Setenv("MCPSHIM_BIN", "/opt/bin/server")
	t.Setenv("MCPSHIM_LEVEL", "trace")
	path := writeConfig(t, `
servers:
  - name: local
    transport: stdio
    command: ${MCPSHIM_BIN}
    args:
      - --level=${MCPSHIM_LEVEL:-info}
      - --fallback=${MCPSHIM_UNSET:-default-arg}
    env:
      LEVEL: ${MCPSHIM_LEVEL}
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := cfg.Servers[0]
	if s.Command != "/opt/bin/server" {
		t.Errorf("command not expanded: %q", s.Command)
	}
	if s.Args[0] != "--level=trace" {
		t.Errorf("args[0] not expanded: %q", s.Args[0])
	}
	if s.Args[1] != "--fallback=default-arg" {
		t.Errorf("args[1] not expanded with default: %q", s.Args[1])
	}
	if s.Env["LEVEL"] != "trace" {
		t.Errorf("env not expanded: %q", s.Env["LEVEL"])
	}
}

func TestEnvExpansionInHeaders(t *testing.T) {
	t.Setenv("MCPSHIM_TOKEN", "abc123")
	path := writeConfig(t, `
servers:
  - name: remote
    transport: http
    url: https://api.example.com
    headers:
      Authorization: Bearer ${MCPSHIM_TOKEN}
      X-Default: ${MCPSHIM_UNSET:-fallback}
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := cfg.Servers[0]
	if s.Headers["Authorization"] != "Bearer abc123" {
		t.Errorf("Authorization = %q", s.Headers["Authorization"])
	}
	if s.Headers["X-Default"] != "fallback" {
		t.Errorf("X-Default = %q", s.Headers["X-Default"])
	}
}

func TestSaveAndReloadStdio(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := &Config{
		Server: ServerConfig{SocketPath: "/tmp/test.sock", DBPath: "/tmp/test.db"},
		Servers: []MCPServer{{
			Name:      "local",
			Transport: "stdio",
			Command:   "/bin/echo",
			Args:      []string{"hello"},
			Env:       map[string]string{"K": "v"},
		}},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(loaded.Servers) != 1 || loaded.Servers[0].Command != "/bin/echo" {
		t.Errorf("round-trip failed: %#v", loaded.Servers)
	}
}
