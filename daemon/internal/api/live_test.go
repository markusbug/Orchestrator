//go:build live

package api

// Live end-to-end check against a running daemon started with --debug:
//
//	ORCH_LIVE_URL=wss://localhost:7399/ws ORCH_LIVE_CWD=/path/to/project \
//	  go test -tags live -run TestLive -v ./internal/api/
//
// It starts a real Claude Code session, sends a prompt, waits for the Stop
// hook to mark the session waiting, reconnects to verify scrollback replay,
// and kills the session. TestLiveStatus walks the status machine through
// what a person does on the phone: idle at start, typing, submitting, an
// interrupt, and a permission prompt.

import (
	"context"
	"crypto/tls"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/markusbug/Orchestrator/daemon/internal/auth"
	"github.com/markusbug/Orchestrator/daemon/internal/protocol"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07|\x1b[()][A-Za-z0-9]|\x1b[=>78]`)

func liveDial(t *testing.T, url string) *client {
	t.Helper()
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
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

func (c *client) waitOutputFor(want []string, timeout time.Duration) string {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, w := range want {
			if c.outputContains(w) {
				return w
			}
		}
		c.next(time.Until(deadline))
	}
	c.t.Fatalf("output never contained any of %v", want)
	return ""
}

func (c *client) plainOutput() string {
	var all []byte
	for _, f := range c.bin {
		if _, _, p, err := protocol.DecodeFrame(f); err == nil {
			all = append(all, p...)
		}
	}
	return ansi.ReplaceAllString(string(all), "")
}

func TestLive(t *testing.T) {
	url := os.Getenv("ORCH_LIVE_URL")
	cwd := os.Getenv("ORCH_LIVE_CWD")
	if url == "" || cwd == "" {
		t.Skip("set ORCH_LIVE_URL and ORCH_LIVE_CWD")
	}
	c := liveDial(t, url)
	c.nonce, _ = auth.NewNonce()
	var h protocol.HelloReply
	c.call("hello", protocol.Hello{Proto: 1, Name: "live-test", ClientNonce: b64(c.nonce)}, &h)
	if h.AuthNeeded {
		t.Fatal("daemon must run with --debug for this test (loopback auto-auth)")
	}
	t.Logf("connected to %s (%s) fp=%s", h.Host, h.Version, h.Fingerprint)

	var created protocol.SessionCreated
	c.call("session.create", protocol.SessionCreate{Cwd: cwd, Cols: 120, Rows: 40, Name: "live-e2e"}, &created)
	s := created.Session
	t.Logf("created session %s handle=%d cmd=%s claude_session=%s", s.ID, s.Handle, s.Cmd, s.ClaudeSessionID)
	if s.Cmd != "claude" || s.ClaudeSessionID == "" {
		t.Fatalf("expected a claude session: %+v", s)
	}
	c.call("session.attach", protocol.SessionAttach{ID: s.ID, Cols: 120, Rows: 40}, nil)
	hit := c.waitOutputFor([]string{"Claude Code", "Welcome", "for shortcuts", "Try \""}, 45*time.Second)
	t.Logf("TUI is up (matched %q)", hit)

	// give the TUI a moment, then send a prompt
	time.Sleep(2 * time.Second)
	c.input(s.Handle, "Reply with exactly the single word PONG and nothing else.")
	time.Sleep(500 * time.Millisecond)
	c.input(s.Handle, "\r")

	// Expect: prompt submit -> running (UserPromptSubmit hook), reply text
	// appears, then Stop hook -> waiting.
	deadline := time.Now().Add(120 * time.Second)
	last := ""
	sawRunning := false
	for time.Now().Before(deadline) {
		if _, err := c.next(time.Until(deadline)); err != nil {
			t.Fatal(err)
		}
		for ; c.seen < len(c.msgs); c.seen++ {
			m := c.msgs[c.seen]
			if m.T == "session.event" {
				var ev protocol.SessionEvent
				m.Decode(&ev)
				if ev.Session.ID == s.ID {
					last = ev.Session.Status
					if last == protocol.StatusRunning {
						sawRunning = true
					}
				}
			}
		}
		if sawRunning && last == protocol.StatusWaiting && c.outputContains("PONG") {
			break
		}
	}
	if !(sawRunning && last == protocol.StatusWaiting && c.outputContains("PONG")) {
		t.Fatalf("hooks: sawRunning=%v last=%s pong=%v; output tail:\n%s", sawRunning, last, c.outputContains("PONG"), tail(c.plainOutput(), 30))
	}
	t.Log("UserPromptSubmit -> running, reply visible, Stop -> waiting: hooks work")
	c.ws.Close(websocket.StatusNormalClosure, "")

	// reconnect and verify replay restores the screen
	c2 := liveDial(t, url)
	c2.nonce, _ = auth.NewNonce()
	c2.call("hello", protocol.Hello{Proto: 1, Name: "live-test-2", ClientNonce: b64(c2.nonce)}, nil)
	var list protocol.SessionListReply
	c2.call("session.list", nil, &list)
	var found *protocol.SessionInfo
	for i := range list.Sessions {
		if list.Sessions[i].ID == s.ID {
			found = &list.Sessions[i]
		}
	}
	if found == nil || found.Status != protocol.StatusWaiting {
		t.Fatalf("session after reconnect: %+v", found)
	}
	t.Logf("after reconnect: status=%s preview=%q", found.Status, found.Preview)
	c2.call("session.attach", protocol.SessionAttach{ID: s.ID, Cols: 120, Rows: 40}, nil)
	c2.waitOutputFor([]string{"PONG", "Claude Code", "Welcome"}, 15*time.Second)
	t.Logf("replay ok; screen tail:\n%s", tail(c2.plainOutput(), 25))

	c2.call("session.kill", protocol.SessionKill{ID: s.ID, Signal: "TERM"}, nil)
	deadline = time.Now().Add(15 * time.Second)
	exited := false
	for !exited && time.Now().Before(deadline) {
		if _, err := c2.next(time.Until(deadline)); err != nil {
			break
		}
		for ; c2.seen < len(c2.msgs); c2.seen++ {
			m := c2.msgs[c2.seen]
			if m.T == "session.event" {
				var ev protocol.SessionEvent
				m.Decode(&ev)
				if ev.Session.ID == s.ID && ev.Session.Status == protocol.StatusExited {
					exited = true
				}
			}
		}
	}
	if !exited {
		t.Fatal("session did not exit after TERM")
	}
	c2.call("session.remove", protocol.SessionRemove{ID: s.ID}, nil)
	t.Log("killed and removed")
}

func tail(s string, n int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	var keep []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			keep = append(keep, strings.TrimRight(l, " "))
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, "\n")
}

// statusAt returns the latest status/reason seen for session id in events
// received so far, and records every transition in order.
func (c *client) statusAt(id string) (status, reason string) {
	for ; c.seen < len(c.msgs); c.seen++ {
		m := c.msgs[c.seen]
		if m.T != "session.event" {
			continue
		}
		var ev protocol.SessionEvent
		m.Decode(&ev)
		if ev.Session.ID == id {
			c.trail = append(c.trail, ev.Session.Status+"/"+ev.Session.WaitReason)
			c.trailAt = append(c.trailAt, time.Now())
		}
	}
	if len(c.trail) == 0 {
		return "", ""
	}
	last := c.trail[len(c.trail)-1]
	i := strings.IndexByte(last, '/')
	return last[:i], last[i+1:]
}

// trailSince renders the transitions from index mark with their time.
func (c *client) trailSince(mark int) string {
	var parts []string
	for i := mark; i < len(c.trail); i++ {
		parts = append(parts, c.trailAt[i].Format("15:04:05.000")+" "+c.trail[i])
	}
	return strings.Join(parts, ", ")
}

// awaitStatus waits until the session reports status/reason, failing after
// timeout with the transitions seen so far.
func (c *client) awaitStatus(id, status, reason string, timeout time.Duration) {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		st, r := c.statusAt(id)
		if st == status && (reason == "" || r == reason) {
			return
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("waited %v for %s/%s, transitions: %v; screen tail:\n%s", timeout, status, reason, c.trail, tail(c.plainOutput(), 20))
		}
		if _, err := c.next(time.Until(deadline)); err != nil {
			c.t.Fatalf("read: %v (transitions so far: %v)", err, c.trail)
		}
	}
}

// holdStatus checks the status is still status/reason after window. A read
// that times out closes the socket, so instead of polling it sleeps, then
// asks for the session list: every event sent before that reply has been
// received once the reply is in.
func (c *client) holdStatus(id, status, reason string, window time.Duration) {
	c.t.Helper()
	time.Sleep(window)
	c.call("session.list", nil, nil)
	if st, r := c.statusAt(id); st != status || r != reason {
		c.t.Fatalf("status moved to %s/%s, want %s/%s; transitions: %v", st, r, status, reason, c.trail)
	}
}

func TestLiveStatus(t *testing.T) {
	url := os.Getenv("ORCH_LIVE_URL")
	cwd := os.Getenv("ORCH_LIVE_CWD")
	if url == "" || cwd == "" {
		t.Skip("set ORCH_LIVE_URL and ORCH_LIVE_CWD")
	}
	c := liveDial(t, url)
	c.nonce, _ = auth.NewNonce()
	var h protocol.HelloReply
	c.call("hello", protocol.Hello{Proto: 1, Name: "live-status", ClientNonce: b64(c.nonce)}, &h)
	if h.AuthNeeded {
		t.Fatal("daemon must run with --debug for this test (loopback auto-auth)")
	}
	var created protocol.SessionCreated
	c.call("session.create", protocol.SessionCreate{Cwd: cwd, Cols: 120, Rows: 40, Name: "live-status",
		Args: []string{"--permission-mode", "default"}}, &created)
	s := created.Session
	id := s.ID
	t.Cleanup(func() {
		c.call("session.kill", protocol.SessionKill{ID: id, Signal: "KILL"}, nil)
		c.awaitStatus(id, protocol.StatusExited, "", 15*time.Second)
		c.call("session.remove", protocol.SessionRemove{ID: id}, nil)
	})
	c.call("session.attach", protocol.SessionAttach{ID: id, Cols: 120, Rows: 40}, nil)

	// 1. Up and idle: SessionStart marks it waiting/idle before any prompt.
	c.awaitStatus(id, protocol.StatusWaiting, protocol.WaitIdle, 45*time.Second)
	t.Log("start: waiting/idle")

	// 2. Terminal replies, scrolling and typing a prompt do not move an idle session.
	c.input(s.Handle, "\x1b[?1;2c")
	c.input(s.Handle, "\x1b[<64;10;5M")
	c.input(s.Handle, "Reply with exactly the single word PONG and nothing else.")
	c.holdStatus(id, protocol.StatusWaiting, protocol.WaitIdle, 1500*time.Millisecond)
	t.Log("typing: still waiting/idle")

	// 3. Submit: running, and it stays running until Stop despite the title
	//    animation, then waiting/idle exactly once.
	mark := len(c.trail)
	c.input(s.Handle, "\r")
	c.awaitStatus(id, protocol.StatusRunning, "", 5*time.Second)
	c.awaitStatus(id, protocol.StatusWaiting, protocol.WaitIdle, 90*time.Second)
	if got := strings.Join(c.trail[mark:], " "); got != "running/ waiting/idle" {
		t.Fatalf("turn transitions: %q", got)
	}
	if !c.outputContains("PONG") {
		t.Fatalf("no PONG on screen:\n%s", tail(c.plainOutput(), 20))
	}
	t.Log("turn: running -> waiting/idle, no flicker")

	// 4. Interrupt: Escape during a long reply fires no hook, but must idle.
	c.input(s.Handle, "Write the numbers from 1 to 2000, one per line, no commentary.")
	time.Sleep(300 * time.Millisecond)
	c.input(s.Handle, "\r")
	c.awaitStatus(id, protocol.StatusRunning, "", 5*time.Second)
	time.Sleep(8 * time.Second) // let it stream for a while
	c.call("session.list", nil, nil)
	if st, _ := c.statusAt(id); st != protocol.StatusRunning {
		t.Fatalf("turn ended before the interrupt could be tested: %v", c.trail)
	}
	mark = len(c.trail)
	c.input(s.Handle, "\x1b")
	c.awaitStatus(id, protocol.StatusWaiting, protocol.WaitIdle, 3*time.Second)
	c.holdStatus(id, protocol.StatusWaiting, protocol.WaitIdle, 4*time.Second)
	t.Logf("interrupt: %s", c.trailSince(mark))

	// 5. Permission: waiting/input the moment the dialog opens, running as
	//    soon as Enter approves it, idle after the turn.
	mark = len(c.trail)
	c.input(s.Handle, "Run this exact bash command and then reply DONE: rm -rf /tmp/zz_orc_live_nonexistent")
	time.Sleep(300 * time.Millisecond)
	c.input(s.Handle, "\r")
	c.awaitStatus(id, protocol.StatusRunning, "", 5*time.Second)
	c.awaitStatus(id, protocol.StatusWaiting, protocol.WaitInput, 90*time.Second)
	t.Logf("permission: %s", c.trailSince(mark))
	// Navigating the dialog is not an answer.
	c.input(s.Handle, "\x1b[B")
	c.input(s.Handle, "\x1b[A")
	c.holdStatus(id, protocol.StatusWaiting, protocol.WaitInput, 1500*time.Millisecond)
	mark = len(c.trail)
	c.input(s.Handle, "\r")
	c.awaitStatus(id, protocol.StatusRunning, "", 2*time.Second)
	c.awaitStatus(id, protocol.StatusWaiting, protocol.WaitIdle, 90*time.Second)
	if !c.outputContains("DONE") {
		t.Fatalf("no DONE on screen:\n%s", tail(c.plainOutput(), 20))
	}
	t.Logf("approve: %s", c.trailSince(mark))
}
