package service

import (
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strings"
)

// The launchd LaunchAgent lives here rather than in service_darwin.go so it
// can be tested from any machine. There is no Mac in this project's loop, and
// a plist that renders wrongly fails at load time with a message nobody sees.

// label is the launchd job name. It matches the app's bundle id so the two sit
// together in ~/Library/LaunchAgents.
func label(name string) string { return "io.freedomfactory." + name }

func plistPathIn(home, name string) string {
	return filepath.Join(home, "Library", "LaunchAgents", label(name)+".plist")
}

// logPathsIn returns the stdout and stderr files launchd writes to. Unlike
// Linux there is no journal to read, so the job keeps its own log files.
func logPathsIn(home, name string) (string, string) {
	dir := filepath.Join(home, "Library", "Logs", "orchestrator")
	return filepath.Join(dir, name+".log"), filepath.Join(dir, name+".err.log")
}

// Plist renders the LaunchAgent, the macOS counterpart of Unit.
//
// RunAtLoad is WantedBy=default.target, and KeepAlive with SuccessfulExit
// false is Restart=on-failure -- a plain <true/> there would resurrect the
// daemon after a clean shutdown. ProcessType Interactive keeps launchd from
// throttling a long-lived PTY supervisor.
func Plist(o Options, home string) string {
	if o.Name == "" {
		o.Name = "orchestrator"
	}
	out, errLog := logPathsIn(home, o.Name)
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

// esc escapes a value for an XML text node. A PATH can contain an ampersand.
func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
