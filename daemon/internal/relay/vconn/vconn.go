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
	once sync.Once
	addr Addr
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
	l.once.Do(func() {
		close(l.done)
		for {
			select {
			case c := <-l.ch:
				c.Close()
			default:
				return
			}
		}
	})
	return nil
}

// Addr returns the listener's name.
func (l *Listener) Addr() net.Addr { return l.addr }

// Push queues c for Accept. It blocks while the queue is full and returns
// net.ErrClosed after Close or ctx.Err() when ctx ends. On error the caller
// still owns c.
func (l *Listener) Push(ctx context.Context, c net.Conn) error {
	select {
	case <-l.done:
		return net.ErrClosed
	default:
	}
	select {
	case l.ch <- c:
		return nil
	case <-l.done:
		return net.ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
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

// NetConn returns the wrapped connection.
func (c *Conn) NetConn() net.Conn { return c.Conn }

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
