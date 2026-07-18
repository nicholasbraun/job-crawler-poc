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
// cancelled first. A cancelled wait rolls its reservation back when it is still
// the latest one for key, so a stop doesn't leave the next caller waiting an
// extra interval. A nil limiter or a non-positive interval never blocks. The
// mutex is released before blocking (frontier convention), so a slow key never
// stalls callers waiting on a different key.
func (l *HostLimiter) Wait(ctx context.Context, key string) error {
	if l == nil || l.interval <= 0 {
		return nil
	}

	l.mu.Lock()
	now := time.Now()
	prev, hadPrev := l.next[key]
	at := now
	if hadPrev && prev.After(now) {
		at = prev
	}
	reserved := at.Add(l.interval)
	l.next[key] = reserved
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
		// The wait was cancelled (typically shutdown). Give this call's slot
		// back so the next caller for key isn't pushed an extra interval — but
		// only if no later caller has already reserved on top of ours, since
		// their slots chain off the schedule we advanced.
		l.mu.Lock()
		if scheduled, ok := l.next[key]; ok && scheduled.Equal(reserved) {
			if hadPrev {
				l.next[key] = prev
			} else {
				delete(l.next, key)
			}
		}
		l.mu.Unlock()
		return ctx.Err()
	}
}
