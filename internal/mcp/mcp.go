package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpproto "github.com/mark3labs/mcp-go/mcp"
	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/protocol"
	"github.com/mcpshim/mcpshim/internal/store"
)

const headersHelperTimeout = 10 * time.Second

type Registry struct {
	mu         sync.RWMutex
	cfg        *config.Config
	store      *store.Store
	toolCache  map[string][]protocol.ToolInfo
	cacheStamp time.Time
	states     map[string]*serverState
	backoff    []time.Duration
}

func NewRegistry(cfg *config.Config, dbStore *store.Store) *Registry {
	return NewRegistryWithBackoff(cfg, dbStore, DefaultBackoff)
}

// NewRegistryWithBackoff lets tests inject a deterministic backoff schedule
// (e.g. zero-duration delays) without mutating package globals.
func NewRegistryWithBackoff(cfg *config.Config, dbStore *store.Store, backoff []time.Duration) *Registry {
	r := &Registry{
		cfg:       cfg,
		store:     dbStore,
		toolCache: map[string][]protocol.ToolInfo{},
		states:    map[string]*serverState{},
		backoff:   backoff,
	}
	for _, s := range cfg.Servers {
		r.states[s.Name] = newServerState()
	}
	return r
}

func (r *Registry) UpdateConfig(cfg *config.Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = cfg
	r.toolCache = map[string][]protocol.ToolInfo{}
	r.cacheStamp = time.Time{}

	// Cancel and drop state for servers that were removed; preserve state for
	// servers that are still configured; create blank state for new ones.
	keep := map[string]*serverState{}
	for _, s := range cfg.Servers {
		if existing, ok := r.states[s.Name]; ok {
			keep[s.Name] = existing
		} else {
			keep[s.Name] = newServerState()
		}
	}
	for name, st := range r.states {
		if _, kept := keep[name]; !kept {
			st.cancelRetry()
		}
	}
	r.states = keep
}

func (r *Registry) Servers() []protocol.ServerInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]protocol.ServerInfo, 0, len(r.cfg.Servers))
	for _, s := range r.cfg.Servers {
		info := protocol.ServerInfo{
			Name:      s.Name,
			Alias:     s.Alias,
			URL:       s.URL,
			Transport: s.Transport,
			HasAuth:   hasAuthorizationHeader(s.Headers),
		}
		if state, ok := r.states[s.Name]; ok {
			status, lastErr, lastSuccess, attempts := state.snapshot()
			info.Status = string(status)
			info.LastError = lastErr
			info.AttemptCount = attempts
			if !lastSuccess.IsZero() {
				info.LastSuccessAt = lastSuccess
			}
		}
		out = append(out, info)
	}
	return out
}

func (r *Registry) stateFor(name string) *serverState {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.states[name]
	if !ok {
		st = newServerState()
		r.states[name] = st
	}
	return st
}

func (r *Registry) ListTools(ctx context.Context, server string) ([]protocol.ToolInfo, error) {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	if server != "" {
		s, ok := findServer(cfg, server)
		if !ok {
			return nil, fmt.Errorf("unknown server %q", server)
		}
		return fetchToolsForServer(ctx, s, r.store, true)
	}

	all := []protocol.ToolInfo{}
	for _, s := range cfg.Servers {
		items, err := fetchToolsForServer(ctx, s, r.store, true)
		if err != nil {
			continue
		}
		all = append(all, items...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Server == all[j].Server {
			return all[i].Name < all[j].Name
		}
		return all[i].Server < all[j].Server
	})
	return all, nil
}

func (r *Registry) Refresh(ctx context.Context) error {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	log.Printf("[registry] refresh starting for %d server(s)", len(cfg.Servers))
	for _, s := range cfg.Servers {
		_, _ = r.refreshServer(ctx, s)
	}
	log.Printf("[registry] refresh complete")
	return nil
}

// RefreshServer refreshes a single server by name and returns its tools. A
// successful refresh cancels any pending backoff retry; a failure schedules
// (or extends) one.
func (r *Registry) RefreshServer(ctx context.Context, name string) ([]protocol.ToolInfo, error) {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()
	s, ok := findServer(cfg, name)
	if !ok {
		return nil, fmt.Errorf("unknown server %q", name)
	}
	return r.refreshServer(ctx, s)
}

// refreshServer is the internal worker used by Refresh and RefreshServer.
// It updates per-server state and (re)schedules backoff retries.
func (r *Registry) refreshServer(ctx context.Context, s config.MCPServer) ([]protocol.ToolInfo, error) {
	state := r.stateFor(s.Name)
	state.mu.Lock()
	state.lastAttemptAt = time.Now().UTC()
	state.mu.Unlock()

	log.Printf("[registry] refreshing server %q (transport=%s, has_auth_header=%v)", s.Name, s.Transport, hasAuthorizationHeader(s.Headers))
	tools, err := fetchToolsForServer(ctx, s, r.store, false)
	if err != nil {
		authReq := isAuthRequiredError(err)
		idx := state.recordFailure(err, authReq)
		log.Printf("[registry] refresh failed for %q (attempt=%d, auth_required=%v): %v", s.Name, idx+1, authReq, err)
		r.mu.Lock()
		delete(r.toolCache, s.Name)
		r.mu.Unlock()
		// Don't auto-retry when the server explicitly needs the user to log in.
		if !authReq {
			r.scheduleBackoffRetry(s, idx)
		}
		return nil, err
	}

	state.recordSuccess()
	log.Printf("[registry] refresh succeeded for %q: %d tools", s.Name, len(tools))

	r.mu.Lock()
	r.toolCache[s.Name] = tools
	r.cacheStamp = time.Now().UTC()
	r.mu.Unlock()
	return tools, nil
}

// scheduleBackoffRetry spawns a goroutine that re-runs refreshServer after
// the configured delay. A subsequent manual refresh or remove cancels it.
func (r *Registry) scheduleBackoffRetry(s config.MCPServer, attempt int) {
	delay := backoffDelay(r.backoff, attempt)
	if delay <= 0 {
		return
	}
	state := r.stateFor(s.Name)
	cancel := state.scheduleRetry(delay)
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-cancel:
			return
		case <-timer.C:
		}
		// Re-resolve the server config in case it was edited or removed.
		r.mu.RLock()
		current, ok := findServer(r.cfg, s.Name)
		r.mu.RUnlock()
		if !ok {
			return
		}
		ctx, cancelCtx := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelCtx()
		_, _ = r.refreshServer(ctx, current)
	}()
}

func (r *Registry) ToolCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	total := 0
	for _, items := range r.toolCache {
		total += len(items)
	}
	return total
}

func (r *Registry) InspectTool(ctx context.Context, server, tool string) (*protocol.ToolDetail, error) {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	s, ok := findServer(cfg, server)
	if !ok {
		return nil, fmt.Errorf("unknown server %q", server)
	}

	tools, err := fetchToolsRaw(ctx, s, r.store, true)
	if err != nil {
		return nil, err
	}
	for _, t := range tools {
		if t.Name == tool {
			required, _ := parseSchema(t.InputSchema)
			return &protocol.ToolDetail{
				Server:      s.Name,
				Name:        t.Name,
				Description: t.Description,
				Properties:  parseSchemaDetail(t.InputSchema, required),
				Meta:        metaToMap(t.Meta),
			}, nil
		}
	}
	return nil, fmt.Errorf("tool %q not found on server %q", tool, server)
}

func (r *Registry) Call(ctx context.Context, server string, tool string, args map[string]interface{}) (interface{}, error) {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	s, ok := findServer(cfg, server)
	if !ok {
		return nil, fmt.Errorf("unknown server %q", server)
	}
	if args == nil {
		args = map[string]interface{}{}
	}

	log.Printf("[registry] Call server=%q tool=%q", server, tool)
	res, err := runWithOAuthFallback(ctx, s, r.store, true, func(cli compatibleClient) (interface{}, error) {
		req := mcpproto.CallToolRequest{}
		req.Params.Name = tool
		req.Params.Arguments = args

		result, err := cli.CallTool(ctx, req)
		if err != nil {
			return nil, err
		}
		return result, nil
	})
	if err != nil {
		log.Printf("[registry] Call server=%q tool=%q failed: %v", server, tool, err)
		return nil, err
	}
	log.Printf("[registry] Call server=%q tool=%q succeeded", server, tool)
	return res, nil
}

func (r *Registry) ListResources(ctx context.Context, server string) ([]protocol.ResourceInfo, error) {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	collect := func(s config.MCPServer) ([]protocol.ResourceInfo, error) {
		raw, err := runWithOAuthFallback(ctx, s, r.store, true, func(cli compatibleClient) ([]mcpproto.Resource, error) {
			res, err := cli.ListResources(ctx, mcpproto.ListResourcesRequest{})
			if err != nil {
				return nil, err
			}
			return res.Resources, nil
		})
		if err != nil {
			return nil, err
		}
		out := make([]protocol.ResourceInfo, 0, len(raw))
		for _, res := range raw {
			out = append(out, protocol.ResourceInfo{
				Server:      s.Name,
				URI:         res.URI,
				Name:        res.Name,
				Description: res.Description,
				MIMEType:    res.MIMEType,
			})
		}
		return out, nil
	}

	if server != "" {
		s, ok := findServer(cfg, server)
		if !ok {
			return nil, fmt.Errorf("unknown server %q", server)
		}
		return collect(s)
	}

	all := []protocol.ResourceInfo{}
	for _, s := range cfg.Servers {
		items, err := collect(s)
		if err != nil {
			log.Printf("[registry] list_resources failed for %q: %v", s.Name, err)
			continue
		}
		all = append(all, items...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Server == all[j].Server {
			return all[i].URI < all[j].URI
		}
		return all[i].Server < all[j].Server
	})
	return all, nil
}

func (r *Registry) ReadResource(ctx context.Context, server, uri string) ([]protocol.ResourceContent, error) {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	s, ok := findServer(cfg, server)
	if !ok {
		return nil, fmt.Errorf("unknown server %q", server)
	}
	if uri == "" {
		return nil, fmt.Errorf("resource uri is required")
	}

	contents, err := runWithOAuthFallback(ctx, s, r.store, true, func(cli compatibleClient) ([]mcpproto.ResourceContents, error) {
		req := mcpproto.ReadResourceRequest{}
		req.Params.URI = uri
		res, err := cli.ReadResource(ctx, req)
		if err != nil {
			return nil, err
		}
		return res.Contents, nil
	})
	if err != nil {
		return nil, err
	}

	out := make([]protocol.ResourceContent, 0, len(contents))
	for _, c := range contents {
		switch tc := c.(type) {
		case mcpproto.TextResourceContents:
			out = append(out, protocol.ResourceContent{URI: tc.URI, MIMEType: tc.MIMEType, Text: tc.Text})
		case mcpproto.BlobResourceContents:
			out = append(out, protocol.ResourceContent{URI: tc.URI, MIMEType: tc.MIMEType, Blob: tc.Blob})
		default:
			// unknown content shape — best-effort marshal
			data, jerr := json.Marshal(c)
			if jerr == nil {
				out = append(out, protocol.ResourceContent{Text: string(data)})
			}
		}
	}
	return out, nil
}

func (r *Registry) ListPrompts(ctx context.Context, server string) ([]protocol.PromptInfo, error) {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	collect := func(s config.MCPServer) ([]protocol.PromptInfo, error) {
		raw, err := runWithOAuthFallback(ctx, s, r.store, true, func(cli compatibleClient) ([]mcpproto.Prompt, error) {
			res, err := cli.ListPrompts(ctx, mcpproto.ListPromptsRequest{})
			if err != nil {
				return nil, err
			}
			return res.Prompts, nil
		})
		if err != nil {
			return nil, err
		}
		out := make([]protocol.PromptInfo, 0, len(raw))
		for _, p := range raw {
			args := make([]protocol.PromptArg, 0, len(p.Arguments))
			for _, a := range p.Arguments {
				args = append(args, protocol.PromptArg{
					Name:        a.Name,
					Description: a.Description,
					Required:    a.Required,
				})
			}
			out = append(out, protocol.PromptInfo{
				Server:      s.Name,
				Name:        p.Name,
				Description: p.Description,
				Arguments:   args,
			})
		}
		return out, nil
	}

	if server != "" {
		s, ok := findServer(cfg, server)
		if !ok {
			return nil, fmt.Errorf("unknown server %q", server)
		}
		return collect(s)
	}

	all := []protocol.PromptInfo{}
	for _, s := range cfg.Servers {
		items, err := collect(s)
		if err != nil {
			log.Printf("[registry] list_prompts failed for %q: %v", s.Name, err)
			continue
		}
		all = append(all, items...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Server == all[j].Server {
			return all[i].Name < all[j].Name
		}
		return all[i].Server < all[j].Server
	})
	return all, nil
}

func (r *Registry) GetPrompt(ctx context.Context, server, name string, args map[string]string) (*protocol.PromptResult, error) {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	s, ok := findServer(cfg, server)
	if !ok {
		return nil, fmt.Errorf("unknown server %q", server)
	}
	if name == "" {
		return nil, fmt.Errorf("prompt name is required")
	}

	res, err := runWithOAuthFallback(ctx, s, r.store, true, func(cli compatibleClient) (*mcpproto.GetPromptResult, error) {
		req := mcpproto.GetPromptRequest{}
		req.Params.Name = name
		req.Params.Arguments = args
		return cli.GetPrompt(ctx, req)
	})
	if err != nil {
		return nil, err
	}

	messages := make([]protocol.PromptMessage, 0, len(res.Messages))
	for _, m := range res.Messages {
		messages = append(messages, protocol.PromptMessage{Role: string(m.Role), Content: m.Content})
	}
	return &protocol.PromptResult{
		Server:      s.Name,
		Name:        name,
		Description: res.Description,
		Messages:    messages,
	}, nil
}

func (r *Registry) Login(ctx context.Context, server string, manual bool) error {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	s, ok := findServer(cfg, server)
	if !ok {
		return fmt.Errorf("unknown server %q", server)
	}

	return runOAuthLogin(ctx, s, r.store, manual)
}

func fetchToolsForServer(ctx context.Context, s config.MCPServer, dbStore *store.Store, interactive bool) ([]protocol.ToolInfo, error) {
	raw, err := fetchToolsRaw(ctx, s, dbStore, interactive)
	if err != nil {
		return nil, err
	}
	items := make([]protocol.ToolInfo, 0, len(raw))
	for _, t := range raw {
		required, properties := parseSchema(t.InputSchema)
		items = append(items, protocol.ToolInfo{
			Server:      s.Name,
			Name:        t.Name,
			Description: t.Description,
			Required:    required,
			Properties:  properties,
			Meta:        metaToMap(t.Meta),
		})
	}
	return items, nil
}

// metaToMap flattens an MCP Meta struct into a JSON-serializable map. Returns
// nil if the meta is empty so the wire response stays clean.
func metaToMap(m *mcpproto.Meta) map[string]any {
	if m == nil {
		return nil
	}
	out := map[string]any{}
	if m.ProgressToken != nil {
		out["progressToken"] = m.ProgressToken
	}
	for k, v := range m.AdditionalFields {
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func fetchToolsRaw(ctx context.Context, s config.MCPServer, dbStore *store.Store, interactive bool) ([]mcpproto.Tool, error) {
	return runWithOAuthFallback(ctx, s, dbStore, interactive, func(cli compatibleClient) ([]mcpproto.Tool, error) {
		list, err := cli.ListTools(ctx, mcpproto.ListToolsRequest{})
		if err != nil {
			return nil, err
		}
		return list.Tools, nil
	})
}

func parseSchema(schema interface{}) ([]string, []string) {
	type inputSchema struct {
		Required   []string               `json:"required"`
		Properties map[string]interface{} `json:"properties"`
	}
	var parsed inputSchema
	b, err := json.Marshal(schema)
	if err != nil {
		return nil, nil
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		return nil, nil
	}
	props := make([]string, 0, len(parsed.Properties))
	for key := range parsed.Properties {
		props = append(props, key)
	}
	sort.Strings(props)
	return parsed.Required, props
}

func parseSchemaDetail(schema interface{}, requiredList []string) []protocol.PropertyDetail {
	type propEntry struct {
		Type        string        `json:"type"`
		Enum        []interface{} `json:"enum"`
		Const       interface{}   `json:"const"`
		Description string        `json:"description"`
	}
	type inputSchema struct {
		Properties map[string]propEntry `json:"properties"`
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var parsed inputSchema
	if err := json.Unmarshal(b, &parsed); err != nil {
		return nil
	}

	required := map[string]bool{}
	for _, r := range requiredList {
		required[r] = true
	}

	keys := make([]string, 0, len(parsed.Properties))
	for k := range parsed.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]protocol.PropertyDetail, 0, len(keys))
	for _, k := range keys {
		p := parsed.Properties[k]
		enum := []string{}
		for _, v := range p.Enum {
			enum = append(enum, fmt.Sprintf("%v", v))
		}
		constValue := ""
		if p.Const != nil {
			constValue = fmt.Sprintf("%v", p.Const)
		}
		out = append(out, protocol.PropertyDetail{
			Name:        k,
			Type:        p.Type,
			Enum:        enum,
			Const:       constValue,
			Description: p.Description,
			Required:    required[k],
		})
	}
	return out
}

type compatibleClient interface {
	Start(ctx context.Context) error
	Initialize(ctx context.Context, request mcpproto.InitializeRequest) (*mcpproto.InitializeResult, error)
	ListTools(ctx context.Context, req mcpproto.ListToolsRequest) (*mcpproto.ListToolsResult, error)
	CallTool(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error)
	ListResources(ctx context.Context, req mcpproto.ListResourcesRequest) (*mcpproto.ListResourcesResult, error)
	ReadResource(ctx context.Context, req mcpproto.ReadResourceRequest) (*mcpproto.ReadResourceResult, error)
	ListPrompts(ctx context.Context, req mcpproto.ListPromptsRequest) (*mcpproto.ListPromptsResult, error)
	GetPrompt(ctx context.Context, req mcpproto.GetPromptRequest) (*mcpproto.GetPromptResult, error)
	Close() error
}

func newClient(ctx context.Context, s config.MCPServer) (compatibleClient, func(), error) {
	trans, err := buildTransport(s, nil)
	if err != nil {
		return nil, nil, err
	}
	cli := mcpclient.NewClient(trans, clientOptionsFor(ctx, s)...)
	return cli, func() { _ = cli.Close() }, nil
}

// buildTransport constructs the lower-level mcp-go transport for s. When
// oauthCfg is non-nil it layers OAuth in (HTTP/SSE only).
func buildTransport(s config.MCPServer, oauthCfg *mcpclient.OAuthConfig) (transport.Interface, error) {
	switch s.Transport {
	case "stdio":
		if oauthCfg != nil {
			return nil, fmt.Errorf("oauth is not supported for stdio transport")
		}
		env := append(os.Environ(), envMapToList(s.Env)...)
		return transport.NewStdioWithOptions(s.Command, env, s.Args), nil
	case "sse":
		headers, err := resolveHeaders(s)
		if err != nil {
			return nil, err
		}
		opts := []transport.ClientOption{}
		if len(headers) > 0 {
			opts = append(opts, transport.WithHeaders(headers))
		}
		if oauthCfg != nil {
			opts = append(opts, transport.WithOAuth(*oauthCfg))
		}
		return transport.NewSSE(s.URL, opts...)
	default: // "http" or unspecified
		headers, err := resolveHeaders(s)
		if err != nil {
			return nil, err
		}
		opts := []transport.StreamableHTTPCOption{}
		if len(headers) > 0 {
			opts = append(opts, transport.WithHTTPHeaders(headers))
		}
		if oauthCfg != nil {
			opts = append(opts, transport.WithHTTPOAuth(*oauthCfg))
		}
		return transport.NewStreamableHTTP(s.URL, opts...)
	}
}

// clientOptionsFor returns the mcp-go ClientOption slice for s. The
// elicitation handler is wired to whatever Session is currently bound to
// ctx (or to no-op decline when running headless).
func clientOptionsFor(ctx context.Context, s config.MCPServer) []mcpclient.ClientOption {
	bridge := &elicitBridge{
		server: s.Name,
		getCtx: func() context.Context { return ctx },
	}
	return []mcpclient.ClientOption{mcpclient.WithElicitationHandler(bridge)}
}

func envMapToList(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// resolveHeaders returns the effective request headers for an HTTP/SSE server,
// combining static `Headers` with output from `HeadersHelper` (helper wins).
// Runs the helper fresh every call (no caching), with a 10-second timeout
// and the same env-var contract Claude Code documents.
func resolveHeaders(s config.MCPServer) (map[string]string, error) {
	headers := map[string]string{}
	for k, v := range s.Headers {
		headers[k] = v
	}
	if s.HeadersHelper == "" {
		return headers, nil
	}
	dynamic, err := runHeadersHelper(s)
	if err != nil {
		return nil, err
	}
	for k, v := range dynamic {
		headers[k] = v
	}
	return headers, nil
}

func runHeadersHelper(s config.MCPServer) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), headersHelperTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", s.HeadersHelper)
	cmd.Env = append(os.Environ(),
		"MCPSHIM_SERVER_NAME="+s.Name,
		"MCPSHIM_SERVER_URL="+s.URL,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("headers_helper for %q timed out after %s", s.Name, headersHelperTimeout)
		}
		stderrTrim := bytes.TrimSpace(stderr.Bytes())
		if len(stderrTrim) > 0 {
			return nil, fmt.Errorf("headers_helper for %q failed: %w: %s", s.Name, err, string(stderrTrim))
		}
		return nil, fmt.Errorf("headers_helper for %q failed: %w", s.Name, err)
	}
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("headers_helper for %q produced empty output", s.Name)
	}
	var result map[string]string
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("headers_helper for %q output is not a JSON object of strings: %w", s.Name, err)
	}
	return result, nil
}

func findServer(cfg *config.Config, nameOrAlias string) (config.MCPServer, bool) {
	for _, s := range cfg.Servers {
		if s.Name == nameOrAlias || s.Alias == nameOrAlias {
			return s, true
		}
	}
	return config.MCPServer{}, false
}
