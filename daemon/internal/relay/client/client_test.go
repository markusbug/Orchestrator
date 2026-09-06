package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/markusbug/Orchestrator/daemon/internal/relay/server"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/vconn"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/wire"
)

const domain = "relay.test"

type relay struct {
	srv    *server.Server
	addr   string
	pool   *x509.CertPool
	cancel context.CancelFunc
	done   chan struct{}
}

func startRelay(t *testing.T, mod func(*server.Config)) *relay {
	t.Helper()
	cfg := server.Config{
		Domain: domain, Dev: true, Version: "test",
		Log:         slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		DialTimeout: 2 * time.Second, DrainTimeout: 2 * time.Second, ConnsPerMinutePerIP: 10000,
	}
	if mod != nil {
		mod(&cfg)
	}
	srv, err := server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &relay{srv: srv, addr: ln.Addr().String(), pool: x509.NewCertPool(), cancel: cancel, done: make(chan struct{})}
	r.pool.AddCert(srv.ApexCert())
	go func() { srv.Serve(ctx, ln); close(r.done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			t.Error("relay did not stop")
		}
	})
	return r
}

// tlsFor dials the relay's TCP port for any host name (no DNS in tests).
func (r *relay) tlsFor() *tls.Config {
	return &tls.Config{RootCAs: r.pool, ServerName: domain}
}

func newClient(t *testing.T, r *relay, key ed25519.PrivateKey, ln *vconn.Listener, mod func(*Options)) (*Client, context.CancelFunc) {
	t.Helper()
	o := Options{
		URL: "https://" + r.addr, HostKey: key, Version: "t", Listener: ln, TLS: r.tlsFor(),
		Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
	if mod != nil {
		mod(&o)
	}
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	// The relay listens on 127.0.0.1:<port>; the URL host is that address, so
	// the client's domain would be "127.0.0.1". Override for the test so the
	// challenge domain check matches the relay's configured apex.
	c.domain = domain
	ctx, cancel := context.WithCancel(context.Background())
	go c.Run(ctx)
	t.Cleanup(cancel)
	return c, cancel
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func genKey(t *testing.T) ed25519.PrivateKey {
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// phone connects to the relay with SNI for the client's host and returns the
// raw TCP conn after the relay has the ClientHello.
func phone(t *testing.T, r *relay, hostID string) (net.Conn, []byte) {
	t.Helper()
	c, err := net.DialTimeout("tcp", r.addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	hello := clientHello(t, wire.Addr(hostID, domain))
	if _, err := c.Write(hello); err != nil {
		t.Fatal(err)
	}
	return c, hello
}

// clientHello captures the bytes a TLS client sends first.
func clientHello(t *testing.T, sni string) []byte {
	a, b := net.Pipe()
	go tls.Client(a, &tls.Config{ServerName: sni, InsecureSkipVerify: true}).Handshake()
	b.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64<<10)
	n, err := b.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	b.Close()
	return buf[:n]
}

func TestConnectAndRelayStream(t *testing.T) {
	r := startRelay(t, nil)
	key := genKey(t)
	ln := vconn.NewListener("relay")
	c, _ := newClient(t, r, key, ln, nil)
	waitFor(t, "online", func() bool { return r.srv.Online(c.HostID()) })
	st := c.Status()
	if !st.Connected || st.HostID != c.HostID() || st.Addr != c.HostID()+"."+domain || st.PingInterval != 60 {
		t.Fatalf("status %+v", st)
	}
	// Phone connects; the daemon side receives a conn on the listener whose
	// RemoteAddr is the phone's IP and whose first bytes are the hello.
	pc, hello := phone(t, r, c.HostID())
	accepted := make(chan net.Conn, 1)
	go func() {
		dc, err := ln.Accept()
		if err != nil {
			t.Error(err)
			return
		}
		accepted <- dc
	}()
	var dc net.Conn
	select {
	case dc = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("listener got no connection")
	}
	if host, _, _ := net.SplitHostPort(dc.RemoteAddr().String()); host != "127.0.0.1" {
		t.Fatalf("remote addr %v", dc.RemoteAddr())
	}
	dc.SetDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(hello))
	if _, err := io.ReadFull(dc, got); err != nil || string(got) != string(hello) {
		t.Fatalf("hello replay: %v", err)
	}
	// Echo through in both directions; close when the phone hangs up like
	// the daemon's HTTP server would.
	go func() { io.Copy(dc, dc); dc.Close() }()
	msg := []byte(strings.Repeat("ping ", 20000))
	go pc.Write(msg)
	pc.SetDeadline(time.Now().Add(5 * time.Second))
	back := make([]byte, len(msg))
	if _, err := io.ReadFull(pc, back); err != nil || string(back) != string(msg) {
		t.Fatalf("echo: %v", err)
	}
	waitFor(t, "stream count", func() bool { return c.Status().Streams == 1 })
	pc.Close()
	waitFor(t, "stream release", func() bool { return c.Status().Streams == 0 })
}

func TestBusyWhenAtCap(t *testing.T) {
	r := startRelay(t, nil)
	key := genKey(t)
	ln := vconn.NewListener("relay")
	c, _ := newClient(t, r, key, ln, func(o *Options) { o.MaxStreams = 1 })
	waitFor(t, "online", func() bool { return r.srv.Online(c.HostID()) })
	p1, _ := phone(t, r, c.HostID())
	dc, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()
	_ = p1
	p2, _ := phone(t, r, c.HostID())
	p2.SetReadDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	if _, err := p2.Read(make([]byte, 1)); err == nil {
		t.Fatal("second phone not refused")
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Fatal("busy did not short-circuit")
	}
}

func TestReconnectAfterRelayRestart(t *testing.T) {
	r := startRelay(t, nil)
	key := genKey(t)
	ln := vconn.NewListener("relay")
	c, _ := newClient(t, r, key, ln, nil)
	waitFor(t, "online", func() bool { return r.srv.Online(c.HostID()) })
	r.cancel()
	<-r.done
	waitFor(t, "disconnected", func() bool { return !c.Status().Connected })
	if !strings.Contains(c.Status().LastError, "1012") && c.Status().LastError == "" {
		t.Fatalf("last error %q", c.Status().LastError)
	}
}

func TestRejectsWrongRelayDomainAndPlainHTTP(t *testing.T) {
	ln := vconn.NewListener("relay")
	if _, err := New(Options{URL: "http://relay.test", HostKey: genKey(t), Listener: ln}); err == nil {
		t.Fatal("plain http accepted")
	}
	if _, err := New(Options{URL: "http://relay.test", HostKey: genKey(t), Listener: ln, Insecure: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{URL: "ftp://x", HostKey: genKey(t), Listener: ln}); err == nil {
		t.Fatal("ftp accepted")
	}
	if _, err := New(Options{URL: "https://x", Listener: ln}); err == nil {
		t.Fatal("missing key accepted")
	}
	c, err := New(Options{URL: "https://relay.example:8443", HostKey: genKey(t), Listener: ln})
	if err != nil {
		t.Fatal(err)
	}
	if c.Port() != 8443 || !strings.HasSuffix(c.Addr(), ".relay.example") {
		t.Fatalf("addr %s port %d", c.Addr(), c.Port())
	}
	// A relay that challenges for a different domain is refused.
	r := startRelay(t, func(cfg *server.Config) { cfg.Domain = "other.test" })
	r.pool = x509.NewCertPool()
	r.pool.AddCert(r.srv.ApexCert())
	o := Options{URL: "https://" + r.addr, HostKey: genKey(t), Listener: ln, TLS: &tls.Config{RootCAs: r.pool, ServerName: "other.test"},
		Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))}
	c, _ = New(o)
	c.domain = domain
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	connected, code, err := c.runOnce(ctx)
	if connected || code != wire.CloseBadRequest || err == nil || !strings.Contains(err.Error(), "other.test") {
		t.Fatalf("connected=%v code=%d err=%v", connected, code, err)
	}
}

func TestBackoff(t *testing.T) {
	r := newRand()
	for i := 0; i < 20; i++ {
		d := backoff(i, 0, r)
		if d < 750*time.Millisecond || d > 90*time.Second {
			t.Fatalf("attempt %d: %v", i, d)
		}
	}
	if d := backoff(0, websocket.StatusServiceRestart, r); d < time.Second || d > 30*time.Second {
		t.Fatalf("restart %v", d)
	}
	if d := backoff(0, wire.CloseReplaced, r); d < 30*time.Second || d > time.Minute {
		t.Fatalf("replaced %v", d)
	}
	if d := backoff(0, wire.CloseUnauthorized, r); d != 5*time.Minute {
		t.Fatalf("unauthorized %v", d)
	}
}

func TestHonoursRelayStreamCap(t *testing.T) {
	// The relay allows one stream per host; the daemon's own cap is higher.
	// The second phone must get a fast busy, not a 429 on the data socket.
	r := startRelay(t, func(cfg *server.Config) { cfg.MaxStreamsPerHost = 1 })
	key := genKey(t)
	ln := vconn.NewListener("relay")
	c, _ := newClient(t, r, key, ln, func(o *Options) { o.MaxStreams = 16 })
	waitFor(t, "online", func() bool { return r.srv.Online(c.HostID()) })
	if c.streamLimit() != 1 {
		t.Fatalf("limit %d, want the relay's 1", c.streamLimit())
	}
	p1, _ := phone(t, r, c.HostID())
	dc, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()
	_ = p1
	p2, _ := phone(t, r, c.HostID())
	p2.SetReadDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	if _, err := p2.Read(make([]byte, 1)); err == nil {
		t.Fatal("second phone not refused")
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Fatal("busy did not short-circuit")
	}
	if st := c.Status(); st.Streams != 1 {
		t.Fatalf("streams %d", st.Streams)
	}
}

func TestStreamCountAfterFailedDataDial(t *testing.T) {
	// A dial whose data socket the relay rejects must release its slot and
	// never drive the count negative.
	r := startRelay(t, nil)
	key := genKey(t)
	ln := vconn.NewListener("relay")
	c, _ := newClient(t, r, key, ln, nil)
	waitFor(t, "online", func() bool { return r.srv.Online(c.HostID()) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tok, _ := wire.NewToken()
	c.answerDial(ctx, nil, wire.Dial{T: wire.TDial, Token: tok, Peer: "127.0.0.1"})
	waitFor(t, "release", func() bool { return c.Status().Streams == 0 })
	time.Sleep(50 * time.Millisecond)
	if st := c.Status(); st.Streams != 0 {
		t.Fatalf("streams %d after failed dial", st.Streams)
	}
}
