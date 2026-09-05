package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncodeProjectPath(t *testing.T) {
	cases := map[string]string{
		"/home/markus/Documents/Orchestrator": "-home-markus-Documents-Orchestrator",
		"/home/markus/dgen1/v0mp1.test_git":   "-home-markus-dgen1-v0mp1-test_git",
		"/home/x/a b":                         "-home-x-a-b",
	}
	for in, want := range cases {
		if got := EncodeProjectPath(in); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}

func TestFirstPrompt(t *testing.T) {
	long := strings.Repeat("x", 70*1024)
	lines := []string{
		`{"type":"summary","summary":"old"}`,
		`{"type":"mode","mode":"normal"}`,
		`{"type":"user","isSidechain":true,"message":{"role":"user","content":"side"}}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"meta"}}`,
		`{"type":"user","message":{"role":"user","content":"<command-name>/clear</command-name>"}}`,
		`{"type":"user","message":{"role":"user","content":"` + long + `"}}`,
	}
	got := FirstPrompt(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	if !strings.HasPrefix(got, "xxxx") || !strings.HasSuffix(got, "…") || len(got) > 210 {
		t.Fatalf("long line prompt wrong: len=%d", len(got))
	}
	arr := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image"},{"type":"text","text":"world"}]}}`
	if got := FirstPrompt(strings.NewReader(arr)); got != "hello world" {
		t.Fatalf("array content: %q", got)
	}
	if got := FirstPrompt(strings.NewReader("not json\n")); got != "" {
		t.Fatalf("garbage: %q", got)
	}
}

func TestConversations(t *testing.T) {
	projects := t.TempDir()
	cwd := "/home/x/proj.a"
	dir := filepath.Join(projects, EncodeProjectPath(cwd))
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "111.jsonl"), []byte(`{"type":"user","message":{"role":"user","content":"first"}}`+"\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "222.jsonl"), []byte(`{"type":"user","message":{"role":"user","content":"second"}}`+"\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "junk.txt"), []byte("x"), 0o600)
	list, err := Conversations(projects, cwd, 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("%v %d", err, len(list))
	}
	seen := map[string]string{}
	for _, c := range list {
		seen[c.SessionID] = c.FirstPrompt
	}
	if seen["111"] != "first" || seen["222"] != "second" {
		t.Fatalf("%+v", seen)
	}
	empty, err := Conversations(projects, "/nope", 10)
	if err != nil || len(empty) != 0 {
		t.Fatal("missing dir should be empty, not error")
	}
	limited, _ := Conversations(projects, cwd, 1)
	if len(limited) != 1 {
		t.Fatal("limit ignored")
	}
}

func TestHookSettingsShape(t *testing.T) {
	b := HookSettings("/opt/my dir/orchestrator")
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []string{"Notification", "Stop", "UserPromptSubmit"} {
		g := doc.Hooks[ev]
		if len(g) != 1 || len(g[0].Hooks) != 1 || g[0].Hooks[0].Type != "command" {
			t.Fatalf("%s: %+v", ev, g)
		}
		if !strings.HasPrefix(g[0].Hooks[0].Command, "'/opt/my dir/orchestrator' _hook ") {
			t.Fatalf("command %q", g[0].Hooks[0].Command)
		}
	}
	if StatusForHook("stop") != "waiting" || StatusForHook("prompt") != "running" || StatusForHook("x") != "" {
		t.Fatal("status mapping")
	}
}
