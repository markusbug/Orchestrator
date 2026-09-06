package server

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/markusbug/Orchestrator/daemon/internal/auth"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/wire"
)

const unresponsiveDials = 3

// handleControl authenticates a daemon and runs its control loop.
func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !wire.ValidHostID(id) {
		http.Error(w, "bad host id", http.StatusBadRequest)
		return
	}
	ip := hostOf(r.RemoteAddr)
	if !s.limits.auth.Allow(ip) {
		http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
		return
	}
	seen := new(atomic.Int64)
	seen.Store(s.now().UnixNano())
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
		OnPingReceived: func(ctx context.Context, payload []byte) bool {
			seen.Store(time.Now().UnixNano())
			return true
		},
	})
	if err != nil {
		s.log.Debug("control accept", "err", err)
		return
	}
	ws.SetReadLimit(64 << 10)
	ctx := s.lifetime

	nonce, err := auth.NewNonce()
	if err != nil {
		ws.Close(websocket.StatusInternalError, "nonce")
		return
	}
	if err := wire.WriteJSON(ctx, ws, wire.Challenge{T: wire.TChallenge, Nonce: b64(nonce), Relay: s.cfg.Domain}); err != nil {
		ws.CloseNow()
		return
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, data, err := ws.Read(rctx)
	cancel()
	if err != nil {
		ws.CloseNow()
		return
	}
	var a wire.Auth
	if t, _ := wire.Type(data); t != wire.TAuth || unmarshal(data, &a) != nil {
		s.fail(ws, ip, wire.CloseBadRequest, "expected auth")
		return
	}
	pub, err1 := auth.DecodeB64(a.PubKey)
	sig, err2 := auth.DecodeB64(a.Sig)
	if err1 != nil || err2 != nil || wire.HostID(pub) != id ||
		!auth.VerifySignature(pub, wire.ChallengeBytes(nonce, id, s.cfg.Domain), sig) {
		s.fail(ws, ip, wire.CloseUnauthorized, "bad signature")
		return
	}
	s.limits.auth.Reset(ip)
	s.m.authOK.Add(1)

	h := &host{
		id: id, ws: ws, peer: ip, since: s.now(), seen: seen,
		sendq: make(chan []byte, 64), done: make(chan struct{}),
	}
	if old := s.reg.put(h); old != nil {
		s.m.replaced.Add(1)
		s.log.Info("host replaced", "host", id, "old_peer", old.peer, "new_peer", ip)
		old.close(wire.CloseReplaced, "another daemon authenticated for this host")
	}
	if err := wire.WriteJSON(ctx, ws, wire.OK{T: wire.TOK, PingIntervalS: int(s.cfg.PingInterval.Seconds()), MaxStreams: s.cfg.MaxStreamsPerHost}); err != nil {
		s.dropHost(h)
		ws.CloseNow()
		return
	}
	s.log.Info("host online", "host", id, "peer", ip, "version", a.Version)
	go h.writer(ctx)
	s.runHost(ctx, h)
}

func (s *Server) fail(ws *websocket.Conn, ip string, code websocket.StatusCode, msg string) {
	s.m.authFail.Add(1)
	if s.limits.auth.Fail(ip) {
		s.log.Warn("control auth locked out", "peer", ip)
	}
	wctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = ws.Write(wctx, websocket.MessageText, wire.Marshal(wire.Error{T: wire.TError, Code: "unauthorized", Message: msg}))
	cancel()
	_ = ws.Close(code, msg)
}

// runHost reads control messages until the socket dies, then unregisters.
func (s *Server) runHost(ctx context.Context, h *host) {
	defer s.dropHost(h)
	for {
		typ, data, err := h.ws.Read(ctx)
		if err != nil {
			return
		}
		h.seen.Store(s.now().UnixNano())
		if typ != websocket.MessageText {
			continue
		}
		t, err := wire.Type(data)
		if err != nil {
			continue
		}
		switch t {
		case wire.TBusy:
			var b wire.Busy
			if unmarshal(data, &b) == nil {
				if p := s.reg.claim(b.Token); p != nil && p.host == h {
					p.timer.Stop()
					p.phone.Close()
					s.m.busy.Add(1)
				}
			}
		case wire.TPush:
			// Reserved for push notifications; nothing is stored or sent yet.
			s.m.pushDropped.Add(1)
			s.log.Debug("push dropped", "host", h.id)
		}
	}
}

// dropHost unregisters h, fails its waiting phones, and stops its writer.
func (s *Server) dropHost(h *host) {
	was, orphans := s.reg.remove(h)
	for _, p := range orphans {
		p.timer.Stop()
		p.phone.Close()
	}
	select {
	case <-h.done:
	default:
		close(h.done)
	}
	h.close(websocket.StatusGoingAway, "")
	if was {
		s.log.Info("host offline", "host", h.id, "peer", h.peer, "up", s.now().Sub(h.since).Round(time.Second))
	}
}

// sweeper closes hosts that stopped pinging and trims limiter state.
func (s *Server) sweeper(ctx context.Context) {
	t := time.NewTicker(s.cfg.SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.sweepOnce()
	}
}

func (s *Server) sweepOnce() {
	cutoff := s.now().Add(-s.cfg.HostIdle).UnixNano()
	for _, h := range s.reg.snapshot() {
		if h.seen.Load() < cutoff {
			s.m.idleClosed.Add(1)
			s.log.Info("host idle", "host", h.id, "peer", h.peer)
			h.close(websocket.StatusGoingAway, "idle")
		}
	}
	s.limits.gc()
}
