// Package vconn provides a net.Listener fed by code instead of the kernel,
// and a net.Conn wrapper that reports chosen addresses. The daemon uses them
// to serve its TLS listener over connections handed to it by a relay.
package vconn

import (
	"context"
	"net"
	"sync"
)

// Addr is the address of a Listener or the local address of a Conn.
type Addr struct{ Name string }

// Network returns "relay".
func (a Addr) Network() string { return "relay" }

// String returns the name.
func (a Addr) String() string { return a.Name }

// Listener hands pushed connections to whoever calls Accept.
type Listener struct {
	ch   chan net.Conn
	done chan struct{}
	addr Addr

	mu     sync.Mutex // orders Close against a Push that just landed
	closed bool
}

// NewListener creates a Listener with a small queue.
func NewListener(name string) *Listener {
	return &Listener{ch: make(chan net.Conn, 16), done: make(chan struct{}), addr: Addr{Name: name}}
}

// Accept returns the next pushed connection, or net.ErrClosed after Close.
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

// Close stops the listener. Queued connections are closed. Idempotent.
func (l *Listener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	close(l.done)
	l.drainLocked()
	return nil
}

// drainLocked closes everything queued. Callers hold l.mu.
func (l *Listener) drainLocked() {
	for {
		select {
		case c := <-l.ch:
			c.Close()
		default:
			return
		}
	}
}

// Addr returns the listener's name.
func (l *Listener) Addr() net.Addr { return l.addr }

// Push queues c for Accept. It blocks while the queue is full and returns
// net.ErrClosed after Close or ctx.Err() when ctx ends. On error the caller
// still owns c. A push that lands just as the listener closes is treated
// like any queued connection: it is closed and Push returns nil.
func (l *Listener) Push(ctx context.Context, c net.Conn) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return net.ErrClosed
	}
	select {
	case l.ch <- c:
		l.mu.Unlock()
		return nil
	default:
	}
	l.mu.Unlock()
	// Queue full: wait without the lock so Close is never blocked.
	select {
	case l.ch <- c:
	case <-l.done:
		return net.ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	// The send raced Close: if Close already drained, drain again so c does
	// not sit in the queue for good (and leak whatever its close releases).
	l.mu.Lock()
	if l.closed {
		l.drainLocked()
	}
	l.mu.Unlock()
	return nil
}

// Conn wraps a net.Conn and overrides its addresses. It is what the daemon's
// HTTP server sees for a relayed phone, so RemoteAddr carries the phone's IP.
type Conn struct {
	net.Conn
	remote  net.Addr
	local   net.Addr
	onClose func()
	once    sync.Once
}

// Wrap returns a Conn reporting remote and local. onClose, if not nil, runs
// once after the first Close.
func Wrap(c net.Conn, remote, local net.Addr, onClose func()) *Conn {
	return &Conn{Conn: c, remote: remote, local: local, onClose: onClose}
}

// RemoteAddr returns the overridden remote address.
func (c *Conn) RemoteAddr() net.Addr { return c.remote }

// LocalAddr returns the overridden local address.
func (c *Conn) LocalAddr() net.Addr { return c.local }

// Close closes the wrapped connection and runs onClose once.
func (c *Conn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
	})
	return err
}
