package vconn

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// wsPair returns a client-side FromWebSocket conn and the server-side raw
// WebSocket that echoes everything.
func wsPair(t *testing.T) (net.Conn, func()) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		nc := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
		io.Copy(nc, nc)
		nc.Close()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := FromWebSocket(ctx, ws)
	return c, func() { cancel(); ts.Close() }
}

func TestFromWebSocketEchoAndDeadlines(t *testing.T) {
	c, stop := wsPair(t)
	defer stop()
	msg := []byte(strings.Repeat("x", 100000))
	go c.Write(msg)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	// A deadline in the past aborts a pending read without breaking the conn
	// (this is what net/http does on Hijack).
	errc := make(chan error, 1)
	go func() { _, err := c.Read(make([]byte, 1)); errc <- err }()
	time.Sleep(20 * time.Millisecond)
	c.SetReadDeadline(time.Unix(1, 0))
	select {
	case err := <-errc:
		var ne net.Error
		if err == nil || !errorsAs(err, &ne) || !ne.Timeout() {
			t.Fatalf("expected timeout, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending read not aborted")
	}
	c.SetReadDeadline(time.Time{})
	if _, err := c.Write([]byte("again")); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got = make([]byte, 5)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "again" {
		t.Fatalf("conn broken after aborted read: %v %q", err, got)
	}
	c.Close()
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("read after close succeeded")
	}
}

func TestFromWebSocketRemoteClose(t *testing.T) {
	c, stop := wsPair(t)
	defer stop()
	stop() // server gone
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected EOF or error after remote close")
	}
}
