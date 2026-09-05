package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSessionCRUD(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now()
	sess := Session{ID: "a", Name: "n", Cwd: "/tmp", Cmd: "sh", Args: []string{"-c", "x"}, PID: 12, Status: "running", CreatedAt: now, LastOutputAt: now, Cols: 80, Rows: 24, ClaudeSessionID: "u"}
	if err := s.PutSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "n" || got.Args[1] != "x" || got.PID != 12 || got.ClaudeSessionID != "u" || got.Cols != 80 {
		t.Fatalf("%+v", got)
	}
	code := 3
	if err := s.UpdateSessionStatus(ctx, "a", "exited", &code); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSession(ctx, "a")
	if got.Status != "exited" || got.ExitCode == nil || *got.ExitCode != 3 {
		t.Fatalf("%+v", got)
	}
	if err := s.UpdateSessionName(ctx, "a", "renamed"); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListSessions(ctx)
	if len(list) != 1 || list[0].Name != "renamed" {
		t.Fatalf("%+v", list)
	}
	rec, _ := s.Recents(ctx, 5)
	if len(rec) != 1 || rec[0] != "/tmp" {
		t.Fatalf("recents %v", rec)
	}
	if err := s.DeleteSession(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, "a"); err != ErrNotFound {
		t.Fatalf("want not found, got %v", err)
	}
	if err := s.UpdateSessionStatus(ctx, "zz", "x", nil); err != ErrNotFound {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestMarkAllStale(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now()
	for i, st := range []string{"running", "waiting", "exited"} {
		s.PutSession(ctx, Session{ID: string(rune('a' + i)), Name: "x", Cwd: "/", Cmd: "sh", Status: st, PID: 5, CreatedAt: now, LastOutputAt: now})
	}
	n, err := s.MarkAllStale(ctx)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	list, _ := s.ListSessions(ctx)
	stale := 0
	for _, x := range list {
		if x.Status == "stale" {
			stale++
			if x.PID != 0 {
				t.Fatal("pid should be cleared")
			}
		}
	}
	if stale != 2 {
		t.Fatalf("stale=%d", stale)
	}
}

func TestDevices(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.PutDevice(ctx, Device{ID: "d1", Name: "phone", PubKey: []byte{1, 2, 3}, CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetDevice(ctx, "d1")
	if err != nil || d.Name != "phone" || len(d.PubKey) != 3 || d.Revoked {
		t.Fatalf("%+v %v", d, err)
	}
	if err := s.RevokeDevice(ctx, "d1"); err != nil {
		t.Fatal(err)
	}
	d, _ = s.GetDevice(ctx, "d1")
	if !d.Revoked {
		t.Fatal("not revoked")
	}
	if err := s.RevokeDevice(ctx, "nope"); err != ErrNotFound {
		t.Fatal(err)
	}
	if _, err := s.GetDevice(ctx, "nope"); err != ErrNotFound {
		t.Fatal(err)
	}
	list, _ := s.ListDevices(ctx)
	if len(list) != 1 {
		t.Fatal(len(list))
	}
}
