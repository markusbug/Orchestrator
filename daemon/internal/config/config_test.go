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

func TestRelayDefaultsAndMerge(t *testing.T) {
	dir := t.TempDir()
	p, _ := DefaultPaths(dir)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Relay.Enabled || cfg.Relay.URL != DefaultRelayURL {
		t.Fatalf("defaults %+v", cfg.Relay)
	}
	if p.HostKeyFile != filepath.Join(dir, "host_key.pem") {
		t.Fatalf("host key path %s", p.HostKeyFile)
	}
	os.WriteFile(p.ConfigFile, []byte("port = 9000\n[relay]\nenabled = false\nurl = \"https://r.example:8443\"\nca_file = \"~/ca.pem\"\n"), 0o600)
	cfg, err = Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Relay.Enabled || cfg.Relay.URL != "https://r.example:8443" || cfg.Relay.CAFile != filepath.Join(p.Home, "ca.pem") {
		t.Fatalf("merged %+v", cfg.Relay)
	}
	if cfg.Relay.Active() {
		t.Fatal("disabled relay reported active")
	}
	if cfg.Relay.Domain() != "r.example" || cfg.Relay.Port() != 8443 {
		t.Fatalf("domain/port %q %d", cfg.Relay.Domain(), cfg.Relay.Port())
	}
	// A file that sets only the url keeps enabled at its default.
	os.WriteFile(p.ConfigFile, []byte("[relay]\nurl = \"https://r.example\"\n"), 0o600)
	cfg, _ = Load(p)
	if !cfg.Relay.Enabled || !cfg.Relay.Active() || cfg.Relay.Port() != 443 {
		t.Fatalf("url-only %+v", cfg.Relay)
	}
}

func TestRelayValidate(t *testing.T) {
	for _, tc := range []struct {
		url      string
		insecure bool
		ok       bool
	}{
		{"", false, true},
		{"https://relay.example", false, true},
		{"https://relay.example:8443/", false, true},
		{"http://127.0.0.1:8080", false, false},
		{"http://127.0.0.1:8080", true, true},
		{"wss://relay.example", false, false},
		{"https://relay.example/v1", false, false},
		{"https://relay.example?x=1", false, false},
		{"https://user@relay.example", false, false},
		{"https://", false, false},
	} {
		err := RelayConfig{URL: tc.url}.Validate(tc.insecure)
		if (err == nil) != tc.ok {
			t.Errorf("Validate(%q, %v) = %v", tc.url, tc.insecure, err)
		}
	}
}

func TestUpdatePreservesKeys(t *testing.T) {
	dir := t.TempDir()
	p, _ := DefaultPaths(dir)
	os.WriteFile(p.ConfigFile, []byte("port = 9000\nroots = [\"/srv\"]\n# a comment\n"), 0o600)
	if err := SetRelay(p, true, "https://r.example"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9000 || len(cfg.Roots) != 1 || cfg.Roots[0] != "/srv" {
		t.Fatalf("unrelated keys lost: %+v", cfg)
	}
	if !cfg.Relay.Enabled || cfg.Relay.URL != "https://r.example" {
		t.Fatalf("relay %+v", cfg.Relay)
	}
	if err := SetRelay(p, false, ""); err != nil {
		t.Fatal(err)
	}
	cfg, _ = Load(p)
	if cfg.Relay.Enabled || cfg.Relay.URL != "https://r.example" {
		t.Fatalf("off kept url? %+v", cfg.Relay)
	}
	st, _ := os.Stat(p.ConfigFile)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	// Works when the file does not exist yet.
	p2, _ := DefaultPaths(filepath.Join(dir, "fresh"))
	if err := SetRelay(p2, true, "https://x.example"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := Load(p2); cfg.Relay.URL != "https://x.example" {
		t.Fatal("fresh file")
	}
}
