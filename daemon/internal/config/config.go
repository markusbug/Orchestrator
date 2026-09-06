// Package config holds daemon configuration and well-known paths.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

const maxSockPath = 96

// DefaultRelayURL is the hosted relay every daemon uses unless configured
// otherwise. Empty until the hosted relay exists; then the relay is on by
// default and `orchestrator relay off` opts out.
const DefaultRelayURL = ""

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

// Validate checks the relay URL. Plain http is only accepted when insecure
// is set (development relays).
func (r RelayConfig) Validate(insecure bool) error {
	if r.URL == "" {
		return nil
	}
	u, err := url.Parse(r.URL)
	if err != nil {
		return fmt.Errorf("relay url: %w", err)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !insecure {
			return errors.New("relay url must use https")
		}
	default:
		return fmt.Errorf("relay url: unsupported scheme %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return errors.New("relay url: missing host")
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return errors.New("relay url must be just scheme and host")
	}
	return nil
}

// Domain returns the relay's host name without port.
func (r RelayConfig) Domain() string {
	u, err := url.Parse(r.URL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// Port returns the relay port (443 unless the URL names one).
func (r RelayConfig) Port() int {
	u, err := url.Parse(r.URL)
	if err != nil {
		return 443
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			return n
		}
	}
	if u.Scheme == "http" {
		return 80
	}
	return 443
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
	sock := filepath.Join(dir, "admin.sock")
	if overrideDir == "" && os.Getenv("ORCHESTRATOR_DIR") == "" {
		if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" && runtime.GOOS == "linux" {
			sock = filepath.Join(rt, "orchestrator.sock")
		}
	}
	// Unix socket paths are limited to ~104 bytes; fall back to a short
	// per-directory path when the config dir is deeply nested.
	if len(sock) > maxSockPath {
		h := fnv.New32a()
		h.Write([]byte(dir))
		sock = filepath.Join(os.TempDir(), fmt.Sprintf("orchestrator-%d-%08x.sock", os.Getuid(), h.Sum32()))
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

// Update rewrites config.toml after applying fn to its decoded document.
// Keys fn does not touch are preserved, so user edits survive.
func Update(p Paths, fn func(doc map[string]any)) error {
	doc := map[string]any{}
	data, err := os.ReadFile(p.ConfigFile)
	switch {
	case err == nil:
		if _, err := toml.Decode(string(data), &doc); err != nil {
			return fmt.Errorf("config: %s: %w", p.ConfigFile, err)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return err
	}
	fn(doc)
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.ConfigFile), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p.ConfigFile, buf.Bytes(), 0o600)
}

// SetRelay writes the [relay] section. An empty url keeps the current one.
func SetRelay(p Paths, enabled bool, rawURL string) error {
	return Update(p, func(doc map[string]any) {
		sec, _ := doc["relay"].(map[string]any)
		if sec == nil {
			sec = map[string]any{}
		}
		sec["enabled"] = enabled
		if rawURL != "" {
			sec["url"] = rawURL
		}
		doc["relay"] = sec
	})
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
	return os.MkdirAll(filepath.Dir(p.CertFile), 0o700)
}
