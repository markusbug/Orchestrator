package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func plistPath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return plistPathIn(home, name), nil
}

// Unit renders the launchd property list for this user.
func Unit(o Options) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return Plist(o, home)
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// domain is the launchd domain for the logged-in user's GUI session, which
// is where a per-user agent belongs.
func domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

// Install writes the LaunchAgent, loads it, and starts it. A user agent runs
// only while the user is logged in; that matches the Linux unit closely
// enough, and unlike systemd there is no lingering to enable.
func Install(o Options) (string, error) {
	if o.Name == "" {
		o.Name = "orchestrator"
	}
	p, err := plistPath(o.Name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	out, _ := logPathsIn(home, o.Name)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(Unit(o)), 0o644); err != nil {
		return "", err
	}
	// Reinstalling over a loaded job fails, so unload first and ignore the
	// error when nothing was loaded.
	_ = run("launchctl", "bootout", domain()+"/"+label(o.Name))
	if err := run("launchctl", "bootstrap", domain(), p); err != nil {
		// bootstrap/bootout arrived in macOS 10.11 and are the supported
		// spelling; fall back for anything older or unusual.
		if err2 := run("launchctl", "load", "-w", p); err2 != nil {
			return p, err
		}
		return p, nil
	}
	_ = run("launchctl", "enable", domain()+"/"+label(o.Name))
	if err := run("launchctl", "kickstart", "-k", domain()+"/"+label(o.Name)); err != nil {
		return p, err
	}
	return p, nil
}

// Uninstall stops and removes the LaunchAgent.
func Uninstall(name string) error {
	if name == "" {
		name = "orchestrator"
	}
	p, err := plistPath(name)
	if err != nil {
		return err
	}
	_ = run("launchctl", "bootout", domain()+"/"+label(name))
	_ = run("launchctl", "unload", "-w", p)
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Status returns `launchctl print` output for the job.
func Status(name string) (string, error) {
	if name == "" {
		name = "orchestrator"
	}
	out, err := exec.Command("launchctl", "print", domain()+"/"+label(name)).CombinedOutput()
	return string(out), err
}

// Enabled reports whether the service is installed and set to start at login.
func Enabled(name string) (bool, error) {
	if name == "" {
		name = "orchestrator"
	}
	p, err := plistPath(name)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(p); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	// The plist always has RunAtLoad, so an installed job starts at login
	// unless launchctl was told otherwise.
	return !disabled(name), nil
}

// LogsArgs returns the command to follow logs.
func LogsArgs(name string) []string {
	if name == "" {
		name = "orchestrator"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	out, errLog := logPathsIn(home, name)
	// -F rather than -f so the tail survives log rotation.
	return []string{"tail", "-n", "200", "-F", out, errLog}
}

// Start starts the service without changing whether it runs at login.
func Start(name string) error {
	if name == "" {
		name = "orchestrator"
	}
	return run("launchctl", "kickstart", domain()+"/"+label(name))
}

// Stop stops the service, leaving the agent loaded for the next login. The
// daemon exits 0 on SIGTERM, and KeepAlive only restarts on failure, so it
// stays down.
func Stop(name string) error {
	if name == "" {
		name = "orchestrator"
	}
	return run("launchctl", "kill", "TERM", domain()+"/"+label(name))
}

// SetEnabled changes whether the service starts at login without touching
// whether it is running now: opting out of autostart must not kill live
// sessions. launchctl disable survives reboots, which is exactly the flag
// that belongs behind a start-at-login switch.
func SetEnabled(name string, on bool) error {
	if name == "" {
		name = "orchestrator"
	}
	p, err := plistPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("service is not installed: %w", err)
	}
	verb := "disable"
	if on {
		verb = "enable"
	}
	return run("launchctl", verb, domain()+"/"+label(name))
}

// disabled reports whether launchctl has the job on its disabled list, which
// is where `launchctl disable` puts it and where it stays across reboots.
func disabled(name string) bool {
	out, err := exec.Command("launchctl", "print-disabled", domain()).CombinedOutput()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, `"`+label(name)+`"`) {
			return strings.Contains(line, "true")
		}
	}
	return false
}

// Installed reports whether the LaunchAgent plist exists.
func Installed(name string) (bool, error) {
	if name == "" {
		name = "orchestrator"
	}
	p, err := plistPath(name)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}
