package fsapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func setup(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "proj", ".git"), 0o755)
	os.MkdirAll(filepath.Join(root, "deep", "a", "b", "target-dir"), 0o755)
	os.MkdirAll(filepath.Join(root, "node_modules", "target-x"), 0o755)
	os.MkdirAll(filepath.Join(root, ".hidden"), 0o755)
	os.WriteFile(filepath.Join(root, "zfile.txt"), []byte("x"), 0o644)
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(root, "escape"))
	s, err := New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	return s, s.Roots()[0]
}

func TestListOrderingAndMarkers(t *testing.T) {
	s, root := setup(t)
	r, err := s.List(root, false)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range r.Entries {
		names = append(names, e.Name)
	}
	// dirs first (deep, escape, node_modules, proj), then file
	if names[len(names)-1] != "zfile.txt" || names[0] != "deep" {
		t.Fatalf("order %v", names)
	}
	for _, e := range r.Entries {
		if e.Name == "proj" && !e.Git {
			t.Fatal("git marker missing")
		}
		if e.Name == ".hidden" {
			t.Fatal("hidden shown")
		}
	}
	r2, _ := s.List(root, true)
	if len(r2.Entries) != len(r.Entries)+1 {
		t.Fatal("hidden toggle failed")
	}
	if r.Parent != "" {
		t.Fatalf("root parent should be empty, got %s", r.Parent)
	}
	sub, _ := s.List(filepath.Join(root, "proj"), false)
	if sub.Parent != root {
		t.Fatalf("parent %s", sub.Parent)
	}
}

func TestEscapeRejected(t *testing.T) {
	s, root := setup(t)
	if _, err := s.List(filepath.Join(root, ".."), false); err != ErrOutsideRoots {
		t.Fatalf("dotdot: %v", err)
	}
	if _, err := s.List(filepath.Join(root, "escape"), false); err != ErrOutsideRoots {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := s.List("/", false); err != ErrOutsideRoots {
		t.Fatalf("absolute: %v", err)
	}
}

func TestSearch(t *testing.T) {
	s, root := setup(t)
	res, err := s.Search(context.Background(), root, "TARGET", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Name != "target-dir" {
		t.Fatalf("%+v", res)
	}
	res, _ = s.Search(context.Background(), root, "", SearchOptions{})
	if len(res) != 0 {
		t.Fatal("empty query should return nothing")
	}
	os.MkdirAll(filepath.Join(root, "x1", "hit"), 0o755)
	os.MkdirAll(filepath.Join(root, "x2", "hit"), 0o755)
	res, _ = s.Search(context.Background(), root, "hit", SearchOptions{Limit: 1})
	if len(res) != 1 {
		t.Fatalf("limit ignored: %d", len(res))
	}
	res, _ = s.Search(context.Background(), root, "target", SearchOptions{MaxDepth: 2})
	if len(res) != 0 {
		t.Fatal("depth limit ignored")
	}
}
