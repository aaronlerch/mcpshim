package server

import (
	"strings"
	"testing"

	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/protocol"
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
