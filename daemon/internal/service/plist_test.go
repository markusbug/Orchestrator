package service

import (
	"encoding/xml"
	"strings"
	"testing"
)

// The macOS service is built by CI and cannot be run here, so the one thing
// that can be checked -- that the plist is well formed and says what it should
// -- is checked from any platform.
func TestPlistIsWellFormed(t *testing.T) {
	got := Plist(Options{Exe: "/Applications/Orchestrator.app/Contents/Resources/orchestrator"}, "/Users/x")
	if err := xml.Unmarshal([]byte(got), new(any)); err != nil {
		t.Fatalf("plist is not valid XML: %v\n%s", err, got)
	}
	for _, want := range []string{
		"<string>io.freedomfactory.orchestrator</string>",
		"<string>/Applications/Orchestrator.app/Contents/Resources/orchestrator</string>",
		"<string>serve</string>",
		"<key>RunAtLoad</key>",
		"/Users/x/Library/Logs/orchestrator/orchestrator.log",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist is missing %q\n%s", want, got)
		}
	}
	// KeepAlive must be the dict form. A bare <true/> would restart the daemon
	// after a clean shutdown, so stopping the service would never take.
	if !strings.Contains(got, "<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>") {
		t.Errorf("KeepAlive is not the SuccessfulExit=false dict\n%s", got)
	}
}

func TestPlistEscapesPath(t *testing.T) {
	// A PATH really can contain an ampersand, and an unescaped one makes the
	// whole file unparseable to launchd.
	got := Plist(Options{Exe: "/opt/a&b/orchestrator", Path: "/usr/bin:/opt/x&y/bin"}, "/Users/x")
	if err := xml.Unmarshal([]byte(got), new(any)); err != nil {
		t.Fatalf("plist with an ampersand is not valid XML: %v\n%s", err, got)
	}
	if strings.Contains(got, "x&y") {
		t.Error("ampersand was not escaped")
	}
	if !strings.Contains(got, "x&amp;y") {
		t.Errorf("escaped PATH is missing\n%s", got)
	}
}

func TestPlistPathIn(t *testing.T) {
	want := "/Users/x/Library/LaunchAgents/io.freedomfactory.orchestrator.plist"
	if got := plistPathIn("/Users/x", "orchestrator"); got != want {
		t.Errorf("plistPathIn = %q, want %q", got, want)
	}
}
