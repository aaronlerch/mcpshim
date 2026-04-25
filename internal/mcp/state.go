package mcp

import (
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
)

// Status describes a server's last-known connection health.
type Status string

const (
	StatusUnknown      Status = "unknown"
	StatusHealthy      Status = "healthy"
	StatusDegraded     Status = "degraded"
	StatusFailed       Status = "failed"
	StatusAuthRequired Status = "auth_required"
)

// DefaultBackoff is the schedule of retry delays after consecutive failures.
// The last entry is reused indefinitely (cap at 5 minutes).
var DefaultBackoff = []time.Duration{
	5 * time.Second,
	15 * time.Second,
	30 * time.Second,
	60 * time.Second,
	120 * time.Second,
	300 * time.Second,
}

// serverState carries per-server health metadata. All access is guarded by
// the parent Registry's mu; the embedded mu serializes refresh attempts for
// this specific server so concurrent callers don't stampede the upstream.
type serverState struct {
	mu             sync.Mutex
	status         Status
	lastError      string
	lastSuccessAt  time.Time
	lastAttemptAt  time.Time
	attemptCount   int
	retryCancel    chan struct{} // closed to cancel a pending retry goroutine
	pendingRetryAt time.Time
}

func newServerState() *serverState {
	return &serverState{status: StatusUnknown}
}

// snapshot returns a copy of the public fields safe for read after release.
func (s *serverState) snapshot() (status Status, lastError string, lastSuccessAt time.Time, attemptCount int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, s.lastError, s.lastSuccessAt, s.attemptCount
}

// recordSuccess marks the server healthy and resets the retry counter. Any
// pending backoff retry goroutine is canceled.
func (s *serverState) recordSuccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = StatusHealthy
	s.lastError = ""
	s.lastSuccessAt = time.Now().UTC()
	s.lastAttemptAt = s.lastSuccessAt
	s.attemptCount = 0
	s.cancelRetryLocked()
}

// recordFailure increments the attempt counter and records the error. Returns
// the next backoff index to use when scheduling a retry.
func (s *serverState) recordFailure(err error, authRequired bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if authRequired {
		s.status = StatusAuthRequired
	} else if s.attemptCount == 0 {
		s.status = StatusDegraded
	} else {
		s.status = StatusFailed
	}
	if err != nil {
		s.lastError = err.Error()
	}
	s.lastAttemptAt = time.Now().UTC()
	s.attemptCount++
	return s.attemptCount - 1
}

// scheduleRetry replaces any pending retry with a new cancel channel. The
// caller is responsible for spawning the goroutine that listens to it.
func (s *serverState) scheduleRetry(delay time.Duration) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelRetryLocked()
	ch := make(chan struct{})
	s.retryCancel = ch
	s.pendingRetryAt = time.Now().UTC().Add(delay)
	return ch
}

// cancelRetryLocked closes the retry channel if one is pending. Caller holds mu.
func (s *serverState) cancelRetryLocked() {
	if s.retryCancel != nil {
		close(s.retryCancel)
		s.retryCancel = nil
		s.pendingRetryAt = time.Time{}
	}
}

func (s *serverState) cancelRetry() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelRetryLocked()
}

// backoffDelay returns the duration for the given attempt index, capping at
// the last schedule entry. attempt is the zero-based failure number.
func backoffDelay(schedule []time.Duration, attempt int) time.Duration {
	if len(schedule) == 0 {
		return 0
	}
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(schedule) {
		attempt = len(schedule) - 1
	}
	return schedule[attempt]
}

// isAuthRequiredError detects mcp-go's OAuth-authorization-required error
// without forcing callers to import the client package directly.
func isAuthRequiredError(err error) bool {
	return mcpclient.IsOAuthAuthorizationRequiredError(err)
}
