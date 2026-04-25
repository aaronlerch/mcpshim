package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mcpshim/mcpshim/internal/protocol"
)

// pipeSession sets up an in-process duplex connection so we can drive a
// session from both sides in a single test.
func pipeSession(t *testing.T) (sess *session, clientEnc *json.Encoder, clientDec *json.Decoder, cleanup func()) {
	t.Helper()
	srvConn, cliConn := net.Pipe()
	srvEnc := json.NewEncoder(srvConn)
	srvDec := json.NewDecoder(srvConn)
	sess = newSession(srvEnc, srvDec, func() {})
	clientEnc = json.NewEncoder(cliConn)
	clientDec = json.NewDecoder(cliConn)
	cleanup = func() {
		_ = srvConn.Close()
		_ = cliConn.Close()
	}
	return sess, clientEnc, clientDec, cleanup
}

func TestSessionRequestElicitation(t *testing.T) {
	sess, cliEnc, cliDec, cleanup := pipeSession(t)
	defer cleanup()

	var (
		got *protocol.ElicitationAnswer
		err error
		wg  sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		got, err = sess.RequestElicitation(context.Background(), protocol.ElicitationRequest{
			Server:  "demo",
			Message: "say yes",
		})
	}()

	// Client side: read the elicitation envelope, send the answer.
	var envelope protocol.Response
	if decErr := cliDec.Decode(&envelope); decErr != nil {
		t.Fatalf("client decode: %v", decErr)
	}
	if envelope.Elicitation == nil || envelope.Elicitation.Message != "say yes" {
		t.Fatalf("missing elicitation envelope: %+v", envelope)
	}
	answer := protocol.Request{
		Action:            "elicitation_response",
		ElicitationAnswer: &protocol.ElicitationAnswer{Action: "accept", Content: map[string]any{"yes": true}},
	}
	if encErr := cliEnc.Encode(answer); encErr != nil {
		t.Fatalf("client encode: %v", encErr)
	}

	wg.Wait()
	if err != nil {
		t.Fatalf("RequestElicitation: %v", err)
	}
	if got == nil || got.Action != "accept" {
		t.Fatalf("unexpected answer: %+v", got)
	}
}

func TestSessionRejectsWrongAction(t *testing.T) {
	sess, cliEnc, cliDec, cleanup := pipeSession(t)
	defer cleanup()

	errCh := make(chan error, 1)
	go func() {
		_, err := sess.RequestElicitation(context.Background(), protocol.ElicitationRequest{Server: "demo", Message: "hi"})
		errCh <- err
	}()

	var envelope protocol.Response
	if err := cliDec.Decode(&envelope); err != nil {
		t.Fatalf("client decode: %v", err)
	}
	// Send a wrong action back.
	if err := cliEnc.Encode(protocol.Request{Action: "tools"}); err != nil {
		t.Fatalf("client encode: %v", err)
	}

	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "expected elicitation_response") {
			t.Fatalf("expected protocol error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RequestElicitation did not return")
	}
}

func TestSessionFinishedRefusesWrites(t *testing.T) {
	sess, _, _, cleanup := pipeSession(t)
	defer cleanup()
	sess.markFinished()
	_, err := sess.RequestElicitation(context.Background(), protocol.ElicitationRequest{})
	if err == nil || !errors.Is(err, errors.New("session already finished")) && !strings.Contains(err.Error(), "finished") {
		t.Fatalf("expected finished error, got %v", err)
	}
}
