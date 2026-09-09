// Package session spawns and supervises PTY-backed terminal sessions.
package session

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/markusbug/Orchestrator/daemon/internal/protocol"
	"github.com/markusbug/Orchestrator/daemon/internal/ringbuf"
	"github.com/markusbug/Orchestrator/daemon/internal/store"
	"github.com/markusbug/Orchestrator/daemon/internal/termscan"
)

// Errors.
var (
	ErrNotFound   = errors.New("session: not found")
	ErrNotRunning = errors.New("session: not running")
	ErrStillAlive = errors.New("session: still running")
)

// Default limits.
const (
	DefaultScrollback    = 1 << 20
	DefaultSubscriberMax = 4 << 20
	MaxChunk             = 32 * 1024
)

// Options configure a Manager.
type Options struct {
	ScrollbackBytes int
	SubscriberMax   int
	ExtraPath       []string          // appended to PATH as fallbacks when resolving commands
	Env             map[string]string // always set on spawned processes
	Store           *store.Store      // optional persistence
	Logger          *slog.Logger
}

// Spec describes a session to create.
type Spec struct {
	Name            string
	Cwd             string
	Cmd             string
	Args            []string // user-visible args (persisted)
	ExtraArgs       []string // injected before Args (not persisted)
	Env             map[string]string
	Cols, Rows      int
	ClaudeSessionID string
	// Hooked marks a session whose running/waiting status is driven by
	// Claude Code hooks. Output bells and ordinary typing are then ignored;
	// only hooks, an interrupt, and answering a pending prompt move it.
	Hooked bool
}

// Event is emitted on session changes.
type Event struct {
	Kind    string // "changed" | "removed"
	Session protocol.SessionInfo
}

// Manager owns all sessions.
type Manager struct {
	opts       Options
	log        *slog.Logger
	mu         sync.Mutex
	sessions   map[string]*Session
	byHandle   map[uint32]*Session
	nextHandle uint32
	listeners  map[int]func(Event)
	nextListen int
	stopping   atomic.Bool
}

// NewManager creates a Manager.
func NewManager(opts Options) *Manager {
	if opts.ScrollbackBytes <= 0 {
		opts.ScrollbackBytes = DefaultScrollback
	}
	if opts.SubscriberMax <= 0 {
		opts.SubscriberMax = DefaultSubscriberMax
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Manager{opts: opts, log: opts.Logger, sessions: map[string]*Session{}, byHandle: map[uint32]*Session{}, nextHandle: 1, listeners: map[int]func(Event){}}
}

// Subscribe registers an event listener; the returned func removes it.
// Listeners must not block.
func (m *Manager) Subscribe(fn func(Event)) func() {
	m.mu.Lock()
	id := m.nextListen
	m.nextListen++
	m.listeners[id] = fn
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.listeners, id)
		m.mu.Unlock()
	}
}

func (m *Manager) emit(ev Event) {
	m.mu.Lock()
	fns := make([]func(Event), 0, len(m.listeners))
	for _, f := range m.listeners {
		fns = append(fns, f)
	}
	m.mu.Unlock()
	for _, f := range fns {
		f(ev)
	}
}

// NewID returns a random UUID v4 string.
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// LoadStale registers sessions from the store as stale (no process attached).
func (m *Manager) LoadStale(ctx context.Context) error {
	if m.opts.Store == nil {
		return nil
	}
	if _, err := m.opts.Store.MarkAllStale(ctx); err != nil {
		return err
	}
	rows, err := m.opts.Store.ListSessions(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rows {
		if _, ok := m.sessions[r.ID]; ok {
			continue
		}
		s := &Session{ID: r.ID, mgr: m, name: r.Name, cwd: r.Cwd, cmd: r.Cmd, args: r.Args,
			status: r.Status, exitCode: r.ExitCode, claudeSessionID: r.ClaudeSessionID,
			createdAt: r.CreatedAt, lastOutputAt: r.LastOutputAt, cols: r.Cols, rows: r.Rows,
			ring: ringbuf.New(1), subs: map[*Subscriber]struct{}{}, done: make(chan struct{})}
		close(s.done)
		s.Handle = m.nextHandle
		m.nextHandle++
		m.sessions[s.ID] = s
		m.byHandle[s.Handle] = s
	}
	return nil
}

// Create spawns a new session.
func (m *Manager) Create(ctx context.Context, spec Spec) (*Session, error) {
	if spec.Cols <= 0 {
		spec.Cols = 80
	}
	if spec.Rows <= 0 {
		spec.Rows = 24
	}
	if spec.Cmd == "" {
		return nil, errors.New("session: command required")
	}
	cwd, err := filepath.Abs(spec.Cwd)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("session: cwd %q is not a directory", cwd)
	}
	env := m.buildEnv(spec.Env)
	path, err := lookPath(spec.Cmd, envValue(env, "PATH"))
	if err != nil {
		return nil, fmt.Errorf("session: command %q not found: %w", spec.Cmd, err)
	}
	name := spec.Name
	if name == "" {
		name = filepath.Base(cwd)
	}
	s := &Session{
		ID: NewID(), mgr: m, name: name, cwd: cwd, cmd: spec.Cmd, args: append([]string(nil), spec.Args...),
		status: protocol.StatusRunning, claudeSessionID: spec.ClaudeSessionID, hooked: spec.Hooked,
		createdAt: time.Now(), lastOutputAt: time.Now(), cols: spec.Cols, rows: spec.Rows,
		ring: ringbuf.New(m.opts.ScrollbackBytes), subs: map[*Subscriber]struct{}{}, done: make(chan struct{}),
	}
	args := append(append([]string(nil), spec.ExtraArgs...), spec.Args...)
	c := exec.Command(path, args...)
	c.Dir = cwd
	c.Env = append(env, "ORCHESTRATOR_SESSION_ID="+s.ID)
	f, err := pty.StartWithSize(c, &pty.Winsize{Cols: uint16(spec.Cols), Rows: uint16(spec.Rows)})
	if err != nil {
		return nil, fmt.Errorf("session: spawn: %w", err)
	}
	s.pty, s.proc = f, c
	s.pid = c.Process.Pid

	m.mu.Lock()
	s.Handle = m.nextHandle
	m.nextHandle++
	m.sessions[s.ID] = s
	m.byHandle[s.Handle] = s
	m.mu.Unlock()

	if m.opts.Store != nil {
		if err := m.opts.Store.PutSession(ctx, s.row()); err != nil {
			m.log.Warn("persist session", "err", err)
		}
	}
	go s.readLoop()
	m.emit(Event{Kind: "changed", Session: s.Info()})
	return s, nil
}

func (m *Manager) buildEnv(extra map[string]string) []string {
	base := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && !inheritedClaudeMarker(k) {
			base[k] = v
		}
	}
	defaults := map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}
	if base["LANG"] == "" && base["LC_ALL"] == "" {
		defaults["LANG"] = "C.UTF-8"
	}
	for k, v := range defaults {
		if base[k] == "" {
			base[k] = v
		}
	}
	for k, v := range m.opts.Env {
		base[k] = v
	}
	if len(m.opts.ExtraPath) > 0 {
		parts := append(strings.Split(base["PATH"], string(os.PathListSeparator)), m.opts.ExtraPath...)
		base["PATH"] = strings.Join(dedupe(parts), string(os.PathListSeparator))
	}
	for k, v := range extra {
		base[k] = v
	}
	keys := make([]string, 0, len(base))
	for k := range base {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+base[k])
	}
	return out
}

// inheritedClaudeMarker reports env vars that mark a process as running
// inside another Claude Code session. They must not leak into spawned
// sessions when the daemon itself was started from a Claude Code terminal.
func inheritedClaudeMarker(k string) bool {
	switch k {
	case "CLAUDECODE", "CLAUDE_PID", "CLAUDE_EFFORT",
		"CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_PARENT_SESSION_ID",
		"CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_EXECPATH", "CLAUDE_CODE_SSE_PORT":
		return true
	}
	return strings.HasPrefix(k, "CLAUDE_CODE_MESSAGING_") || strings.HasPrefix(k, "CLAUDE_CODE_BRIDGE_")
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

func lookPath(cmd, path string) (string, error) {
	if strings.Contains(cmd, string(os.PathSeparator)) {
		abs, err := filepath.Abs(cmd)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(abs); err != nil {
			return "", err
		}
		return abs, nil
	}
	for _, dir := range strings.Split(path, string(os.PathListSeparator)) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, cmd)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return exec.LookPath(cmd)
}

// Get returns a session by id.
func (m *Manager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}
	return s, nil
}

// ByHandle returns a session by its per-run handle.
func (m *Manager) ByHandle(h uint32) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byHandle[h]
	return s, ok
}

// List returns all sessions, waiting first, then running, exited, stale;
// newest activity first within each group.
func (m *Manager) List() []protocol.SessionInfo {
	m.mu.Lock()
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	infos := make([]protocol.SessionInfo, 0, len(all))
	for _, s := range all {
		infos = append(infos, s.Info())
	}
	rank := map[string]int{protocol.StatusWaiting: 0, protocol.StatusRunning: 1, protocol.StatusExited: 2, protocol.StatusStale: 3}
	sort.Slice(infos, func(i, j int) bool {
		if rank[infos[i].Status] != rank[infos[j].Status] {
			return rank[infos[i].Status] < rank[infos[j].Status]
		}
		return infos[i].LastOutputAt > infos[j].LastOutputAt
	})
	return infos
}

// Remove forgets an exited or stale session.
func (m *Manager) Remove(ctx context.Context, id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	s.mu.Lock()
	alive := s.status == protocol.StatusRunning || s.status == protocol.StatusWaiting
	s.mu.Unlock()
	if alive {
		m.mu.Unlock()
		return ErrStillAlive
	}
	delete(m.sessions, id)
	delete(m.byHandle, s.Handle)
	m.mu.Unlock()
	if m.opts.Store != nil {
		_ = m.opts.Store.DeleteSession(ctx, id)
	}
	m.emit(Event{Kind: "removed", Session: s.Info()})
	return nil
}

// Shutdown terminates all running sessions. Sessions that die this way are
// recorded as stale (the daemon went away), not exited, so they can be resumed.
func (m *Manager) Shutdown(timeout time.Duration) {
	m.stopping.Store(true)
	m.mu.Lock()
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	for _, s := range all {
		_ = s.Kill("HUP")
	}
	deadline := time.After(timeout)
	for _, s := range all {
		select {
		case <-s.done:
		case <-deadline:
			_ = s.Kill("KILL")
		}
	}
}

// Session is one PTY-backed process.
type Session struct {
	ID     string
	Handle uint32
	mgr    *Manager

	mu              sync.Mutex
	name, cwd, cmd  string
	args            []string
	pid             int
	status          string
	waitReason      string
	hooked          bool
	bell            termscan.Bell // fed from readLoop only
	exitCode        *int
	claudeSessionID string
	createdAt       time.Time
	lastOutputAt    time.Time
	cols, rows      int
	pty             *os.File
	proc            *exec.Cmd
	ring            *ringbuf.Buffer
	subs            map[*Subscriber]struct{}
	done            chan struct{}
}

func (s *Session) row() store.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return store.Session{ID: s.ID, Name: s.name, Cwd: s.cwd, Cmd: s.cmd, Args: s.args, PID: s.pid,
		Status: s.status, ExitCode: s.exitCode, ClaudeSessionID: s.claudeSessionID,
		CreatedAt: s.createdAt, LastOutputAt: s.lastOutputAt, Cols: s.cols, Rows: s.rows}
}

// Info returns a snapshot for clients.
func (s *Session) Info() protocol.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return protocol.SessionInfo{ID: s.ID, Handle: s.Handle, Name: s.name, Cwd: s.cwd, Cmd: s.cmd,
		Args: append([]string{}, s.args...), PID: s.pid, Status: s.status, WaitReason: s.waitReason, ExitCode: s.exitCode,
		ClaudeSessionID: s.claudeSessionID, CreatedAt: s.createdAt.UnixMilli(),
		LastOutputAt: s.lastOutputAt.UnixMilli(), Cols: s.cols, Rows: s.rows, Preview: preview(s.ring)}
}

// Done is closed when the process has exited (or immediately for stale sessions).
func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) readLoop() {
	buf := make([]byte, MaxChunk)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			s.ring.Write(chunk)
			bell := s.bell.Feed(chunk) > 0
			s.mu.Lock()
			s.lastOutputAt = time.Now()
			changed := false
			// A real bell means "look at me" for programs we know nothing
			// about. Hooked sessions say so through hooks instead, and a bell
			// from a command Claude runs must not masquerade as a question.
			if bell && !s.hooked && s.status == protocol.StatusRunning {
				s.status = protocol.StatusWaiting
				s.waitReason = protocol.WaitInput
				changed = true
			}
			subs := make([]*Subscriber, 0, len(s.subs))
			for sub := range s.subs {
				subs = append(subs, sub)
			}
			s.mu.Unlock()
			for _, sub := range subs {
				if !sub.push(chunk) {
					s.Detach(sub)
				}
			}
			if changed {
				s.mgr.emit(Event{Kind: "changed", Session: s.Info()})
			}
		}
		if err != nil {
			break
		}
	}
	err := s.proc.Wait()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
			if code == -1 {
				code = 128
			}
		} else {
			code = 1
		}
	}
	final := protocol.StatusExited
	if s.mgr.stopping.Load() {
		final = protocol.StatusStale
	}
	s.mu.Lock()
	s.status = final
	s.waitReason = ""
	s.exitCode = &code
	s.pty.Close()
	subs := make([]*Subscriber, 0, len(s.subs))
	for sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()
	for _, sub := range subs {
		sub.close(ErrSubscriberClosed)
	}
	if s.mgr.opts.Store != nil {
		_ = s.mgr.opts.Store.UpdateSessionStatus(context.Background(), s.ID, final, &code)
		_ = s.mgr.opts.Store.TouchSession(context.Background(), s.ID, time.Now())
	}
	// Notify listeners before releasing Done so anyone waiting on Done
	// observes the final status and its event.
	s.mgr.emit(Event{Kind: "changed", Session: s.Info()})
	close(s.done)
}

// Attach registers a subscriber and returns it with a scrollback snapshot
// taken atomically with the registration.
func (s *Session) Attach() (*Subscriber, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == protocol.StatusStale {
		return nil, nil, ErrNotRunning
	}
	sub := newSubscriber(s.mgr.opts.SubscriberMax)
	snap := s.ring.Snapshot()
	if s.status == protocol.StatusExited {
		sub.close(ErrSubscriberClosed)
		return sub, snap, nil
	}
	s.subs[sub] = struct{}{}
	return sub, snap, nil
}

// Detach removes a subscriber.
func (s *Session) Detach(sub *Subscriber) {
	s.mu.Lock()
	delete(s.subs, sub)
	s.mu.Unlock()
	sub.close(ErrSubscriberClosed)
}

// Write sends input to the process and lets the status follow what the
// person did. Reports the terminal emulator generates on its own (device
// attributes, cursor position, mouse, focus) never count as input.
//
// For hooked sessions a bare Escape or Ctrl-C interrupts the turn, which
// fires no hook, so it marks the session idle; an answering key while a
// prompt is pending marks it running (the tool proceeds long before its
// PostToolUse hook); ordinary typing changes nothing, because Claude is
// still waiting until the prompt is submitted. Other sessions go back to
// running on any real keystroke, as before.
func (s *Session) Write(p []byte) error {
	kind := termscan.Classify(p)
	s.mu.Lock()
	if s.pty == nil || (s.status != protocol.StatusRunning && s.status != protocol.StatusWaiting) {
		s.mu.Unlock()
		return ErrNotRunning
	}
	changed := false
	switch {
	case kind == termscan.Report:
	case !s.hooked:
		changed = s.setLocked(protocol.StatusRunning, "")
	case kind == termscan.Interrupt:
		changed = s.setLocked(protocol.StatusWaiting, protocol.WaitIdle)
	case kind == termscan.Answer && s.status == protocol.StatusWaiting && s.waitReason == protocol.WaitInput:
		changed = s.setLocked(protocol.StatusRunning, "")
	}
	f := s.pty
	s.mu.Unlock()
	_, err := f.Write(p)
	if changed {
		s.persistStatus()
		s.mgr.emit(Event{Kind: "changed", Session: s.Info()})
	}
	return err
}

// Resize sets the terminal size. If nudge is set and the size is unchanged,
// a transient resize forces SIGWINCH so the TUI redraws.
func (s *Session) Resize(cols, rows int, nudge bool) error {
	if cols <= 0 || rows <= 0 {
		return errors.New("session: bad size")
	}
	s.mu.Lock()
	f := s.pty
	same := s.cols == cols && s.rows == rows
	s.cols, s.rows = cols, rows
	alive := s.status == protocol.StatusRunning || s.status == protocol.StatusWaiting
	s.mu.Unlock()
	if f == nil || !alive {
		return ErrNotRunning
	}
	if same && nudge {
		if err := pty.Setsize(f, &pty.Winsize{Cols: uint16(cols - 1), Rows: uint16(rows)}); err != nil {
			return err
		}
	}
	return pty.Setsize(f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Kill sends a signal (TERM default; also KILL, INT, HUP).
func (s *Session) Kill(sig string) error {
	s.mu.Lock()
	p := s.proc
	alive := s.status == protocol.StatusRunning || s.status == protocol.StatusWaiting
	s.mu.Unlock()
	if p == nil || p.Process == nil || !alive {
		return ErrNotRunning
	}
	var sg syscall.Signal
	switch strings.ToUpper(strings.TrimPrefix(sig, "SIG")) {
	case "", "TERM":
		sg = syscall.SIGTERM
	case "KILL":
		sg = syscall.SIGKILL
	case "INT":
		sg = syscall.SIGINT
	case "HUP":
		sg = syscall.SIGHUP
	default:
		return fmt.Errorf("session: unknown signal %q", sig)
	}
	return p.Process.Signal(sg)
}

// Rename changes the display name.
func (s *Session) Rename(ctx context.Context, name string) {
	s.mu.Lock()
	s.name = name
	s.mu.Unlock()
	if s.mgr.opts.Store != nil {
		_ = s.mgr.opts.Store.UpdateSessionName(ctx, s.ID, name)
	}
	s.mgr.emit(Event{Kind: "changed", Session: s.Info()})
}

// SetStatus applies a running/waiting transition from an external signal
// (Claude Code hooks). reason qualifies a waiting status (protocol.WaitIdle
// or protocol.WaitInput) and is ignored for running. It is ignored unless
// the session is alive, and reports whether anything changed.
func (s *Session) SetStatus(status, reason string) bool {
	if status != protocol.StatusRunning && status != protocol.StatusWaiting {
		return false
	}
	if status == protocol.StatusRunning {
		reason = ""
	} else if reason != protocol.WaitIdle && reason != protocol.WaitInput {
		reason = protocol.WaitInput
	}
	s.mu.Lock()
	changed := s.setLocked(status, reason)
	s.mu.Unlock()
	if changed {
		s.persistStatus()
		s.mgr.emit(Event{Kind: "changed", Session: s.Info()})
	}
	return changed
}

// setLocked moves an alive session to status/reason. Caller holds s.mu.
func (s *Session) setLocked(status, reason string) bool {
	alive := s.status == protocol.StatusRunning || s.status == protocol.StatusWaiting
	if !alive || (s.status == status && s.waitReason == reason) {
		return false
	}
	s.status, s.waitReason = status, reason
	return true
}

func (s *Session) persistStatus() {
	if s.mgr.opts.Store == nil {
		return
	}
	s.mu.Lock()
	st, code := s.status, s.exitCode
	s.mu.Unlock()
	_ = s.mgr.opts.Store.UpdateSessionStatus(context.Background(), s.ID, st, code)
}

// Scrollback returns a copy of the retained output.
func (s *Session) Scrollback() []byte { return s.ring.Snapshot() }

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?<=>]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][A-Za-z0-9]|\x1b[=>]|\x1b[78]|[\x00-\x08\x0b-\x1f\x7f]`)

// preview extracts the last non-empty plain-text line from scrollback.
func preview(r *ringbuf.Buffer) string {
	if r == nil || r.Len() == 0 {
		return ""
	}
	snap := r.Snapshot()
	if len(snap) > 4096 {
		snap = snap[len(snap)-4096:]
	}
	text := ansiRe.ReplaceAllString(string(snap), "")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		l = strings.TrimFunc(l, func(r rune) bool { return r < 32 })
		if len(l) > 120 {
			l = l[:120]
		}
		return strings.ToValidUTF8(l, "")
	}
	return ""
}

var _ io.Writer = (*ringbuf.Buffer)(nil)
