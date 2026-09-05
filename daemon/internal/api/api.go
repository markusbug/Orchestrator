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
	"github.com/markusbug/Orchestrator/daemon/web"
)

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
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	loopback := ip != nil && ip.IsLoopback()
	c := newConn(s, ws, host, loopback)
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

// ListenAndServe serves until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	addr := net.JoinHostPort(s.Core.Cfg.Bind, fmt.Sprint(s.Core.Cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve serves TLS on ln until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.http = &http.Server{
		Handler:           s.Handler(),
		TLSConfig:         s.TLSConfig(),
		ReadHeaderTimeout: 10 * time.Second,
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){}, // no h2
	}
	errc := make(chan error, 1)
	go func() { errc <- s.http.ServeTLS(ln, "", "") }()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.http.Shutdown(sctx)
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func isTimeout(err error) bool {
	return err != nil && strings.Contains(err.Error(), "timeout")
}
