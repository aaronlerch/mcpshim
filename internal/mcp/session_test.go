package mcp

import (
	"context"
	"testing"

	mcpproto "github.com/mark3labs/mcp-go/mcp"
	"github.com/mcpshim/mcpshim/internal/protocol"
)

func TestMapElicitationAction(t *testing.T) {
	cases := map[string]mcpproto.ElicitationResponseAction{
		"accept":  mcpproto.ElicitationResponseActionAccept,
		"decline": mcpproto.ElicitationResponseActionDecline,
		"cancel":  mcpproto.ElicitationResponseActionCancel,
		"":        mcpproto.ElicitationResponseActionCancel, // empty defaults to cancel
	}
	for in, want := range cases {
		got, err := mapElicitationAction(in)
		if err != nil {
			t.Errorf("mapElicitationAction(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("mapElicitationAction(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := mapElicitationAction("yolo"); err == nil {
		t.Error("expected error for unknown action")
	}
}

func TestElicitBridgeNoSessionDeclines(t *testing.T) {
	bridge := &elicitBridge{server: "demo"}
	res, err := bridge.Elicit(context.Background(), mcpproto.ElicitationRequest{})
	if err != nil {
		t.Fatalf("Elicit error: %v", err)
	}
	if res == nil || res.Action != mcpproto.ElicitationResponseActionDecline {
		t.Errorf("expected decline action when no session, got %+v", res)
	}
}

type stubSession struct {
	last protocol.ElicitationRequest
	out  *protocol.ElicitationAnswer
	err  error
}

func (s *stubSession) RequestElicitation(ctx context.Context, req protocol.ElicitationRequest) (*protocol.ElicitationAnswer, error) {
	s.last = req
	return s.out, s.err
}

func TestElicitBridgeUsesSession(t *testing.T) {
	stub := &stubSession{out: &protocol.ElicitationAnswer{Action: "accept", Content: map[string]any{"k": "v"}}}
	ctx := WithSession(context.Background(), stub)

	bridge := &elicitBridge{server: "fs", getCtx: func() context.Context { return ctx }}
	req := mcpproto.ElicitationRequest{}
	req.Params.Mode = "form"
	req.Params.Message = "give me a value"
	req.Params.RequestedSchema = map[string]any{"type": "object"}

	res, err := bridge.Elicit(ctx, req)
	if err != nil {
		t.Fatalf("Elicit error: %v", err)
	}
	if stub.last.Server != "fs" || stub.last.Message != "give me a value" {
		t.Errorf("session received wrong request: %+v", stub.last)
	}
	if res.Action != mcpproto.ElicitationResponseActionAccept {
		t.Errorf("expected accept action, got %q", res.Action)
	}
}
