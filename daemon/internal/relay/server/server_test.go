package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/markusbug/Orchestrator/daemon/internal/relay/wire"
)

const testDomain = "relay.test"

type testRelay struct {
	t      *testing.T
	srv    *Server
	addr   string // host:port of the TCP listener
	cancel context.CancelFunc
	done   chan struct{} // closed when Serve returned
	err    error
	pool   *x509.CertPool
	clock  *atomic.Int64
}

func startRelay(t *testing.T, mod func(*Config)) *testRelay {
	t.Helper()
	clock := new(atomic.Int64)
	clock.Store(time.Now().UnixNano())
	cfg := Config{
		Domain:  testDomain,
		Dev:     true,
		Version: "test",
		Log:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Now:     func() time.Time { return time.Unix(0, clock.Load()) },
		// Fast timers for tests.
		PeekTimeout: 2 * time.Second, DialTimeout: 500 * time.Millisecond, SweepInterval: 50 * time.Millisecond,
		HostIdle: time.Hour, DrainTimeout: 2 * time.Second, IdleTimeout: 10 * time.Second,
		ConnsPerMinutePerIP: 10000,
	}
	if mod != nil {
		mod(&cfg)
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	pool := x509.NewCertPool()
	pool.AddCert(srv.ApexCert())
	r := &testRelay{t: t, srv: srv, addr: ln.Addr().String(), cancel: cancel, done: done, pool: pool, clock: clock}
	go func() { r.err = srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("relay did not stop")
		}
	})
	return r
}

// apexClient returns an HTTP client that trusts the dev apex cert and dials
// the test listener regardless of the URL host.
func (r *testRelay) apexClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: r.pool, ServerName: testDomain},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", r.addr)
		},
	}}
}

func (r *testRelay) url(path string) string { return "https://" + testDomain + path }

// mockDaemon speaks the daemon side of the control protocol.
type mockDaemon struct {
	t      *testing.T
	r      *testRelay
	key    ed25519.PrivateKey
	id     string
	ws     *websocket.Conn
	ok     wire.OK
	dials  chan wire.Dial
	closed chan websocket.StatusCode
	// onDial is invoked for each dial; default answers with a data socket
	// that echoes bytes back.
	onDial func(d wire.Dial)
}

func newKey(t *testing.T) (ed25519.PrivateKey, string) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv, wire.HostID(pub)
}

// connectDaemon opens a control socket and authenticates (unless badSig).
func connectDaemon(t *testing.T, r *testRelay, key ed25519.PrivateKey, id string, badSig bool) (*mockDaemon, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "wss://"+testDomain+wire.ControlPath+id, &websocket.DialOptions{HTTPClient: r.apexClient()})
	if err != nil {
		return nil, err
	}
	_, data, err := ws.Read(ctx)
	if err != nil {
		ws.CloseNow()
		return nil, err
	}
	var ch wire.Challenge
	json.Unmarshal(data, &ch)
	if ch.T != wire.TChallenge || ch.Relay != testDomain {
		t.Fatalf("challenge %s", data)
	}
	nonce, _ := decodeB64(ch.Nonce)
	msg := wire.ChallengeBytes(nonce, id, ch.Relay)
	if badSig {
		msg = append(msg, 'x')
	}
	sig := ed25519.Sign(key, msg)
	a := wire.Auth{T: wire.TAuth, PubKey: b64(key.Public().(ed25519.PublicKey)), Sig: b64(sig), Version: "t"}
	if err := ws.Write(ctx, websocket.MessageText, wire.Marshal(a)); err != nil {
		return nil, err
	}
	_, data, err = ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	typ, _ := wire.Type(data)
	if typ != wire.TOK {
		ws.CloseNow()
		return nil, &authError{string(data)}
	}
	d := &mockDaemon{t: t, r: r, key: key, id: id, ws: ws, dials: make(chan wire.Dial, 16), closed: make(chan websocket.StatusCode, 1)}
	json.Unmarshal(data, &d.ok)
	d.onDial = d.echoDial
	go d.loop()
	t.Cleanup(func() { ws.CloseNow() })
	return d, nil
}

type authError struct{ msg string }

func (e *authError) Error() string { return "auth failed: " + e.msg }

func decodeB64(s string) ([]byte, error) {
	return io.ReadAll(base64Reader(s))
}

func (d *mockDaemon) loop() {
	for {
		_, data, err := d.ws.Read(context.Background())
		if err != nil {
			d.closed <- websocket.CloseStatus(err)
			return
		}
		typ, _ := wire.Type(data)
		if typ == wire.TDial {
			var dl wire.Dial
			json.Unmarshal(data, &dl)
			d.dials <- dl
			if d.onDial != nil {
				go d.onDial(dl)
			}
		}
	}
}

// openData dials the data socket for a token and returns it as a net.Conn.
func (d *mockDaemon) openData(token string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "wss://"+testDomain+wire.DataPath+token, &websocket.DialOptions{HTTPClient: d.r.apexClient()})
	if err != nil {
		return nil, err
	}
	return websocket.NetConn(context.Background(), ws, websocket.MessageBinary), nil
}

// echoDial answers a dial with a data socket that echoes everything.
func (d *mockDaemon) echoDial(dl wire.Dial) {
	c, err := d.openData(dl.Token)
	if err != nil {
		return
	}
	go func() {
		defer c.Close()
		io.Copy(c, c)
	}()
}

func (d *mockDaemon) busy(token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	d.ws.Write(ctx, websocket.MessageText, wire.Marshal(wire.Busy{T: wire.TBusy, Token: token}))
}

func (d *mockDaemon) waitClose(t *testing.T) websocket.StatusCode {
	select {
	case c := <-d.closed:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("control socket not closed")
		return 0
	}
}

// phoneDial connects like a phone: raw TCP to the relay with SNI for the host.
// It returns the raw conn after writing the ClientHello via a TLS client that
// runs in the background; the relay forwards those bytes to the daemon.
func phoneConn(t *testing.T, r *testRelay) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", r.addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// rawHello returns a TLS ClientHello for sni captured from a real client.
func rawHello(t *testing.T, sni string) []byte {
	t.Helper()
	a, b := net.Pipe()
	go func() { tls.Client(a, &tls.Config{ServerName: sni, InsecureSkipVerify: true}).Handshake() }()
	b.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, rec, err := peekClientHello(b, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	b.Close()
	return rec
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

func TestHealthAndPresence(t *testing.T) {
	r := startRelay(t, nil)
	resp, err := r.apexClient().Get(r.url(wire.HealthPath))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"ok":true`) || !strings.Contains(string(body), `"version":"test"`) {
		t.Fatalf("healthz %d %s", resp.StatusCode, body)
	}
	key, id := newKey(t)
	resp, _ = r.apexClient().Get(r.url(wire.HostsPath + id))
	body, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"online":false`) {
		t.Fatalf("presence %s", body)
	}
	if _, err := connectDaemon(t, r, key, id, false); err != nil {
		t.Fatal(err)
	}
	resp, _ = r.apexClient().Get(r.url(wire.HostsPath + id))
	body, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"online":true`) {
		t.Fatalf("presence %s", body)
	}
	resp, _ = r.apexClient().Get(r.url(wire.HostsPath + "nope"))
	if resp.StatusCode != 400 {
		t.Fatalf("bad id status %d", resp.StatusCode)
	}
}

func TestAuthFailuresAndLockout(t *testing.T) {
	r := startRelay(t, nil)
	key, id := newKey(t)
	for i := 0; i < 5; i++ {
		if _, err := connectDaemon(t, r, key, id, true); err == nil {
			t.Fatal("bad signature accepted")
		}
	}
	if r.srv.Online(id) {
		t.Fatal("registered without auth")
	}
	// Sixth attempt (even a good one) is locked out at the HTTP layer.
	if _, err := connectDaemon(t, r, key, id, false); err == nil {
		t.Fatal("lockout not applied")
	}
	// Wrong key for the id.
	other, _ := newKey(t)
	r.clock.Add(int64(11 * time.Minute))
	if _, err := connectDaemon(t, r, other, id, false); err == nil {
		t.Fatal("wrong key accepted")
	}
}

func TestReplaceOnlyAfterAuth(t *testing.T) {
	r := startRelay(t, nil)
	key, id := newKey(t)
	d1, err := connectDaemon(t, r, key, id, false)
	if err != nil {
		t.Fatal(err)
	}
	h1 := r.srv.reg.get(id)
	// An unauthenticated connection for the same id must not evict d1.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	ws, _, err := websocket.Dial(ctx, "wss://"+testDomain+wire.ControlPath+id, &websocket.DialOptions{HTTPClient: r.apexClient()})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	ws.Read(context.Background()) // challenge; never answer
	time.Sleep(50 * time.Millisecond)
	if r.srv.reg.get(id) != h1 {
		t.Fatal("unauthenticated connection evicted the host")
	}
	ws.CloseNow()
	// An authenticated one replaces it with 4409.
	if _, err := connectDaemon(t, r, key, id, false); err != nil {
		t.Fatal(err)
	}
	if code := d1.waitClose(t); code != wire.CloseReplaced {
		t.Fatalf("old daemon close code %d", code)
	}
	if h2 := r.srv.reg.get(id); h2 == nil || h2 == h1 {
		t.Fatal("new daemon not registered")
	}
	// The old one's cleanup must not unregister the new one.
	time.Sleep(50 * time.Millisecond)
	if !r.srv.Online(id) {
		t.Fatal("host went offline after replacement")
	}
}

func TestPassthroughEcho(t *testing.T) {
	r := startRelay(t, nil)
	key, id := newKey(t)
	if _, err := connectDaemon(t, r, key, id, false); err != nil {
		t.Fatal(err)
	}
	// The "daemon" echoes. The "phone" sends a ClientHello (routing) and
	// then arbitrary bytes; everything must come back byte for byte.
	c := phoneConn(t, r)
	hello := rawHello(t, id+"."+testDomain)
	if _, err := c.Write(hello); err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(hello))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("read echo of hello: %v", err)
	}
	if string(got) != string(hello) {
		t.Fatal("hello not replayed verbatim")
	}
	payload := []byte(strings.Repeat("orchestrator ", 10000)) // > 32 KiB, crosses buffers
	go c.Write(payload)
	got = make([]byte, len(payload))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatal("payload corrupted")
	}
	waitFor(t, "stream gauge", func() bool { return r.srv.streams.Load() == 1 })
	c.Close()
	waitFor(t, "stream release", func() bool { return r.srv.streams.Load() == 0 })
	if r.srv.m.bytesUp.Value() < int64(len(payload)) {
		t.Fatalf("bytes_up %d", r.srv.m.bytesUp.Value())
	}
}

func TestUnknownHostClosedBeforeServerHello(t *testing.T) {
	r := startRelay(t, nil)
	_, id := newKey(t)
	c := phoneConn(t, r)
	c.Write(rawHello(t, id+"."+testDomain))
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	if n, err := c.Read(buf); n != 0 || err == nil {
		t.Fatalf("expected close, got n=%d err=%v", n, err)
	}
	if r.srv.m.hostOffline.Value() != 1 {
		t.Fatalf("host_offline %d", r.srv.m.hostOffline.Value())
	}
	// Foreign SNI and plain HTTP are dropped too.
	c2 := phoneConn(t, r)
	c2.Write(rawHello(t, "www.example.com"))
	c2.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c2.Read(buf); err == nil {
		t.Fatal("foreign sni not closed")
	}
	c3 := phoneConn(t, r)
	c3.Write([]byte("GET / HTTP/1.0\r\n\r\n"))
	c3.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c3.Read(buf); err == nil {
		t.Fatal("plain http not closed")
	}
	waitFor(t, "counters", func() bool { return r.srv.m.unknownSNI.Value() == 1 && r.srv.m.peekFail.Value() == 1 })
}

func TestDialTimeoutAndUnresponsive(t *testing.T) {
	r := startRelay(t, nil)
	key, id := newKey(t)
	d, err := connectDaemon(t, r, key, id, false)
	if err != nil {
		t.Fatal(err)
	}
	d.onDial = nil // never answer
	hello := rawHello(t, id+"."+testDomain)
	for i := 0; i < unresponsiveDials; i++ {
		c := phoneConn(t, r)
		c.Write(hello)
		<-d.dials
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatal("phone not closed after dial timeout")
		}
	}
	if code := d.waitClose(t); code != wire.CloseUnresponsive {
		t.Fatalf("close code %d", code)
	}
	waitFor(t, "offline", func() bool { return !r.srv.Online(id) })
	if r.srv.m.dialTimeout.Value() != unresponsiveDials {
		t.Fatalf("dial_timeout %d", r.srv.m.dialTimeout.Value())
	}
}

func TestBusyClosesPhoneImmediately(t *testing.T) {
	r := startRelay(t, func(c *Config) { c.DialTimeout = 10 * time.Second })
	key, id := newKey(t)
	d, err := connectDaemon(t, r, key, id, false)
	if err != nil {
		t.Fatal(err)
	}
	d.onDial = func(dl wire.Dial) { d.busy(dl.Token) }
	c := phoneConn(t, r)
	c.Write(rawHello(t, id+"."+testDomain))
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("phone not closed")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("busy did not short-circuit the dial timeout")
	}
	waitFor(t, "busy counter", func() bool { return r.srv.m.busy.Value() == 1 })
	// A stale token is rejected on the data path.
	if _, err := d.openData(strings.Repeat("A", 43)); err == nil {
		t.Fatal("unknown token accepted")
	}
}

func TestPendingCap(t *testing.T) {
	r := startRelay(t, func(c *Config) { c.MaxPendingPerHost = 2; c.DialTimeout = 10 * time.Second })
	key, id := newKey(t)
	d, err := connectDaemon(t, r, key, id, false)
	if err != nil {
		t.Fatal(err)
	}
	d.onDial = nil
	hello := rawHello(t, id+"."+testDomain)
	var conns []net.Conn
	for i := 0; i < 3; i++ {
		c := phoneConn(t, r)
		c.Write(hello)
		conns = append(conns, c)
	}
	// Third phone is refused while the first two wait.
	conns[2].SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := conns[2].Read(make([]byte, 1)); err == nil {
		t.Fatal("third phone not refused")
	}
	if r.srv.m.pendingReject.Value() != 1 {
		t.Fatalf("pending_rejected %d", r.srv.m.pendingReject.Value())
	}
	_, p := r.srv.reg.counts()
	if p != 2 {
		t.Fatalf("pending %d", p)
	}
}

func TestStreamCap(t *testing.T) {
	r := startRelay(t, func(c *Config) { c.MaxStreamsPerHost = 1 })
	key, id := newKey(t)
	if _, err := connectDaemon(t, r, key, id, false); err != nil {
		t.Fatal(err)
	}
	hello := rawHello(t, id+"."+testDomain)
	c1 := phoneConn(t, r)
	c1.Write(hello)
	c1.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c1, make([]byte, len(hello))); err != nil {
		t.Fatal(err)
	}
	c2 := phoneConn(t, r)
	c2.Write(hello)
	c2.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c2.Read(make([]byte, 1)); err == nil {
		t.Fatal("second stream not refused")
	}
	waitFor(t, "stream_rejected", func() bool { return r.srv.m.streamRejected.Value() == 1 })
}

func TestSweeperClosesIdleHost(t *testing.T) {
	r := startRelay(t, func(c *Config) { c.HostIdle = time.Minute })
	key, id := newKey(t)
	d, err := connectDaemon(t, r, key, id, false)
	if err != nil {
		t.Fatal(err)
	}
	// A ping refreshes lastSeen (via OnPingReceived) even without frames.
	r.clock.Add(int64(50 * time.Second))
	pctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := d.ws.Ping(pctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	// OnPingReceived stores wall time; make the fake clock agree.
	r.clock.Store(time.Now().UnixNano())
	time.Sleep(120 * time.Millisecond)
	if !r.srv.Online(id) {
		t.Fatal("host closed despite ping")
	}
	r.clock.Add(int64(2 * time.Minute))
	if code := d.waitClose(t); code != websocket.StatusGoingAway {
		t.Fatalf("close code %d", code)
	}
	waitFor(t, "offline", func() bool { return !r.srv.Online(id) })
}

func TestShutdownSendsServiceRestart(t *testing.T) {
	r := startRelay(t, nil)
	key, id := newKey(t)
	d, err := connectDaemon(t, r, key, id, false)
	if err != nil {
		t.Fatal(err)
	}
	// An active stream keeps working through the drain window.
	c := phoneConn(t, r)
	hello := rawHello(t, id+"."+testDomain)
	c.Write(hello)
	c.SetDeadline(time.Now().Add(5 * time.Second))
	io.ReadFull(c, make([]byte, len(hello)))
	r.cancel()
	if code := d.waitClose(t); code != websocket.StatusServiceRestart {
		t.Fatalf("close code %d", code)
	}
	c.Write([]byte("still here"))
	buf := make([]byte, 10)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "still here" {
		t.Fatalf("stream broken during drain: %v %q", err, buf)
	}
	c.Close()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
}

func TestLimitsBucketAndPeek(t *testing.T) {
	now := time.Now()
	l := newLimits(2, 1, func() time.Time { return now })
	if !l.allowConn("a") || !l.allowConn("a") || l.allowConn("a") {
		t.Fatal("burst wrong")
	}
	now = now.Add(31 * time.Second)
	if !l.allowConn("a") || l.allowConn("a") {
		t.Fatal("refill wrong")
	}
	rel, ok := l.enterPeek("b")
	if !ok {
		t.Fatal("first peek refused")
	}
	if _, ok := l.enterPeek("b"); ok {
		t.Fatal("second peek allowed")
	}
	rel()
	if _, ok := l.enterPeek("b"); !ok {
		t.Fatal("peek slot not released")
	}
	now = now.Add(time.Hour)
	l.gc()
	if len(l.buckets) != 0 {
		t.Fatal("gc kept full buckets")
	}
	if fdLimit(10) < 16 {
		t.Fatal("fdLimit floor")
	}
}

func TestHostDropWithPendingDialClosesPhones(t *testing.T) {
	// The host's control socket drops while phones wait for it to dial.
	// Their timers must be stopped (never nil) and the phones closed.
	r := startRelay(t, func(c *Config) { c.DialTimeout = 10 * time.Second })
	key, id := newKey(t)
	d, err := connectDaemon(t, r, key, id, false)
	if err != nil {
		t.Fatal(err)
	}
	d.onDial = nil
	hello := rawHello(t, id+"."+testDomain)
	var conns []net.Conn
	for i := 0; i < 3; i++ {
		c := phoneConn(t, r)
		c.Write(hello)
		conns = append(conns, c)
	}
	for i := 0; i < 3; i++ {
		<-d.dials
	}
	d.ws.CloseNow()
	waitFor(t, "offline", func() bool { return !r.srv.Online(id) })
	for _, c := range conns {
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatal("waiting phone not closed when its host dropped")
		}
	}
	_, p := r.srv.reg.counts()
	if p != 0 {
		t.Fatalf("pending %d after drop", p)
	}
	if r.srv.m.dialTimeout.Value() != 0 {
		t.Fatalf("dial timers fired after the host dropped: %d", r.srv.m.dialTimeout.Value())
	}
}
