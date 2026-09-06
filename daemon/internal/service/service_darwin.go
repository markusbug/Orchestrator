package service

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// label is the launchd job name. It matches the app's bundle id so the two
// sit together in ~/Library/LaunchAgents.
func label(name string) string { return "io.freedomfactory." + name }

func plistPath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label(name)+".plist"), nil
}

// logPaths returns the stdout and stderr files launchd writes to. Unlike
// Linux there is no journal to read, so the job keeps its own log files.
func logPaths(name string) (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(home, "Library", "Logs", "orchestrator")
	return filepath.Join(dir, name+".log"), filepath.Join(dir, name+".err.log"), nil
}

// Unit renders the launchd property list.
func Unit(o Options) string {
	if o.Name == "" {
		o.Name = "orchestrator"
	}
	out, errLog, _ := logPaths(o.Name)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>serve</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>2</integer>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>%s</string>
	</dict>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, esc(label(o.Name)), esc(o.Exe), esc(o.Path), esc(out), esc(errLog))
}

// esc escapes a value for an XML text node.
func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
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
	out, _, err := logPaths(o.Name)
	if err != nil {
		return "", err
	}
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
	// The plist always has RunAtLoad, so being loaded is the same as being
	// enabled; an unloaded plist left on disk is not.
	return exec.Command("launchctl", "print", domain()+"/"+label(name)).Run() == nil, nil
}

// LogsArgs returns the command to follow logs.
func LogsArgs(name string) []string {
	if name == "" {
		name = "orchestrator"
	}
	out, errLog, err := logPaths(name)
	if err != nil {
		return nil
	}
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
