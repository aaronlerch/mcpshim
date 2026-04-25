package mcp

import (
	"errors"
	"testing"
	"time"

	"github.com/mcpshim/mcpshim/internal/config"
)

func TestBackoffDelay(t *testing.T) {
	schedule := []time.Duration{1, 2, 3}
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{-1, 1},
		{0, 1},
		{1, 2},
		{2, 3},
		{3, 3}, // capped
		{99, 3},
	}
	for _, tc := range cases {
		if got := backoffDelay(schedule, tc.attempt); got != tc.want {
			t.Errorf("backoffDelay(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
	if got := backoffDelay(nil, 0); got != 0 {
		t.Errorf("empty schedule should return 0, got %v", got)
	}
}

func TestServerStateLifecycle(t *testing.T) {
	st := newServerState()
	if status, _, _, _ := st.snapshot(); status != StatusUnknown {
		t.Fatalf("initial status = %q, want unknown", status)
	}

	idx := st.recordFailure(errors.New("first boom"), false)
	if idx != 0 {
		t.Errorf("first failure index = %d, want 0", idx)
	}
	status, lastErr, _, attempts := st.snapshot()
	if status != StatusDegraded || lastErr != "first boom" || attempts != 1 {
		t.Errorf("after first fail: status=%q err=%q attempts=%d", status, lastErr, attempts)
	}

	idx = st.recordFailure(errors.New("second boom"), false)
	if idx != 1 {
		t.Errorf("second failure index = %d, want 1", idx)
	}
	if status, _, _, attempts := st.snapshot(); status != StatusFailed || attempts != 2 {
		t.Errorf("after second fail: status=%q attempts=%d", status, attempts)
	}

	st.recordSuccess()
	status, lastErr, lastSuccess, attempts := st.snapshot()
	if status != StatusHealthy || lastErr != "" || attempts != 0 {
		t.Errorf("after success: status=%q err=%q attempts=%d", status, lastErr, attempts)
	}
	if lastSuccess.IsZero() {
		t.Error("lastSuccessAt should be set")
	}
}

func TestServerStateAuthRequired(t *testing.T) {
	st := newServerState()
	st.recordFailure(errors.New("oauth needed"), true)
	if status, _, _, _ := st.snapshot(); status != StatusAuthRequired {
		t.Errorf("status = %q, want auth_required", status)
	}
}

func TestServerStateScheduleAndCancelRetry(t *testing.T) {
	st := newServerState()
	ch := st.scheduleRetry(1 * time.Hour)
	select {
	case <-ch:
		t.Fatal("retry channel should not be closed yet")
	default:
	}
	// recordSuccess cancels pending retries.
	st.recordSuccess()
	select {
	case <-ch:
		// good
	default:
		t.Fatal("retry channel should be closed after recordSuccess")
	}
}

func TestServerStateScheduleSupersedesPriorRetry(t *testing.T) {
	st := newServerState()
	first := st.scheduleRetry(1 * time.Hour)
	second := st.scheduleRetry(1 * time.Hour)
	select {
	case <-first:
		// expected: first retry is canceled when superseded
	default:
		t.Fatal("scheduling a second retry should cancel the first")
	}
	select {
	case <-second:
		t.Fatal("second retry should not be canceled yet")
	default:
	}
}

func TestRegistryUpdateConfigPreservesAndCancels(t *testing.T) {
	cfg := &config.Config{Servers: []config.MCPServer{
		{Name: "keep", Transport: "http", URL: "http://k"},
		{Name: "drop", Transport: "http", URL: "http://d"},
	}}
	r := NewRegistryWithBackoff(cfg, nil, []time.Duration{0})

	keepState := r.stateFor("keep")
	dropState := r.stateFor("drop")
	keepRetry := keepState.scheduleRetry(1 * time.Hour)
	dropRetry := dropState.scheduleRetry(1 * time.Hour)

	newCfg := &config.Config{Servers: []config.MCPServer{
		{Name: "keep", Transport: "http", URL: "http://k"},
		{Name: "fresh", Transport: "http", URL: "http://f"},
	}}
	r.UpdateConfig(newCfg)

	// keep's state object should still be present (same identity).
	if got := r.stateFor("keep"); got != keepState {
		t.Error("kept server should preserve its serverState identity")
	}
	// keep's pending retry should still be live (we did not cancel it).
	select {
	case <-keepRetry:
		t.Error("keep retry should not be canceled by UpdateConfig")
	default:
	}
	// drop's pending retry must be canceled.
	select {
	case <-dropRetry:
		// expected
	default:
		t.Error("dropped server's retry should be canceled")
	}
	// fresh server should have a brand-new state.
	if r.stateFor("fresh") == nil {
		t.Error("new server should have a state slot")
	}
}
