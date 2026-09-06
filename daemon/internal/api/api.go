// Package api serves the client-facing WebSocket protocol over TLS.
package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/markusbug/Orchestrator/daemon/internal/core"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/vconn"
	"github.com/markusbug/Orchestrator/daemon/web"
)

// viaRelayKey marks request contexts of connections that arrived through a
// relay (see internal/relay). Those are never loopback, whatever address the
// relay reports, so debug auto-auth cannot apply to them.
type viaRelayKey struct{}

func connContext(ctx context.Context, c net.Conn) context.Context {
	if tc, ok := c.(*tls.Conn); ok {
		c = tc.NetConn()
	}
	if _, ok := c.(*vconn.Conn); ok {
		return context.WithValue(ctx, viaRelayKey{}, true)
	}
	return ctx
}

// Server is the TLS + WebSocket front end.
type Server struct {
	Core *core.Core
	Log  *slog.Logger
	http *http.Server
}

// Handler builds the HTTP mux.
func (s *Server) Handler() http.Handler {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "ok %s\n", core.Version)
	})
	mux.HandleFunc("GET /ws", s.handleWS)
	if s.Core.Debug {
		sub, _ := fs.Sub(web.FS, ".")
		mux.Handle("GET /_debug/", http.StripPrefix("/_debug/", http.FileServerFS(sub)))
		mux.HandleFunc("GET /_debug", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/_debug/", http.StatusFound)
		})
	}
	return mux
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		s.Log.Debug("ws accept", "err", err)
		return
	}
	ws.SetReadLimit(1 << 20)
	viaRelay := r.Context().Value(viaRelayKey{}) != nil
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	loopback := !viaRelay && ip != nil && ip.IsLoopback()
	key := host
	if viaRelay {
		// The relay reports the phone's IP; keep its lockouts separate from
		// direct connections so a relay cannot unlock or lock a LAN address.
		key = "relay:" + host
	}
	c := newConn(s, ws, key, loopback)
	c.run(r.Context())
}

// TLSConfig returns the server TLS configuration.
func (s *Server) TLSConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{s.Core.Identity.Cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
	}
}

// ListenAndServe listens on the configured TCP address and serves it plus
// any extra listeners (such as a relay's virtual listener) until ctx is
// cancelled.
func (s *Server) ListenAndServe(ctx context.Context, extra ...net.Listener) error {
	addr := net.JoinHostPort(s.Core.Cfg.Bind, fmt.Sprint(s.Core.Cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, append([]net.Listener{ln}, extra...)...)
}

// Serve serves TLS on every listener with one HTTP server until ctx is
// cancelled or a listener fails.
func (s *Server) Serve(ctx context.Context, lns ...net.Listener) error {
	s.http = &http.Server{
		Handler:           s.Handler(),
		TLSConfig:         s.TLSConfig(),
		ReadHeaderTimeout: 10 * time.Second,
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){}, // no h2
		ConnContext:       connContext,
	}
	errc := make(chan error, len(lns))
	for _, ln := range lns {
		go func(ln net.Listener) { errc <- s.http.ServeTLS(ln, "", "") }(ln)
	}
	shutdown := func() {
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.http.Shutdown(sctx)
	}
	select {
	case <-ctx.Done():
		shutdown()
		return nil
	case err := <-errc:
		shutdown()
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
}

func isTimeout(err error) bool {
	return err != nil && strings.Contains(err.Error(), "timeout")
}
