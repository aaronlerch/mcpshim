package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/mcpshim/mcpshim/internal/protocol"
)

// session bridges the duplex Unix-socket connection between the daemon and a
// CLI process. It serializes mid-call writes (elicitation requests) so they
// don't interleave with the final Response.
type session struct {
	mu       sync.Mutex
	enc      *json.Encoder
	dec      *json.Decoder
	flush    func()
	finished bool
}

func newSession(enc *json.Encoder, dec *json.Decoder, flush func()) *session {
	return &session{enc: enc, dec: dec, flush: flush}
}

// RequestElicitation implements mcp.Session — it sends an elicitation
// envelope on the wire and waits for the matching `elicitation_response`
// message from the CLI.
func (s *session) RequestElicitation(ctx context.Context, req protocol.ElicitationRequest) (*protocol.ElicitationAnswer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return nil, errors.New("session already finished")
	}

	envelope := protocol.Response{OK: true, Elicitation: &req}
	if err := s.enc.Encode(envelope); err != nil {
		return nil, fmt.Errorf("send elicitation: %w", err)
	}
	if s.flush != nil {
		s.flush()
	}

	var inbound protocol.Request
	if err := s.dec.Decode(&inbound); err != nil {
		return nil, fmt.Errorf("read elicitation answer: %w", err)
	}
	if inbound.Action != "elicitation_response" || inbound.ElicitationAnswer == nil {
		return nil, fmt.Errorf("expected elicitation_response, got %q", inbound.Action)
	}
	return inbound.ElicitationAnswer, nil
}

// markFinished prevents further mid-call writes after the final Response is
// emitted.
func (s *session) markFinished() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = true
}
