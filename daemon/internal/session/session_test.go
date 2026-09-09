package session

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/markusbug/Orchestrator/daemon/internal/protocol"
)

func waitFor(t *testing.T, sub *Subscriber, want string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var acc bytes.Buffer
	for !strings.Contains(acc.String(), want) {
		b, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("waiting for %q, got %q, err %v", want, acc.String(), err)
		}
		acc.Write(b)
	}
	return acc.String()
}

func TestSpawnAttachWriteKill(t *testing.T) {
	m := NewManager(Options{})
	var events []Event
	var emu sync.Mutex
	m.Subscribe(func(e Event) { emu.Lock(); events = append(events, e); emu.Unlock() })
	s, err := m.Create(context.Background(), Spec{Cwd: t.TempDir(), Cmd: "sh", Args: []string{"-c", "echo ready; cat"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if s.Handle == 0 || s.ID == "" || s.Info().Status != protocol.StatusRunning || s.Info().PID == 0 {
		t.Fatalf("%+v", s.Info())
	}
	sub, snap, err := s.Attach()
	if err != nil {
		t.Fatal(err)
	}
	out := string(snap) + waitFor(t, sub, "ready")
	if !strings.Contains(out, "ready") {
		t.Fatalf("no ready: %q", out)
	}
	if err := s.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sub, "hello")
	if err := s.Resize(100, 30, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Resize(100, 30, true); err != nil {
		t.Fatal("nudge resize failed:", err)
	}
	if err := s.Kill("TERM"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("did not exit")
	}
	info := s.Info()
	if info.Status != protocol.StatusExited || info.ExitCode == nil {
		t.Fatalf("%+v", info)
	}
	// pending output is delivered before the terminal error
	var nerr error
	for nerr == nil {
		_, nerr = sub.Next(context.Background())
	}
	if nerr != ErrSubscriberClosed {
		t.Fatalf("want closed, got %v", nerr)
	}
	if err := s.Write([]byte("x")); err != ErrNotRunning {
		t.Fatal(err)
	}
	if s.Info().Preview == "" {
		t.Fatal("preview empty")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		emu.Lock()
		n, last := len(events), events[len(events)-1]
		emu.Unlock()
		if n >= 2 && last.Session.Status == protocol.StatusExited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no exit event; last %+v", last)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := m.Remove(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(s.ID); err != ErrNotFound {
		t.Fatal("not removed")
	}
}

func TestLateAttachReplays(t *testing.T) {
	m := NewManager(Options{})
	s, err := m.Create(context.Background(), Spec{Cwd: t.TempDir(), Cmd: "sh", Args: []string{"-c", "echo early-output; cat"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Kill("KILL")
	deadline := time.Now().Add(5 * time.Second)
	for !bytes.Contains(s.Scrollback(), []byte("early-output")) {
		if time.Now().After(deadline) {
			t.Fatal("output never arrived")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, snap, err := s.Attach()
	if err != nil || !bytes.Contains(snap, []byte("early-output")) {
		t.Fatalf("snapshot %q err %v", snap, err)
	}
}

func TestExitCode(t *testing.T) {
	m := NewManager(Options{})
	s, err := m.Create(context.Background(), Spec{Cwd: t.TempDir(), Cmd: "sh", Args: []string{"-c", "exit 3"}})
	if err != nil {
		t.Fatal(err)
	}
	<-s.Done()
	if c := s.Info().ExitCode; c == nil || *c != 3 {
		t.Fatalf("exit %v", c)
	}
	if err := m.Remove(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveRefusesAlive(t *testing.T) {
	m := NewManager(Options{})
	s, _ := m.Create(context.Background(), Spec{Cwd: t.TempDir(), Cmd: "sh", Args: []string{"-c", "cat"}})
	defer s.Kill("KILL")
	if err := m.Remove(context.Background(), s.ID); err != ErrStillAlive {
		t.Fatal(err)
	}
}

func TestSlowSubscriberDropped(t *testing.T) {
	m := NewManager(Options{SubscriberMax: 1024})
	s, err := m.Create(context.Background(), Spec{Cwd: t.TempDir(), Cmd: "sh", Args: []string{"-c", "yes | head -c 100000; sleep 5"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Kill("KILL")
	sub, _, _ := s.Attach()
	deadline := time.Now().Add(5 * time.Second)
	for sub.Err() == nil {
		if time.Now().After(deadline) {
			t.Fatal("subscriber never dropped")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sub.Err() != ErrSubscriberDropped {
		t.Fatal(sub.Err())
	}
	if _, err := sub.Next(context.Background()); err != ErrSubscriberDropped {
		t.Fatal(err)
	}
}

func TestStatusTransitions(t *testing.T) {
	m := NewManager(Options{})
	s, _ := m.Create(context.Background(), Spec{Cwd: t.TempDir(), Cmd: "sh", Args: []string{"-c", "printf 'a\\007'; cat"}})
	defer s.Kill("KILL")
	deadline := time.Now().Add(5 * time.Second)
	for s.Info().Status != protocol.StatusWaiting {
		if time.Now().After(deadline) {
			t.Fatal("bell did not set waiting")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.Info().WaitReason != protocol.WaitInput {
		t.Fatalf("bell reason %q", s.Info().WaitReason)
	}
	// Terminal replies and mouse reports are not the person answering.
	s.Write([]byte("\x1b[?1;2c"))
	s.Write([]byte("\x1b[<64;3;4M"))
	if s.Info().Status != protocol.StatusWaiting {
		t.Fatal("terminal report cleared waiting")
	}
	s.Write([]byte("x"))
	if st := s.Info(); st.Status != protocol.StatusRunning || st.WaitReason != "" {
		t.Fatalf("input did not clear waiting: %+v", st)
	}
	if !s.SetStatus(protocol.StatusWaiting, protocol.WaitIdle) || s.Info().Status != protocol.StatusWaiting {
		t.Fatal("SetStatus")
	}
	if s.SetStatus(protocol.StatusWaiting, protocol.WaitIdle) {
		t.Fatal("repeat SetStatus reported a change")
	}
	if s.SetStatus("bogus", "") {
		t.Fatal("bogus status accepted")
	}
}

// waitUntil polls until cond holds or the deadline passes.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHookedSessionIgnoresBellsAndTyping(t *testing.T) {
	m := NewManager(Options{})
	// Claude Code rewrites the title through OSC … BEL constantly, and a
	// command it runs may ring a real bell; neither is a question for us.
	s, _ := m.Create(context.Background(), Spec{Cwd: t.TempDir(), Cmd: "sh", Hooked: true,
		Args: []string{"-c", "printf '\\033]0;title\\007real\\007'; cat"}})
	defer s.Kill("KILL")
	waitUntil(t, "no output", func() bool { return strings.Contains(string(s.Scrollback()), "real") })
	if s.Info().Status != protocol.StatusRunning {
		t.Fatal("bell moved a hooked session")
	}
	// Idle after Stop: typing a prompt keeps it waiting; only the hook starts a turn.
	s.SetStatus(protocol.StatusWaiting, protocol.WaitIdle)
	for _, in := range []string{"hello", "\x1b[A", "\r", "\x1b[?1;2c", "\x1b[<64;3;4M", "y", "1"} {
		s.Write([]byte(in))
		if st := s.Info(); st.Status != protocol.StatusWaiting || st.WaitReason != protocol.WaitIdle {
			t.Fatalf("input %q moved an idle hooked session: %+v", in, st)
		}
	}
	s.SetStatus(protocol.StatusRunning, "")
	if st := s.Info(); st.Status != protocol.StatusRunning || st.WaitReason != "" {
		t.Fatalf("%+v", st)
	}
	// Running: typing and reports change nothing; Escape interrupts to idle.
	for _, in := range []string{"x", "\r", "\x1b[?1;2c", "\x1b[A"} {
		s.Write([]byte(in))
		if s.Info().Status != protocol.StatusRunning {
			t.Fatalf("input %q moved a running hooked session", in)
		}
	}
	s.Write([]byte("\x1b"))
	if st := s.Info(); st.Status != protocol.StatusWaiting || st.WaitReason != protocol.WaitIdle {
		t.Fatalf("escape did not idle: %+v", st)
	}
	s.SetStatus(protocol.StatusRunning, "")
	s.Write([]byte("\x03"))
	if st := s.Info(); st.Status != protocol.StatusWaiting || st.WaitReason != protocol.WaitIdle {
		t.Fatalf("ctrl-c did not idle: %+v", st)
	}
	// A pending permission: navigation and reports keep waiting, Enter answers.
	s.SetStatus(protocol.StatusWaiting, protocol.WaitInput)
	for _, in := range []string{"\x1b[B", "\x1b[?1;2c", "\x1b[<64;3;4M"} {
		s.Write([]byte(in))
		if st := s.Info(); st.Status != protocol.StatusWaiting || st.WaitReason != protocol.WaitInput {
			t.Fatalf("input %q moved a pending prompt: %+v", in, st)
		}
	}
	s.Write([]byte("\r"))
	if st := s.Info(); st.Status != protocol.StatusRunning {
		t.Fatalf("enter did not answer: %+v", st)
	}
	s.SetStatus(protocol.StatusWaiting, protocol.WaitInput)
	s.Write([]byte("\x1b"))
	if st := s.Info(); st.Status != protocol.StatusWaiting || st.WaitReason != protocol.WaitIdle {
		t.Fatalf("escape on a prompt did not idle: %+v", st)
	}
	// Reason changes are events too.
	s.SetStatus(protocol.StatusWaiting, protocol.WaitInput)
	if !s.SetStatus(protocol.StatusWaiting, protocol.WaitIdle) {
		t.Fatal("reason change not reported")
	}
}

func TestBadSpecs(t *testing.T) {
	m := NewManager(Options{})
	if _, err := m.Create(context.Background(), Spec{Cwd: "/nonexistent-dir-xyz", Cmd: "sh"}); err == nil {
		t.Fatal("bad cwd accepted")
	}
	if _, err := m.Create(context.Background(), Spec{Cwd: t.TempDir(), Cmd: "definitely-not-a-command-xyz"}); err == nil {
		t.Fatal("bad cmd accepted")
	}
}

func TestEnvAndListOrder(t *testing.T) {
	m := NewManager(Options{Env: map[string]string{"FOO": "bar"}})
	s, err := m.Create(context.Background(), Spec{Cwd: t.TempDir(), Cmd: "sh", Args: []string{"-c", "echo TERM=$TERM FOO=$FOO SID=$ORCHESTRATOR_SESSION_ID"}})
	if err != nil {
		t.Fatal(err)
	}
	<-s.Done()
	out := string(s.Scrollback())
	if !strings.Contains(out, "TERM=xterm-256color") || !strings.Contains(out, "FOO=bar") || !strings.Contains(out, "SID="+s.ID) {
		t.Fatalf("env: %q", out)
	}
	s2, _ := m.Create(context.Background(), Spec{Cwd: t.TempDir(), Cmd: "sh", Args: []string{"-c", "cat"}})
	defer s2.Kill("KILL")
	list := m.List()
	if len(list) != 2 || list[0].ID != s2.ID || list[1].Status != protocol.StatusExited {
		t.Fatalf("order %+v", list)
	}
}

func TestPreview(t *testing.T) {
	m := NewManager(Options{})
	s, _ := m.Create(context.Background(), Spec{Cwd: t.TempDir(), Cmd: "sh", Args: []string{"-c", "printf '\\033[>0q\\033[<u\\033[31mred line\\033[0m\\n\\n'"}})
	<-s.Done()
	if p := s.Info().Preview; p != "red line" {
		t.Fatalf("preview %q", p)
	}
}

func TestClaudeMarkersStripped(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	m := NewManager(Options{})
	s, _ := m.Create(context.Background(), Spec{Cwd: t.TempDir(), Cmd: "sh", Args: []string{"-c", "echo CC=[$CLAUDECODE][$CLAUDE_CODE_CHILD_SESSION]"}})
	<-s.Done()
	if !strings.Contains(string(s.Scrollback()), "CC=[][]") {
		t.Fatalf("markers leaked: %q", s.Scrollback())
	}
}

func TestNewID(t *testing.T) {
	id := NewID()
	if len(id) != 36 || id[14] != '4' {
		t.Fatalf("bad uuid %s", id)
	}
}
