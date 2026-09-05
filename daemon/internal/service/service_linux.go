package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func unitPath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", name+".service"), nil
}

// Unit renders the systemd user unit.
func Unit(o Options) string {
	return fmt.Sprintf(`[Unit]
Description=Orchestrator - remote Claude Code sessions
After=network.target

[Service]
Type=simple
ExecStart=%s serve
Restart=on-failure
RestartSec=2
Environment=PATH=%s
KillMode=mixed
TimeoutStopSec=15

[Install]
WantedBy=default.target
`, o.Exe, o.Path)
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Install writes the unit, enables and starts it, and enables lingering so
// the service keeps running after logout.
func Install(o Options) (string, error) {
	if o.Name == "" {
		o.Name = "orchestrator"
	}
	p, err := unitPath(o.Name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(Unit(o)), 0o644); err != nil {
		return "", err
	}
	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return p, err
	}
	if err := run("systemctl", "--user", "enable", "--now", o.Name+".service"); err != nil {
		return p, err
	}
	if u := os.Getenv("USER"); u != "" {
		_ = run("loginctl", "enable-linger", u)
	}
	return p, nil
}

// Uninstall stops and removes the unit.
func Uninstall(name string) error {
	if name == "" {
		name = "orchestrator"
	}
	p, err := unitPath(name)
	if err != nil {
		return err
	}
	_ = run("systemctl", "--user", "disable", "--now", name+".service")
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return run("systemctl", "--user", "daemon-reload")
}

// Status returns `systemctl --user status` output.
func Status(name string) (string, error) {
	if name == "" {
		name = "orchestrator"
	}
	out, err := exec.Command("systemctl", "--user", "--no-pager", "status", name+".service").CombinedOutput()
	return string(out), err
}

// LogsArgs returns the command to follow logs.
func LogsArgs(name string) []string {
	if name == "" {
		name = "orchestrator"
	}
	return []string{"journalctl", "--user", "-u", name + ".service", "-f", "-n", "200"}
}
