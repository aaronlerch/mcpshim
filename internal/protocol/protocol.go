package protocol

import "time"

type Request struct {
	Action            string                 `json:"action"`
	Name              string                 `json:"name,omitempty"`
	Server            string                 `json:"server,omitempty"`
	Tool              string                 `json:"tool,omitempty"`
	Limit             int                    `json:"limit,omitempty"`
	Alias             string                 `json:"alias,omitempty"`
	URL               string                 `json:"url,omitempty"`
	Transport         string                 `json:"transport,omitempty"`
	Headers           map[string]string      `json:"headers,omitempty"`
	HeadersHelper     string                 `json:"headers_helper,omitempty"`
	Command           string                 `json:"command,omitempty"`
	CmdArgs           []string               `json:"cmd_args,omitempty"`
	Env               map[string]string      `json:"env,omitempty"`
	Args              map[string]interface{} `json:"args,omitempty"`
	URI               string                 `json:"uri,omitempty"`
	PromptArgs        map[string]string      `json:"prompt_args,omitempty"`
	ElicitationAnswer *ElicitationAnswer     `json:"elicitation_answer,omitempty"`
}

// ElicitationRequest is sent from the daemon to the CLI mid-call when the
// upstream MCP server invokes elicitation/create. The CLI prompts the user
// and replies with `Request{Action: "elicitation_response", ElicitationAnswer: ...}`.
type ElicitationRequest struct {
	Server          string `json:"server"`
	Mode            string `json:"mode,omitempty"` // "form" (default) or "url"
	Message         string `json:"message"`
	RequestedSchema any    `json:"requested_schema,omitempty"`
	URL             string `json:"url,omitempty"`
	ElicitationID   string `json:"elicitation_id,omitempty"`
}

// ElicitationAnswer is the CLI's response. Action is one of "accept",
// "decline", or "cancel". Content carries form-mode data and is omitted
// for decline/cancel.
type ElicitationAnswer struct {
	Action  string `json:"action"`
	Content any    `json:"content,omitempty"`
}

type ServerInfo struct {
	Name          string    `json:"name"`
	Alias         string    `json:"alias,omitempty"`
	URL           string    `json:"url"`
	Transport     string    `json:"transport"`
	HasAuth       bool      `json:"has_auth"`
	Status        string    `json:"status,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	LastSuccessAt time.Time `json:"last_success_at,omitzero"`
	AttemptCount  int       `json:"attempt_count,omitempty"`
}

type ToolInfo struct {
	Server      string         `json:"server"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Required    []string       `json:"required,omitempty"`
	Properties  []string       `json:"properties,omitempty"`
	Meta        map[string]any `json:"_meta,omitempty"`
}

type PropertyDetail struct {
	Name        string   `json:"name"`
	Type        string   `json:"type,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Const       string   `json:"const,omitempty"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required"`
}

type ToolDetail struct {
	Server      string           `json:"server"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Properties  []PropertyDetail `json:"properties,omitempty"`
	Meta        map[string]any   `json:"_meta,omitempty"`
}

type ResourceInfo struct {
	Server      string `json:"server"`
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mime_type,omitempty"`
}

// ResourceContent is the wire form of an MCP ResourceContents entry. Either
// Text (for text resources) or Blob (base64) is set, never both.
type ResourceContent struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mime_type,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

type PromptArg struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type PromptInfo struct {
	Server      string      `json:"server"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Arguments   []PromptArg `json:"arguments,omitempty"`
}

// PromptMessage mirrors mcp.PromptMessage. Content shapes vary (text, image,
// resource reference) so we keep it as raw JSON-y data rather than enumerate.
type PromptMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type PromptResult struct {
	Server      string          `json:"server"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages,omitempty"`
}

type Status struct {
	StartedAt   time.Time `json:"started_at"`
	UptimeSec   int64     `json:"uptime_sec"`
	ServerCount int       `json:"server_count"`
	ToolCount   int       `json:"tool_count"`
}

type HistoryItem struct {
	At         time.Time              `json:"at"`
	Server     string                 `json:"server"`
	Tool       string                 `json:"tool"`
	Args       map[string]interface{} `json:"args,omitempty"`
	Success    bool                   `json:"success"`
	Error      string                 `json:"error,omitempty"`
	DurationMs int64                  `json:"duration_ms"`
}

type Response struct {
	OK               bool                `json:"ok"`
	Error            string              `json:"error,omitempty"`
	Status           *Status             `json:"status,omitempty"`
	Servers          []ServerInfo        `json:"servers,omitempty"`
	Tools            []ToolInfo          `json:"tools,omitempty"`
	History          []HistoryItem       `json:"history,omitempty"`
	ToolDetail       *ToolDetail         `json:"tool_detail,omitempty"`
	Resources        []ResourceInfo      `json:"resources,omitempty"`
	ResourceContents []ResourceContent   `json:"resource_contents,omitempty"`
	Prompts          []PromptInfo        `json:"prompts,omitempty"`
	PromptResult     *PromptResult       `json:"prompt_result,omitempty"`
	Result           interface{}         `json:"result,omitempty"`
	Text             string              `json:"text,omitempty"`
	Elicitation      *ElicitationRequest `json:"elicitation,omitempty"`
}
