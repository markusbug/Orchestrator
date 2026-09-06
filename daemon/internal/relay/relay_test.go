// End-to-end: a real daemon core served over a relay, reached by a phone
// that pins the daemon's certificate. The relay only ever sees TLS.
package relay_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/markusbug/Orchestrator/daemon/internal/api"
	"github.com/markusbug/Orchestrator/daemon/internal/auth"
	"github.com/markusbug/Orchestrator/daemon/internal/config"
	"github.com/markusbug/Orchestrator/daemon/internal/core"
	"github.com/markusbug/Orchestrator/daemon/internal/protocol"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/client"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/server"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/vconn"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/wire"
)

// The relay's domain must match the host in the daemon's relay URL, and
// tests have no DNS, so the relay is "localhost" on an ephemeral port.
const domain = "localhost"

type world struct {
	t       *testing.T
	relay   *server.Server
	relayLn string
	pool    *x509.CertPool
	core    *core.Core
	root    string
	vln     *vconn.Listener
	stopRC  context.CancelFunc
	rc      *client.Client
	stopAll context.CancelFunc
}

func quiet() *slog.Logger {
	lvl := slog.LevelWarn
	if os.Getenv("RELAY_TEST_DEBUG") != "" {
		lvl = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func startWorld(t *testing.T, debug bool) *world {
	t.Helper()
	w := &world{t: t}
	// Relay.
	srv, err := server.New(server.Config{Domain: domain, Dev: true, Version: "test", Log: quiet(),
		DialTimeout: 3 * time.Second, DrainTimeout: 2 * time.Second, ConnsPerMinutePerIP: 10000})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	w.relay, w.relayLn = srv, net.JoinHostPort(domain, port)
	w.pool = x509.NewCertPool()
	w.pool.AddCert(srv.ApexCert())
	ctx, cancel := context.WithCancel(context.Background())
	w.stopAll = cancel
	relayDone := make(chan struct{})
	go func() { srv.Serve(ctx, ln); close(relayDone) }()

	// Daemon core, configured to use that relay.
	dir := t.TempDir()
	w.root = filepath.Join(dir, "root")
	os.MkdirAll(filepath.Join(w.root, "proj"), 0o755)
	paths, err := config.DefaultPaths(filepath.Join(dir, "cfg"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(paths.Home)
	cfg.Roots = []string{w.root}
	cfg.DefaultCommand = "sh"
	cfg.Relay = config.RelayConfig{Enabled: true, URL: "https://" + w.relayLn}
	c, err := core.Open(ctx, paths, cfg, debug, quiet())
	if err != nil {
		t.Fatal(err)
	}
	w.core = c

	// Daemon API over the relay's virtual listener only (no TCP needed).
	w.vln = vconn.NewListener("relay")
	apiDone := make(chan error, 1)
	go func() { apiDone <- (&api.Server{Core: c, Log: quiet()}).Serve(ctx, w.vln) }()
	w.startClient(ctx)

	t.Cleanup(func() {
		cancel()
		select {
		case <-relayDone:
		case <-time.After(10 * time.Second):
			t.Error("relay did not stop")
		}
		select {
		case err := <-apiDone:
			if err != nil {
				t.Errorf("api: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("api did not stop")
		}
		c.Close()
	})
	return w
}

// startClient runs the daemon-side relay client (what `serve` does).
func (w *world) startClient(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	w.stopRC = cancel
	rc, err := client.New(client.Options{
		URL: w.core.Cfg.Relay.URL, HostKey: w.core.HostKey, Version: "test", Listener: w.vln,
		TLS: &tls.Config{RootCAs: w.pool}, Log: quiet(),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	w.rc = rc
	w.core.Relay = rc
	go rc.Run(ctx)
	waitFor(w.t, "host online", func() bool { return w.relay.Online(w.core.HostID) })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// phone is a protocol client that reaches the daemon through the relay and
// pins the daemon's TLS fingerprint, exactly like the app.
type phone struct {
	t   *testing.T
	ws  *websocket.Conn
	rid int64
	sn  []byte // server nonce
	cn  []byte // client nonce
	fp  string
	bin [][]byte
}

func (w *world) dialPhone(t *testing.T) *phone {
	t.Helper()
	fp := w.core.Identity.Fingerprint
	sni := wire.Addr(w.core.HostID, domain)
	tr := &http.Transport{DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		raw, err := d.DialContext(ctx, "tcp", w.relayLn)
		if err != nil {
			return nil, err
		}
		tc := tls.Client(raw, &tls.Config{
			ServerName:         sni,
			InsecureSkipVerify: true,
			VerifyConnection: func(cs tls.ConnectionState) error {
				if auth.Fingerprint(cs.PeerCertificates[0].Raw) != fp {
					return x509.CertificateInvalidError{Reason: x509.NotAuthorizedToSign}
				}
				return nil
			},
		})
		if err := tc.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, err
		}
		return tc, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "wss://"+sni+"/ws", &websocket.DialOptions{HTTPClient: &http.Client{Transport: tr}})
	if err != nil {
		t.Fatalf("phone dial via relay: %v", err)
	}
	ws.SetReadLimit(1 << 20)
	t.Cleanup(func() { ws.CloseNow() })
	return &phone{t: t, ws: ws}
}

func (p *phone) send(typ string, body any) int64 {
	p.rid++
	b, err := protocol.Marshal(typ, p.rid, body)
	if err != nil {
		p.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.ws.Write(ctx, websocket.MessageText, b); err != nil {
		p.t.Fatal(err)
	}
	return p.rid
}

// reply reads until the reply to rid arrives; binary frames are collected.
func (p *phone) reply(rid int64) protocol.Message {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for {
		typ, data, err := p.ws.Read(ctx)
		if err != nil {
			p.t.Fatalf("waiting for rid %d: %v", rid, err)
		}
		if typ == websocket.MessageBinary {
			p.bin = append(p.bin, data)
			continue
		}
		m, err := protocol.Parse(data)
		if err != nil {
			p.t.Fatal(err)
		}
		if m.ID == rid {
			return m
		}
	}
}

func (p *phone) call(typ string, body any, out any) protocol.Message {
	p.t.Helper()
	m := p.reply(p.send(typ, body))
	if m.T == "error" || m.T == "auth.fail" {
		var e protocol.Error
		m.Decode(&e)
		p.t.Fatalf("%s: %s", typ, e.Error())
	}
	if out != nil {
		if err := m.Decode(out); err != nil {
			p.t.Fatal(err)
		}
	}
	return m
}

func (p *phone) hello() protocol.HelloReply {
	p.t.Helper()
	p.cn = make([]byte, 32)
	rand.Read(p.cn)
	var r protocol.HelloReply
	p.call("hello", protocol.Hello{Proto: protocol.Version, Name: "phone", ClientNonce: base64.StdEncoding.EncodeToString(p.cn)}, &r)
	p.sn, _ = auth.DecodeB64(r.ServerNonce)
	p.fp = r.Fingerprint
	return r
}

func (p *phone) outputContains(s string) bool {
	for _, f := range p.bin {
		if kind, _, payload, err := protocol.DecodeFrame(f); err == nil && kind == protocol.KindOutput && strings.Contains(string(payload), s) {
			return true
		}
	}
	return false
}

func TestPhoneThroughRelay(t *testing.T) {
	w := startWorld(t, true) // debug on: relayed phones must still need auth

	// Presence API sees the daemon.
	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: w.pool}}}
	resp, err := hc.Get("https://" + w.relayLn + wire.HostsPath + w.core.HostID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"online":true`) {
		t.Fatalf("presence: %s", body)
	}

	// host.info and the pairing payload advertise the relay address.
	info := w.core.HostInfo()
	if info.Relay == nil || info.Relay.HostID != w.core.HostID || !strings.HasSuffix(info.Relay.Addr, "."+domain) {
		t.Fatalf("host info relay %+v", info.Relay)
	}
	var relayAddr *protocol.HostAddr
	for i := range info.Addrs {
		if info.Addrs[i].Kind == protocol.AddrRelay {
			relayAddr = &info.Addrs[i]
		}
	}
	if relayAddr == nil || relayAddr.IP != info.Relay.Addr || relayAddr.Port != info.Relay.Port {
		t.Fatalf("relay addr missing from addrs: %+v", info.Addrs)
	}

	// Phone 1 pairs and drives a shell session.
	p := w.dialPhone(t)
	hr := p.hello()
	if !hr.AuthNeeded {
		t.Fatal("relayed phone was auto-authenticated by a debug daemon")
	}
	if hr.Fingerprint != w.core.Identity.Fingerprint {
		t.Fatal("fingerprint mismatch")
	}
	pay, err := w.core.IssuePairing()
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	var pok protocol.PairOK
	p.call("pair", protocol.Pair{Code: pay.Code, PubKey: base64.StdEncoding.EncodeToString(pub), Name: "phone"}, &pok)
	var created protocol.SessionCreated
	p.call("session.create", protocol.SessionCreate{Cwd: filepath.Join(w.root, "proj"), Cmd: "sh", Cols: 80, Rows: 24}, &created)
	var attached protocol.SessionAttached
	p.call("session.attach", protocol.SessionAttach{ID: created.Session.ID, Cols: 80, Rows: 24}, &attached)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := p.ws.Write(ctx, websocket.MessageBinary, protocol.EncodeFrame(protocol.KindInput, attached.Session.Handle, []byte("echo relay-$((20+22))\n"))); err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(8 * time.Second)
	for !p.outputContains("relay-42") && time.Now().Before(deadline) {
		rctx, cancel := context.WithTimeout(context.Background(), time.Until(deadline))
		typ, data, err := p.ws.Read(rctx)
		cancel()
		if err != nil {
			t.Fatalf("reading output: %v", err)
		}
		if typ == websocket.MessageBinary {
			p.bin = append(p.bin, data)
		}
	}
	if !p.outputContains("relay-42") {
		t.Fatal("no shell output through the relay")
	}

	// Phone 2 authenticates with the paired key concurrently.
	p2 := w.dialPhone(t)
	hr2 := p2.hello()
	sn, _ := auth.DecodeB64(hr2.ServerNonce)
	sig := ed25519.Sign(priv, protocol.ChallengeBytes(sn, p2.cn, hr2.Fingerprint, pok.DeviceID))
	p2.call("auth", protocol.Auth{DeviceID: pok.DeviceID, Signature: base64.StdEncoding.EncodeToString(sig)}, nil)
	var list protocol.SessionListReply
	p2.call("session.list", nil, &list)
	if len(list.Sessions) != 1 || list.Sessions[0].ID != created.Session.ID {
		t.Fatalf("sessions via second phone: %+v", list.Sessions)
	}
	waitFor(t, "two streams", func() bool { return w.rc.Status().Streams == 2 })

	// Wrong pairing code through the relay is rate limited per phone IP under
	// the relay: prefix, not the raw address.
	p3 := w.dialPhone(t)
	p3.hello()
	m := p3.reply(p3.send("pair", protocol.Pair{Code: "000000", PubKey: base64.StdEncoding.EncodeToString(pub)}))
	if m.T != "error" {
		t.Fatalf("bad code accepted: %s", m.T)
	}
	if !w.core.Limiter.Allow("relay:127.0.0.1") || w.core.Limiter.Allow("127.0.0.1") == false {
		t.Fatal("limiter keyed unexpectedly")
	}

	p.ws.Close(websocket.StatusNormalClosure, "")
	p2.ws.Close(websocket.StatusNormalClosure, "")
	p3.ws.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "streams released", func() bool { return w.rc.Status().Streams == 0 })
	if _, err := w.core.Mgr.Get(created.Session.ID); err != nil {
		t.Fatal("session died with the phone")
	}
}

func TestUnknownHostAndDaemonRestart(t *testing.T) {
	w := startWorld(t, false)

	// A phone for an unknown host id is cut off before any TLS ServerHello.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	raw, err := net.Dial("tcp", w.relayLn)
	if err != nil {
		t.Fatal(err)
	}
	tc := tls.Client(raw, &tls.Config{ServerName: wire.Addr(wire.HostID(other.Public().(ed25519.PublicKey)), domain), InsecureSkipVerify: true})
	tc.SetDeadline(time.Now().Add(5 * time.Second))
	if err := tc.Handshake(); err == nil {
		t.Fatal("handshake to unknown host succeeded")
	}
	raw.Close()

	// The daemon's relay client restarts (as after a daemon upgrade); phones
	// reconnect once it is back.
	w.stopRC()
	waitFor(t, "offline", func() bool { return !w.relay.Online(w.core.HostID) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.startClient(ctx)
	p := w.dialPhone(t)
	if hr := p.hello(); !hr.AuthNeeded {
		t.Fatal("auth not needed?")
	}

	// The host key, and therefore the host id, is stable on disk.
	k, err := auth.EnsureHostKey(w.core.Paths.HostKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if wire.HostID(k.Public().(ed25519.PublicKey)) != w.core.HostID {
		t.Fatal("host id changed")
	}
	var st struct {
		Relay *client.Status `json:"relay"`
	}
	b, _ := json.Marshal(map[string]any{"relay": w.rc.Status()})
	json.Unmarshal(b, &st)
	if st.Relay == nil || !st.Relay.Connected || st.Relay.Addr != wire.Addr(w.core.HostID, domain) {
		t.Fatalf("status %+v", st.Relay)
	}
}
