package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/markusbug/Orchestrator/daemon/internal/relay/wire"
)

const pipeBuf = 32 << 10

// dial asks h's daemon to open a data socket for phone. The recorded
// ClientHello is replayed once the daemon connects.
func (s *Server) dial(h *host, phone net.Conn, hello []byte, peer string) {
	tok, err := wire.NewToken()
	if err != nil {
		phone.Close()
		return
	}
	p := &pending{token: tok, host: h, phone: phone, hello: hello, peer: peer, created: s.now()}
	if !s.reg.addPending(p, s.cfg.MaxPendingPerHost) {
		s.m.pendingReject.Add(1)
		phone.Close()
		return
	}
	p.timer = time.AfterFunc(s.cfg.DialTimeout, func() {
		if s.reg.claim(tok) == nil {
			return
		}
		s.m.dialTimeout.Add(1)
		phone.Close()
		if h.dialFailed(unresponsiveDials) {
			s.m.unresponsive.Add(1)
			s.log.Warn("host unresponsive to dials", "host", h.id, "peer", h.peer)
			h.close(wire.CloseUnresponsive, "dials unanswered")
		}
	})
	if !h.trySend(wire.Marshal(wire.Dial{T: wire.TDial, Token: tok, Peer: peer})) {
		if s.reg.claim(tok) != nil {
			p.timer.Stop()
			phone.Close()
		}
		s.m.unresponsive.Add(1)
		h.close(wire.CloseUnresponsive, "control queue full")
	}
}

// handleData is the daemon's answer to a dial: pair it with the phone.
func (s *Server) handleData(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	if !wire.ValidToken(tok) {
		http.NotFound(w, r)
		return
	}
	p := s.reg.claim(tok)
	if p == nil {
		http.NotFound(w, r)
		return
	}
	p.timer.Stop()
	h := p.host
	if !h.acquireStream(s.cfg.MaxStreamsPerHost) {
		s.m.streamRejected.Add(1)
		p.phone.Close()
		http.Error(w, "too many streams", http.StatusTooManyRequests)
		return
	}
	defer h.releaseStream()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		p.phone.Close()
		return
	}
	h.dialAnswered()
	s.m.streamsTotal.Add(1)
	s.streams.Add(1)
	s.pipes.Add(1)
	defer s.pipes.Done()
	defer s.streams.Add(-1)
	start := s.now()
	up, down := pipe(s.lifetime, p.phone, ws, p.hello, s.cfg.IdleTimeout)
	h.bytesUp.Add(up)
	h.bytesDown.Add(down)
	s.m.bytesUp.Add(up)
	s.m.bytesDown.Add(down)
	s.log.Debug("stream closed", "host", h.id, "peer", p.peer, "up", up, "down", down, "dur", s.now().Sub(start).Round(time.Millisecond))
}

// pipe copies bytes between a phone's TCP connection and a daemon's data
// WebSocket until either side closes or the stream idles out. TCP does the
// flow control; the relay holds one buffer per direction.
func pipe(parent context.Context, tcp net.Conn, ws *websocket.Conn, prefix []byte, idle time.Duration) (up, down int64) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	var upN, downN atomic.Int64

	if len(prefix) > 0 {
		if err := ws.Write(ctx, websocket.MessageBinary, prefix); err != nil {
			tcp.Close()
			ws.CloseNow()
			return 0, 0
		}
		upN.Add(int64(len(prefix)))
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // phone -> daemon
		defer wg.Done()
		buf := make([]byte, pipeBuf)
		var rerr error
		for {
			n, err := tcp.Read(buf)
			if n > 0 {
				last.Store(time.Now().UnixNano())
				if werr := ws.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					rerr = werr
					break
				}
				upN.Add(int64(n))
			}
			if err != nil {
				rerr = err
				break
			}
		}
		if errors.Is(rerr, io.EOF) {
			_ = ws.Close(websocket.StatusNormalClosure, "")
		} else {
			ws.CloseNow()
		}
		cancel()
	}()
	go func() { // daemon -> phone
		defer wg.Done()
		buf := make([]byte, pipeBuf)
		for {
			_, r, err := ws.Reader(ctx)
			if err != nil {
				break
			}
			n, err := io.CopyBuffer(tcp, r, buf)
			downN.Add(n)
			last.Store(time.Now().UnixNano())
			if err != nil {
				break
			}
		}
		tcp.Close()
		cancel()
	}()
	go func() { // idle watchdog
		tick := idle / 4
		if tick > time.Minute {
			tick = time.Minute
		}
		if tick <= 0 {
			tick = time.Second
		}
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if time.Since(time.Unix(0, last.Load())) > idle {
					tcp.Close()
					ws.CloseNow()
					cancel()
					return
				}
			}
		}
	}()
	wg.Wait()
	return upN.Load(), downN.Load()
}
