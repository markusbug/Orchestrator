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

// Hook event names reported by `orchestrator _hook <event>`. They are the
// Claude Code hook names, lower-cased, so a settings file and a report line
// up without a table.
const (
	HookSessionStart = "sessionstart"
	HookPrompt       = "userpromptsubmit"
	HookStop         = "stop"
	HookStopFailure  = "stopfailure"
	HookNotification = "notification"
	HookPermission   = "permissionrequest"
	HookPreTool      = "pretooluse"
	HookPostTool     = "posttooluse"
)

// Hook is one hook event as delivered to the daemon: the event name plus
// the payload fields that decide what it means.
type Hook struct {
	Event            string `json:"event"`
	NotificationType string `json:"notification_type,omitempty"`
	ToolName         string `json:"tool_name,omitempty"`
	// AgentID is set when the hook fired inside a subagent. Subagents keep
	// running after the main agent has stopped, so their hooks say nothing
	// about whether the person is needed.
	AgentID string `json:"agent_id,omitempty"`
}

// ParseHookPayload extracts the fields Hook needs from the JSON Claude Code
// writes to a hook's stdin. Anything unreadable yields an empty Hook.
func ParseHookPayload(r io.Reader) Hook {
	var h Hook
	_ = json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&h)
	return h
}

// StatusForHook maps a hook event to a session status and, for waiting, the
// reason (protocol.WaitIdle or protocol.WaitInput). It returns "" for events
// that must not move the status.
//
// The mapping follows what Claude Code 2.1 actually fires:
//
//   - UserPromptSubmit starts a turn, including queued prompts and the
//     task-notification wake-ups after a background agent finishes.
//   - PreToolUse/PostToolUse mean the main agent is working. They also lift
//     an input wait once a permission was answered or a question submitted,
//     which no other hook reports. Inside subagents they are ignored.
//   - PermissionRequest fires the moment a permission dialog opens;
//     Notification(permission_prompt) follows only after several seconds.
//   - AskUserQuestion is a tool whose PreToolUse fires as its dialog opens.
//   - Stop and StopFailure end a turn. Nothing fires on a user interrupt;
//     the session layer handles that from the Escape key itself.
//   - SessionStart fires once the TUI is up and idle at its prompt, so a
//     fresh session is not shown as running until its first Stop.
func StatusForHook(h Hook) (status, reason string) {
	if h.AgentID != "" {
		return "", ""
	}
	switch strings.ToLower(h.Event) {
	case HookPrompt, HookPostTool:
		return protocol.StatusRunning, ""
	case HookPreTool:
		if h.ToolName == "AskUserQuestion" {
			return protocol.StatusWaiting, protocol.WaitInput
		}
		return protocol.StatusRunning, ""
	case HookPermission:
		return protocol.StatusWaiting, protocol.WaitInput
	case HookNotification:
		switch h.NotificationType {
		case "permission_prompt", "elicitation_dialog":
			return protocol.StatusWaiting, protocol.WaitInput
		case "idle_prompt":
			return protocol.StatusWaiting, protocol.WaitIdle
		}
		return "", ""
	case HookStop, HookStopFailure, HookSessionStart:
		return protocol.StatusWaiting, protocol.WaitIdle
	}
	return "", ""
}

// hookEvents lists the Claude Code hook names the daemon subscribes to.
var hookEvents = []string{"SessionStart", "UserPromptSubmit", "Stop", "StopFailure", "Notification", "PermissionRequest", "PreToolUse", "PostToolUse"}

// HookSettings builds the JSON passed to `claude --settings`.
func HookSettings(exe string) []byte {
	hooks := map[string]any{}
	for _, ev := range hookEvents {
		hooks[ev] = []map[string]any{{
			"hooks": []map[string]any{{
				"type":    "command",
				"command": shellQuote(exe) + " _hook " + strings.ToLower(ev),
				"timeout": 5,
			}},
		}}
	}
	b, _ := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
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
