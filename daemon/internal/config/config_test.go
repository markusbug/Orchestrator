package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	p, err := DefaultPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 7391 || cfg.DefaultCommand != "claude" || len(cfg.Roots) != 1 {
		t.Fatalf("%+v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	dir := t.TempDir()
	p, _ := DefaultPaths(dir)
	os.WriteFile(p.ConfigFile, []byte("port = 9000\nroots = [\"~/code\", \"/srv\"]\ndefault_command = \"bash\"\n"), 0o600)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9000 || cfg.DefaultCommand != "bash" {
		t.Fatalf("%+v", cfg)
	}
	if cfg.Roots[0] != filepath.Join(p.Home, "code") || cfg.Roots[1] != "/srv" {
		t.Fatalf("roots %v", cfg.Roots)
	}
	if cfg.ScrollbackBytes != 1<<20 {
		t.Fatal("scrollback default lost")
	}
}

func TestLoadBadToml(t *testing.T) {
	dir := t.TempDir()
	p, _ := DefaultPaths(dir)
	os.WriteFile(p.ConfigFile, []byte("port = = 1"), 0o600)
	if _, err := Load(p); err == nil {
		t.Fatal("expected error")
	}
}

func TestLongSocketPathFallsBack(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a-very-long-directory-name-to-push-the-socket-path-over-the-limit", "and-then-some-more-nesting", "cfg")
	p, err := DefaultPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.AdminSocket) > maxSockPath+8 {
		t.Fatalf("socket path too long: %s", p.AdminSocket)
	}
	if p.AdminSocket == filepath.Join(dir, "admin.sock") {
		t.Fatal("expected fallback path")
	}
	short, _ := DefaultPaths("/tmp/o")
	if short.AdminSocket != "/tmp/o/admin.sock" {
		t.Fatalf("short path changed: %s", short.AdminSocket)
	}
}
