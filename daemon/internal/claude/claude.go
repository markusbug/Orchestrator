// Package claude integrates with Claude Code: transcript discovery, hook
// settings, and mapping hook events to session status.
package claude

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/markusbug/Orchestrator/daemon/internal/protocol"
)

// EncodeProjectPath maps a working directory to its ~/.claude/projects
// directory name. Every byte other than [A-Za-z0-9_] becomes '-'. The mapping is
// lossy, so always encode; never try to decode a directory name.
func EncodeProjectPath(cwd string) string {
	b := []byte(cwd)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			b[i] = '-'
		}
	}
	return string(b)
}

// ProjectsDir returns ~/.claude/projects (or the override).
func ProjectsDir(home string) string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return filepath.Join(v, "projects")
	}
	return filepath.Join(home, ".claude", "projects")
}

// Conversations lists transcripts for cwd, newest first, up to limit.
func Conversations(projectsDir, cwd string, limit int) ([]protocol.Conversation, error) {
	dir := filepath.Join(projectsDir, EncodeProjectPath(cwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []protocol.Conversation{}, nil
		}
		return nil, err
	}
	var out []protocol.Conversation
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, protocol.Conversation{
			SessionID:  strings.TrimSuffix(e.Name(), ".jsonl"),
			ModifiedAt: info.ModTime().UnixMilli(),
			Size:       info.Size(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModifiedAt > out[j].ModifiedAt })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	for i := range out {
		f, err := os.Open(filepath.Join(dir, out[i].SessionID+".jsonl"))
		if err != nil {
			continue
		}
		out[i].FirstPrompt = FirstPrompt(f)
		f.Close()
	}
	if out == nil {
		out = []protocol.Conversation{}
	}
	return out, nil
}

type transcriptLine struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Message     struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// FirstPrompt scans a transcript for the first real user prompt.
// Lines may be very long, so a bufio.Reader is used instead of Scanner.
func FirstPrompt(r io.Reader) string {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if p, ok := promptFromLine(line); ok {
				return p
			}
		}
		if err != nil {
			return ""
		}
	}
}

func promptFromLine(line []byte) (string, bool) {
	var tl transcriptLine
	if err := json.Unmarshal(line, &tl); err != nil {
		return "", false
	}
	if tl.Type != "user" || tl.IsSidechain || tl.IsMeta || tl.Message.Role != "user" {
		return "", false
	}
	text := contentText(tl.Message.Content)
	text = strings.TrimSpace(text)
	if text == "" || strings.HasPrefix(text, "<command-name>") || strings.HasPrefix(text, "<local-command") || strings.HasPrefix(text, "<system-reminder>") {
		return "", false
	}
	const max = 200
	if len(text) > max {
		text = text[:max] + "…"
	}
	return text, true
}

func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" && p.Text != "" {
				if b.Len() > 0 {
					b.WriteString(" ")
				}
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

// Hook event names reported by `orchestrator _hook <event>`.
const (
	HookNotification = "notification"
	HookStop         = "stop"
	HookPrompt       = "prompt"
)

// StatusForHook maps a hook event to a session status, or "" for no change.
func StatusForHook(event string) string {
	switch event {
	case HookNotification, HookStop:
		return protocol.StatusWaiting
	case HookPrompt:
		return protocol.StatusRunning
	}
	return ""
}

// HookSettings builds the JSON passed to `claude --settings`.
func HookSettings(exe string) []byte {
	cmd := func(ev string) map[string]any {
		return map[string]any{
			"hooks": []map[string]any{{
				"type":    "command",
				"command": shellQuote(exe) + " _hook " + ev,
				"timeout": 5,
			}},
		}
	}
	doc := map[string]any{
		"hooks": map[string]any{
			"Notification":     []map[string]any{cmd(HookNotification)},
			"Stop":             []map[string]any{cmd(HookStop)},
			"UserPromptSubmit": []map[string]any{cmd(HookPrompt)},
		},
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	return b
}

// WriteHookSettings writes the hook settings file for the given executable.
func WriteHookSettings(path, exe string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, HookSettings(exe), 0o600)
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}()<>|&;") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
