package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/markusbug/Orchestrator/daemon/internal/protocol"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/vconn"
)

// serveEnv runs Server.Serve on a TCP listener and a virtual relay listener.
func serveEnv(t *testing.T, debug bool) (*env, net.Listener, *vconn.Listener) {
	t.Helper()
	e := newEnv(t, debug)
	s := &Server{Core: e.core}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	vln := vconn.NewListener("relay")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, tcp, vln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return")
		}
	})
	return e, tcp, vln
}

func insecureHTTP(dial func(ctx context.Context) (net.Conn, error)) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			c, err := dial(ctx)
			if err != nil {
				return nil, err
			}
			tc := tls.Client(c, &tls.Config{InsecureSkipVerify: true})
			if err := tc.HandshakeContext(ctx); err != nil {
				c.Close()
				return nil, err
			}
			return tc, nil
		},
	}}
}

func TestServeMultipleListeners(t *testing.T) {
	_, tcp, vln := serveEnv(t, false)
	// Over TCP.
	cl := insecureHTTP(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", tcp.Addr().String())
	})
	resp, err := cl.Get("https://daemon/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "ok") {
		t.Fatalf("tcp healthz %q", body)
	}
	// Over the virtual listener: push one end of a pipe, dial the other.
	cl = insecureHTTP(func(ctx context.Context) (net.Conn, error) {
		a, b := net.Pipe()
		if err := vln.Push(ctx, vconn.Wrap(b, &net.TCPAddr{IP: net.ParseIP("203.0.113.5")}, vconn.Addr{Name: "relay"}, nil)); err != nil {
			return nil, err
		}
		return a, nil
	})
	resp, err = cl.Get("https://daemon/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "ok") {
		t.Fatalf("relay healthz %q", body)
	}
}

// A debug daemon auto-authenticates loopback clients. A connection that
// arrives through the relay must never count as loopback, even when the
// relay reports 127.0.0.1 as the phone's address.
func TestRelayConnNeverAutoAuths(t *testing.T) {
	_, tcp, vln := serveEnv(t, true)
	hello := func(cl *http.Client) protocol.HelloReply {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ws, _, err := websocket.Dial(ctx, "wss://daemon/ws", &websocket.DialOptions{HTTPClient: cl})
		if err != nil {
			t.Fatal(err)
		}
		defer ws.CloseNow()
		b, _ := protocol.Marshal("hello", 1, protocol.Hello{Proto: 1, Name: "t", ClientNonce: strings.Repeat("QQ", 22)})
		if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
		_, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var r protocol.HelloReply
		json.Unmarshal(data, &r)
		return r
	}
	direct := insecureHTTP(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", tcp.Addr().String())
	})
	if r := hello(direct); r.AuthNeeded {
		t.Fatal("debug loopback over TCP should auto-auth")
	}
	viaRelay := insecureHTTP(func(ctx context.Context) (net.Conn, error) {
		a, b := net.Pipe()
		if err := vln.Push(ctx, vconn.Wrap(b, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}, vconn.Addr{Name: "relay"}, nil)); err != nil {
			return nil, err
		}
		return a, nil
	})
	if r := hello(viaRelay); !r.AuthNeeded {
		t.Fatal("relayed connection was auto-authenticated")
	}
}
