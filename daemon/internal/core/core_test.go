package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/markusbug/Orchestrator/daemon/internal/config"
	"github.com/markusbug/Orchestrator/daemon/internal/protocol"
)

func newCore(t *testing.T) (*Core, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o755)
	// fake claude that prints its args and exits
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho ARGS: \"$@\"\necho SID=$ORCHESTRATOR_SESSION_ID\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := filepath.Join(dir, "root")
	os.MkdirAll(root, 0o755)
	paths, _ := config.DefaultPaths(filepath.Join(dir, "cfg"))
	cfg := config.Defaults(paths.Home)
	cfg.Roots = []string{root}
	c, err := Open(context.Background(), paths, cfg, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c, root
}

func TestClaudeFlagInjectionAndResume(t *testing.T) {
	c, root := newCore(t)
	s, err := c.CreateSession(context.Background(), protocol.SessionCreate{Cwd: root, Args: []string{"--model", "x"}})
	if err != nil {
		t.Fatal(err)
	}
	<-s.Done()
	out := string(s.Scrollback())
	info := s.Info()
	if info.ClaudeSessionID == "" || !strings.Contains(out, "--session-id "+info.ClaudeSessionID) || !strings.Contains(out, "--settings "+c.Paths.HooksFile) || !strings.Contains(out, "--model x") {
		t.Fatalf("args not injected: %q", out)
	}
	if len(info.Args) != 2 {
		t.Fatalf("user args should be persisted without injection: %v", info.Args)
	}
	if _, err := os.Stat(c.Paths.HooksFile); err != nil {
		t.Fatal("hooks file missing")
	}
	r, err := c.ResumeSession(context.Background(), s.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	<-r.Done()
	rout := string(r.Scrollback())
	if !strings.Contains(rout, "--resume "+info.ClaudeSessionID) || !strings.Contains(rout, "--model x") {
		t.Fatalf("resume args wrong: %q", rout)
	}
	if _, err := c.Mgr.Get(s.ID); err == nil {
		t.Fatal("old session should be removed after resume")
	}
	if _, err := c.ResumeSession(context.Background(), "missing", 80, 24); err == nil {
		t.Fatal("expected not found")
	}
}

func TestExplicitResumeNotDoubled(t *testing.T) {
	c, root := newCore(t)
	s, _ := c.CreateSession(context.Background(), protocol.SessionCreate{Cwd: root, Args: []string{"--continue"}})
	<-s.Done()
	out := string(s.Scrollback())
	if strings.Contains(out, "--session-id") || !strings.Contains(out, "--settings") {
		t.Fatalf("%q", out)
	}
}

func TestStaleAfterRestartAndHook(t *testing.T) {
	c, root := newCore(t)
	s, _ := c.CreateSession(context.Background(), protocol.SessionCreate{Cwd: root, Cmd: "sh", Args: []string{"-c", "cat"}})
	if !c.ApplyHook(s.ID, "stop") || s.Info().Status != protocol.StatusWaiting {
		t.Fatal("hook did not set waiting")
	}
	if !c.ApplyHook(s.ID, "prompt") || s.Info().Status != protocol.StatusRunning {
		t.Fatal("hook did not set running")
	}
	if c.ApplyHook(s.ID, "unknown") || c.ApplyHook("nope", "stop") {
		t.Fatal("bad hooks accepted")
	}
	paths, cfg := c.Paths, c.Cfg
	c.Close() // kills the session, closes the store
	c2, err := Open(context.Background(), paths, cfg, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	list := c2.Mgr.List()
	if len(list) != 1 || list[0].Status != protocol.StatusStale || list[0].ID != s.ID {
		t.Fatalf("%+v", list)
	}
	r, err := c2.ResumeSession(context.Background(), s.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Kill("KILL")
	if r.Info().Cwd != root || r.Info().Cmd != "sh" {
		t.Fatalf("%+v", r.Info())
	}
	if len(c2.Mgr.List()) != 1 {
		t.Fatal("stale row should be replaced")
	}
	_ = time.Second
}

func TestOutsideRootRejected(t *testing.T) {
	c, _ := newCore(t)
	if _, err := c.CreateSession(context.Background(), protocol.SessionCreate{Cwd: "/", Cmd: "sh"}); err == nil {
		t.Fatal("cwd outside roots accepted")
	}
}
