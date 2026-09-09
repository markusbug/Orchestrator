// Package config holds daemon configuration and well-known paths.
package config

import (
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/markusbug/Orchestrator/daemon/internal/relay/wire"
)

const maxSockPath = 96

// DefaultRelayURL is the hosted relay every daemon uses unless configured
// otherwise. The relay is on by default; `orchestrator relay off` opts out.
const DefaultRelayURL = "https://relay.markushaas.com"

// Config is the on-disk configuration (config.toml). All fields optional.
type Config struct {
	Port            int         `toml:"port"`
	Bind            string      `toml:"bind"`
	Roots           []string    `toml:"roots"`
	DefaultCommand  string      `toml:"default_command"`
	ScrollbackBytes int         `toml:"scrollback_bytes"`
	Debug           bool        `toml:"debug"`
	Relay           RelayConfig `toml:"relay"`
}

// RelayConfig controls the outbound connection to a relay (docs/RELAY.md).
type RelayConfig struct {
	Enabled bool   `toml:"enabled"`
	URL     string `toml:"url"`     // https://relay.example[:port]
	CAFile  string `toml:"ca_file"` // optional PEM bundle for a private CA
}

// Active reports whether the daemon should connect to a relay.
func (r RelayConfig) Active() bool { return r.Enabled && r.URL != "" }

// Validate checks the relay URL and, when set, that ca_file is readable
// and holds at least one certificate. Plain http is only accepted when
// insecure is set (development relays).
func (r RelayConfig) Validate(insecure bool) error {
	if r.URL == "" {
		return nil
	}
	if _, err := wire.ParseURL(r.URL, insecure); err != nil {
		return err
	}
	if r.CAFile != "" {
		pem, err := os.ReadFile(r.CAFile)
		if err != nil {
			return fmt.Errorf("relay ca_file: %w", err)
		}
		if !strings.Contains(string(pem), "-----BEGIN CERTIFICATE-----") {
			return fmt.Errorf("relay ca_file: no certificates in %s", r.CAFile)
		}
	}
	return nil
}

// Endpoint parses the relay URL leniently (http allowed) for display; use
// Validate for acceptance.
func (r RelayConfig) Endpoint() wire.Endpoint {
	e, _ := wire.ParseURL(r.URL, true)
	return e
}

// Domain returns the relay's host name without port.
func (r RelayConfig) Domain() string { return r.Endpoint().Domain }

// Port returns the relay port (443 unless the URL names one).
func (r RelayConfig) Port() int {
	if p := r.Endpoint().Port; p != 0 {
		return p
	}
	return wire.DefaultPort
}

// Paths are derived locations that are not user-configurable.
type Paths struct {
	Dir         string // config directory
	ConfigFile  string
	DBFile      string
	CertFile    string
	KeyFile     string
	HooksFile   string
	HostKeyFile string // Ed25519 relay identity
	AdminSocket string
	Home        string
}

// Defaults returns the default configuration.
func Defaults(home string) Config {
	return Config{
		Port:            7391,
		Bind:            "",
		Roots:           []string{home},
		DefaultCommand:  "claude",
		ScrollbackBytes: 1 << 20,
		Relay:           RelayConfig{Enabled: true, URL: DefaultRelayURL},
	}
}

// DefaultPaths computes paths using XDG conventions (or an override dir).
func DefaultPaths(overrideDir string) (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	dir := overrideDir
	if dir == "" {
		if v := os.Getenv("ORCHESTRATOR_DIR"); v != "" {
			dir = v
		} else if runtime.GOOS == "darwin" {
			dir = filepath.Join(home, "Library", "Application Support", "orchestrator")
		} else if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
			dir = filepath.Join(x, "orchestrator")
		} else {
			dir = filepath.Join(home, ".config", "orchestrator")
		}
	}
	// Sessions run in their own working directory and Claude Code resolves
	// the hooks file from there, so the directory must be absolute.
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	sock := filepath.Join(dir, "admin.sock")
	if overrideDir == "" && os.Getenv("ORCHESTRATOR_DIR") == "" {
		if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" && runtime.GOOS == "linux" {
			sock = filepath.Join(rt, "orchestrator.sock")
		}
	}
	// Unix socket paths are limited to ~104 bytes; fall back to a short
	// per-directory path when the config dir is deeply nested. The socket
	// goes inside its own directory rather than straight into the temp
	// directory: EnsureDirs makes that 0700, so no other user can reach the
	// socket even for the moment before it is chmodded, and no other user
	// can squat the path we are about to bind.
	if len(sock) > maxSockPath {
		h := fnv.New32a()
		h.Write([]byte(dir))
		sock = filepath.Join(os.TempDir(), fmt.Sprintf("orchestrator-%d-%08x", os.Getuid(), h.Sum32()), "admin.sock")
	}
	return Paths{
		Dir:         dir,
		ConfigFile:  filepath.Join(dir, "config.toml"),
		DBFile:      filepath.Join(dir, "orchestrator.db"),
		CertFile:    filepath.Join(dir, "tls", "cert.pem"),
		KeyFile:     filepath.Join(dir, "tls", "key.pem"),
		HooksFile:   filepath.Join(dir, "claude-hooks.json"),
		HostKeyFile: filepath.Join(dir, "host_key.pem"),
		AdminSocket: sock,
		Home:        home,
	}, nil
}

// Load reads config.toml if present and applies defaults.
func Load(p Paths) (Config, error) {
	cfg := Defaults(p.Home)
	data, err := os.ReadFile(p.ConfigFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}
	var file Config
	md, err := toml.Decode(string(data), &file)
	if err != nil {
		return cfg, fmt.Errorf("config: %s: %w", p.ConfigFile, err)
	}
	if file.Port != 0 {
		cfg.Port = file.Port
	}
	if file.Bind != "" {
		cfg.Bind = file.Bind
	}
	if len(file.Roots) > 0 {
		cfg.Roots = nil
		for _, r := range file.Roots {
			cfg.Roots = append(cfg.Roots, ExpandHome(r, p.Home))
		}
	}
	if file.DefaultCommand != "" {
		cfg.DefaultCommand = file.DefaultCommand
	}
	if file.ScrollbackBytes > 0 {
		cfg.ScrollbackBytes = file.ScrollbackBytes
	}
	cfg.Debug = file.Debug
	if md.IsDefined("relay", "enabled") {
		cfg.Relay.Enabled = file.Relay.Enabled
	}
	if file.Relay.URL != "" {
		cfg.Relay.URL = file.Relay.URL
	}
	if file.Relay.CAFile != "" {
		cfg.Relay.CAFile = ExpandHome(file.Relay.CAFile, p.Home)
	}
	return cfg, nil
}

// SetRelay writes the [relay] section. An empty url keeps the current one.
// The file is edited textually so comments, blank lines and key order in a
// hand-maintained config survive.
func SetRelay(p Paths, enabled bool, rawURL string) error {
	keys := []kv{{"enabled", fmt.Sprint(enabled)}}
	if rawURL != "" {
		keys = append(keys, kv{"url", tomlString(rawURL)})
	}
	data, err := os.ReadFile(p.ConfigFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(data) > 0 {
		// Refuse to touch a file we cannot parse rather than make it worse.
		if _, err := toml.Decode(string(data), &map[string]any{}); err != nil {
			return fmt.Errorf("config: %s: %w", p.ConfigFile, err)
		}
	}
	out := setSectionKeys(string(data), "relay", keys)
	var check Config
	md, err := toml.Decode(out, &check)
	if err != nil {
		return fmt.Errorf("config: rewriting %s produced invalid TOML: %w", p.ConfigFile, err)
	}
	if check.Relay.Enabled != enabled || (rawURL != "" && check.Relay.URL != rawURL) || !md.IsDefined("relay", "enabled") {
		return fmt.Errorf("config: %s: the relay keys are set somewhere this tool cannot edit (dotted keys?); edit the file by hand", p.ConfigFile)
	}
	if err := os.MkdirAll(filepath.Dir(p.ConfigFile), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p.ConfigFile, []byte(out), 0o600)
}

type kv struct{ key, value string }

var (
	sectionRe = regexp.MustCompile(`^\s*\[`)
	keyRe     = regexp.MustCompile(`^\s*([A-Za-z0-9_-]+)\s*=`)
)

// setSectionKeys replaces or inserts "key = value" lines in [section] of a
// TOML document, keeping everything else byte for byte. A missing section is
// appended. Only bare keys directly under a plain [section] header are
// handled; the caller verifies the result by decoding it.
func setSectionKeys(doc, section string, keys []kv) string {
	var lines []string
	if doc != "" {
		lines = strings.Split(doc, "\n")
	}
	// Trailing newline gives an empty last element; keep it separate.
	trailing := ""
	if strings.HasSuffix(doc, "\n") {
		lines = lines[:len(lines)-1]
		trailing = "\n"
	}
	header := "[" + section + "]"
	start, end := -1, len(lines)
	for i, l := range lines {
		if start < 0 {
			if strings.TrimSpace(strings.SplitN(l, "#", 2)[0]) == header {
				start = i
			}
			continue
		}
		if sectionRe.MatchString(l) {
			end = i
			break
		}
	}
	if start < 0 {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, header)
		for _, k := range keys {
			lines = append(lines, k.key+" = "+k.value)
		}
		return strings.Join(lines, "\n") + "\n"
	}
	pending := append([]kv(nil), keys...)
	for i := start + 1; i < end; i++ {
		m := keyRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		for j, k := range pending {
			if m[1] == k.key {
				lines[i] = k.key + " = " + k.value
				pending = append(pending[:j], pending[j+1:]...)
				break
			}
		}
	}
	if len(pending) > 0 {
		// Insert after the last key line of the section (before trailing
		// blanks and comments), or right after the header.
		at := start + 1
		for i := start + 1; i < end; i++ {
			if keyRe.MatchString(lines[i]) {
				at = i + 1
			}
		}
		ins := make([]string, 0, len(pending))
		for _, k := range pending {
			ins = append(ins, k.key+" = "+k.value)
		}
		lines = append(lines[:at], append(ins, lines[at:]...)...)
	}
	out := strings.Join(lines, "\n") + trailing
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// ExpandHome replaces a leading ~ with home.
func ExpandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// EnsureDirs creates the config directory tree with private permissions.
func EnsureDirs(p Paths) error {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.CertFile), 0o700); err != nil {
		return err
	}
	return EnsurePrivateDir(filepath.Dir(p.AdminSocket))
}

// EnsurePrivateDir makes dir exist as a real directory that only this user
// can enter. The admin socket has no authentication beyond its permissions,
// so a directory anyone else can traverse would hand them an API that issues
// pairing codes; one anyone else owns could be a squatted path whose socket
// answers the CLI in the daemon's place.
func EnsurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Lstat, not Stat: a symlink planted where the directory should be must
	// not pass for the directory.
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("config: %s is not a directory", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		// Chmod fails for a directory owned by someone else, which is the
		// case worth refusing rather than binding into.
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("config: %s must not be readable by other users: %w", dir, err)
		}
	}
	return nil
}
