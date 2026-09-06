package api

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/markusbug/Orchestrator/daemon/internal/auth"
	"github.com/markusbug/Orchestrator/daemon/internal/fsapi"
	"github.com/markusbug/Orchestrator/daemon/internal/protocol"
	"github.com/markusbug/Orchestrator/daemon/internal/session"
	"github.com/markusbug/Orchestrator/daemon/internal/store"
)

const (
	maxProtocolErrors = 3
	outQueue          = 256
	eventQueue        = 256
	replayChunk       = 32 * 1024
)

// authDeadline is how long a connection may stay unauthenticated. Hello and
// auth are one round trip each, so this is generous; without it a peer can
// hold a socket open forever by doing nothing, or by pinging, which dispatch
// answers before authentication. A variable so tests can shorten it.
var authDeadline = 30 * time.Second

type outMsg struct {
	typ  websocket.MessageType
	data []byte
}

type attachment struct {
	sess   *session.Session
	sub    *session.Subscriber
	cancel context.CancelFunc
}

type conn struct {
	srv      *Server
	ws       *websocket.Conn
	ip       string
	loopback bool

	ctx    context.Context
	cancel context.CancelFunc

	out    chan outMsg
	events chan outMsg

	mu          sync.Mutex
	helloDone   bool
	authed      bool
	released    bool // gave back the srv.unauthed slot
	deviceID    string
	serverNonce []byte
	clientNonce []byte
	errCount    int
	attached    map[uint32]*attachment
	unsub       func()
}

func newConn(s *Server, ws *websocket.Conn, ip string, loopback bool) *conn {
	return &conn{srv: s, ws: ws, ip: ip, loopback: loopback,
		out: make(chan outMsg, outQueue), events: make(chan outMsg, eventQueue),
		attached: map[uint32]*attachment{}}
}

func (c *conn) run(parent context.Context) {
	c.ctx, c.cancel = context.WithCancel(parent)
	defer c.cleanup()
	// Cancelling is enough to end the connection: reader and writer both
	// block on c.ctx, and cleanup closes the socket. Writing a reason here
	// would race the writer goroutine, which owns every write.
	deadline := time.AfterFunc(authDeadline, func() {
		if !c.isAuthed() {
			c.srv.Log.Debug("closing unauthenticated connection", "ip", c.ip, "after", authDeadline)
			c.cancel()
		}
	})
	defer deadline.Stop()
	go c.writer()
	c.reader()
}

func (c *conn) cleanup() {
	c.cancel()
	c.releaseUnauthed()
	c.mu.Lock()
	if c.unsub != nil {
		c.unsub()
		c.unsub = nil
	}
	atts := c.attached
	c.attached = map[uint32]*attachment{}
	c.mu.Unlock()
	for _, a := range atts {
		a.cancel()
		a.sess.Detach(a.sub)
	}
	_ = c.ws.CloseNow()
}

// writer is the only goroutine that writes to the socket.
func (c *conn) writer() {
	defer c.cancel()
	for {
		var m outMsg
		select {
		case <-c.ctx.Done():
			return
		case m = <-c.out:
		case m = <-c.events:
		}
		wctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
		err := c.ws.Write(wctx, m.typ, m.data)
		cancel()
		if err != nil {
			return
		}
	}
}

// send enqueues a message, blocking under backpressure.
func (c *conn) send(typ websocket.MessageType, data []byte) bool {
	select {
	case c.out <- outMsg{typ, data}:
		return true
	case <-c.ctx.Done():
		return false
	}
}

func (c *conn) sendJSON(t string, rid int64, body any) bool {
	b, err := protocol.Marshal(t, rid, body)
	if err != nil {
		c.srv.Log.Error("marshal", "t", t, "err", err)
		return false
	}
	return c.send(websocket.MessageText, b)
}

func (c *conn) sendError(rid int64, code, msg string) {
	c.sendJSON("error", rid, protocol.Error{Code: code, Message: msg})
}

// sendFinal writes one message synchronously and then closes the socket, so
// the client sees the reason before the connection drops.
func (c *conn) sendFinal(t string, rid int64, body any, status websocket.StatusCode, reason string) {
	if b, err := protocol.Marshal(t, rid, body); err == nil {
		wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = c.ws.Write(wctx, websocket.MessageText, b)
		cancel()
	}
	cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.ws.CloseNow()
	<-cctx.Done()
}

// sendEvent enqueues without blocking; events are dropped if the client is
// too slow, and it resyncs with session.list.
func (c *conn) sendEvent(t string, body any) {
	b, err := protocol.Marshal(t, 0, body)
	if err != nil {
		return
	}
	select {
	case c.events <- outMsg{websocket.MessageText, b}:
	default:
	}
}

func (c *conn) reader() {
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			c.handleBinary(data)
			continue
		}
		msg, err := protocol.Parse(data)
		if err != nil {
			if c.protocolError(0, protocol.CodeBadRequest, err.Error()) {
				return
			}
			continue
		}
		if !c.dispatch(msg) {
			return
		}
	}
}

// protocolError reports an error and returns true if the connection should close.
func (c *conn) protocolError(rid int64, code, msg string) bool {
	c.mu.Lock()
	c.errCount++
	n := c.errCount
	c.mu.Unlock()
	if n >= maxProtocolErrors {
		c.sendFinal("error", rid, protocol.Error{Code: code, Message: msg}, websocket.StatusPolicyViolation, "too many protocol errors")
		return true
	}
	c.sendError(rid, code, msg)
	return false
}

func (c *conn) isAuthed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.authed
}

// releaseUnauthed gives back the pending-authentication slot, once, whether
// the connection authenticated or went away first.
func (c *conn) releaseUnauthed() {
	c.mu.Lock()
	was := c.released
	c.released = true
	c.mu.Unlock()
	if !was {
		c.srv.unauthed.Add(-1)
	}
}

// dispatch handles one JSON message; returns false to close the connection.
func (c *conn) dispatch(m protocol.Message) bool {
	if !c.helloDone {
		if m.T != "hello" {
			return !c.protocolError(m.ID, protocol.CodeBadRequest, "expected hello")
		}
		return c.handleHello(m)
	}
	if !c.isAuthed() {
		switch m.T {
		case "pair":
			return c.handlePair(m)
		case "auth":
			return c.handleAuth(m)
		case "ping":
			c.sendJSON("pong", m.ID, nil)
			return true
		default:
			return !c.protocolError(m.ID, protocol.CodeUnauthorized, "authenticate first")
		}
	}
	switch m.T {
	case "ping":
		c.sendJSON("pong", m.ID, nil)
	case "host.info":
		c.sendJSON("host.info", m.ID, c.srv.Core.HostInfo())
	case "session.list":
		c.sendJSON("session.list", m.ID, protocol.SessionListReply{Sessions: c.srv.Core.Mgr.List()})
	case "session.create":
		var req protocol.SessionCreate
		if err := m.Decode(&req); err != nil {
			return !c.protocolError(m.ID, protocol.CodeBadRequest, err.Error())
		}
		s, err := c.srv.Core.CreateSession(c.ctx, req)
		if err != nil {
			c.sendErr(m.ID, err)
			return true
		}
		c.sendJSON("session.created", m.ID, protocol.SessionCreated{Session: s.Info()})
	case "session.resume":
		var req protocol.SessionResume
		if err := m.Decode(&req); err != nil {
			return !c.protocolError(m.ID, protocol.CodeBadRequest, err.Error())
		}
		s, err := c.srv.Core.ResumeSession(c.ctx, req.ID, req.Cols, req.Rows)
		if err != nil {
			c.sendErr(m.ID, err)
			return true
		}
		c.sendJSON("session.created", m.ID, protocol.SessionCreated{Session: s.Info()})
	case "session.attach":
		var req protocol.SessionAttach
		if err := m.Decode(&req); err != nil {
			return !c.protocolError(m.ID, protocol.CodeBadRequest, err.Error())
		}
		c.handleAttach(m.ID, req)
	case "session.detach":
		var req protocol.SessionDetach
		if err := m.Decode(&req); err != nil {
			return !c.protocolError(m.ID, protocol.CodeBadRequest, err.Error())
		}
		s, err := c.srv.Core.Mgr.Get(req.ID)
		if err != nil {
			c.sendErr(m.ID, err)
			return true
		}
		c.detach(s.Handle)
		c.sendJSON("ok", m.ID, nil)
	case "session.resize":
		var req protocol.SessionResize
		if err := m.Decode(&req); err != nil {
			return !c.protocolError(m.ID, protocol.CodeBadRequest, err.Error())
		}
		s, err := c.srv.Core.Mgr.Get(req.ID)
		if err != nil {
			c.sendErr(m.ID, err)
			return true
		}
		if err := s.Resize(req.Cols, req.Rows, false); err != nil {
			c.sendErr(m.ID, err)
			return true
		}
		c.sendJSON("ok", m.ID, nil)
	case "session.kill":
		var req protocol.SessionKill
		if err := m.Decode(&req); err != nil {
			return !c.protocolError(m.ID, protocol.CodeBadRequest, err.Error())
		}
		s, err := c.srv.Core.Mgr.Get(req.ID)
		if err != nil {
			c.sendErr(m.ID, err)
			return true
		}
		if err := s.Kill(req.Signal); err != nil {
			c.sendErr(m.ID, err)
			return true
		}
		c.sendJSON("ok", m.ID, nil)
	case "session.rename":
		var req protocol.SessionRename
		if err := m.Decode(&req); err != nil {
			return !c.protocolError(m.ID, protocol.CodeBadRequest, err.Error())
		}
		s, err := c.srv.Core.Mgr.Get(req.ID)
		if err != nil {
			c.sendErr(m.ID, err)
			return true
		}
		s.Rename(c.ctx, req.Name)
		c.sendJSON("ok", m.ID, nil)
	case "session.remove":
		var req protocol.SessionRemove
		if err := m.Decode(&req); err != nil {
			return !c.protocolError(m.ID, protocol.CodeBadRequest, err.Error())
		}
		if err := c.srv.Core.Mgr.Remove(c.ctx, req.ID); err != nil {
			c.sendErr(m.ID, err)
			return true
		}
		c.sendJSON("ok", m.ID, nil)
	case "fs.list":
		var req protocol.FSList
		if err := m.Decode(&req); err != nil {
			return !c.protocolError(m.ID, protocol.CodeBadRequest, err.Error())
		}
		reply, err := c.srv.Core.FS.List(req.Path, req.Hidden)
		if err != nil {
			c.sendErr(m.ID, err)
			return true
		}
		c.sendJSON("fs.list", m.ID, reply)
	case "fs.search":
		var req protocol.FSSearch
		if err := m.Decode(&req); err != nil {
			return !c.protocolError(m.ID, protocol.CodeBadRequest, err.Error())
		}
		entries, err := c.srv.Core.FS.Search(c.ctx, req.Root, req.Query, fsapi.SearchOptions{Limit: req.Limit})
		if err != nil {
			c.sendErr(m.ID, err)
			return true
		}
		c.sendJSON("fs.search", m.ID, protocol.FSSearchReply{Entries: entries})
	case "fs.recents":
		paths, err := c.srv.Core.Store.Recents(c.ctx, 10)
		if err != nil {
			c.sendErr(m.ID, err)
			return true
		}
		if paths == nil {
			paths = []string{}
		}
		c.sendJSON("fs.recents", m.ID, protocol.FSRecentsReply{Paths: paths})
	case "claude.conversations":
		var req protocol.ClaudeConversations
		if err := m.Decode(&req); err != nil {
			return !c.protocolError(m.ID, protocol.CodeBadRequest, err.Error())
		}
		list, err := c.srv.Core.Conversations(req.Cwd)
		if err != nil {
			c.sendErr(m.ID, err)
			return true
		}
		c.sendJSON("claude.conversations", m.ID, protocol.ClaudeConversationsReply{Conversations: list})
	default:
		return !c.protocolError(m.ID, protocol.CodeBadRequest, "unknown message type "+m.T)
	}
	return true
}

func (c *conn) sendErr(rid int64, err error) {
	code := protocol.CodeInternal
	switch {
	case errors.Is(err, session.ErrNotFound), errors.Is(err, store.ErrNotFound):
		code = protocol.CodeNotFound
	case errors.Is(err, fsapi.ErrOutsideRoots):
		code = protocol.CodeForbidden
	case errors.Is(err, session.ErrNotRunning), errors.Is(err, session.ErrStillAlive):
		code = protocol.CodeBadRequest
	default:
		var pe *protocol.Error
		if errors.As(err, &pe) {
			code = pe.Code
		} else if isTimeout(err) {
			code = protocol.CodeBadRequest
		}
	}
	c.sendError(rid, code, err.Error())
}

func (c *conn) handleHello(m protocol.Message) bool {
	var h protocol.Hello
	if err := m.Decode(&h); err != nil {
		return !c.protocolError(m.ID, protocol.CodeBadRequest, err.Error())
	}
	if h.Proto != protocol.Version {
		c.sendFinal("error", m.ID, protocol.Error{Code: protocol.CodeBadRequest, Message: "unsupported protocol version"}, websocket.StatusPolicyViolation, "protocol")
		return false
	}
	clientNonce, err := auth.DecodeB64(h.ClientNonce)
	if err != nil || len(clientNonce) < 16 {
		return !c.protocolError(m.ID, protocol.CodeBadRequest, "client_nonce must be at least 16 random bytes")
	}
	serverNonce, err := auth.NewNonce()
	if err != nil {
		c.sendError(m.ID, protocol.CodeInternal, err.Error())
		return false
	}
	autoAuth := c.srv.Core.Debug && c.loopback
	c.mu.Lock()
	c.helloDone = true
	c.clientNonce, c.serverNonce = clientNonce, serverNonce
	c.mu.Unlock()
	c.sendJSON("hello", m.ID, protocol.HelloReply{
		Proto: protocol.Version, Host: c.srv.Core.Hostname, Version: coreVersion(),
		ServerNonce: b64(serverNonce), Fingerprint: c.srv.Core.Identity.Fingerprint, AuthNeeded: !autoAuth,
	})
	if autoAuth {
		c.markAuthed("debug-loopback")
	}
	return true
}

func (c *conn) handlePair(m protocol.Message) bool {
	var p protocol.Pair
	if err := m.Decode(&p); err != nil {
		return !c.protocolError(m.ID, protocol.CodeBadRequest, err.Error())
	}
	if !c.srv.Core.Limiter.Allow(c.ip) {
		c.sendFinal("error", m.ID, protocol.Error{Code: protocol.CodeRateLimited, Message: "too many failed attempts; try again later"}, websocket.StatusPolicyViolation, "rate limited")
		return false
	}
	pub, err := auth.DecodeB64(p.PubKey)
	if err != nil || len(pub) != 32 {
		return !c.protocolError(m.ID, protocol.CodeBadRequest, "pubkey must be a 32-byte Ed25519 key")
	}
	if err := c.srv.Core.Codes.Consume(p.Code); err != nil {
		c.srv.Core.Limiter.Fail(c.ip)
		c.srv.Log.Warn("pairing failed", "ip", c.ip, "err", err)
		return !c.protocolError(m.ID, protocol.CodeUnauthorized, err.Error())
	}
	id, err := auth.NewDeviceID()
	if err != nil {
		c.sendError(m.ID, protocol.CodeInternal, err.Error())
		return false
	}
	name := p.Name
	if name == "" {
		name = "device"
	}
	now := time.Now()
	if err := c.srv.Core.Store.PutDevice(c.ctx, store.Device{ID: id, Name: name, PubKey: pub, CreatedAt: now, LastSeenAt: now}); err != nil {
		c.sendError(m.ID, protocol.CodeInternal, err.Error())
		return false
	}
	c.srv.Core.Limiter.Reset(c.ip)
	c.srv.Log.Info("device paired", "id", id, "name", name, "ip", c.ip)
	c.markAuthed(id)
	c.sendJSON("pair.ok", m.ID, protocol.PairOK{DeviceID: id})
	return true
}

func (c *conn) handleAuth(m protocol.Message) bool {
	var a protocol.Auth
	if err := m.Decode(&a); err != nil {
		return !c.protocolError(m.ID, protocol.CodeBadRequest, err.Error())
	}
	if !c.srv.Core.Limiter.Allow(c.ip) {
		c.sendFinal("error", m.ID, protocol.Error{Code: protocol.CodeRateLimited, Message: "too many failed attempts; try again later"}, websocket.StatusPolicyViolation, "rate limited")
		return false
	}
	fail := func(reason string) bool {
		c.srv.Core.Limiter.Fail(c.ip)
		c.srv.Log.Warn("auth failed", "ip", c.ip, "device", a.DeviceID, "reason", reason)
		c.sendFinal("auth.fail", m.ID, protocol.Error{Code: protocol.CodeUnauthorized, Message: reason}, websocket.StatusPolicyViolation, "auth failed")
		return false
	}
	c.mu.Lock()
	sn, cn := c.serverNonce, c.clientNonce
	c.serverNonce = nil // single use
	c.mu.Unlock()
	if sn == nil {
		return fail("nonce already used; reconnect")
	}
	dev, err := c.srv.Core.Store.GetDevice(c.ctx, a.DeviceID)
	if err != nil || dev.Revoked {
		return fail("unknown or revoked device")
	}
	sig, err := auth.DecodeB64(a.Signature)
	if err != nil {
		return fail("bad signature encoding")
	}
	msg := protocol.ChallengeBytes(sn, cn, c.srv.Core.Identity.Fingerprint, a.DeviceID)
	if !auth.VerifySignature(dev.PubKey, msg, sig) {
		return fail("signature verification failed")
	}
	c.srv.Core.Limiter.Reset(c.ip)
	_ = c.srv.Core.Store.TouchDevice(c.ctx, dev.ID, time.Now())
	c.markAuthed(dev.ID)
	c.sendJSON("auth.ok", m.ID, map[string]string{"device_id": dev.ID})
	return true
}

func (c *conn) markAuthed(deviceID string) {
	c.mu.Lock()
	c.authed = true
	c.deviceID = deviceID
	if c.unsub == nil {
		c.unsub = c.srv.Core.Mgr.Subscribe(func(ev session.Event) {
			switch ev.Kind {
			case "removed":
				c.sendEvent("session.removed", protocol.SessionRemoved{ID: ev.Session.ID})
			default:
				c.sendEvent("session.event", protocol.SessionEvent{Session: ev.Session})
			}
		})
	}
	c.mu.Unlock()
	c.releaseUnauthed()
}

func (c *conn) handleAttach(rid int64, req protocol.SessionAttach) {
	s, err := c.srv.Core.Mgr.Get(req.ID)
	if err != nil {
		c.sendErr(rid, err)
		return
	}
	c.detach(s.Handle)
	sub, snap, err := s.Attach()
	if err != nil {
		c.sendErr(rid, err)
		return
	}
	actx, cancel := context.WithCancel(c.ctx)
	a := &attachment{sess: s, sub: sub, cancel: cancel}
	c.mu.Lock()
	c.attached[s.Handle] = a
	c.mu.Unlock()

	c.sendJSON("session.attached", rid, protocol.SessionAttached{Session: s.Info()})
	for len(snap) > 0 {
		n := min(len(snap), replayChunk)
		if !c.send(websocket.MessageBinary, protocol.EncodeFrame(protocol.KindOutput, s.Handle, snap[:n])) {
			return
		}
		snap = snap[n:]
	}
	if req.Cols > 0 && req.Rows > 0 {
		_ = s.Resize(req.Cols, req.Rows, true)
	}
	go c.pump(actx, a)
}

func (c *conn) pump(ctx context.Context, a *attachment) {
	handle := a.sess.Handle
	for {
		data, err := a.sub.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			reason := "closed"
			if errors.Is(err, session.ErrSubscriberDropped) {
				reason = "slow"
			} else if errors.Is(err, session.ErrSubscriberClosed) {
				reason = "exited"
			}
			c.mu.Lock()
			if c.attached[handle] == a {
				delete(c.attached, handle)
			}
			c.mu.Unlock()
			c.sendEvent("session.detached", protocol.SessionDetached{ID: a.sess.ID, Reason: reason})
			return
		}
		for len(data) > 0 {
			n := min(len(data), replayChunk)
			if !c.send(websocket.MessageBinary, protocol.EncodeFrame(protocol.KindOutput, handle, data[:n])) {
				return
			}
			data = data[n:]
		}
	}
}

func (c *conn) detach(handle uint32) {
	c.mu.Lock()
	a, ok := c.attached[handle]
	if ok {
		delete(c.attached, handle)
	}
	c.mu.Unlock()
	if ok {
		a.cancel()
		a.sess.Detach(a.sub)
	}
}

func (c *conn) handleBinary(data []byte) {
	if !c.isAuthed() {
		return
	}
	kind, handle, payload, err := protocol.DecodeFrame(data)
	if err != nil || kind != protocol.KindInput {
		return
	}
	c.mu.Lock()
	a, ok := c.attached[handle]
	c.mu.Unlock()
	if !ok {
		return
	}
	if err := a.sess.Write(payload); err != nil {
		c.srv.Log.Debug("input write", "session", a.sess.ID, "err", err)
	}
}
