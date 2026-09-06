package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/markusbug/Orchestrator/daemon/internal/auth"
	"github.com/markusbug/Orchestrator/daemon/internal/config"
	"github.com/markusbug/Orchestrator/daemon/internal/core"
	"github.com/markusbug/Orchestrator/daemon/internal/protocol"
)

type env struct {
	core *core.Core
	srv  *httptest.Server
	root string
}

func newEnv(t *testing.T, debug bool) *env {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	os.MkdirAll(filepath.Join(root, "proj"), 0o755)
	paths, err := config.DefaultPaths(filepath.Join(dir, "cfg"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(paths.Home)
	cfg.Roots = []string{root}
	cfg.DefaultCommand = "sh"
	c, err := core.Open(context.Background(), paths, cfg, debug, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Core: c}
	ts := httptest.NewUnstartedServer(s.Handler())
	ts.TLS = s.TLSConfig()
	ts.StartTLS()
	t.Cleanup(func() { ts.Close(); c.Close() })
	return &env{core: c, srv: ts, root: root}
}

// client is a minimal protocol client that pins the server fingerprint.
type client struct {
	t     *testing.T
	ws    *websocket.Conn
	rid   int64
	nonce []byte
	bin   [][]byte
	msgs  []protocol.Message
	seen  int // cursor into msgs for waitMatch
}

func (e *env) dial(t *testing.T) *client {
	t.Helper()
	fp := e.core.Identity.Fingerprint
	tr := &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if auth.Fingerprint(cs.PeerCertificates[0].Raw) != fp {
				return x509.CertificateInvalidError{Reason: x509.NotAuthorizedToSign}
			}
			return nil
		},
	}}
	url := "wss" + strings.TrimPrefix(e.srv.URL, "https") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: &http.Client{Transport: tr}})
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(1 << 20)
	c := &client{t: t, ws: ws}
	t.Cleanup(func() { ws.CloseNow() })
	return c
}

func (c *client) send(typ string, body any) int64 {
	c.rid++
	b, err := protocol.Marshal(typ, c.rid, body)
	if err != nil {
		c.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
		c.t.Fatal(err)
	}
	return c.rid
}

// next reads one frame. Binary frames are stored and returned as a message
// with T == "_binary" so callers can keep polling without a read timeout
// (coder/websocket closes the connection when a Read context expires).
func (c *client) next(timeout time.Duration) (protocol.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	typ, data, err := c.ws.Read(ctx)
	if err != nil {
		return protocol.Message{}, err
	}
	if typ == websocket.MessageBinary {
		c.bin = append(c.bin, data)
		return protocol.Message{Envelope: protocol.Envelope{T: "_binary"}}, nil
	}
	m, err := protocol.Parse(data)
	if err != nil {
		return m, err
	}
	c.msgs = append(c.msgs, m)
	return m, nil
}

// waitReply waits for the reply to rid. Replies are unique per rid, so any
// already-received message can be returned.
func (c *client) waitReply(rid int64) protocol.Message {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range c.msgs {
			if m.ID == rid {
				return m
			}
		}
		if _, err := c.next(time.Until(deadline)); err != nil {
			c.t.Fatalf("waiting for rid %d: %v", rid, err)
		}
	}
	c.t.Fatalf("no reply for rid %d", rid)
	return protocol.Message{}
}

// waitMatch returns the first unconsumed unsolicited message (rid == 0)
// satisfying pred, reading more frames as needed. Events and replies may
// arrive in either order, so earlier-received events are scanned first.
func (c *client) waitMatch(what string, pred func(protocol.Message) bool) protocol.Message {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for c.seen < len(c.msgs) {
			m := c.msgs[c.seen]
			c.seen++
			if m.ID == 0 && pred(m) {
				return m
			}
		}
		if _, err := c.next(time.Until(deadline)); err != nil {
			c.t.Fatalf("waiting for %s: %v", what, err)
		}
	}
	c.t.Fatalf("no %s", what)
	return protocol.Message{}
}

// waitType waits for an unsolicited message of type t.
func (c *client) waitType(t string) protocol.Message {
	c.t.Helper()
	return c.waitMatch(t, func(m protocol.Message) bool { return m.T == t })
}

func (c *client) call(typ string, body any, out any) protocol.Message {
	c.t.Helper()
	m := c.waitReply(c.send(typ, body))
	if m.T == "error" {
		var e protocol.Error
		m.Decode(&e)
		c.t.Fatalf("%s: %s", typ, e.Error())
	}
	if out != nil {
		if err := m.Decode(out); err != nil {
			c.t.Fatal(err)
		}
	}
	return m
}

func (c *client) callErr(typ string, body any) protocol.Error {
	c.t.Helper()
	m := c.waitReply(c.send(typ, body))
	if m.T != "error" && m.T != "auth.fail" {
		c.t.Fatalf("%s: expected error, got %s", typ, m.T)
	}
	var e protocol.Error
	m.Decode(&e)
	return e
}

func (c *client) hello(deviceID string) protocol.HelloReply {
	c.t.Helper()
	c.nonce, _ = auth.NewNonce()
	var h protocol.HelloReply
	c.call("hello", protocol.Hello{Proto: 1, DeviceID: deviceID, Name: "test", ClientNonce: b64(c.nonce)}, &h)
	return h
}

func (c *client) outputContains(want string) bool {
	var all []byte
	for _, f := range c.bin {
		if _, _, p, err := protocol.DecodeFrame(f); err == nil {
			all = append(all, p...)
		}
	}
	return strings.Contains(string(all), want)
}

func (c *client) waitOutput(want string) {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !c.outputContains(want) {
		if time.Now().After(deadline) {
			c.t.Fatalf("output never contained %q", want)
		}
		c.next(time.Until(deadline))
	}
}

func (c *client) input(handle uint32, s string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageBinary, protocol.EncodeFrame(protocol.KindInput, handle, []byte(s))); err != nil {
		c.t.Fatal(err)
	}
}

func TestPairCreateAttachInputKill(t *testing.T) {
	e := newEnv(t, false)
	c := e.dial(t)
	h := c.hello("")
	if !h.AuthNeeded || h.Fingerprint != e.core.Identity.Fingerprint {
		t.Fatalf("hello %+v", h)
	}
	// unauthenticated request rejected
	if err := c.callErr("session.list", nil); err.Code != protocol.CodeUnauthorized {
		t.Fatalf("want unauthorized, got %+v", err)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := c.callErr("pair", protocol.Pair{Code: "000000", PubKey: b64(pub), Name: "phone"}); err.Code != protocol.CodeUnauthorized {
		t.Fatalf("no active code should fail: %+v", err)
	}
	payload, err := e.core.IssuePairing()
	if err != nil {
		t.Fatal(err)
	}
	if payload.FP != e.core.Identity.Fingerprint || len(payload.Code) != 6 {
		t.Fatalf("%+v", payload)
	}
	var ok protocol.PairOK
	c.call("pair", protocol.Pair{Code: payload.Code, PubKey: b64(pub), Name: "phone"}, &ok)
	if ok.DeviceID == "" {
		t.Fatal("no device id")
	}

	var created protocol.SessionCreated
	c.call("session.create", protocol.SessionCreate{Cwd: filepath.Join(e.root, "proj"), Cmd: "sh", Args: []string{"-c", "echo ready; cat"}, Cols: 80, Rows: 24}, &created)
	s := created.Session
	if s.Status != protocol.StatusRunning || s.Handle == 0 || s.Cwd != filepath.Join(e.root, "proj") {
		t.Fatalf("%+v", s)
	}
	var att protocol.SessionAttached
	c.call("session.attach", protocol.SessionAttach{ID: s.ID, Cols: 100, Rows: 30}, &att)
	c.waitOutput("ready")
	c.input(s.Handle, "hello-input\n")
	c.waitOutput("hello-input")

	var list protocol.SessionListReply
	c.call("session.list", nil, &list)
	if len(list.Sessions) != 1 || list.Sessions[0].Cols != 100 {
		t.Fatalf("%+v", list)
	}
	c.call("session.rename", protocol.SessionRename{ID: s.ID, Name: "renamed"}, nil)
	c.waitMatch("rename event", func(m protocol.Message) bool {
		var se protocol.SessionEvent
		return m.T == "session.event" && m.Decode(&se) == nil && se.Session.Name == "renamed"
	})

	c.call("session.kill", protocol.SessionKill{ID: s.ID, Signal: "KILL"}, nil)
	killMark := c.seen
	c.waitMatch("exit event", func(m protocol.Message) bool {
		if m.T != "session.event" {
			return false
		}
		var ev protocol.SessionEvent
		m.Decode(&ev)
		return ev.Session.Status == protocol.StatusExited
	})
	c.seen = killMark
	c.waitMatch("detached", func(m protocol.Message) bool { return m.T == "session.detached" })
	c.call("session.remove", protocol.SessionRemove{ID: s.ID}, nil)
	c.waitType("session.removed")
	c.call("session.list", nil, &list)
	if len(list.Sessions) != 0 {
		t.Fatal("session not removed")
	}
}

func TestAuthChallenge(t *testing.T) {
	e := newEnv(t, false)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	c := e.dial(t)
	c.hello("")
	payload, _ := e.core.IssuePairing()
	var ok protocol.PairOK
	c.call("pair", protocol.Pair{Code: payload.Code, PubKey: b64(pub), Name: "phone"}, &ok)
	c.ws.Close(websocket.StatusNormalClosure, "")

	// good signature
	c2 := e.dial(t)
	h := c2.hello(ok.DeviceID)
	sn, _ := auth.DecodeB64(h.ServerNonce)
	msg := protocol.ChallengeBytes(sn, c2.nonce, h.Fingerprint, ok.DeviceID)
	var authOK map[string]any
	c2.call("auth", protocol.Auth{DeviceID: ok.DeviceID, Signature: b64(ed25519.Sign(priv, msg))}, &authOK)
	var list protocol.SessionListReply
	c2.call("session.list", nil, &list)

	// bad signature closes the connection
	c3 := e.dial(t)
	h3 := c3.hello(ok.DeviceID)
	sn3, _ := auth.DecodeB64(h3.ServerNonce)
	wrong := protocol.ChallengeBytes(sn3, c3.nonce, "sha256:other", ok.DeviceID)
	if err := c3.callErr("auth", protocol.Auth{DeviceID: ok.DeviceID, Signature: b64(ed25519.Sign(priv, wrong))}); err.Code != protocol.CodeUnauthorized {
		t.Fatalf("%+v", err)
	}
	if _, err := c3.next(2 * time.Second); err == nil {
		t.Fatal("connection should be closed after auth failure")
	}

	// revoked device
	if err := e.core.Store.RevokeDevice(context.Background(), ok.DeviceID); err != nil {
		t.Fatal(err)
	}
	c4 := e.dial(t)
	h4 := c4.hello(ok.DeviceID)
	sn4, _ := auth.DecodeB64(h4.ServerNonce)
	msg4 := protocol.ChallengeBytes(sn4, c4.nonce, h4.Fingerprint, ok.DeviceID)
	if err := c4.callErr("auth", protocol.Auth{DeviceID: ok.DeviceID, Signature: b64(ed25519.Sign(priv, msg4))}); err.Code != protocol.CodeUnauthorized {
		t.Fatalf("revoked device accepted: %+v", err)
	}
}

func TestWrongCodeLockout(t *testing.T) {
	e := newEnv(t, false)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	payload, _ := e.core.IssuePairing()
	wrong := "000000"
	if wrong == payload.Code {
		wrong = "000001"
	}
	c := e.dial(t)
	c.hello("")
	e1 := c.callErr("pair", protocol.Pair{Code: wrong, PubKey: b64(pub)})
	e2 := c.callErr("pair", protocol.Pair{Code: wrong, PubKey: b64(pub)})
	if e1.Code != protocol.CodeUnauthorized || e2.Code != protocol.CodeUnauthorized {
		t.Fatalf("%+v %+v", e1, e2)
	}
	// third protocol error closes the socket
	c.send("pair", protocol.Pair{Code: wrong, PubKey: b64(pub)})
	c.next(2 * time.Second)
	if _, err := c.next(2 * time.Second); err == nil {
		t.Fatal("expected close after repeated errors")
	}
	// code is still valid on a new connection (only 3 attempts so far)
	c2 := e.dial(t)
	c2.hello("")
	var ok protocol.PairOK
	c2.call("pair", protocol.Pair{Code: payload.Code, PubKey: b64(pub)}, &ok)
}

func TestFilesystemAndInfo(t *testing.T) {
	e := newEnv(t, true) // loopback auto-auth
	c := e.dial(t)
	h := c.hello("")
	if h.AuthNeeded {
		t.Fatal("debug loopback should not need auth")
	}
	var info protocol.HostInfo
	c.call("host.info", nil, &info)
	if info.Fingerprint != e.core.Identity.Fingerprint || len(info.Roots) != 1 {
		t.Fatalf("%+v", info)
	}
	var list protocol.FSListReply
	c.call("fs.list", protocol.FSList{Path: e.root}, &list)
	if len(list.Entries) != 1 || list.Entries[0].Name != "proj" || !list.Entries[0].Dir {
		t.Fatalf("%+v", list)
	}
	if err := c.callErr("fs.list", protocol.FSList{Path: "/"}); err.Code != protocol.CodeForbidden {
		t.Fatalf("%+v", err)
	}
	var search protocol.FSSearchReply
	c.call("fs.search", protocol.FSSearch{Query: "pro"}, &search)
	if len(search.Entries) != 1 {
		t.Fatalf("%+v", search)
	}
	var conv protocol.ClaudeConversationsReply
	c.call("claude.conversations", protocol.ClaudeConversations{Cwd: filepath.Join(e.root, "proj")}, &conv)
	if conv.Conversations == nil {
		t.Fatal("conversations should be an empty list")
	}
	var rec protocol.FSRecentsReply
	c.call("fs.recents", nil, &rec)
	if rec.Paths == nil {
		t.Fatal("recents should be an empty list")
	}
	c.call("ping", nil, nil)
	if err := c.callErr("bogus.type", nil); err.Code != protocol.CodeBadRequest {
		t.Fatalf("%+v", err)
	}
	if err := c.callErr("session.attach", protocol.SessionAttach{ID: "nope"}); err.Code != protocol.CodeNotFound {
		t.Fatalf("%+v", err)
	}
}

func TestLateAttachReplaysAndTwoClients(t *testing.T) {
	e := newEnv(t, true)
	c := e.dial(t)
	c.hello("")
	var created protocol.SessionCreated
	c.call("session.create", protocol.SessionCreate{Cwd: e.root, Args: []string{"-c", "echo replay-me; cat"}}, &created)
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, _ := e.core.Mgr.Get(created.Session.ID)
		if strings.Contains(string(s.Scrollback()), "replay-me") || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c2 := e.dial(t)
	c2.hello("")
	c2.call("session.attach", protocol.SessionAttach{ID: created.Session.ID, Cols: 80, Rows: 24}, nil)
	c2.waitOutput("replay-me")
	c.call("session.attach", protocol.SessionAttach{ID: created.Session.ID, Cols: 80, Rows: 24}, nil)
	c.waitOutput("replay-me")
	c2.input(created.Session.Handle, "from-two\n")
	c.waitOutput("from-two")
	c2.waitOutput("from-two")
	c.call("session.detach", protocol.SessionDetach{ID: created.Session.ID}, nil)
	c.call("session.kill", protocol.SessionKill{ID: created.Session.ID, Signal: "KILL"}, nil)
	c2.waitType("session.detached")
}

func TestHealthz(t *testing.T) {
	e := newEnv(t, false)
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := hc.Get(e.srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	if resp, _ := hc.Get(e.srv.URL + "/_debug/"); resp.StatusCode != 404 {
		t.Fatalf("debug page should be absent without --debug, got %d", resp.StatusCode)
	}
	e2 := newEnv(t, true)
	resp2, _ := hc.Get(e2.srv.URL + "/_debug/")
	if resp2.StatusCode != 200 {
		t.Fatalf("debug page: %d", resp2.StatusCode)
	}
	resp3, _ := hc.Get(e2.srv.URL + "/_debug/static/xterm.js")
	if resp3.StatusCode != 200 {
		t.Fatalf("xterm asset: %d", resp3.StatusCode)
	}
}

var _ = json.Marshal

// dialRaw opens a socket without pinning or failing the test, so callers can
// inspect how the server ends it.
func dialRaw(t *testing.T, e *env) (*websocket.Conn, error) {
	t.Helper()
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	url := "wss" + strings.TrimPrefix(e.srv.URL, "https") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: &http.Client{Transport: tr}})
	return ws, err
}

func shortAuthDeadline(t *testing.T, d time.Duration) {
	t.Helper()
	old := authDeadline
	authDeadline = d
	t.Cleanup(func() { authDeadline = old })
}

func TestUnauthenticatedConnectionTimesOut(t *testing.T) {
	shortAuthDeadline(t, 150*time.Millisecond)
	e := newEnv(t, false)
	c := e.dial(t)
	c.hello("")
	// The read timeout is far past the deadline, so an error that arrives
	// early is the server closing the connection rather than the client
	// giving up.
	start := time.Now()
	if _, err := c.next(5 * time.Second); err == nil {
		t.Fatal("unauthenticated connection stayed open and sent a frame")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("connection lived %v; the deadline is %v", elapsed, authDeadline)
	}
}

func TestAuthenticatedConnectionSurvivesDeadline(t *testing.T) {
	shortAuthDeadline(t, 150*time.Millisecond)
	// Debug auto-authenticates loopback, which is what httptest serves.
	e := newEnv(t, true)
	c := e.dial(t)
	if h := c.hello(""); h.AuthNeeded {
		t.Fatal("debug loopback should not need auth")
	}
	time.Sleep(400 * time.Millisecond)
	c.call("ping", nil, nil) // fails the test if the socket was closed
}

func TestUnauthenticatedConnectionsAreCapped(t *testing.T) {
	e := newEnv(t, false)
	open := make([]*websocket.Conn, 0, maxUnauthed)
	t.Cleanup(func() {
		for _, ws := range open {
			ws.CloseNow()
		}
	})
	for i := 0; i < maxUnauthed; i++ {
		ws, err := dialRaw(t, e)
		if err != nil {
			t.Fatalf("connection %d refused below the cap: %v", i, err)
		}
		open = append(open, ws)
	}
	// rejected reports whether the server turned this connection away. An
	// accepted connection sends nothing, so waiting out the timeout is the
	// answer "not rejected".
	rejected := func(ws *websocket.Conn, wait time.Duration) bool {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		_, _, err := ws.Read(ctx)
		return websocket.CloseStatus(err) == websocket.StatusTryAgainLater
	}
	ws, err := dialRaw(t, e)
	if err != nil {
		t.Fatalf("dial past the cap: %v", err)
	}
	if !rejected(ws, 3*time.Second) {
		ws.CloseNow()
		t.Fatal("connection past the cap was accepted")
	}
	ws.CloseNow()

	// Closing one frees its slot for the next client.
	open[0].CloseNow()
	deadline := time.Now().Add(3 * time.Second)
	for {
		next, err := dialRaw(t, e)
		if err == nil {
			free := !rejected(next, 300*time.Millisecond)
			next.CloseNow()
			if free {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("slot was not released when a connection closed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
