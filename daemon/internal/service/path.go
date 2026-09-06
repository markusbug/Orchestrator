package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// LoginPath resolves the PATH a login shell gives the user.
//
// The service file bakes a PATH in so `claude` resolves under it. Until now
// the installing terminal's PATH was good enough, but the desktop app
// installs the service too, and a GUI process inherits the desktop session's
// PATH instead -- on macOS that is only /usr/bin:/bin:/usr/sbin:/sbin, which
// has no claude in it. Asking the user's login shell is the one reliable way
// to get the PATH they actually type commands with.
func LoginPath() string {
	if p := loginShellPath(); p != "" {
		return augment(p)
	}
	return augment(os.Getenv("PATH"))
}

// loginShellPath runs the user's shell as a login shell and reads back its
// PATH. Failures are silent: the caller falls back to the inherited PATH.
func loginShellPath() string {
	sh := os.Getenv("SHELL")
	if sh == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// -l reads the login profile; -i is deliberately omitted, since an
	// interactive shell may block on prompts or write to the tty.
	out, err := exec.CommandContext(ctx, sh, "-lc", `printf %s "$PATH"`).Output()
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(string(out))
	// A profile that prints a banner would poison the value; PATH never has
	// a newline in it, so keep only the last line.
	if i := strings.LastIndexByte(p, '\n'); i >= 0 {
		p = p[i+1:]
	}
	if !strings.Contains(p, string(os.PathListSeparator)) && !filepath.IsAbs(p) {
		return ""
	}
	return p
}

// augment appends the directories user-installed tools land in, so `claude`
// resolves even when the login shell was not the one that installed it.
func augment(path string) string {
	extra := []string{"/opt/homebrew/bin", "/usr/local/bin"}
	if home, err := os.UserHomeDir(); err == nil {
		extra = append([]string{filepath.Join(home, ".local", "bin")}, extra...)
	}
	have := make(map[string]bool)
	parts := strings.Split(path, string(os.PathListSeparator))
	for _, p := range parts {
		have[p] = true
	}
	for _, e := range extra {
		if !have[e] {
			if _, err := os.Stat(e); err == nil {
				parts = append(parts, e)
				have[e] = true
			}
		}
	}
	return strings.Join(parts, string(os.PathListSeparator))
}
