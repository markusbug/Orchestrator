package session

import (
	"context"
	"errors"
	"sync"
)

// ErrSubscriberClosed is returned by Next after the subscriber was detached.
var ErrSubscriberClosed = errors.New("session: subscriber closed")

// ErrSubscriberDropped is returned by Next when the subscriber fell too far behind.
var ErrSubscriberDropped = errors.New("session: subscriber dropped (too slow)")

// Subscriber receives coalesced PTY output for one attached client.
// The producer appends to a pending buffer; the consumer drains it with Next.
// If pending grows beyond max bytes the subscriber is dropped.
type Subscriber struct {
	mu      sync.Mutex
	pending []byte
	max     int
	notify  chan struct{}
	err     error
}

func newSubscriber(max int) *Subscriber {
	return &Subscriber{max: max, notify: make(chan struct{}, 1)}
}

// push appends p; returns false when the subscriber has been dropped.
func (s *Subscriber) push(p []byte) bool {
	s.mu.Lock()
	if s.err != nil {
		s.mu.Unlock()
		return false
	}
	if len(s.pending)+len(p) > s.max {
		s.err = ErrSubscriberDropped
		s.pending = nil
		s.mu.Unlock()
		s.signal()
		return false
	}
	s.pending = append(s.pending, p...)
	s.mu.Unlock()
	s.signal()
	return true
}

func (s *Subscriber) signal() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *Subscriber) close(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	s.signal()
}

// Next blocks until output is available and returns it, or returns the
// terminal error once the subscriber is closed or dropped.
func (s *Subscriber) Next(ctx context.Context) ([]byte, error) {
	for {
		s.mu.Lock()
		if len(s.pending) > 0 {
			out := s.pending
			s.pending = nil
			s.mu.Unlock()
			return out, nil
		}
		if s.err != nil {
			err := s.err
			s.mu.Unlock()
			return nil, err
		}
		s.mu.Unlock()
		select {
		case <-s.notify:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Err returns the terminal error, if any.
func (s *Subscriber) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
