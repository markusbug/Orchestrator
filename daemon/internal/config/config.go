// Package config holds daemon configuration and well-known paths.
package config

import (
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
)

const maxSockPath = 96

// Config is the on-disk configuration (config.toml). All fields optional.
type Config struct {
	Port            int      `toml:"port"`
	Bind            string   `toml:"bind"`
	Roots           []string `toml:"roots"`
	DefaultCommand  string   `toml:"default_command"`
	ScrollbackBytes int      `toml:"scrollback_bytes"`
	Debug           bool     `toml:"debug"`
}

// Paths are derived locations that are not user-configurable.
type Paths struct {
	Dir         string // config directory
	ConfigFile  string
	DBFile      string
	CertFile    string
	KeyFile     string
	HooksFile   string
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
	if _, err := toml.Decode(string(data), &file); err != nil {
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
	return cfg, nil
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
