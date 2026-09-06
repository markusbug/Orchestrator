package server

import (
	"sync"
	"syscall"
	"time"

	"github.com/markusbug/Orchestrator/daemon/internal/auth"
)

type bucket struct {
	tokens float64
	last   time.Time
}

// limits holds the relay's per-IP protections: a token bucket for new
// connections, a cap on concurrent ClientHello peeks, and the control-auth
// failure lockout.
type limits struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	peeking map[string]int
	rate    float64 // tokens per second
	burst   float64
	maxPeek int
	auth    *auth.Limiter
	now     func() time.Time
}

func newLimits(perMinute, maxPeek int, now func() time.Time) *limits {
	return &limits{
		buckets: map[string]*bucket{},
		peeking: map[string]int{},
		rate:    float64(perMinute) / 60,
		burst:   float64(perMinute),
		maxPeek: maxPeek,
		auth:    auth.NewLimiter(5, 10*time.Minute, 10*time.Minute, auth.Clock(now)),
		now:     now,
	}
}

// allowConn spends one token for ip.
func (l *limits) allowConn(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[ip]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[ip] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// enterPeek reserves a concurrent peek slot for ip. The release function
// must be called when the peek ends.
func (l *limits) enterPeek(ip string) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.peeking[ip] >= l.maxPeek {
		return nil, false
	}
	l.peeking[ip]++
	return func() {
		l.mu.Lock()
		if l.peeking[ip] <= 1 {
			delete(l.peeking, ip)
		} else {
			l.peeking[ip]--
		}
		l.mu.Unlock()
	}, true
}

// gc drops buckets that have refilled completely and expired auth lockouts.
func (l *limits) gc() {
	l.auth.GC()
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for ip, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, ip)
		}
	}
}

// fdLimit returns 90% of the soft RLIMIT_NOFILE minus headroom, or fallback.
func fdLimit(fallback int) int {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil || rl.Cur == 0 {
		return fallback
	}
	n := int(rl.Cur)
	if n > 1<<24 {
		n = 1 << 24
	}
	n = n*9/10 - 64
	if n < 16 {
		return 16
	}
	return n
}
