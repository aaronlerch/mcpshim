package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mcpshim/mcpshim/internal/config"
	"github.com/mcpshim/mcpshim/internal/mcp"
	"github.com/mcpshim/mcpshim/internal/protocol"
	"github.com/mcpshim/mcpshim/internal/store"
)

type headerArgs map[string]string

func (h *headerArgs) String() string {
	if h == nil || *h == nil {
		return ""
	}
	parts := make([]string, 0, len(*h))
	for k, v := range *h {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func (h *headerArgs) Set(value string) error {
	parts := strings.SplitN(value, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid header %q, expected key=value", value)
	}
	key := strings.TrimSpace(parts[0])
	val := strings.TrimSpace(parts[1])
	if key == "" {
		return errors.New("header key cannot be empty")
	}
	if *h == nil {
		*h = map[string]string{}
	}
	(*h)[key] = val
	return nil
}

type stringSliceArgs []string

func (s *stringSliceArgs) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(*s, ",")
}

func (s *stringSliceArgs) Set(value string) error {
	*s = append(*s, value)
	return nil
}
func Run(binaryName string, argv []string) int {
	if binaryName == "" {
		binaryName = filepath.Base(os.Args[0])
	}

	if binaryName != "mcpshim" {
		if len(argv) < 1 {
			fmt.Fprintf(os.Stderr, "%s requires a tool name\n", binaryName)
			return 1
		}
		resp, err := call(protocol.Request{
			Action: "call",
			Server: binaryName,
			Tool:   argv[0],
			Args:   parseDynamicArgs(argv[1:]),
		}, config.DefaultSocketPath())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return printResponse(resp, true)
	}

	if len(argv) == 0 {
		usage()
		return 1
	}

	socketPath := config.DefaultSocketPath()
	jsonOut := !isTerminal(os.Stdout.Fd())

	global := flag.NewFlagSet("global", flag.ContinueOnError)
	global.StringVar(&socketPath, "socket", socketPath, "unix socket path")
	global.BoolVar(&jsonOut, "json", jsonOut, "json output")
	global.SetOutput(os.Stderr)
	_ = global.Parse(argv)
	args := global.Args()
	if len(args) == 0 {
		usage()
		return 1
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "servers":
		resp, err := call(protocol.Request{Action: "servers"}, socketPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return printResponse(resp, jsonOut)
	case "tools":
		fs := flag.NewFlagSet("tools", flag.ContinueOnError)
		var server string
		var full bool
		fs.StringVar(&server, "server", "", "server name or alias")
		fs.BoolVar(&full, "full", false, "show full tool descriptions")
		_ = fs.Parse(rest)
		resp, err := call(protocol.Request{Action: "tools", Server: server}, socketPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if jsonOut {
			return printResponse(resp, jsonOut)
		}
		if !resp.OK {
			fmt.Fprintln(os.Stderr, resp.Error)
			return 1
		}
		printToolsList(resp.Tools, full)
		return 0
	case "inspect":
		fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
		var server, tool string
		fs.StringVar(&server, "server", "", "server name or alias")
		fs.StringVar(&tool, "tool", "", "tool name")
		_ = fs.Parse(rest)
		// allow positional: inspect <server> <tool>
		if server == "" || tool == "" {
			pos := fs.Args()
			if server == "" && len(pos) > 0 {
				server = pos[0]
				pos = pos[1:]
			}
			if tool == "" && len(pos) > 0 {
				tool = pos[0]
			}
		}
		if server == "" || tool == "" {
			fmt.Fprintln(os.Stderr, "usage: mcpshim inspect --server <name> --tool <tool>")
			return 1
		}
		resp, err := call(protocol.Request{Action: "inspect", Server: server, Tool: tool}, socketPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return printResponse(resp, jsonOut)
	case "call":
		return runCall(rest, socketPath, jsonOut)
	case "add":
		fs := flag.NewFlagSet("add", flag.ContinueOnError)
		var name, alias, url, transport, command, headersHelper, clientID, clientSecret string
		var headers headerArgs
		var cmdArgs stringSliceArgs
		var env headerArgs
		fs.StringVar(&name, "name", "", "server name")
		fs.StringVar(&alias, "alias", "", "short alias")
		fs.StringVar(&url, "url", "", "mcp endpoint (http/sse only)")
		fs.StringVar(&transport, "transport", "http", "http|sse|stdio")
		fs.Var(&headers, "header", "request header key=value (repeatable, http/sse only)")
		fs.StringVar(&headersHelper, "headers-helper", "", "shell command producing JSON header map (http/sse only)")
		fs.StringVar(&command, "command", "", "executable to launch (stdio only)")
		fs.Var(&cmdArgs, "arg", "command argument (repeatable, stdio only)")
		fs.Var(&env, "env", "environment variable key=value (repeatable, stdio only)")
		fs.StringVar(&clientID, "client-id", "", "pre-configured OAuth client_id (skips dynamic registration)")
		fs.StringVar(&clientSecret, "client-secret", "", "pre-configured OAuth client_secret")
		_ = fs.Parse(rest)
		headersMap := map[string]string(headers)
		envMap := map[string]string(env)
		resp, err := call(protocol.Request{
			Action:        "add_server",
			Name:          name,
			Alias:         alias,
			URL:           url,
			Transport:     transport,
			Headers:       headersMap,
			HeadersHelper: headersHelper,
			Command:       command,
			CmdArgs:       []string(cmdArgs),
			Env:           envMap,
			ClientID:      clientID,
			ClientSecret:  clientSecret,
		}, socketPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return printResponse(resp, jsonOut)
	case "set":
		return runSetCommand(rest, socketPath, jsonOut)
	case "remove":
		fs := flag.NewFlagSet("remove", flag.ContinueOnError)
		var name string
		fs.StringVar(&name, "name", "", "server name")
		_ = fs.Parse(rest)
		resp, err := call(protocol.Request{Action: "remove_server", Name: name}, socketPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return printResponse(resp, jsonOut)
	case "status":
		resp, err := call(protocol.Request{Action: "status"}, socketPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return printResponse(resp, jsonOut)
	case "history":
		fs := flag.NewFlagSet("history", flag.ContinueOnError)
		var server, tool string
		var limit int
		fs.StringVar(&server, "server", "", "filter by server name or alias")
		fs.StringVar(&tool, "tool", "", "filter by tool name")
		fs.IntVar(&limit, "limit", 50, "max entries to return (1-500)")
		_ = fs.Parse(rest)
		resp, err := call(protocol.Request{Action: "history", Server: server, Tool: tool, Limit: limit}, socketPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return printResponse(resp, jsonOut)
	case "reload":
		resp, err := call(protocol.Request{Action: "reload"}, socketPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return printResponse(resp, jsonOut)
	case "refresh":
		fs := flag.NewFlagSet("refresh", flag.ContinueOnError)
		var server string
		fs.StringVar(&server, "server", "", "server name or alias (omit to refresh all)")
		_ = fs.Parse(rest)
		resp, err := call(protocol.Request{Action: "refresh", Server: server}, socketPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return printResponse(resp, jsonOut)
	case "validate":
		fs := flag.NewFlagSet("validate", flag.ContinueOnError)
		configPath := fs.String("config", config.DefaultConfigPath(), "config path to validate")
		_ = fs.Parse(rest)
		if _, err := config.Load(*configPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("config is valid: %s\n", *configPath)
		return 0
	case "login":
		fs := flag.NewFlagSet("login", flag.ContinueOnError)
		var server string
		var manual bool
		fs.StringVar(&server, "server", "", "server name or alias")
		fs.BoolVar(&manual, "manual", false, "complete oauth by pasting redirect url/code")
		_ = fs.Parse(rest)
		if server == "" {
			pos := fs.Args()
			if len(pos) > 0 {
				server = pos[0]
			}
		}
		if server == "" {
			fmt.Fprintln(os.Stderr, "usage: mcpshim login --server <name>")
			return 1
		}
		return runLoginLocal(server, manual)
	case "manifest":
		fs := flag.NewFlagSet("manifest", flag.ContinueOnError)
		var pathOnly bool
		fs.BoolVar(&pathOnly, "path", false, "print the manifest file path instead of its content")
		_ = fs.Parse(rest)
		resp, err := call(protocol.Request{Action: "manifest"}, socketPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !resp.OK {
			fmt.Fprintln(os.Stderr, resp.Error)
			return 1
		}
		if jsonOut {
			return printResponse(resp, jsonOut)
		}
		if pathOnly {
			fmt.Println(resp.ManifestPath)
			return 0
		}
		fmt.Print(resp.ManifestContent)
		return 0
	case "logout":
		fs := flag.NewFlagSet("logout", flag.ContinueOnError)
		var server string
		var full bool
		fs.StringVar(&server, "server", "", "server name or alias")
		fs.BoolVar(&full, "full", false, "also delete persisted client credentials")
		_ = fs.Parse(rest)
		if server == "" {
			pos := fs.Args()
			if len(pos) > 0 {
				server = pos[0]
			}
		}
		if server == "" {
			fmt.Fprintln(os.Stderr, "usage: mcpshim logout --server <name> [--full]")
			return 1
		}
		resp, err := call(protocol.Request{Action: "logout", Server: server, Full: full}, socketPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return printResponse(resp, jsonOut)
	case "resources":
		fs := flag.NewFlagSet("resources", flag.ContinueOnError)
		var server string
		fs.StringVar(&server, "server", "", "server name or alias")
		_ = fs.Parse(rest)
		resp, err := call(protocol.Request{Action: "resources", Server: server}, socketPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if jsonOut {
			return printResponse(resp, jsonOut)
		}
		if !resp.OK {
			fmt.Fprintln(os.Stderr, resp.Error)
			return 1
		}
		printResourcesList(resp.Resources)
		return 0
	case "read":
		fs := flag.NewFlagSet("read", flag.ContinueOnError)
		var server, uri string
		fs.StringVar(&server, "server", "", "server name or alias")
		fs.StringVar(&uri, "uri", "", "resource uri")
		_ = fs.Parse(rest)
		if server == "" || uri == "" {
			pos := fs.Args()
			if server == "" && len(pos) > 0 {
				server = pos[0]
				pos = pos[1:]
			}
			if uri == "" && len(pos) > 0 {
				uri = pos[0]
			}
		}
		if server == "" || uri == "" {
			fmt.Fprintln(os.Stderr, "usage: mcpshim read --server <name> --uri <resource-uri>")
			return 1
		}
		resp, err := call(protocol.Request{Action: "read_resource", Server: server, URI: uri}, socketPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if jsonOut {
			return printResponse(resp, jsonOut)
		}
		if !resp.OK {
			fmt.Fprintln(os.Stderr, resp.Error)
			return 1
		}
		printResourceContents(resp.ResourceContents)
		return 0
	case "prompts":
		fs := flag.NewFlagSet("prompts", flag.ContinueOnError)
		var server string
		fs.StringVar(&server, "server", "", "server name or alias")
		_ = fs.Parse(rest)
		resp, err := call(protocol.Request{Action: "prompts", Server: server}, socketPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if jsonOut {
			return printResponse(resp, jsonOut)
		}
		if !resp.OK {
			fmt.Fprintln(os.Stderr, resp.Error)
			return 1
		}
		printPromptsList(resp.Prompts)
		return 0
	case "get-prompt":
		fs := flag.NewFlagSet("get-prompt", flag.ContinueOnError)
		var server, name string
		var args headerArgs
		fs.StringVar(&server, "server", "", "server name or alias")
		fs.StringVar(&name, "name", "", "prompt name")
		fs.Var(&args, "arg", "prompt argument key=value (repeatable)")
		_ = fs.Parse(rest)
		if server == "" || name == "" {
			fmt.Fprintln(os.Stderr, "usage: mcpshim get-prompt --server <name> --name <prompt> [--arg K=V]")
			return 1
		}
		resp, err := call(protocol.Request{Action: "get_prompt", Server: server, Name: name, PromptArgs: map[string]string(args)}, socketPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return printResponse(resp, jsonOut)
	case "script":
		return runScriptCommand(rest, socketPath)
	default:
		if len(rest) > 0 {
			resp, err := call(protocol.Request{
				Action: "call",
				Server: cmd,
				Tool:   rest[0],
				Args:   parseDynamicArgs(rest[1:]),
			}, socketPath)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			return printResponse(resp, jsonOut)
		}
		usage()
		return 1
	}
}

func runScriptCommand(args []string, socket string) int {
	fs := flag.NewFlagSet("script", flag.ContinueOnError)
	install := fs.Bool("install", false, "install executable wrappers instead of printing shell script")
	dir := fs.String("dir", filepath.Join(os.Getenv("HOME"), ".local", "bin"), "target directory for wrappers when --install is set")
	_ = fs.Parse(args)

	resp, err := call(protocol.Request{Action: "servers"}, socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintln(os.Stderr, resp.Error)
		return 1
	}

	if *install {
		if err := installAliasScripts(*dir, resp.Servers); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("installed %d wrappers in %s\n", len(resp.Servers), *dir)
		return 0
	}

	printAliasScript(resp.Servers)
	return 0
}

func runSetCommand(args []string, socket string, jsonOut bool) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mcpshim set auth --server <name> --header K=V")
		return 1
	}

	sub := args[0]
	if sub != "auth" {
		fmt.Fprintf(os.Stderr, "unknown set target %q (supported: auth)\n", sub)
		return 1
	}

	fs := flag.NewFlagSet("set auth", flag.ContinueOnError)
	var name, clientID, clientSecret string
	var headers headerArgs
	fs.StringVar(&name, "server", "", "server name")
	fs.Var(&headers, "header", "request header key=value (repeatable)")
	fs.StringVar(&clientID, "client-id", "", "pre-configured OAuth client_id")
	fs.StringVar(&clientSecret, "client-secret", "", "pre-configured OAuth client_secret")
	_ = fs.Parse(args[1:])
	if name == "" {
		fmt.Fprintln(os.Stderr, "usage: mcpshim set auth --server <name> [--header K=V] [--client-id X --client-secret Y]")
		return 1
	}
	headersMap := map[string]string(headers)
	resp, err := call(protocol.Request{Action: "set_auth", Name: name, Headers: headersMap, ClientID: clientID, ClientSecret: clientSecret}, socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printResponse(resp, jsonOut)
}

func runCall(args []string, socket string, jsonOut bool) int {
	server, tool, rest, helpRequested, parseTextJSON, err := parseCallArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if server == "" && len(rest) > 0 {
		server = rest[0]
		rest = rest[1:]
	}
	if tool == "" && len(rest) > 0 {
		tool = rest[0]
		rest = rest[1:]
	}
	if server == "" || tool == "" {
		fmt.Fprintln(os.Stderr, "usage: mcpshim call --server <name> --tool <tool> [--flag value ...]")
		return 1
	}

	if helpRequested {
		return printCallHelp(server, tool, socket)
	}

	dynamicArgs := parseDynamicArgs(rest)
	if detail, err := fetchToolDetail(server, tool, socket); err == nil && detail != nil {
		missing := []string{}
		for _, p := range detail.Properties {
			if p.Required {
				if _, ok := dynamicArgs[p.Name]; !ok {
					missing = append(missing, p.Name)
				}
			}
		}
		if len(missing) > 0 {
			fmt.Fprintf(os.Stderr, "missing required argument(s):")
			for _, name := range missing {
				fmt.Fprintf(os.Stderr, " --%s", name)
			}
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr)
			printCallHelpFromDetail(detail)
			return 1
		}
	}

	resp, err := call(protocol.Request{Action: "call", Server: server, Tool: tool, Args: dynamicArgs}, socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if parseTextJSON {
		resp.Result = parseJSONLikeContentText(resp.Result)
	}
	return printResponse(resp, jsonOut)
}

func parseCallArgs(args []string) (server string, tool string, rest []string, help bool, parseTextJSON bool, err error) {
	rest = make([]string, 0, len(args))
	passthrough := false
	for i := 0; i < len(args); i++ {
		item := args[i]
		if passthrough {
			rest = append(rest, item)
			continue
		}
		switch {
		case item == "--":
			passthrough = true
		case item == "--help" || item == "-h":
			help = true
		case item == "--json":
			parseTextJSON = true
		case item == "--json=true":
			parseTextJSON = true
		case item == "--json=false":
			parseTextJSON = false
		case item == "--server":
			if i+1 >= len(args) {
				return "", "", nil, false, false, errors.New("missing value for --server")
			}
			server = args[i+1]
			i++
		case item == "--tool":
			if i+1 >= len(args) {
				return "", "", nil, false, false, errors.New("missing value for --tool")
			}
			tool = args[i+1]
			i++
		case strings.HasPrefix(item, "--server="):
			server = strings.TrimPrefix(item, "--server=")
		case strings.HasPrefix(item, "--tool="):
			tool = strings.TrimPrefix(item, "--tool=")
		default:
			rest = append(rest, item)
		}
	}
	return server, tool, rest, help, parseTextJSON, nil
}

func parseJSONLikeContentText(result interface{}) interface{} {
	value, _ := walkAndParseJSONText(result)
	return value
}

func walkAndParseJSONText(value interface{}) (interface{}, bool) {
	switch typed := value.(type) {
	case map[string]interface{}:
		out := map[string]interface{}{}
		changed := false
		for key, item := range typed {
			if key == "text" {
				if text, ok := item.(string); ok {
					if parsed, ok := tryParseJSONValue(text); ok {
						out[key] = parsed
						changed = true
						continue
					}
				}
			}
			next, itemChanged := walkAndParseJSONText(item)
			out[key] = next
			if itemChanged {
				changed = true
			}
		}
		return out, changed
	case []interface{}:
		out := make([]interface{}, len(typed))
		changed := false
		for i, item := range typed {
			next, itemChanged := walkAndParseJSONText(item)
			out[i] = next
			if itemChanged {
				changed = true
			}
		}
		return out, changed
	default:
		return value, false
	}
}

func tryParseJSONValue(text string) (interface{}, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, false
	}
	first := trimmed[0]
	if first != '{' && first != '[' {
		return nil, false
	}
	var parsed interface{}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, false
	}
	return parsed, true
}

func stripHelpFlags(args []string) ([]string, bool) {
	clean := make([]string, 0, len(args))
	help := false
	for _, item := range args {
		if item == "--help" || item == "-h" {
			help = true
			continue
		}
		clean = append(clean, item)
	}
	return clean, help
}

func printCallHelp(server, tool, socket string) int {
	if server != "" && tool != "" {
		fmt.Printf("usage: mcpshim call --server %s --tool %s [--json] [--arg value ...]\n", server, tool)
	} else {
		fmt.Println("usage: mcpshim call --server <name> --tool <tool> [--json] [--arg value ...]")
	}
	fmt.Println("       mcpshim call --server <name> --tool <tool> -- [--reserved-arg value ...]")
	fmt.Println("       --json parses JSON-like content text fields in tool results")
	fmt.Println()
	detail, err := fetchToolDetail(server, tool, socket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load tool details: %v\n", err)
		return 1
	}
	printCallHelpFromDetail(detail)
	return 0
}

func fetchToolDetail(server, tool, socket string) (*protocol.ToolDetail, error) {
	resp, err := call(protocol.Request{Action: "inspect", Server: server, Tool: tool}, socket)
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, errors.New(resp.Error)
	}
	if resp.ToolDetail == nil {
		return nil, errors.New("tool details not available")
	}
	return resp.ToolDetail, nil
}

func printCallHelpFromDetail(d *protocol.ToolDetail) {
	if d == nil {
		return
	}
	fmt.Printf("server: %s\n", d.Server)
	fmt.Printf("tool:   %s\n", d.Name)
	if d.Description != "" {
		fmt.Println("\ndescription:")
		printIndentedBlock(normalizeMultiline(d.Description), "  ")
	}
	if len(d.Properties) > 0 {
		fmt.Println("\nparameters:")
		for _, p := range d.Properties {
			req := ""
			if p.Required {
				req = " (required)"
			}
			typ := p.Type
			if typ == "" {
				typ = "any"
			}
			if len(p.Enum) > 0 {
				typ += " enum(" + strings.Join(p.Enum, "|") + ")"
			}
			if p.Const != "" {
				typ += " const(" + p.Const + ")"
			}
			if p.Description != "" {
				descLines := splitNonEmptyLines(p.Description)
				first := ""
				if len(descLines) > 0 {
					first = descLines[0]
				}
				fmt.Printf("  --%-20s %s%s - %s\n", p.Name, typ, req, first)
				for _, line := range descLines[1:] {
					fmt.Printf("  %-20s   %s\n", "", line)
				}
			} else {
				fmt.Printf("  --%-20s %s%s\n", p.Name, typ, req)
			}
		}
	}
}

func printIndentedBlock(text string, indent string) {
	if text == "" {
		return
	}
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			fmt.Println()
			continue
		}
		fmt.Printf("%s%s\n", indent, line)
	}
}

func splitNonEmptyLines(text string) []string {
	normalized := normalizeMultiline(text)
	if normalized == "" {
		return nil
	}
	parts := strings.Split(normalized, "\n")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func serverStatusBadge(status string) string {
	switch status {
	case "healthy":
		return "[ok]    "
	case "degraded":
		return "[degr]  "
	case "failed":
		return "[fail]  "
	case "auth_required":
		return "[auth]  "
	default:
		return "[?]     "
	}
}

func truncateForDisplay(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func printResourcesList(items []protocol.ResourceInfo) {
	for _, r := range items {
		mime := r.MIMEType
		if mime == "" {
			mime = "?"
		}
		desc := summarizeDescription(r.Description)
		line := fmt.Sprintf("%s  %s  [%s]", r.Server, r.URI, mime)
		if r.Name != "" && r.Name != r.URI {
			line += "  " + r.Name
		}
		if desc != "" {
			line += "  — " + desc
		}
		fmt.Println(line)
	}
}

func printResourceContents(items []protocol.ResourceContent) {
	for i, c := range items {
		if i > 0 {
			fmt.Println("---")
		}
		mime := c.MIMEType
		if mime == "" {
			mime = "?"
		}
		fmt.Printf("# %s [%s]\n", c.URI, mime)
		if c.Text != "" {
			fmt.Println(c.Text)
		} else if c.Blob != "" {
			fmt.Printf("(blob, %d base64 bytes)\n", len(c.Blob))
		}
	}
}

func printPromptsList(items []protocol.PromptInfo) {
	for _, p := range items {
		args := []string{}
		for _, a := range p.Arguments {
			marker := ""
			if a.Required {
				marker = "*"
			}
			args = append(args, a.Name+marker)
		}
		argStr := ""
		if len(args) > 0 {
			argStr = " (" + strings.Join(args, ", ") + ")"
		}
		desc := summarizeDescription(p.Description)
		fmt.Printf("%s/%s%s", p.Server, p.Name, argStr)
		if desc != "" {
			fmt.Printf("  %s", desc)
		}
		fmt.Println()
	}
}

func printToolsList(items []protocol.ToolInfo, full bool) {
	if len(items) == 0 {
		return
	}

	singleServer := true
	firstServer := items[0].Server
	for _, item := range items[1:] {
		if item.Server != firstServer {
			singleServer = false
			break
		}
	}

	if full {
		for i, item := range items {
			name := item.Name
			if !singleServer {
				name = item.Server + "/" + item.Name
			}
			fmt.Println(name)
			if len(item.Required) > 0 {
				fmt.Printf("  required: %s\n", strings.Join(item.Required, ", "))
			}
			if len(item.Properties) > 0 {
				fmt.Printf("  parameters: %s\n", strings.Join(item.Properties, ", "))
			}
			detail := normalizeMultiline(item.Description)
			if detail != "" {
				fmt.Println("  description:")
				for _, line := range strings.Split(detail, "\n") {
					fmt.Printf("    %s\n", line)
				}
			}
			if i < len(items)-1 {
				fmt.Println()
			}
		}
		return
	}

	for _, item := range items {
		name := item.Name
		if !singleServer {
			name = item.Server + "/" + item.Name
		}
		summary := summarizeDescription(item.Description)
		if len(item.Required) > 0 {
			if summary != "" {
				summary += " "
			}
			summary += "required: " + strings.Join(item.Required, ",")
		}
		if summary != "" {
			fmt.Printf("%-30s  %s\n", name, summary)
		} else {
			fmt.Printf("%s\n", name)
		}
	}
}

func summarizeDescription(input string) string {
	text := normalizeMultiline(input)
	if text == "" {
		return ""
	}
	firstLine := ""
	for _, line := range strings.Split(text, "\n") {
		candidate := strings.TrimSpace(line)
		if candidate == "" {
			continue
		}
		if strings.HasPrefix(candidate, "<example") || strings.HasPrefix(candidate, "</example") || strings.HasPrefix(candidate, "<examples") || strings.HasPrefix(candidate, "</examples") {
			continue
		}
		if strings.HasPrefix(candidate, "{") || strings.HasPrefix(candidate, "[") {
			continue
		}
		candidate = strings.TrimSpace(strings.TrimLeft(candidate, "#"))
		if candidate == "" {
			continue
		}
		firstLine = candidate
		break
	}
	if firstLine == "" {
		return ""
	}
	if idx := strings.Index(firstLine, ". "); idx > 0 {
		firstLine = firstLine[:idx+1]
	}
	if len(firstLine) > 100 {
		firstLine = firstLine[:97] + "..."
	}
	return firstLine
}

func normalizeMultiline(input string) string {
	if input == "" {
		return ""
	}
	rawLines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	lines := make([]string, 0, len(rawLines))
	blank := false
	for _, line := range rawLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if blank {
				continue
			}
			blank = true
			lines = append(lines, "")
			continue
		}
		blank = false
		lines = append(lines, trimmed)
	}
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func runLoginLocal(server string, manual bool) int {
	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dbStore, err := store.Open(cfg.Server.DBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer dbStore.Close()

	registry := mcp.NewRegistry(cfg, dbStore)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	if err := registry.Login(ctx, server, manual); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("oauth login completed for %s\n", server)
	return 0
}

func parseDynamicArgs(args []string) map[string]interface{} {
	out := map[string]interface{}{}
	for i := 0; i < len(args); i++ {
		item := args[i]
		if !strings.HasPrefix(item, "--") {
			continue
		}
		key := strings.TrimPrefix(item, "--")
		if strings.Contains(key, "=") {
			parts := strings.SplitN(key, "=", 2)
			out[parts[0]] = normalize(parts[1])
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			out[key] = normalize(args[i+1])
			i++
			continue
		}
		out[key] = true
	}
	return out
}

func normalize(v string) interface{} {
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	if i, err := strconv.ParseInt(v, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	return v
}

func call(req protocol.Request, socketPath string) (*protocol.Response, error) {
	conn, err := net.DialTimeout("unix", socketPath, 4*time.Second)
	if err != nil {
		fallback := fallbackSocketPath(socketPath)
		if fallback != "" && fallback != socketPath {
			conn, err = net.DialTimeout("unix", fallback, 4*time.Second)
		}
	}
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// Long deadline tolerates user input mid-call for elicitation prompts.
	_ = conn.SetDeadline(time.Now().Add(15 * time.Minute))

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	if err := enc.Encode(req); err != nil {
		return nil, err
	}
	for {
		var resp protocol.Response
		if err := dec.Decode(&resp); err != nil {
			return nil, err
		}
		if resp.Elicitation == nil {
			return &resp, nil
		}
		answer := promptElicitation(resp.Elicitation)
		reply := protocol.Request{Action: "elicitation_response", ElicitationAnswer: answer}
		if err := enc.Encode(reply); err != nil {
			return nil, err
		}
	}
}

// promptElicitation surfaces an upstream MCP server's elicitation request to
// the user. In non-interactive contexts (no TTY on stdin) it auto-declines so
// programmatic invocations don't hang.
func promptElicitation(req *protocol.ElicitationRequest) *protocol.ElicitationAnswer {
	if !isStdinTerminal() {
		fmt.Fprintf(os.Stderr, "[mcpshim] elicitation declined automatically (non-interactive): %s\n", req.Message)
		return &protocol.ElicitationAnswer{Action: "decline"}
	}

	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "[mcpshim] %s asks:\n", req.Server)
	fmt.Fprintf(os.Stderr, "  %s\n", req.Message)

	mode := req.Mode
	if mode == "" {
		mode = "form"
	}

	switch mode {
	case "url":
		if req.URL != "" {
			fmt.Fprintf(os.Stderr, "  URL: %s\n", req.URL)
		}
		fmt.Fprint(os.Stderr, "  Visit URL and confirm? [y/N/cancel]: ")
		line := readStdinLine()
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return &protocol.ElicitationAnswer{Action: "accept"}
		case "c", "cancel":
			return &protocol.ElicitationAnswer{Action: "cancel"}
		default:
			return &protocol.ElicitationAnswer{Action: "decline"}
		}
	default: // form
		if req.RequestedSchema != nil {
			schemaJSON, _ := json.MarshalIndent(req.RequestedSchema, "  ", "  ")
			fmt.Fprintf(os.Stderr, "  Expected JSON schema:\n  %s\n", string(schemaJSON))
		}
		fmt.Fprint(os.Stderr, "  Reply with JSON object (or 'decline'/'cancel'): ")
		line := readStdinLine()
		trimmed := strings.TrimSpace(line)
		switch strings.ToLower(trimmed) {
		case "decline", "":
			return &protocol.ElicitationAnswer{Action: "decline"}
		case "cancel":
			return &protocol.ElicitationAnswer{Action: "cancel"}
		}
		var content any
		if err := json.Unmarshal([]byte(trimmed), &content); err != nil {
			fmt.Fprintf(os.Stderr, "  invalid JSON (%v); declining\n", err)
			return &protocol.ElicitationAnswer{Action: "decline"}
		}
		return &protocol.ElicitationAnswer{Action: "accept", Content: content}
	}
}

func readStdinLine() string {
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return ""
	}
	return line
}

func isStdinTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func fallbackSocketPath(requested string) string {
	if strings.TrimSpace(requested) != strings.TrimSpace(config.DefaultSocketPath()) {
		return ""
	}
	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil || cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Server.SocketPath)
}

func printResponse(resp *protocol.Response, jsonOut bool) int {
	if resp == nil {
		fmt.Fprintln(os.Stderr, "empty response")
		return 1
	}
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(resp)
	} else {
		if !resp.OK {
			fmt.Fprintln(os.Stderr, resp.Error)
			return 1
		}
		if resp.Text != "" {
			fmt.Println(resp.Text)
		}
		if resp.Status != nil {
			fmt.Printf("uptime=%ds servers=%d tools=%d\n", resp.Status.UptimeSec, resp.Status.ServerCount, resp.Status.ToolCount)
		}
		if len(resp.Servers) > 0 {
			for _, s := range resp.Servers {
				status := s.Status
				if status == "" {
					status = "unknown"
				}
				badge := serverStatusBadge(status)
				target := s.URL
				if target == "" {
					target = "(stdio)"
				}
				line := fmt.Sprintf("%s %s (%s) %s", badge, s.Name, s.Transport, target)
				if s.AttemptCount > 0 && status != "healthy" {
					line += fmt.Sprintf(" attempts=%d", s.AttemptCount)
				}
				if s.LastError != "" && status != "healthy" {
					line += "  err=" + truncateForDisplay(s.LastError, 80)
				}
				fmt.Println(line)
			}
		}
		if len(resp.History) > 0 {
			for _, h := range resp.History {
				status := "ok"
				if !h.Success {
					status = "error"
				}
				fmt.Printf("%s %s/%s %s (%dms)\n", h.At.Format(time.RFC3339), h.Server, h.Tool, status, h.DurationMs)
				if !h.Success && h.Error != "" {
					fmt.Printf("  error: %s\n", h.Error)
				}
				if len(h.Args) > 0 {
					data, _ := json.Marshal(h.Args)
					fmt.Printf("  args: %s\n", string(data))
				}
			}
		}
		if len(resp.Tools) > 0 {
			printToolsList(resp.Tools, false)
		}
		if resp.ToolDetail != nil {
			d := resp.ToolDetail
			fmt.Printf("server: %s\ntool:   %s\n", d.Server, d.Name)
			if d.Description != "" {
				fmt.Printf("\n%s\n", d.Description)
			}
			if len(d.Properties) > 0 {
				fmt.Println("\nparameters:")
				for _, p := range d.Properties {
					req := ""
					if p.Required {
						req = " (required)"
					}
					typ := p.Type
					if typ == "" {
						typ = "any"
					}
					if p.Description != "" {
						fmt.Printf("  --%-20s %s%s - %s\n", p.Name, typ, req, p.Description)
					} else {
						fmt.Printf("  --%-20s %s%s\n", p.Name, typ, req)
					}
				}
			}
		}
		if resp.Result != nil {
			data, _ := json.MarshalIndent(resp.Result, "", "  ")
			fmt.Println(string(data))
		}
	}
	if !resp.OK {
		if !jsonOut {
			fmt.Fprintln(os.Stderr, resp.Error)
		}
		return 1
	}
	return 0
}

func printAliasScript(items []protocol.ServerInfo) {
	fmt.Println("# source this in your shell")
	for _, target := range buildAliasTargets(items) {
		if target.Sanitized != target.Source {
			fmt.Printf("# %q renamed to %s for shell compatibility\n", target.Source, target.Sanitized)
		}
		fmt.Printf("%s() {\n", target.Sanitized)
		fmt.Printf("  if [ $# -lt 1 ]; then mcpshim tools --server %s; return 1; fi\n", shellQuote(target.ServerName))
		fmt.Printf("  mcpshim call --server %s --tool \"$1\" \"${@:2}\"\n", shellQuote(target.ServerName))
		fmt.Printf("}\n\n")
	}
}

type aliasTarget struct {
	ServerName string
	Source     string
	Sanitized  string
}

func buildAliasTargets(items []protocol.ServerInfo) []aliasTarget {
	used := map[string]int{}
	out := make([]aliasTarget, 0, len(items))
	for _, item := range items {
		source := item.Alias
		if source == "" {
			source = item.Name
		}
		if source == "" {
			continue
		}
		base := sanitizeAliasName(source)
		if base == "" {
			continue
		}
		used[base]++
		sanitized := base
		if used[base] > 1 {
			sanitized = fmt.Sprintf("%s_%d", base, used[base])
		}
		out = append(out, aliasTarget{
			ServerName: item.Name,
			Source:     source,
			Sanitized:  sanitized,
		})
	}
	return out
}

func sanitizeAliasName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range name {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				b.WriteRune('_')
				lastUnderscore = true
			}
		}
	}
	sanitized := strings.Trim(b.String(), "_")
	if sanitized == "" {
		return ""
	}
	if sanitized[0] >= '0' && sanitized[0] <= '9' {
		sanitized = "s_" + sanitized
	}
	return sanitized
}

func installAliasScripts(dir string, items []protocol.ServerInfo) error {
	if dir == "" {
		return errors.New("directory is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, target := range buildAliasTargets(items) {
		if target.Sanitized == "" {
			continue
		}
		path := filepath.Join(dir, target.Sanitized)
		content := "#!/usr/bin/env bash\n" +
			"set -euo pipefail\n" +
			"if [ $# -lt 1 ]; then\n" +
			"  mcpshim tools --server " + shellQuote(target.ServerName) + "\n" +
			"  exit 1\n" +
			"fi\n" +
			"tool=$1\n" +
			"shift\n" +
			"exec mcpshim call --server " + shellQuote(target.ServerName) + " --tool \"$tool\" \"$@\"\n"
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func isTerminal(fd uintptr) bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	if (fi.Mode() & os.ModeCharDevice) == 0 {
		return false
	}
	_, err = os.OpenFile("/proc/self/fd/"+strconv.FormatUint(uint64(fd), 10), os.O_RDONLY, 0)
	return err == nil || errors.Is(err, os.ErrPermission)
}

func usage() {
	fmt.Println("mcpshim <command>")
	fmt.Println("  servers")
	fmt.Println("  tools [--server name] [--full]")
	fmt.Println("  inspect --server name --tool name")
	fmt.Println("  call --server name --tool name [--json] [--arg value]")
	fmt.Println("       use '--' before tool args to pass reserved names (e.g. --help, --server)")
	fmt.Println("  add --name x --url http://... [--transport http|sse] [--alias short] [--header K=V] [--headers-helper 'cmd']")
	fmt.Println("  add --name x --transport stdio --command bin [--arg a] [--env K=V]")
	fmt.Println("       optional: [--client-id X --client-secret Y] for pre-configured OAuth")
	fmt.Println("  set auth --server x [--header K=V] [--client-id X --client-secret Y]")
	fmt.Println("  remove --name x")
	fmt.Println("  reload")
	fmt.Println("  refresh [--server name]")
	fmt.Println("  manifest [--path]")
	fmt.Println("  validate [--config path]")
	fmt.Println("  login --server name [--manual]")
	fmt.Println("  logout --server name [--full]")
	fmt.Println("  status")
	fmt.Println("  history [--server name] [--tool name] [--limit 50]")
	fmt.Println("  resources [--server name]")
	fmt.Println("  read --server name --uri 'protocol://path'")
	fmt.Println("  prompts [--server name]")
	fmt.Println("  get-prompt --server name --name promptname [--arg K=V]")
	fmt.Println("  script [--install] [--dir ~/.local/bin]")
	fmt.Println("  <server-alias> <tool> [--arg value]")
}
