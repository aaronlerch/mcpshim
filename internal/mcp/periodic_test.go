package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mcpshim/mcpshim/internal/config"
)

func lastAttempt(st *serverState) time.Time {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.lastAttemptAt
}

func TestSkipPeriodic(t *testing.T) {
	now := time.Now().UTC()

	healthy := newServerState()
	healthy.recordSuccess()
	if skip, _ := healthy.skipPeriodic(now); skip {
		t.Error("a healthy server must not be skipped")
	}

	authed := newServerState()
	authed.recordFailure(errors.New("oauth needed"), true)
	skip, reason := authed.skipPeriodic(now)
	if !skip || reason != "auth_required" {
		t.Errorf("auth_required: skip=%v reason=%q, want true/auth_required", skip, reason)
	}

	backing := newServerState()
	backing.recordFailure(errors.New("boom"), false)
	backing.scheduleRetry(1 * time.Hour)
	skip, reason = backing.skipPeriodic(now)
	if !skip || reason != "backoff retry pending" {
		t.Errorf("pending retry: skip=%v reason=%q, want true/'backoff retry pending'", skip, reason)
	}

	// A retry whose deadline has already passed no longer defers the ticker --
	// otherwise a dropped retry goroutine would park the server forever.
	stale := newServerState()
	stale.recordFailure(errors.New("boom"), false)
	stale.scheduleRetry(-1 * time.Hour)
	if skip, _ := stale.skipPeriodic(now); skip {
		t.Error("an elapsed retry deadline must not skip the periodic refresh")
	}
}

// The regression this change exists for: a server stuck in auth_required was
// re-attempted by the daemon ticker every 2 minutes indefinitely, because the
// "don't auto-retry when the user must log in" guard only suppressed the
// backoff path while the ticker never consulted status at all.
func TestRefreshPeriodicSkipsAuthRequired(t *testing.T) {
	cfg := &config.Config{Servers: []config.MCPServer{
		{Name: "stuck", Transport: "http", URL: "http://127.0.0.1:1"},
		{Name: "live", Transport: "http", URL: "http://127.0.0.1:1"},
	}}
	r := NewRegistryWithBackoff(cfg, nil, []time.Duration{0})

	stuck := r.stateFor("stuck")
	stuck.recordFailure(errors.New("oauth needed"), true)
	before := lastAttempt(stuck)
	liveBefore := lastAttempt(r.stateFor("live"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = r.RefreshPeriodic(ctx)

	if got := lastAttempt(stuck); !got.Equal(before) {
		t.Errorf("auth_required server was attempted: lastAttemptAt moved %v -> %v", before, got)
	}
	if got := lastAttempt(r.stateFor("live")); got.Equal(liveBefore) {
		t.Error("a non-blocked server should still be attempted by the periodic refresh")
	}
}

// Refresh stays unconditional: the explicit refresh/reload actions and the one
// attempt at daemon startup must still probe a server the ticker has parked,
// or there would be no way back to healthy short of restarting the daemon.
func TestRefreshStillAttemptsAuthRequired(t *testing.T) {
	cfg := &config.Config{Servers: []config.MCPServer{
		{Name: "stuck", Transport: "http", URL: "http://127.0.0.1:1"},
	}}
	r := NewRegistryWithBackoff(cfg, nil, []time.Duration{0})

	stuck := r.stateFor("stuck")
	stuck.recordFailure(errors.New("oauth needed"), true)
	before := lastAttempt(stuck)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = r.Refresh(ctx)

	if got := lastAttempt(stuck); got.Equal(before) {
		t.Error("explicit Refresh must attempt an auth_required server")
	}
}

func TestHealthCountsAndNames(t *testing.T) {
	cfg := &config.Config{Servers: []config.MCPServer{
		{Name: "ok", Transport: "http", URL: "http://127.0.0.1:1"},
		{Name: "needs-login", Transport: "http", URL: "http://127.0.0.1:1"},
		{Name: "never-tried", Transport: "http", URL: "http://127.0.0.1:1"},
	}}
	r := NewRegistryWithBackoff(cfg, nil, []time.Duration{0})
	r.stateFor("ok").recordSuccess()
	r.stateFor("needs-login").recordFailure(errors.New("oauth needed"), true)

	counts, authRequired := r.Health()
	if counts["healthy"] != 1 || counts["auth_required"] != 1 || counts["unknown"] != 1 {
		t.Errorf("counts = %v, want healthy=1 auth_required=1 unknown=1", counts)
	}
	if len(authRequired) != 1 || authRequired[0] != "needs-login" {
		t.Errorf("authRequired = %v, want [needs-login]", authRequired)
	}
}
