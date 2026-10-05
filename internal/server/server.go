package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/mcp"
	"github.com/mcpshim/mcpshim/internal/protocol"
	"github.com/mcpshim/mcpshim/internal/store"
)

const (
	// How often the daemon re-probes servers in the background.
	refreshInterval = 2 * time.Minute
	// Ceiling on one whole refresh cycle across every configured server. Kept
	// under refreshInterval so a wedged cycle cannot outlive the tick that
	// started it and pile goroutines up behind a dead upstream.
	refreshTimeout = 90 * time.Second
)

type Server struct {
	mu         sync.RWMutex
	configPath string
	cfg        *config.Config
	registry   *mcp.Registry
	store      *store.Store
	startedAt  time.Time
	debug      bool
}

func New(configPath string, cfg *config.Config) *Server {
	cfg = config.Clone(cfg)
	return &Server{
		configPath: configPath,
		cfg:        cfg,
		registry:   mcp.NewRegistry(cfg, nil),
		startedAt:  time.Now().UTC(),
	}
}

func (s *Server) SetDebug(debug bool) {
	s.debug = debug
}

func (s *Server) Run() error {
	if s.store == nil {
		dbStore, err := store.Open(s.cfg.Server.DBPath)
		if err != nil {
			return err
		}
		s.store = dbStore
		s.registry = mcp.NewRegistry(s.cfg, s.store)
	}

	defer func() {
		_, _, dbStore := s.snapshot()
		if dbStore != nil {
			_ = dbStore.Close()
		}
	}()

	if err := os.MkdirAll(filepath.Dir(s.cfg.Server.SocketPath), 0o700); err != nil {
		return err
	}
	_ = os.Remove(s.cfg.Server.SocketPath)
	ln, err := net.Listen("unix", s.cfg.Server.SocketPath)
	if err != nil {
		return err
	}
	defer ln.Close()
	if err := os.Chmod(s.cfg.Server.SocketPath, 0o600); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Warm the cache off the critical path, with a deadline. This used to run
	// inline, right here, before the accept loop below -- so a single slow or
	// unresponsive upstream made the ENTIRE daemon unreachable: the socket
	// existed (it is created above) but nothing was accepting on it, and every
	// CLI call blocked forever with no error. Observed in the wild as an
	// 11-minute hang on one server that never answered, with the other seven
	// never probed at all.
	//
	// Both refresh paths take a bounded context for the same reason. The
	// unbounded context.Background() they used before had no way to end.
	go func() {
		refreshCtx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
		defer cancel()
		_, registry, _ := s.snapshot()
		_ = registry.Refresh(refreshCtx)
		s.writeManifest()
	}()

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Periodic, not unconditional: a server whose OAuth
				// credentials are dead stays skipped until the user logs
				// in again. The startup Refresh above is what discovers
				// that state; this is what stops us re-discovering it
				// every two minutes for the rest of the daemon's life.
				refreshCtx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
				_, registry, _ := s.snapshot()
				_ = registry.RefreshPeriodic(refreshCtx)
				cancel()
				s.writeManifest()
			}
		}
	}()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			if s.debug {
				log.Printf("accept error: %v", err)
			}
			continue
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	dec := json.NewDecoder(r)
	enc := json.NewEncoder(w)

	var req protocol.Request
	if err := dec.Decode(&req); err != nil {
		_ = enc.Encode(protocol.Response{OK: false, Error: err.Error()})
		_ = w.Flush()
		return
	}
	sess := newSession(enc, dec, func() { _ = w.Flush() })
	ctx := mcp.WithSession(context.Background(), sess)
	resp := s.handleCtx(ctx, req)
	sess.markFinished()
	_ = enc.Encode(resp)
	_ = w.Flush()
}

// handle preserves the existing zero-context entry point for tests and
// internal callers that don't need elicitation.
func (s *Server) handle(req protocol.Request) protocol.Response {
	return s.handleCtx(context.Background(), req)
}

// snapshot returns the current config, registry, and store under the read
// lock. Request handlers work from the snapshot and release the lock before
// doing any network I/O, so a slow upstream call (or a six-minute browser
// login) never holds the lock a reload is waiting for -- a writer waiting on
// an RWMutex blocks every new reader, which would make `mcpshim status` hang
// behind that one call.
func (s *Server) snapshot() (*config.Config, *mcp.Registry, *store.Store) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg, s.registry, s.store
}

func (s *Server) handleCtx(ctx context.Context, req protocol.Request) protocol.Response {
	switch req.Action {
	case "add_server", "remove_server", "set_auth", "reload":
		return s.handleRegistryChange(req)
	}

	cfg, registry, dbStore := s.snapshot()

	switch req.Action {
	case "status":
		health, authRequired := registry.Health()
		return protocol.Response{OK: true, Status: &protocol.Status{
			StartedAt:    s.startedAt,
			UptimeSec:    int64(time.Since(s.startedAt).Seconds()),
			ServerCount:  len(cfg.Servers) + len(cfg.HTTPServices),
			ToolCount:    registry.ToolCount(),
			Health:       health,
			AuthRequired: authRequired,
		}}
	case "servers":
		return protocol.Response{OK: true, Servers: registry.Servers()}
	case "tools":
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		items, err := registry.ListTools(ctx, req.Server)
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		return protocol.Response{OK: true, Tools: items}
	case "history":
		limit := req.Limit
		if limit <= 0 {
			limit = 50
		}
		items, err := dbStore.ListHistory(canonicalServerName(cfg, req.Server), req.Tool, limit)
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		return protocol.Response{OK: true, History: items}
	case "clear_history":
		cleared, err := dbStore.ClearHistory(canonicalServerName(cfg, req.Server), req.Tool, req.All)
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		return protocol.Response{
			OK:      true,
			Cleared: cleared,
			Text:    fmt.Sprintf("cleared %d history entries", cleared),
		}
	case "inspect":
		if req.Server == "" || req.Tool == "" {
			return protocol.Response{OK: false, Error: "server and tool are required"}
		}
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		detail, err := registry.InspectTool(ctx, req.Server, req.Tool)
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		return protocol.Response{OK: true, ToolDetail: detail}
	case "call":
		if req.Server == "" || req.Tool == "" {
			return protocol.Response{OK: false, Error: "server and tool are required"}
		}
		started := time.Now().UTC()
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		result, err := registry.Call(ctx, req.Server, req.Tool, req.Args)
		historyItem := protocol.HistoryItem{
			At:         started,
			Server:     canonicalServerName(cfg, req.Server),
			Tool:       req.Tool,
			Args:       req.Args,
			Success:    err == nil,
			DurationMs: int64(time.Since(started) / time.Millisecond),
		}
		if err != nil {
			historyItem.Error = err.Error()
		}
		if dbStore != nil {
			_ = dbStore.InsertHistory(historyItem, cfg.Server.HistorySize)
		}
		if err != nil {
			// A tool-level error still carries the tool's own result; return it
			// alongside the error so the caller can show what the tool said.
			return protocol.Response{OK: false, Error: err.Error(), Result: result}
		}
		return protocol.Response{OK: true, Result: result}
	case "refresh":
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if req.Server != "" {
			if _, err := registry.RefreshServer(ctx, req.Server); err != nil {
				s.writeManifest()
				return protocol.Response{OK: false, Error: err.Error(), Servers: registry.Servers()}
			}
			s.writeManifest()
			return protocol.Response{OK: true, Text: fmt.Sprintf("refreshed %s", req.Server), Servers: registry.Servers()}
		}
		_ = registry.Refresh(ctx)
		s.writeManifest()
		return protocol.Response{OK: true, Text: "refreshed all servers", Servers: registry.Servers()}
	case "resources":
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		items, err := registry.ListResources(ctx, req.Server)
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		return protocol.Response{OK: true, Resources: items}
	case "read_resource":
		if req.Server == "" || req.URI == "" {
			return protocol.Response{OK: false, Error: "server and uri are required"}
		}
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		contents, err := registry.ReadResource(ctx, req.Server, req.URI)
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		return protocol.Response{OK: true, ResourceContents: contents}
	case "prompts":
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		items, err := registry.ListPrompts(ctx, req.Server)
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		return protocol.Response{OK: true, Prompts: items}
	case "get_prompt":
		if req.Server == "" || req.Name == "" {
			return protocol.Response{OK: false, Error: "server and name are required"}
		}
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		result, err := registry.GetPrompt(ctx, req.Server, req.Name, req.PromptArgs)
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		return protocol.Response{OK: true, PromptResult: result}
	case "manifest":
		// Always regenerate on demand so the response reflects live state.
		content, err := s.manifestContent()
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		s.writeManifest()
		return protocol.Response{
			OK:              true,
			ManifestPath:    cfg.Server.ManifestPath,
			ManifestContent: content,
		}
	case "logout":
		if req.Server == "" {
			return protocol.Response{OK: false, Error: "server is required"}
		}
		server, ok := findMCPServer(cfg, req.Server)
		if !ok {
			return protocol.Response{OK: false, Error: fmt.Sprintf("unknown server %q", req.Server)}
		}
		if dbStore == nil {
			return protocol.Response{OK: false, Error: "store not initialized"}
		}
		// Clear both the endpoint-bound key and the legacy bare-name key, so a
		// token that was never migrated cannot be adopted after logout.
		if err := dbStore.DeleteTokens(mcp.TokenStoreKey(server), server.Name); err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		text := fmt.Sprintf("cleared oauth token for %s", server.Name)
		if req.Full {
			if err := dbStore.DeleteOAuthClient(server.Name); err != nil {
				return protocol.Response{OK: false, Error: err.Error()}
			}
			text = fmt.Sprintf("cleared oauth token and client credentials for %s", server.Name)
		}
		return protocol.Response{OK: true, Text: text}
	case "login":
		if req.Server == "" {
			return protocol.Response{OK: false, Error: "server is required"}
		}
		ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
		defer cancel()
		if err := registry.Login(ctx, req.Server, false); err != nil {
			return protocol.Response{OK: false, Error: err.Error()}
		}
		s.writeManifest()
		return protocol.Response{OK: true, Text: fmt.Sprintf("oauth login completed for %s", req.Server)}
	default:
		return protocol.Response{OK: false, Error: "unknown action"}
	}
}

// handleRegistryChange runs the actions that replace the daemon's config. The
// change itself happens under the write lock; the refresh that follows does
// not, for the same reason snapshot exists.
func (s *Server) handleRegistryChange(req protocol.Request) protocol.Response {
	resp, changed := s.applyRegistryChange(req)
	if !changed {
		return resp
	}
	_, registry, _ := s.snapshot()
	if req.Action != "set_auth" {
		refreshCtx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
		_ = registry.Refresh(refreshCtx)
		cancel()
	}
	s.writeManifest()
	return resp
}

func (s *Server) applyRegistryChange(req protocol.Request) (protocol.Response, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Registry mutations persist to the config file, so socket access would
	// otherwise imply config write access -- and in this fork a config write
	// can add a headers_helper or a stdio command, which the daemon executes.
	// Reload is not gated: it only re-reads whatever is already on disk.
	switch req.Action {
	case "add_server", "remove_server", "set_auth":
		if !s.cfg.Server.AllowRegistryWrites {
			return protocol.Response{
				OK: false,
				Error: "registry writes are disabled; edit the config file and run 'mcpshim reload', " +
					"or set server.allow_registry_writes: true to permit socket clients to change the registry",
			}, false
		}
	}

	switch req.Action {
	case "add_server":
		if req.Name == "" {
			return protocol.Response{OK: false, Error: "name is required"}, false
		}
		item := config.MCPServer{
			Name:          req.Name,
			Alias:         req.Alias,
			URL:           req.URL,
			Transport:     req.Transport,
			Headers:       req.Headers,
			HeadersHelper: req.HeadersHelper,
			Command:       req.Command,
			Args:          req.CmdArgs,
			Env:           req.Env,
		}
		previous, hadPrevious := findMCPServer(s.cfg, req.Name)
		candidate := config.Clone(s.cfg)
		if err := config.UpsertServer(candidate, item); err != nil {
			return protocol.Response{OK: false, Error: err.Error()}, false
		}
		if err := config.Save(s.configPath, candidate); err != nil {
			return protocol.Response{OK: false, Error: err.Error()}, false
		}
		if s.store != nil {
			// Re-pointing a name at a new endpoint must not hand the old
			// endpoint's token to the new one.
			// The same goes for OAuth client credentials: they are keyed by
			// name only, and a dynamically registered client_secret belongs to
			// the old endpoint's authorization server. Sending it to whatever
			// authorization server the new URL names would leak it. A client
			// supplied with this same request is saved again just below.
			if hadPrevious && config.ResolveServer(previous).URL != config.ResolveServer(item).URL {
				_ = s.store.DeleteTokens(previous.Name, mcp.TokenStoreKey(previous))
				_ = s.store.DeleteOAuthClient(previous.Name)
			}
			if req.ClientID != "" {
				if err := s.store.SaveOAuthClient(req.Name, req.ClientID, req.ClientSecret); err != nil {
					return protocol.Response{OK: false, Error: err.Error()}, false
				}
			}
		}
		s.cfg = candidate
		s.registry.UpdateConfig(candidate)
		return protocol.Response{OK: true, Text: fmt.Sprintf("added server %s", req.Name)}, true
	case "remove_server":
		if req.Name == "" {
			return protocol.Response{OK: false, Error: "name is required"}, false
		}
		removed, _ := findMCPServer(s.cfg, req.Name)
		candidate := config.Clone(s.cfg)
		if !config.RemoveServer(candidate, req.Name) {
			return protocol.Response{OK: false, Error: "server not found"}, false
		}
		if err := config.Save(s.configPath, candidate); err != nil {
			return protocol.Response{OK: false, Error: err.Error()}, false
		}
		if s.store != nil {
			// Client credentials too: they are keyed by bare name, so leaving
			// them would hand them to any later server registered under it.
			_ = s.store.DeleteTokens(removed.Name, mcp.TokenStoreKey(removed))
			_ = s.store.DeleteOAuthClient(removed.Name)
		}
		s.cfg = candidate
		s.registry.UpdateConfig(candidate)
		return protocol.Response{OK: true, Text: fmt.Sprintf("removed server %s", req.Name)}, true
	case "set_auth":
		if req.Name == "" {
			return protocol.Response{OK: false, Error: "name is required"}, false
		}
		updated := false
		candidate := config.Clone(s.cfg)
		for i := range candidate.Servers {
			if candidate.Servers[i].Name == req.Name {
				if len(req.Headers) > 0 {
					if candidate.Servers[i].Headers == nil {
						candidate.Servers[i].Headers = map[string]string{}
					}
					for k, v := range req.Headers {
						candidate.Servers[i].Headers[k] = v
					}
				}
				updated = true
				break
			}
		}
		if !updated {
			return protocol.Response{OK: false, Error: "server not found"}, false
		}
		if err := config.Save(s.configPath, candidate); err != nil {
			return protocol.Response{OK: false, Error: err.Error()}, false
		}
		if req.ClientID != "" && s.store != nil {
			if err := s.store.SaveOAuthClient(req.Name, req.ClientID, req.ClientSecret); err != nil {
				return protocol.Response{OK: false, Error: err.Error()}, false
			}
		}
		s.cfg = candidate
		s.registry.UpdateConfig(candidate)
		return protocol.Response{OK: true, Text: "updated authentication"}, true
	case "reload":
		cfg, err := config.Load(s.configPath)
		if err != nil {
			return protocol.Response{OK: false, Error: err.Error()}, false
		}
		if strings.TrimSpace(cfg.Server.DBPath) != strings.TrimSpace(s.cfg.Server.DBPath) {
			nextStore, openErr := store.Open(cfg.Server.DBPath)
			if openErr != nil {
				return protocol.Response{OK: false, Error: openErr.Error()}, false
			}
			previousStore := s.store
			s.store = nextStore
			s.registry = mcp.NewRegistry(cfg, nextStore)
			if previousStore != nil {
				_ = previousStore.Close()
			}
		}
		forgetRepointedOAuthState(s.store, s.cfg, cfg)
		s.cfg = cfg
		s.registry.UpdateConfig(cfg)
		return protocol.Response{OK: true, Text: "reloaded config"}, true
	}
	return protocol.Response{OK: false, Error: "unknown action"}, false
}

// forgetRepointedOAuthState applies add_server's re-point rule to a reload:
// a server whose resolved URL changed loses its stored token and OAuth client,
// because both were issued for the previous endpoint's authorization server.
// Servers that disappeared from the file are left alone -- commenting one out
// and reloading should not log it out. This only sees edits made while the
// daemon is running; an edit made while it is stopped has no previous config
// to compare against.
func forgetRepointedOAuthState(dbStore *store.Store, previous, next *config.Config) {
	if dbStore == nil || previous == nil || next == nil {
		return
	}
	nextURL := make(map[string]string, len(next.Servers))
	for _, server := range next.Servers {
		nextURL[server.Name] = config.ResolveServer(server).URL
	}
	for _, old := range previous.Servers {
		url, ok := nextURL[old.Name]
		if !ok || url == config.ResolveServer(old).URL {
			continue
		}
		_ = dbStore.DeleteTokens(old.Name, mcp.TokenStoreKey(old))
		_ = dbStore.DeleteOAuthClient(old.Name)
	}
}

// canonicalServerName maps an alias to its server's name so history is
// recorded and queried under one identifier.
func canonicalServerName(cfg *config.Config, name string) string {
	if cfg == nil {
		return name
	}
	for _, server := range cfg.Servers {
		if server.Name == name || server.Alias == name {
			return server.Name
		}
	}
	for _, service := range cfg.HTTPServices {
		if service.Name == name || service.Alias == name {
			return service.Name
		}
	}
	return name
}

func findMCPServer(cfg *config.Config, nameOrAlias string) (config.MCPServer, bool) {
	if cfg == nil {
		return config.MCPServer{}, false
	}
	for _, server := range cfg.Servers {
		if server.Name == nameOrAlias || server.Alias == nameOrAlias {
			return server, true
		}
	}
	return config.MCPServer{}, false
}
