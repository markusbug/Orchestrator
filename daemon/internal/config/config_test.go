package config

import (
	"os"
	"path/filepath"
	"strings"
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
	const orig = "# main settings\nport = 9000\n\n# roots = [\"/other\"]\nroots = [\"/srv\"]\n# a comment\n"
	os.WriteFile(p.ConfigFile, []byte(orig), 0o600)
	if err := SetRelay(p, true, "https://r.example"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p.ConfigFile)
	if !strings.HasPrefix(string(got), orig) {
		t.Fatalf("hand-written content changed:\n%s", got)
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
	got2, _ := os.ReadFile(p.ConfigFile)
	if !strings.HasPrefix(string(got2), orig) || strings.Count(string(got2), "[relay]") != 1 || strings.Count(string(got2), "enabled =") != 1 {
		t.Fatalf("second edit did not rewrite in place:\n%s", got2)
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

func TestSetSectionKeysEditsInPlace(t *testing.T) {
	doc := "port = 1\n\n[relay]\n# keep me\nenabled = false  # trailing\n\n[other]\nx = 1\n"
	out := setSectionKeys(doc, "relay", []kv{{"enabled", "true"}, {"url", `"https://r"`}})
	want := "port = 1\n\n[relay]\n# keep me\nenabled = true\nurl = \"https://r\"\n\n[other]\nx = 1\n"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	// Section at the end without a trailing newline.
	out = setSectionKeys("[relay]\nurl = \"a\"", "relay", []kv{{"enabled", "true"}})
	if out != "[relay]\nurl = \"a\"\nenabled = true\n" {
		t.Fatalf("no-trailing-newline:\n%s", out)
	}
	// Empty document.
	if out = setSectionKeys("", "relay", []kv{{"enabled", "true"}}); out != "[relay]\nenabled = true\n" {
		t.Fatalf("empty:\n%s", out)
	}
}

func TestSetRelayRefusesUneditableLayout(t *testing.T) {
	dir := t.TempDir()
	p, _ := DefaultPaths(dir)
	// Dotted keys at top level define relay.enabled outside a [relay]
	// section; appending a section would be a TOML error, so refuse.
	os.WriteFile(p.ConfigFile, []byte("relay.enabled = true\n"), 0o600)
	if err := SetRelay(p, false, ""); err == nil {
		t.Fatal("expected an error for dotted relay keys")
	}
	if err := SetRelay(p, false, ""); err == nil {
		t.Fatal("still no error")
	}
	got, _ := os.ReadFile(p.ConfigFile)
	if string(got) != "relay.enabled = true\n" {
		t.Fatalf("file modified: %q", got)
	}
}

func TestTomlString(t *testing.T) {
	for in, want := range map[string]string{
		"https://r.example:8443": `"https://r.example:8443"`,
		`a"b\c`:                  `"a\"b\\c"`,
		"x\ny":                   `"x\ny"`,
	} {
		if got := tomlString(in); got != want {
			t.Errorf("tomlString(%q) = %s want %s", in, got, want)
		}
	}
}

func TestEnsureDirsMakesSocketDirPrivate(t *testing.T) {
	dir := t.TempDir()
	p, err := DefaultPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureDirs(p); err != nil {
		t.Fatal(err)
	}
	sockDir := filepath.Dir(p.AdminSocket)
	fi, err := os.Stat(sockDir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("%s is %v; other users must not be able to enter it", sockDir, fi.Mode().Perm())
	}
}

func TestLongConfigDirGetsPrivateSocketDir(t *testing.T) {
	// A config directory deep enough to blow the unix socket path limit
	// falls back to the temp directory, which every user can write to.
	deep := filepath.Join(t.TempDir(), strings.Repeat("nested/", 20))
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	p, err := DefaultPaths(deep)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p.AdminSocket) == os.TempDir() {
		t.Fatalf("socket %s sits directly in the shared temp directory", p.AdminSocket)
	}
	if err := EnsureDirs(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(p.AdminSocket)) })
	fi, err := os.Stat(filepath.Dir(p.AdminSocket))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("socket dir is %v, want 0700", fi.Mode().Perm())
	}
}

func TestEnsurePrivateDirRefusesWorldReadable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sock")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(dir)
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir left at %v; it should have been narrowed", fi.Mode().Perm())
	}
}

func TestEnsurePrivateDirRefusesSymlink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := EnsurePrivateDir(link); err == nil {
		t.Fatal("a symlink was accepted as the socket directory")
	}
}
