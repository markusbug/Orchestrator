// Package admin exposes a local control API over a unix socket for the CLI
// and for Claude Code hooks. No authentication beyond socket permissions.
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/markusbug/Orchestrator/daemon/internal/core"
	"github.com/markusbug/Orchestrator/daemon/internal/protocol"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/client"
)

// Status is the reply of GET /status.
type Status struct {
	Version     string              `json:"version"`
	Host        string              `json:"host"`
	Port        int                 `json:"port"`
	Bind        string              `json:"bind"`
	Fingerprint string              `json:"fingerprint"`
	Addrs       []protocol.HostAddr `json:"addrs"`
	Sessions    int                 `json:"sessions"`
	Devices     int                 `json:"devices"`
	UptimeSec   int64               `json:"uptime_sec"`
	Debug       bool                `json:"debug"`
	PID         int                 `json:"pid"`
	ConfigDir   string              `json:"config_dir"`
	HostID      string              `json:"host_id"`
	Relay       *client.Status      `json:"relay,omitempty"`
}

// DeviceInfo is a device row for the CLI.
type DeviceInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	Revoked    bool   `json:"revoked"`
}

// HookRequest is posted by `orchestrator _hook`.
type HookRequest struct {
	SessionID string `json:"session_id"`
	Event     string `json:"event"`
}

// Server serves the admin API.
type Server struct {
	Core *core.Core
	ln   net.Listener
	srv  *http.Server
}

// Handler builds the admin mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		devs, _ := s.Core.Store.ListDevices(r.Context())
		active := 0
		for _, d := range devs {
			if !d.Revoked {
				active++
			}
		}
		st := Status{
			Version: core.Version, Host: s.Core.Hostname, Port: s.Core.Cfg.Port, Bind: s.Core.Cfg.Bind,
			Fingerprint: s.Core.Identity.Fingerprint, Addrs: s.Core.Addrs(),
			Sessions: len(s.Core.Mgr.List()), Devices: active,
			UptimeSec: int64(time.Since(s.Core.StartedAt).Seconds()), Debug: s.Core.Debug,
			PID: os.Getpid(), ConfigDir: s.Core.Paths.Dir, HostID: s.Core.HostID,
		}
		if s.Core.Relay != nil {
			rs := s.Core.Relay.Status()
			st.Relay = &rs
		}
		writeJSON(w, st)
	})
	mux.HandleFunc("GET /sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, protocol.SessionListReply{Sessions: s.Core.Mgr.List()})
	})
	mux.HandleFunc("POST /sessions/{id}/kill", func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.Core.Mgr.Get(s.resolveID(r.PathValue("id")))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		sig := r.URL.Query().Get("signal")
		if err := sess.Kill(sig); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /sessions/{id}/remove", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Core.Mgr.Remove(r.Context(), s.resolveID(r.PathValue("id"))); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /devices", func(w http.ResponseWriter, r *http.Request) {
		devs, err := s.Core.Store.ListDevices(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := []DeviceInfo{}
		for _, d := range devs {
			out = append(out, DeviceInfo{ID: d.ID, Name: d.Name, CreatedAt: d.CreatedAt.UnixMilli(), LastSeenAt: d.LastSeenAt.UnixMilli(), Revoked: d.Revoked})
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("POST /devices/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Core.Store.RevokeDevice(r.Context(), r.PathValue("id")); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /pair", func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Core.IssuePairing()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, p)
	})
	mux.HandleFunc("POST /hook", func(w http.ResponseWriter, r *http.Request) {
		var req HookRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		changed := s.Core.ApplyHook(req.SessionID, req.Event)
		writeJSON(w, map[string]bool{"changed": changed})
	})
	return mux
}

// Listen starts serving on the unix socket path.
func (s *Server) Listen(path string) error {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return err
	}
	s.ln = ln
	s.srv = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return nil
}

// Close stops the server and removes the socket.
func (s *Server) Close() {
	if s.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(ctx)
	}
	if s.ln != nil {
		_ = os.Remove(s.ln.Addr().String())
	}
}

// resolveID expands a unique session id prefix (as printed by the CLI) to
// the full id. Ambiguous or unknown prefixes are returned unchanged.
func (s *Server) resolveID(prefix string) string {
	var match string
	for _, info := range s.Core.Mgr.List() {
		if info.ID == prefix {
			return prefix
		}
		if strings.HasPrefix(info.ID, prefix) {
			if match != "" {
				return prefix
			}
			match = info.ID
		}
	}
	if match != "" {
		return match
	}
	return prefix
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ErrNotRunning is returned when the daemon socket is not reachable.
var ErrNotRunning = errors.New("daemon is not running (start it with `orchestrator serve` or `orchestrator install`)")

// Client talks to the admin socket.
type Client struct {
	Socket string
	http   *http.Client
}

// NewClient creates a client for the socket path.
func NewClient(socket string) *Client {
	return &Client{Socket: socket, http: &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}},
	}}
}

func (c *Client) do(method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://orchestrator"+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ne *net.OpError
		if errors.As(err, &ne) || strings.Contains(err.Error(), "connect:") || strings.Contains(err.Error(), "no such file") {
			return ErrNotRunning
		}
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s", strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Status fetches daemon status.
func (c *Client) Status() (Status, error) {
	var s Status
	return s, c.do("GET", "/status", nil, &s)
}

// Sessions lists sessions.
func (c *Client) Sessions() ([]protocol.SessionInfo, error) {
	var r protocol.SessionListReply
	return r.Sessions, c.do("GET", "/sessions", nil, &r)
}

// KillSession signals a session.
func (c *Client) KillSession(id, signal string) error {
	return c.do("POST", "/sessions/"+id+"/kill?signal="+signal, nil, nil)
}

// RemoveSession forgets an exited session.
func (c *Client) RemoveSession(id string) error {
	return c.do("POST", "/sessions/"+id+"/remove", nil, nil)
}

// Devices lists paired devices.
func (c *Client) Devices() ([]DeviceInfo, error) {
	var d []DeviceInfo
	return d, c.do("GET", "/devices", nil, &d)
}

// RevokeDevice revokes a device.
func (c *Client) RevokeDevice(id string) error {
	return c.do("POST", "/devices/"+id+"/revoke", nil, nil)
}

// Pair issues a pairing code.
func (c *Client) Pair() (protocol.PairPayload, error) {
	var p protocol.PairPayload
	return p, c.do("POST", "/pair", nil, &p)
}

// Hook reports a Claude Code hook event.
func (c *Client) Hook(sessionID, event string) error {
	return c.do("POST", "/hook", HookRequest{SessionID: sessionID, Event: event}, nil)
}
