package server

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/protocol"
	"github.com/mcpshim/mcpshim/internal/store"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		Server:  config.ServerConfig{SocketPath: "/tmp/mcpshim-test.sock", DBPath: "/tmp/mcpshim-test.db"},
		Servers: []config.MCPServer{},
	}
	return New("/tmp/mcpshim-test-config.yaml", cfg)
}

func TestHandleResourcesUnknownAction(t *testing.T) {
	s := newTestServer(t)
	resp := s.handle(protocol.Request{Action: "totally-fake"})
	if resp.OK {
		t.Fatal("expected unknown action to fail")
	}
	if !strings.Contains(resp.Error, "unknown action") {
		t.Fatalf("error = %q", resp.Error)
	}
}

func TestHandleReadResourceRequiresArgs(t *testing.T) {
	s := newTestServer(t)
	resp := s.handle(protocol.Request{Action: "read_resource"})
	if resp.OK {
		t.Fatal("expected missing-args error")
	}
	if !strings.Contains(resp.Error, "server and uri are required") {
		t.Fatalf("error = %q", resp.Error)
	}
	resp = s.handle(protocol.Request{Action: "read_resource", Server: "x"})
	if resp.OK || !strings.Contains(resp.Error, "server and uri are required") {
		t.Fatalf("missing uri not caught: %q", resp.Error)
	}
}

func TestHandleGetPromptRequiresArgs(t *testing.T) {
	s := newTestServer(t)
	resp := s.handle(protocol.Request{Action: "get_prompt"})
	if resp.OK || !strings.Contains(resp.Error, "server and name are required") {
		t.Fatalf("error = %q", resp.Error)
	}
	resp = s.handle(protocol.Request{Action: "get_prompt", Server: "x"})
	if resp.OK || !strings.Contains(resp.Error, "server and name are required") {
		t.Fatalf("missing name not caught: %q", resp.Error)
	}
}

func TestHandleResourcesEmptyConfig(t *testing.T) {
	s := newTestServer(t)
	resp := s.handle(protocol.Request{Action: "resources"})
	if !resp.OK {
		t.Fatalf("expected OK for empty server list, got error: %q", resp.Error)
	}
	if len(resp.Resources) != 0 {
		t.Errorf("expected empty resources, got %v", resp.Resources)
	}
}

func TestHandlePromptsEmptyConfig(t *testing.T) {
	s := newTestServer(t)
	resp := s.handle(protocol.Request{Action: "prompts"})
	if !resp.OK {
		t.Fatalf("expected OK for empty server list, got error: %q", resp.Error)
	}
	if len(resp.Prompts) != 0 {
		t.Errorf("expected empty prompts, got %v", resp.Prompts)
	}
}

func TestHandleResourcesUnknownServer(t *testing.T) {
	s := newTestServer(t)
	resp := s.handle(protocol.Request{Action: "resources", Server: "nope"})
	if resp.OK {
		t.Fatal("expected error for unknown server")
	}
	if !strings.Contains(resp.Error, "unknown server") {
		t.Fatalf("error = %q", resp.Error)
	}
}

func TestHandleRefreshAllEmpty(t *testing.T) {
	s := newTestServer(t)
	resp := s.handle(protocol.Request{Action: "refresh"})
	if !resp.OK {
		t.Fatalf("refresh-all on empty config should succeed, got error: %q", resp.Error)
	}
	if resp.Text == "" {
		t.Error("expected status text in refresh response")
	}
}

func TestHandleRefreshUnknownServer(t *testing.T) {
	s := newTestServer(t)
	resp := s.handle(protocol.Request{Action: "refresh", Server: "missing"})
	if resp.OK {
		t.Fatal("expected error for unknown server")
	}
	if !strings.Contains(resp.Error, "unknown server") {
		t.Fatalf("error = %q", resp.Error)
	}
}

// newTestServerWithStore wires a real (empty) SQLite store for tests that
// need the OAuth tables.
func newTestServerWithStore(t *testing.T, servers []config.MCPServer) *Server {
	t.Helper()
	dir := t.TempDir()
	dbStore, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = dbStore.Close() })
	cfg := &config.Config{
		Server:  config.ServerConfig{SocketPath: filepath.Join(dir, "sock"), DBPath: filepath.Join(dir, "test.db")},
		Servers: servers,
	}
	srv := New(filepath.Join(dir, "config.yaml"), cfg)
	srv.store = dbStore
	return srv
}

func TestHandleLogoutUnknownServer(t *testing.T) {
	s := newTestServerWithStore(t, nil)
	resp := s.handle(protocol.Request{Action: "logout", Server: "ghost"})
	if resp.OK || !strings.Contains(resp.Error, "unknown server") {
		t.Fatalf("error = %q", resp.Error)
	}
}

func TestHandleLogoutClearsToken(t *testing.T) {
	cfg := []config.MCPServer{{Name: "demo", Transport: "http", URL: "http://x"}}
	s := newTestServerWithStore(t, cfg)
	if err := s.store.SaveOAuthClient("demo", "id-1", "secret-1"); err != nil {
		t.Fatalf("SaveOAuthClient: %v", err)
	}

	resp := s.handle(protocol.Request{Action: "logout", Server: "demo"})
	if !resp.OK {
		t.Fatalf("logout failed: %q", resp.Error)
	}

	// Client creds should still exist (no --full).
	c, _ := s.store.GetOAuthClient("demo")
	if c == nil || c.ClientID != "id-1" {
		t.Fatalf("client creds should be preserved without --full, got %+v", c)
	}
}

func TestHandleLogoutFullClearsBoth(t *testing.T) {
	cfg := []config.MCPServer{{Name: "demo", Transport: "http", URL: "http://x"}}
	s := newTestServerWithStore(t, cfg)
	if err := s.store.SaveOAuthClient("demo", "id-1", "secret-1"); err != nil {
		t.Fatalf("SaveOAuthClient: %v", err)
	}

	resp := s.handle(protocol.Request{Action: "logout", Server: "demo", Full: true})
	if !resp.OK {
		t.Fatalf("logout --full failed: %q", resp.Error)
	}

	c, _ := s.store.GetOAuthClient("demo")
	if c != nil {
		t.Fatalf("client creds should be cleared with --full, got %+v", c)
	}
}

func TestHandleSetAuthSavesClientCredentials(t *testing.T) {
	cfg := []config.MCPServer{{Name: "demo", Transport: "http", URL: "http://x"}}
	s := newTestServerWithStore(t, cfg)
	resp := s.handle(protocol.Request{
		Action:       "set_auth",
		Name:         "demo",
		ClientID:     "preconfig-id",
		ClientSecret: "preconfig-secret",
	})
	if !resp.OK {
		t.Fatalf("set_auth failed: %q", resp.Error)
	}
	c, err := s.store.GetOAuthClient("demo")
	if err != nil {
		t.Fatalf("GetOAuthClient: %v", err)
	}
	if c == nil || c.ClientID != "preconfig-id" || c.ClientSecret != "preconfig-secret" {
		t.Fatalf("client creds not stored: %+v", c)
	}
}
