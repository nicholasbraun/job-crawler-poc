package atsingest

import (
	"context"
	"sync"
	"time"
)

// HostLimiter paces board-API calls per key so the crawler stays a polite client
// of each ATS provider's board API (ADR-0022). Calls are keyed by provider — one
// provider is one board-API host family, so every tenant of a provider shares a
// single cadence. Reservations are handed out under the lock and waited on
// outside it, so many workers pace deterministically without a thundering herd.
type HostLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     map[string]time.Time
}

// NewHostLimiter returns a limiter that spaces successive Wait calls for the same
// key by interval. An interval <= 0 makes every Wait return immediately.
func NewHostLimiter(interval time.Duration) *HostLimiter {
	return &HostLimiter{
		interval: interval,
		next:     map[string]time.Time{},
	}
}

// Wait blocks until this call's reserved slot for key — spaced interval after the
// previous reservation for the same key — or returns ctx.Err() if ctx is
// cancelled first. A nil limiter or a non-positive interval never blocks. The
// mutex is released before blocking (frontier convention), so a slow key never
// stalls callers waiting on a different key.
func (l *HostLimiter) Wait(ctx context.Context, key string) error {
	if l == nil || l.interval <= 0 {
		return nil
	}

	l.mu.Lock()
	now := time.Now()
	at := now
	if scheduled, ok := l.next[key]; ok && scheduled.After(now) {
		at = scheduled
	}
	l.next[key] = at.Add(l.interval)
	l.mu.Unlock()

	delay := time.Until(at)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
