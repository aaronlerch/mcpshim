package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server       ServerConfig  `yaml:"server"`
	Servers      []MCPServer   `yaml:"servers"`
	HTTPServices []HTTPService `yaml:"http_services,omitempty"`
}

const DefaultHistorySize = 1000

type ServerConfig struct {
	SocketPath   string `yaml:"socket_path"`
	DBPath       string `yaml:"db_path"`
	ManifestPath string `yaml:"manifest_path,omitempty"`
	HistorySize  int    `yaml:"history_size,omitempty"`

	// AllowRegistryWrites permits socket clients to edit the registry through
	// add_server, set_auth, and remove_server. It is off by default because
	// those actions persist to the config file, which makes socket access
	// equivalent to config write access. Operators can always edit the file
	// directly and reload.
	AllowRegistryWrites bool `yaml:"allow_registry_writes,omitempty"`
}

type MCPServer struct {
	Name          string            `yaml:"name"`
	Alias         string            `yaml:"alias,omitempty"`
	URL           string            `yaml:"url,omitempty"`
	Transport     string            `yaml:"transport,omitempty"`
	Headers       map[string]string `yaml:"headers,omitempty"`
	HeadersHelper string            `yaml:"headers_helper,omitempty"`
	Command       string            `yaml:"command,omitempty"`
	Args          []string          `yaml:"args,omitempty"`
	Env           map[string]string `yaml:"env,omitempty"`
}

func normalizeTransport(value string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "", "http", "streamable-http":
		return "http", nil
	case "sse":
		return "sse", nil
	case "stdio":
		return "stdio", nil
	default:
		return "", fmt.Errorf("unsupported transport %q (expected http, sse, or stdio)", value)
	}
}

func DefaultConfigPath() string {
	if envPath := strings.TrimSpace(os.Getenv("MCPSHIM_CONFIG")); envPath != "" {
		return envPath
	}
	return filepath.Join(xdgConfigHome(), "mcpshim", "config.yaml")
}

func DefaultSocketPath() string {
	if runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); runtimeDir != "" {
		return filepath.Join(runtimeDir, "mcpshim.sock")
	}
	return fmt.Sprintf("/tmp/mcpshim-%d.sock", os.Getuid())
}

func DefaultDBPath() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dir != "" {
		return filepath.Join(dir, "mcpshim", "mcpshim.db")
	}
	return filepath.Join(homeDir(), ".local", "share", "mcpshim", "mcpshim.db")
}

// DefaultManifestPath returns the default location of the live manifest file.
// If dbPath is set, the manifest sits in the same directory next to the db.
// Otherwise it falls back to the XDG data dir.
func DefaultManifestPath(dbPath string) string {
	if dbPath != "" {
		return filepath.Join(filepath.Dir(dbPath), "manifest.md")
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dir != "" {
		return filepath.Join(dir, "mcpshim", "manifest.md")
	}
	return filepath.Join(homeDir(), ".local", "share", "mcpshim", "manifest.md")
}

func xdgConfigHome() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(), ".config")
}

func homeDir() string {
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		return home
	}
	return "/tmp/mcpshim-" + strconv.Itoa(os.Getuid())
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	if cfg.Server.SocketPath == "" {
		cfg.Server.SocketPath = DefaultSocketPath()
	}
	if cfg.Server.DBPath == "" {
		cfg.Server.DBPath = DefaultDBPath()
	}
	if cfg.Server.ManifestPath == "" {
		cfg.Server.ManifestPath = DefaultManifestPath(cfg.Server.DBPath)
	}
	if cfg.Server.HistorySize == 0 {
		cfg.Server.HistorySize = DefaultHistorySize
	}
	// Environment references stay unexpanded in the loaded config and are
	// resolved per use by ResolveServer. Expanding here would let a later
	// Save (add_server, set_auth) write resolved credentials back to disk.
	for i := range cfg.Servers {
		s := &cfg.Servers[i]
		transport, transportErr := normalizeTransport(s.Transport)
		if transportErr != nil {
			return nil, transportErr
		}
		s.Transport = transport
		if s.Alias == "" {
			s.Alias = s.Name
		}
	}
	normalizeHTTPServices(cfg.HTTPServices)
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ResolveServer expands environment references in a disposable server copy,
// using the same ${VAR:-default} rules as everywhere else in the config.
// Keeping the source config untouched prevents later CLI mutations from
// writing resolved credentials back to disk.
func ResolveServer(server MCPServer) MCPServer {
	server.URL = expandEnv(server.URL)
	server.Headers = expandMap(server.Headers)
	server.Command = expandEnv(server.Command)
	if server.Args != nil {
		args := make([]string, len(server.Args))
		for i, a := range server.Args {
			args[i] = expandEnv(a)
		}
		server.Args = args
	}
	server.Env = expandMap(server.Env)
	return server
}

func expandMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = expandEnv(v)
	}
	return out
}

func copyMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func Clone(cfg *Config) *Config {
	if cfg == nil {
		return nil
	}
	clone := *cfg
	clone.Servers = make([]MCPServer, len(cfg.Servers))
	for i, server := range cfg.Servers {
		clone.Servers[i] = server
		clone.Servers[i].Headers = copyMap(server.Headers)
		clone.Servers[i].Env = copyMap(server.Env)
		if server.Args != nil {
			clone.Servers[i].Args = append([]string(nil), server.Args...)
		}
	}
	clone.HTTPServices = cloneHTTPServices(cfg.HTTPServices)
	return &clone
}

func Save(path string, cfg *Config) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	if err := validate(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, out, 0o600); err != nil {
		return err
	}
	if _, err := Load(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("resulting config is invalid: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func LoadOrInit(path string) (*Config, error) {
	cfg, err := Load(path)
	if err == nil {
		return cfg, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	cfg = &Config{
		Server: ServerConfig{
			SocketPath:   DefaultSocketPath(),
			DBPath:       DefaultDBPath(),
			ManifestPath: DefaultManifestPath(DefaultDBPath()),
			HistorySize:  DefaultHistorySize,
		},
		Servers: []MCPServer{},
	}
	if err := Save(path, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func validate(cfg *Config) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	if cfg.Server.HistorySize < 0 {
		return errors.New("server history_size cannot be negative")
	}
	seen := map[string]bool{}
	aliases := map[string]bool{}
	for _, s := range cfg.Servers {
		if s.Name == "" {
			return errors.New("server name is required")
		}
		transport, err := normalizeTransport(s.Transport)
		if err != nil {
			return fmt.Errorf("server %q: %w", s.Name, err)
		}
		switch transport {
		case "stdio":
			if s.Command == "" {
				return fmt.Errorf("server %q: command is required for stdio transport", s.Name)
			}
			if s.URL != "" {
				return fmt.Errorf("server %q: url is not valid for stdio transport", s.Name)
			}
			if len(s.Headers) > 0 || s.HeadersHelper != "" {
				return fmt.Errorf("server %q: headers/headers_helper are not valid for stdio transport", s.Name)
			}
		default: // http or sse
			if strings.TrimSpace(expandEnv(s.URL)) == "" {
				return fmt.Errorf("server %q: url is required for %s transport", s.Name, transport)
			}
			if s.Command != "" || len(s.Args) > 0 || len(s.Env) > 0 {
				return fmt.Errorf("server %q: command/args/env are only valid for stdio transport", s.Name)
			}
		}
		if seen[s.Name] || aliases[s.Name] {
			return fmt.Errorf("duplicate server identifier %q", s.Name)
		}
		alias := s.Alias
		if alias == "" {
			alias = s.Name
		}
		if alias != s.Name && (seen[alias] || aliases[alias]) {
			return fmt.Errorf("duplicate server identifier %q", alias)
		}
		seen[s.Name] = true
		aliases[alias] = true
	}
	return validateHTTPServices(cfg, seen, aliases)
}

func UpsertServer(cfg *Config, item MCPServer) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	transport, err := normalizeTransport(item.Transport)
	if err != nil {
		return err
	}
	item.Transport = transport
	if item.Alias == "" {
		item.Alias = item.Name
	}
	candidate := Clone(cfg)
	for i := range candidate.Servers {
		if candidate.Servers[i].Name == item.Name {
			candidate.Servers[i] = item
			if err := validate(candidate); err != nil {
				return err
			}
			*cfg = *candidate
			return nil
		}
	}
	candidate.Servers = append(candidate.Servers, item)
	if err := validate(candidate); err != nil {
		return err
	}
	*cfg = *candidate
	return nil
}

func RemoveServer(cfg *Config, name string) bool {
	for i := range cfg.Servers {
		if cfg.Servers[i].Name == name {
			cfg.Servers = append(cfg.Servers[:i], cfg.Servers[i+1:]...)
			return true
		}
	}
	return false
}
