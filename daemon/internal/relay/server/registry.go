package server

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// host is an authenticated daemon control connection.
type host struct {
	id      string
	ws      *websocket.Conn
	peer    string
	version string
	since   time.Time
	seen    *atomic.Int64 // unix nanos of the last frame or ping from the daemon

	sendq chan []byte   // drained by writer; the only goroutine that writes
	done  chan struct{} // closed when the control loop has exited

	closeOnce sync.Once

	mu        sync.Mutex
	streams   int // active data pipes
	dialFails int // consecutive unanswered dials
	bytesUp   atomic.Int64
	bytesDown atomic.Int64
}

// trySend queues a control message without blocking. false means the daemon
// is not keeping up and should be dropped.
func (h *host) trySend(b []byte) bool {
	select {
	case h.sendq <- b:
		return true
	default:
		return false
	}
}

// close starts the WebSocket close handshake once. It never blocks.
func (h *host) close(code websocket.StatusCode, reason string) {
	h.closeOnce.Do(func() {
		go func() {
			// Close writes the close frame and waits briefly for the peer's.
			_ = h.ws.Close(code, reason)
		}()
	})
}

func (h *host) writer(ctx context.Context) {
	for {
		select {
		case <-h.done:
			return
		case <-ctx.Done():
			return
		case b := <-h.sendq:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := h.ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				h.close(websocket.StatusGoingAway, "write failed")
				return
			}
		}
	}
}

func (h *host) acquireStream(max int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.streams >= max {
		return false
	}
	h.streams++
	return true
}

func (h *host) releaseStream() {
	h.mu.Lock()
	h.streams--
	h.mu.Unlock()
}

func (h *host) activeStreams() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.streams
}

// dialFailed records an unanswered dial and reports whether the host should
// be dropped as unresponsive.
func (h *host) dialFailed(limit int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dialFails++
	return h.dialFails >= limit
}

func (h *host) dialAnswered() {
	h.mu.Lock()
	h.dialFails = 0
	h.mu.Unlock()
}

// pending is a phone connection waiting for its daemon to dial.
type pending struct {
	token   string
	host    *host
	phone   net.Conn
	hello   []byte
	peer    string
	created time.Time
	timer   *time.Timer
}

// registry maps host ids to control connections and dial tokens to waiting
// phones. Its lock is held only for map operations, never around I/O.
type registry struct {
	mu      sync.Mutex
	hosts   map[string]*host
	pending map[string]*pending
	perHost map[*host]map[string]*pending
}

func newRegistry() *registry {
	return &registry{hosts: map[string]*host{}, pending: map[string]*pending{}, perHost: map[*host]map[string]*pending{}}
}

func (r *registry) get(id string) *host {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hosts[id]
}

// put registers an authenticated host and returns the one it replaced.
func (r *registry) put(h *host) (replaced *host) {
	r.mu.Lock()
	defer r.mu.Unlock()
	replaced = r.hosts[h.id]
	r.hosts[h.id] = h
	return replaced
}

// remove unregisters h if it is still the current connection for its id and
// returns its pending dials so the caller can close them.
func (r *registry) remove(h *host) (was bool, orphans []*pending) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hosts[h.id] == h {
		delete(r.hosts, h.id)
		was = true
	}
	for tok, p := range r.perHost[h] {
		delete(r.pending, tok)
		orphans = append(orphans, p)
	}
	delete(r.perHost, h)
	return was, orphans
}

// addPending registers p unless its host already has max dials waiting.
func (r *registry) addPending(p *pending, max int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.perHost[p.host]
	if len(m) >= max {
		return false
	}
	if m == nil {
		m = map[string]*pending{}
		r.perHost[p.host] = m
	}
	m[p.token] = p
	r.pending[p.token] = p
	return true
}

// claim removes and returns the pending dial for token, or nil. Exactly one
// caller wins between the data handler, the expiry timer, and a busy reply.
func (r *registry) claim(token string) *pending {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.pending[token]
	if p == nil {
		return nil
	}
	delete(r.pending, token)
	if m := r.perHost[p.host]; m != nil {
		delete(m, token)
		if len(m) == 0 {
			delete(r.perHost, p.host)
		}
	}
	return p
}

func (r *registry) snapshot() []*host {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*host, 0, len(r.hosts))
	for _, h := range r.hosts {
		out = append(out, h)
	}
	return out
}

// drainPending removes and returns every waiting phone.
func (r *registry) drainPending() []*pending {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*pending, 0, len(r.pending))
	for tok, p := range r.pending {
		out = append(out, p)
		delete(r.pending, tok)
	}
	r.perHost = map[*host]map[string]*pending{}
	return out
}

func (r *registry) counts() (hosts, pending int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.hosts), len(r.pending)
}
