package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/mcp"
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
		Server:  config.ServerConfig{SocketPath: filepath.Join(dir, "sock"), DBPath: filepath.Join(dir, "test.db"), AllowRegistryWrites: true},
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

	server := cfg[0]
	for _, key := range []string{mcp.TokenStoreKey(server), server.Name} {
		if err := s.store.SaveToken(key, &mcpclient.Token{AccessToken: "t"}); err != nil {
			t.Fatalf("SaveToken: %v", err)
		}
	}

	resp := s.handle(protocol.Request{Action: "logout", Server: "demo"})
	if !resp.OK {
		t.Fatalf("logout failed: %q", resp.Error)
	}
	// Both the endpoint-bound token and any unmigrated legacy token go.
	for _, key := range []string{mcp.TokenStoreKey(server), server.Name} {
		if tok, _ := s.store.GetToken(key); tok != nil {
			t.Fatalf("token under %q survived logout", key)
		}
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

func TestAddServerValidationFailureDoesNotMutateRuntimeConfig(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{AllowRegistryWrites: true},
		Servers: []config.MCPServer{{
			Name:      "existing",
			Alias:     "shared",
			URL:       "https://existing.example.com/mcp",
			Transport: "http",
		}},
	}
	srv := New(filepath.Join(t.TempDir(), "config.yaml"), cfg)

	resp := srv.handle(protocol.Request{
		Action:    "add_server",
		Name:      "new",
		Alias:     "shared",
		URL:       "https://new.example.com/mcp",
		Transport: "http",
	})
	if resp.OK {
		t.Fatal("add_server succeeded, want duplicate alias error")
	}
	if len(srv.cfg.Servers) != 1 || srv.cfg.Servers[0].Name != "existing" {
		t.Fatalf("failed add mutated runtime config: %#v", srv.cfg.Servers)
	}
}

func TestRemoveServerSaveFailureDoesNotMutateRuntimeConfig(t *testing.T) {
	tempDir := t.TempDir()
	blocker := filepath.Join(tempDir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{AllowRegistryWrites: true},
		Servers: []config.MCPServer{{
			Name:      "existing",
			Alias:     "existing",
			URL:       "https://existing.example.com/mcp",
			Transport: "http",
		}},
	}
	srv := New(filepath.Join(blocker, "config.yaml"), cfg)

	resp := srv.handle(protocol.Request{Action: "remove_server", Name: "existing"})
	if resp.OK {
		t.Fatal("remove_server succeeded, want save error")
	}
	if len(srv.cfg.Servers) != 1 || srv.cfg.Servers[0].Name != "existing" {
		t.Fatalf("failed remove mutated runtime config: %#v", srv.cfg.Servers)
	}
}

func TestSetAuthSaveFailureDoesNotMutateRuntimeConfig(t *testing.T) {
	tempDir := t.TempDir()
	blocker := filepath.Join(tempDir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{AllowRegistryWrites: true},
		Servers: []config.MCPServer{{
			Name:      "existing",
			Alias:     "existing",
			URL:       "https://existing.example.com/mcp",
			Transport: "http",
		}},
	}
	srv := New(filepath.Join(blocker, "config.yaml"), cfg)

	resp := srv.handle(protocol.Request{
		Action:  "set_auth",
		Name:    "existing",
		Headers: map[string]string{"Authorization": "Bearer secret"},
	})
	if resp.OK {
		t.Fatal("set_auth succeeded, want save error")
	}
	if len(srv.cfg.Servers[0].Headers) != 0 {
		t.Fatalf("failed auth update mutated runtime config: %#v", srv.cfg.Servers[0].Headers)
	}
}

func TestRegistryWritesAreRefusedByDefault(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{
		Servers: []config.MCPServer{{
			Name:      "existing",
			Alias:     "existing",
			URL:       "https://existing.example.com/mcp",
			Transport: "http",
		}},
	}
	srv := New(configPath, cfg)

	mutations := []protocol.Request{
		{Action: "add_server", Name: "new", URL: "https://new.example.com/mcp", Transport: "http"},
		{Action: "set_auth", Name: "existing", Headers: map[string]string{"Authorization": "Bearer secret"}},
		{Action: "remove_server", Name: "existing"},
	}
	for _, request := range mutations {
		resp := srv.handle(request)
		if resp.OK {
			t.Fatalf("%s succeeded with registry writes disabled", request.Action)
		}
		if !strings.Contains(resp.Error, "registry writes are disabled") {
			t.Fatalf("%s error = %q", request.Action, resp.Error)
		}
	}

	if len(srv.cfg.Servers) != 1 || srv.cfg.Servers[0].Name != "existing" {
		t.Fatalf("refused mutations changed runtime config: %#v", srv.cfg.Servers)
	}
	if len(srv.cfg.Servers[0].Headers) != 0 {
		t.Fatalf("refused set_auth changed headers: %#v", srv.cfg.Servers[0].Headers)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("refused mutations wrote the config file: %v", err)
	}

	// Reload stays available because it only re-reads what is already on disk.
	if resp := srv.handle(protocol.Request{Action: "reload"}); resp.OK {
		t.Fatal("reload succeeded without a config file on disk")
	} else if strings.Contains(resp.Error, "registry writes are disabled") {
		t.Fatalf("reload was gated as a registry write: %q", resp.Error)
	}
}

func TestRegistryWritesSucceedWhenEnabled(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{
		Server:  config.ServerConfig{AllowRegistryWrites: true},
		Servers: []config.MCPServer{},
	}
	srv := New(configPath, cfg)

	resp := srv.handle(protocol.Request{
		Action:    "add_server",
		Name:      "notion",
		URL:       "https://mcp.notion.com/mcp",
		Transport: "http",
	})
	if !resp.OK {
		t.Fatalf("add_server response = %#v", resp)
	}

	saved, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Servers) != 1 || saved.Servers[0].Name != "notion" {
		t.Fatalf("saved servers = %#v", saved.Servers)
	}
	if !saved.Server.AllowRegistryWrites {
		t.Fatal("saved config dropped allow_registry_writes")
	}
}

func TestStatusHistoryAndClearRequests(t *testing.T) {
	dbStore, err := store.Open(filepath.Join(t.TempDir(), "mcpshim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbStore.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{HistorySize: 100},
		Servers: []config.MCPServer{{
			Name:      "example",
			Alias:     "short",
			URL:       "https://example.com/mcp",
			Transport: "http",
		}},
	}
	srv := New(filepath.Join(t.TempDir(), "config.yaml"), cfg)
	srv.store = dbStore
	srv.registry = mcp.NewRegistry(srv.cfg, dbStore)

	status := srv.handle(protocol.Request{Action: "status"})
	if !status.OK || status.Status == nil || status.Status.ServerCount != 1 {
		t.Fatalf("status response = %#v", status)
	}
	servers := srv.handle(protocol.Request{Action: "servers"})
	if !servers.OK || len(servers.Servers) != 1 || servers.Servers[0].Alias != "short" {
		t.Fatalf("servers response = %#v", servers)
	}

	if err := dbStore.InsertHistory(protocol.HistoryItem{
		At:      time.Now(),
		Server:  "example",
		Tool:    "search",
		Success: true,
	}, 100); err != nil {
		t.Fatal(err)
	}
	history := srv.handle(protocol.Request{Action: "history", Server: "short"})
	if !history.OK || len(history.History) != 1 {
		t.Fatalf("history response = %#v", history)
	}

	unsafeClear := srv.handle(protocol.Request{Action: "clear_history"})
	if unsafeClear.OK {
		t.Fatalf("unsafe clear response = %#v", unsafeClear)
	}
	cleared := srv.handle(protocol.Request{
		Action: "clear_history",
		Server: "short",
	})
	if !cleared.OK || cleared.Cleared != 1 {
		t.Fatalf("clear response = %#v", cleared)
	}

	unknown := srv.handle(protocol.Request{Action: "unknown"})
	if unknown.OK {
		t.Fatalf("unknown action response = %#v", unknown)
	}
}

func TestHTTPToolCallIsRecordedUnderCanonicalServiceName(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"conflict"}`))
	}))
	defer upstream.Close()

	dbStore, err := store.Open(filepath.Join(t.TempDir(), "mcpshim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbStore.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{HistorySize: 100},
		HTTPServices: []config.HTTPService{{
			Name:    "deployment-api",
			Alias:   "deploy",
			BaseURL: upstream.URL,
			RawTool: &config.HTTPRawTool{
				Name:    "request",
				Methods: []string{"GET"},
				Paths:   []string{"/v1/**"},
			},
		}},
	}
	srv := New(filepath.Join(t.TempDir(), "config.yaml"), cfg)
	srv.store = dbStore
	srv.registry = mcp.NewRegistry(srv.cfg, dbStore)

	response := srv.handle(protocol.Request{
		Action: "call",
		Server: "deploy",
		Tool:   "request",
		Args: map[string]interface{}{
			"method": "GET",
			"path":   "/v1/deployments/one",
		},
	})
	if response.OK || response.Result == nil {
		t.Fatalf("call response = %#v", response)
	}

	history, err := dbStore.ListHistory("deployment-api", "request", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Success || history[0].Server != "deployment-api" {
		t.Fatalf("history = %#v", history)
	}
}
