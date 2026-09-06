package vconn

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestListenerPushAccept(t *testing.T) {
	l := NewListener("test")
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if err := l.Push(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	got, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if got != a {
		t.Fatal("wrong conn")
	}
	if l.Addr().String() != "test" || l.Addr().Network() != "relay" {
		t.Fatalf("addr %v", l.Addr())
	}
}

func TestListenerCloseUnblocksAccept(t *testing.T) {
	l := NewListener("test")
	errc := make(chan error, 1)
	go func() { _, err := l.Accept(); errc <- err }()
	time.Sleep(10 * time.Millisecond)
	l.Close()
	l.Close() // idempotent
	select {
	case err := <-errc:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Accept did not return")
	}
	a, b := net.Pipe()
	defer b.Close()
	if err := l.Push(context.Background(), a); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Push after close = %v", err)
	}
}

func TestListenerCloseDropsQueued(t *testing.T) {
	l := NewListener("test")
	a, b := net.Pipe()
	defer b.Close()
	l.Push(context.Background(), a)
	l.Close()
	if _, err := a.Write([]byte("x")); err == nil {
		t.Fatal("queued conn not closed")
	}
}

func TestListenerPushContext(t *testing.T) {
	l := NewListener("test")
	for i := 0; i < cap(l.ch); i++ {
		a, _ := net.Pipe()
		l.Push(context.Background(), a)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	a, _ := net.Pipe()
	if err := l.Push(ctx, a); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Push on full queue = %v", err)
	}
}

func TestConnAddrsAndOnClose(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	remote := &net.TCPAddr{IP: net.ParseIP("203.0.113.9")}
	n := 0
	c := Wrap(a, remote, Addr{"relay"}, func() { n++ })
	if c.RemoteAddr().String() != "203.0.113.9:0" {
		t.Fatalf("remote %v", c.RemoteAddr())
	}
	if c.LocalAddr().String() != "relay" {
		t.Fatalf("local %v", c.LocalAddr())
	}
	c.Close()
	c.Close()
	if n != 1 {
		t.Fatalf("onClose ran %d times", n)
	}
}

func TestListenerPushDuringCloseIsClosed(t *testing.T) {
	// A push that lands while Close drains must not leave a live conn in the
	// queue: Close's contract is that queued connections are closed.
	for i := 0; i < 200; i++ {
		l := NewListener("x")
		a, b := net.Pipe()
		done := make(chan error, 1)
		go func() { done <- l.Push(context.Background(), a) }()
		l.Close()
		err := <-done
		if err == nil {
			// Accepted: it must have been closed by the listener.
			b.SetReadDeadline(time.Now().Add(time.Second))
			if _, rerr := b.Read(make([]byte, 1)); rerr == nil {
				t.Fatal("queued conn survived Close")
			}
		}
		a.Close()
		b.Close()
	}
}
