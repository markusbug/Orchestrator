package auth

import (
	"sync"
	"time"
)

// Limiter locks out a key (usually a client IP) after repeated failures.
type Limiter struct {
	mu       sync.Mutex
	now      Clock
	max      int
	window   time.Duration
	lockout  time.Duration
	failures map[string][]time.Time
	locked   map[string]time.Time
}

// NewLimiter locks a key for lockout after max failures within window.
func NewLimiter(max int, window, lockout time.Duration, clock Clock) *Limiter {
	if clock == nil {
		clock = time.Now
	}
	return &Limiter{now: clock, max: max, window: window, lockout: lockout,
		failures: map[string][]time.Time{}, locked: map[string]time.Time{}}
}

// Allow reports whether key may attempt authentication now.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until, ok := l.locked[key]; ok {
		if l.now().Before(until) {
			return false
		}
		delete(l.locked, key)
		delete(l.failures, key)
	}
	return true
}

// Fail records a failure; returns true if the key is now locked.
func (l *Limiter) Fail(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var recent []time.Time
	for _, t := range l.failures[key] {
		if now.Sub(t) < l.window {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	l.failures[key] = recent
	if len(recent) >= l.max {
		l.locked[key] = now.Add(l.lockout)
		return true
	}
	return false
}

// Reset clears failures for a key after a successful authentication.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.failures, key)
	delete(l.locked, key)
	l.mu.Unlock()
}
