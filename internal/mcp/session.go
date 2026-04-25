package mcp

import (
	"context"
	"errors"
	"fmt"

	mcpproto "github.com/mark3labs/mcp-go/mcp"
	"github.com/mcpshim/mcpshim/internal/protocol"
)

// Session is the bridge an upstream-server-initiated elicitation request
// crosses to reach the CLI process: daemon's elicitation handler -> Session
// -> Unix socket -> CLI -> user. The CLI's reply travels back the same way.
//
// It is implemented by `internal/server.session` (which wraps the Unix
// socket connection's encoder/decoder and a write mutex). The mcp package
// only consumes the interface to avoid a circular import.
type Session interface {
	RequestElicitation(ctx context.Context, req protocol.ElicitationRequest) (*protocol.ElicitationAnswer, error)
}

type sessionKey struct{}

// WithSession binds a Session to ctx so downstream operations (Call,
// ReadResource, GetPrompt) can pick it up via SessionFromContext.
func WithSession(ctx context.Context, s Session) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, sessionKey{}, s)
}

// SessionFromContext returns the Session bound to ctx, or nil if none.
func SessionFromContext(ctx context.Context) Session {
	if v, ok := ctx.Value(sessionKey{}).(Session); ok {
		return v
	}
	return nil
}

// elicitBridge implements mcp-go's ElicitationHandler interface by
// delegating to the Session pulled from the active call's context.
type elicitBridge struct {
	server string
	getCtx func() context.Context
}

func (b *elicitBridge) Elicit(ctx context.Context, req mcpproto.ElicitationRequest) (*mcpproto.ElicitationResult, error) {
	// mcp-go calls this from inside the operation context; prefer that ctx
	// but fall back to the registry-level one if mcp-go drops the value.
	sess := SessionFromContext(ctx)
	if sess == nil && b.getCtx != nil {
		sess = SessionFromContext(b.getCtx())
	}
	if sess == nil {
		// Background refresh, no user available — best to decline rather than
		// hang waiting on a CLI that does not exist.
		return &mcpproto.ElicitationResult{
			ElicitationResponse: mcpproto.ElicitationResponse{Action: mcpproto.ElicitationResponseActionDecline},
		}, nil
	}

	wireReq := protocol.ElicitationRequest{
		Server:          b.server,
		Mode:            req.Params.Mode,
		Message:         req.Params.Message,
		RequestedSchema: req.Params.RequestedSchema,
		URL:             req.Params.URL,
		ElicitationID:   req.Params.ElicitationID,
	}
	answer, err := sess.RequestElicitation(ctx, wireReq)
	if err != nil {
		return nil, err
	}
	if answer == nil {
		return nil, errors.New("nil elicitation answer")
	}
	action, err := mapElicitationAction(answer.Action)
	if err != nil {
		return nil, err
	}
	return &mcpproto.ElicitationResult{
		ElicitationResponse: mcpproto.ElicitationResponse{
			Action:  action,
			Content: answer.Content,
		},
	}, nil
}

func mapElicitationAction(s string) (mcpproto.ElicitationResponseAction, error) {
	switch s {
	case string(mcpproto.ElicitationResponseActionAccept):
		return mcpproto.ElicitationResponseActionAccept, nil
	case string(mcpproto.ElicitationResponseActionDecline):
		return mcpproto.ElicitationResponseActionDecline, nil
	case string(mcpproto.ElicitationResponseActionCancel), "":
		return mcpproto.ElicitationResponseActionCancel, nil
	default:
		return "", fmt.Errorf("unknown elicitation action %q", s)
	}
}
