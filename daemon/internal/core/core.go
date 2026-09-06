// Package core bundles the daemon's shared state so the API server, admin
// socket, and CLI can share one implementation of host-level operations.
package core

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/markusbug/Orchestrator/daemon/internal/auth"
	"github.com/markusbug/Orchestrator/daemon/internal/buildinfo"
	"github.com/markusbug/Orchestrator/daemon/internal/claude"
	"github.com/markusbug/Orchestrator/daemon/internal/config"
	"github.com/markusbug/Orchestrator/daemon/internal/fsapi"
	"github.com/markusbug/Orchestrator/daemon/internal/netaddr"
	"github.com/markusbug/Orchestrator/daemon/internal/protocol"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/client"
	"github.com/markusbug/Orchestrator/daemon/internal/relay/wire"
	"github.com/markusbug/Orchestrator/daemon/internal/session"
	"github.com/markusbug/Orchestrator/daemon/internal/store"
)

// Version is the daemon build version (stamped into buildinfo at link time).
var Version = buildinfo.Version

// Core holds everything the daemon needs at runtime.
type Core struct {
	Cfg       config.Config
	Paths     config.Paths
	Identity  auth.Identity
	HostKey   ed25519.PrivateKey // relay identity
	HostID    string             // derived from HostKey
	Relay     *client.Client     // set by serve when the relay is active
	Codes     *auth.Codes
	Limiter   *auth.Limiter
	Store     *store.Store
	Mgr       *session.Manager
	FS        *fsapi.Service
	Hostname  string
	Exe       string
	StartedAt time.Time
	Debug     bool
	Log       *slog.Logger
}

// Open initializes all components. Call Close when done.
func Open(ctx context.Context, paths config.Paths, cfg config.Config, debug bool, log *slog.Logger) (*Core, error) {
	if log == nil {
		log = slog.Default()
	}
	if err := config.EnsureDirs(paths); err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "orchestrator"
	}
	id, err := auth.EnsureTLS(paths.CertFile, paths.KeyFile, host)
	if err != nil {
		return nil, err
	}
	hostKey, err := auth.EnsureHostKey(paths.HostKeyFile)
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		exe = "orchestrator"
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	if err := claude.WriteHookSettings(paths.HooksFile, exe); err != nil {
		return nil, err
	}
	st, err := store.Open(paths.DBFile)
	if err != nil {
		return nil, err
	}
	fs, err := fsapi.New(cfg.Roots)
	if err != nil {
		st.Close()
		return nil, err
	}
	mgr := session.NewManager(session.Options{
		ScrollbackBytes: cfg.ScrollbackBytes,
		ExtraPath:       extraPath(paths.Home),
		Env:             map[string]string{"ORCHESTRATOR_SOCKET": paths.AdminSocket},
		Store:           st,
		Logger:          log,
	})
	if err := mgr.LoadStale(ctx); err != nil {
		st.Close()
		return nil, err
	}
	return &Core{
		Cfg: cfg, Paths: paths, Identity: id,
		HostKey: hostKey, HostID: wire.HostID(hostKey.Public().(ed25519.PublicKey)),
		Codes:   auth.NewCodes(nil),
		Limiter: auth.NewLimiter(5, 10*time.Minute, 10*time.Minute, nil),
		Store:   st, Mgr: mgr, FS: fs, Hostname: host, Exe: exe,
		StartedAt: time.Now(), Debug: debug, Log: log,
	}, nil
}

func extraPath(home string) []string {
	return []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".claude", "local"),
		filepath.Join(home, ".npm-global", "bin"),
		"/usr/local/bin", "/opt/homebrew/bin",
	}
}

// Close shuts down sessions and the store.
func (c *Core) Close() {
	c.Mgr.Shutdown(5 * time.Second)
	c.Store.Close()
}

// Addrs lists reachable addresses: LAN and Tailscale first, then the relay
// name when a relay is configured. Every entry carries its port so a client
// never has to guess which port belongs to which address.
func (c *Core) Addrs() []protocol.HostAddr {
	addrs := netaddr.List()
	for i := range addrs {
		addrs[i].Port = c.Cfg.Port
	}
	if r := c.RelayInfo(); r != nil {
		addrs = append(addrs, protocol.HostAddr{IP: r.Addr, Kind: protocol.AddrRelay, Port: r.Port})
	}
	return addrs
}

// RelayInfo describes the relay path, or nil when no relay is configured.
// The address is what the running client uses, or what the config implies
// before the client exists.
func (c *Core) RelayInfo() *protocol.RelayInfo {
	if !c.Cfg.Relay.Active() {
		return nil
	}
	info := &protocol.RelayInfo{URL: c.Cfg.Relay.URL, HostID: c.HostID}
	if rc := c.Relay; rc != nil {
		info.Addr, info.Port = rc.Addr(), rc.Port()
	} else {
		info.Addr, info.Port = wire.Addr(c.HostID, c.Cfg.Relay.Domain()), c.Cfg.Relay.Port()
	}
	return info
}

// HostInfo describes this host to clients.
func (c *Core) HostInfo() protocol.HostInfo {
	return protocol.HostInfo{
		Host: c.Hostname, Version: Version, Fingerprint: c.Identity.Fingerprint,
		Port: c.Cfg.Port, Addrs: c.Addrs(), Roots: c.FS.Roots(),
		DefaultCmd: c.Cfg.DefaultCommand, Home: c.Paths.Home, Relay: c.RelayInfo(),
	}
}

// IssuePairing creates a new pairing code and the payload for the QR.
func (c *Core) IssuePairing() (protocol.PairPayload, error) {
	code, exp, err := c.Codes.Issue()
	if err != nil {
		return protocol.PairPayload{}, err
	}
	return protocol.PairPayload{
		V: 1, Host: c.Hostname, Addrs: c.Addrs(), Port: c.Cfg.Port,
		FP: c.Identity.Fingerprint, Code: code, ExpiresAt: exp.Unix(),
	}, nil
}

// IsClaude reports whether cmd launches Claude Code.
func IsClaude(cmd string) bool {
	base := filepath.Base(cmd)
	return base == "claude" || strings.HasPrefix(base, "claude-")
}

// CreateSession validates and spawns a session, injecting Claude Code flags
// when applicable.
func (c *Core) CreateSession(ctx context.Context, req protocol.SessionCreate) (*session.Session, error) {
	cwd, err := c.FS.Resolve(req.Cwd)
	if err != nil {
		return nil, err
	}
	cmd := req.Cmd
	if cmd == "" {
		cmd = c.Cfg.DefaultCommand
	}
	spec := session.Spec{Name: req.Name, Cwd: cwd, Cmd: cmd, Args: req.Args, Cols: req.Cols, Rows: req.Rows}
	if IsClaude(cmd) && !hasResumeFlag(req.Args) {
		spec.ClaudeSessionID = session.NewID()
		spec.ExtraArgs = []string{"--session-id", spec.ClaudeSessionID, "--settings", c.Paths.HooksFile}
	} else if IsClaude(cmd) {
		spec.ExtraArgs = []string{"--settings", c.Paths.HooksFile}
	}
	return c.Mgr.Create(ctx, spec)
}

func hasResumeFlag(args []string) bool {
	for _, a := range args {
		switch a {
		case "--resume", "-r", "--continue", "-c", "--session-id":
			return true
		}
	}
	return false
}

// ResumeSession relaunches a stale or exited session in the same folder.
// For Claude Code it resumes the same conversation. The old row is removed.
func (c *Core) ResumeSession(ctx context.Context, id string, cols, rows int) (*session.Session, error) {
	old, err := c.Mgr.Get(id)
	if err != nil {
		return nil, err
	}
	info := old.Info()
	if info.Status == protocol.StatusRunning || info.Status == protocol.StatusWaiting {
		return nil, session.ErrStillAlive
	}
	cwd, err := c.FS.Resolve(info.Cwd)
	if err != nil {
		return nil, err
	}
	spec := session.Spec{Name: info.Name, Cwd: cwd, Cmd: info.Cmd, Args: info.Args, Cols: cols, Rows: rows}
	if IsClaude(info.Cmd) {
		if info.ClaudeSessionID != "" {
			spec.ClaudeSessionID = info.ClaudeSessionID
			spec.ExtraArgs = []string{"--resume", info.ClaudeSessionID, "--settings", c.Paths.HooksFile}
		} else {
			spec.ExtraArgs = []string{"--continue", "--settings", c.Paths.HooksFile}
		}
	}
	s, err := c.Mgr.Create(ctx, spec)
	if err != nil {
		return nil, err
	}
	_ = c.Mgr.Remove(ctx, id)
	return s, nil
}

// ApplyHook updates a session's status from a Claude Code hook event.
func (c *Core) ApplyHook(sessionID, event string) bool {
	status := claude.StatusForHook(event)
	if status == "" {
		return false
	}
	s, err := c.Mgr.Get(sessionID)
	if err != nil {
		return false
	}
	return s.SetStatus(status)
}

// Conversations lists Claude Code transcripts for a folder under the roots.
func (c *Core) Conversations(cwd string) ([]protocol.Conversation, error) {
	real, err := c.FS.Resolve(cwd)
	if err != nil {
		return nil, err
	}
	return claude.Conversations(claude.ProjectsDir(c.Paths.Home), real, 20)
}
