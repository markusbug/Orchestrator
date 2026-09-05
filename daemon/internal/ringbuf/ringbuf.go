// Package ringbuf implements a fixed-capacity byte ring buffer used to keep
// terminal scrollback for sessions.
package ringbuf

import "sync"

// Buffer keeps the most recent cap bytes written to it.
type Buffer struct {
	mu   sync.Mutex
	buf  []byte
	head int // index of the oldest byte when full
	size int // number of valid bytes
}

// New returns a buffer that retains at most capacity bytes.
func New(capacity int) *Buffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &Buffer{buf: make([]byte, capacity)}
}

// Cap returns the buffer capacity.
func (b *Buffer) Cap() int { return len(b.buf) }

// Len returns the number of bytes currently retained.
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.size
}

// Write appends p, discarding the oldest bytes if the buffer overflows.
// It never fails and always reports len(p).
func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	c := len(b.buf)
	if n >= c {
		copy(b.buf, p[n-c:])
		b.head = 0
		b.size = c
		return n, nil
	}
	tail := (b.head + b.size) % c
	first := copy(b.buf[tail:], p)
	if first < n {
		copy(b.buf, p[first:])
	}
	if b.size+n > c {
		b.head = (tail + n) % c
		b.size = c
	} else {
		b.size += n
	}
	return n, nil
}

// Snapshot returns a copy of the retained bytes in write order.
func (b *Buffer) Snapshot() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, b.size)
	c := len(b.buf)
	first := copy(out, b.buf[b.head:min(b.head+b.size, c)])
	if first < b.size {
		copy(out[first:], b.buf[:b.size-first])
	}
	return out
}

// Reset discards all retained bytes.
func (b *Buffer) Reset() {
	b.mu.Lock()
	b.head, b.size = 0, 0
	b.mu.Unlock()
}
