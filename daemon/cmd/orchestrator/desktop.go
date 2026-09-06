package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/markusbug/Orchestrator/daemon/internal/admin"
	"github.com/markusbug/Orchestrator/daemon/internal/core"
	"github.com/markusbug/Orchestrator/daemon/internal/service"
)

// appBundleID matches the mobile app's bundle id, so the desktop app is
// findable by LaunchServices under the same name.
const appBundleID = "io.freedomfactory.orchestrator"

// launchApp opens the installed desktop app. Bare `orchestrator` calls it, so
// a user who types the name of the thing they installed gets the app instead
// of a usage screen.
func launchApp() error {
	switch runtime.GOOS {
	case "darwin":
		if err := startDetached("open", "-b", appBundleID); err == nil {
			return nil
		}
		return startDetached("open", "-a", "Orchestrator")
	case "linux":
		p, err := findLinuxApp()
		if err != nil {
			return err
		}
		return startDetached(p)
	}
	return fmt.Errorf("no desktop app on %s yet", runtime.GOOS)
}

// findLinuxApp looks where the packages put the desktop binary: next to this
// executable (.deb, AppImage), then anywhere on PATH.
func findLinuxApp() (string, error) {
	const name = "orchestrator-desktop"
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), name)
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s is not installed", name)
	}
	return p, nil
}

// startDetached runs a command without waiting for it. The child is
// reparented when this process exits, which is what launching an app means.
func startDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// runPaths prints the resolved config paths. Hidden: it exists so the desktop
// app and a support request can see where the daemon keeps its state without
// reimplementing config.DefaultPaths.
func runPaths(args []string) error {
	fs := flag.NewFlagSet("_paths", flag.ExitOnError)
	dir := fs.String("dir", "", "config directory")
	parseArgs(fs, args)
	p, err := paths(*dir)
	if err != nil {
		return err
	}
	exe, _ := os.Executable()
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"dir":          p.Dir,
		"config_file":  p.ConfigFile,
		"db_file":      p.DBFile,
		"admin_socket": p.AdminSocket,
		"home":         p.Home,
		"exe":          exe,
		"version":      core.Version,
	})
}

// runService reports or changes the background service. Hidden: the desktop
// app drives the start-at-login switch and the tray's stop item through it.
func runService(args []string) error {
	fs := flag.NewFlagSet("_service", flag.ExitOnError)
	dir := fs.String("dir", "", "config directory")
	rest := parseArgs(fs, args)
	if len(rest) > 0 {
		switch rest[0] {
		case "start":
			return service.Start("")
		case "stop":
			return service.Stop("")
		case "status":
		default:
			return fmt.Errorf("usage: orchestrator _service [status | start | stop]")
		}
	}
	enabled, err := service.Enabled("")
	if err != nil && !errors.Is(err, service.ErrUnsupported) {
		return err
	}
	out := map[string]any{
		"supported": !errors.Is(err, service.ErrUnsupported),
		"enabled":   enabled,
		"running":   false,
	}
	if p, perr := paths(*dir); perr == nil {
		if st, serr := admin.NewClient(p.AdminSocket).Status(); serr == nil {
			out["running"], out["pid"] = true, st.PID
		}
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
