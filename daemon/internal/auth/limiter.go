package auth

import (
	"sort"
	"sync"
	"time"
)

// Limiter state is bounded two ways: Fail sweeps entries whose window and
// lockout have both passed, at most once per gcInterval, and DefaultMaxKeys
// caps what a flood of distinct keys can hold between sweeps. Without both,
// a key that fails and never returns is never forgotten, and the daemon's
// limiter is reachable from the internet through a relay.
const (
	gcInterval = time.Minute

	// DefaultMaxKeys is the ceiling on tracked keys. It is far above any
	// real number of clients, so only a flood reaches it.
	DefaultMaxKeys = 8192
)

// Limiter locks out a key (usually a client IP) after repeated failures.
type Limiter struct {
	mu       sync.Mutex
	now      Clock
	max      int
	window   time.Duration
	lockout  time.Duration
	maxKeys  int
	lastGC   time.Time
	failures map[string][]time.Time
	locked   map[string]time.Time
}

// NewLimiter locks a key for lockout after max failures within window.
func NewLimiter(max int, window, lockout time.Duration, clock Clock) *Limiter {
	if clock == nil {
		clock = time.Now
	}
	return &Limiter{now: clock, max: max, window: window, lockout: lockout,
		maxKeys: DefaultMaxKeys, lastGC: clock(),
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
	locked := false
	if len(recent) >= l.max {
		l.locked[key] = now.Add(l.lockout)
		locked = true
	}
	if now.Sub(l.lastGC) >= gcInterval || len(l.failures) > l.maxKeys {
		l.gcLocked(now)
	}
	return locked
}

// Reset clears failures for a key after a successful authentication.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.failures, key)
	delete(l.locked, key)
	l.mu.Unlock()
}

// GC drops expired entries. Fail calls it on its own schedule; a caller with
// a sweeper of its own can call it to reclaim after a flood stops.
func (l *Limiter) GC() {
	l.mu.Lock()
	l.gcLocked(l.now())
	l.mu.Unlock()
}

// Len reports how many keys are tracked. For tests and metrics.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.failures)
}

// gcLocked forgets keys whose lockout has expired and whose failures have
// all aged out of the window, then enforces maxKeys. The caller holds mu.
func (l *Limiter) gcLocked(now time.Time) {
	l.lastGC = now
	for key, ts := range l.failures {
		if until, ok := l.locked[key]; ok {
			if now.Before(until) {
				continue
			}
			delete(l.locked, key)
		}
		if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= l.window {
			delete(l.failures, key)
		}
	}
	l.evictLocked()
}

// evictLocked drops the least recently active keys until the map is within
// maxKeys. It runs only when distinct keys arrive faster than they age out;
// dropping the oldest first keeps the freshest lockouts in place.
func (l *Limiter) evictLocked() {
	if l.maxKeys <= 0 || len(l.failures) <= l.maxKeys {
		return
	}
	type entry struct {
		key  string
		last time.Time
	}
	all := make([]entry, 0, len(l.failures))
	for key, ts := range l.failures {
		var last time.Time
		if n := len(ts); n > 0 {
			last = ts[n-1]
		}
		all = append(all, entry{key, last})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].last.Before(all[j].last) })
	for _, e := range all[:len(all)-l.maxKeys] {
		delete(l.failures, e.key)
		delete(l.locked, e.key)
	}
}
