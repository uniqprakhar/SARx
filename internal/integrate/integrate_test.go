package integrate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sarx/internal/spec"
	"sarx/internal/store"
)

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpsertBlockKeepsUserText(t *testing.T) {
	p := filepath.Join(t.TempDir(), "CLAUDE.md")
	os.WriteFile(p, []byte("# Mine\n\nkeep this\n"), 0o644)
	if r, err := UpsertBlock(p, "# H\n", "rules v1"); err != nil || r != Updated {
		t.Fatalf("%v %v", r, err)
	}
	if r, _ := UpsertBlock(p, "# H\n", "rules v1"); r != Unchanged {
		t.Fatalf("second run: %s", r)
	}
	if r, _ := UpsertBlock(p, "# H\n", "rules v2"); r != Updated {
		t.Fatalf("change: %s", r)
	}
	s := read(t, p)
	if !strings.Contains(s, "keep this") || !strings.Contains(s, "rules v2") || strings.Contains(s, "rules v1") {
		t.Fatalf("content:\n%s", s)
	}
	if strings.Count(s, BeginMarker) != 1 {
		t.Fatal("duplicate block")
	}
}

func TestUpsertBlockCreatesWithHeader(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "x.md")
	if r, err := UpsertBlock(p, "# Header\n\n", "body"); err != nil || r != Created {
		t.Fatalf("%v %v", r, err)
	}
	if !strings.HasPrefix(read(t, p), "# Header") {
		t.Fatal("header missing")
	}
}

func TestMergeMCPKeepsOtherServers(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".mcp.json")
	os.WriteFile(p, []byte(`{"mcpServers":{"other":{"command":"x"}},"extra":1}`), 0o644)
	if r, err := MergeMCP(p); err != nil || r != Updated {
		t.Fatalf("%v %v", r, err)
	}
	if r, _ := MergeMCP(p); r != Unchanged {
		t.Fatalf("second: %s", r)
	}
	var doc map[string]any
	json.Unmarshal([]byte(read(t, p)), &doc)
	srv := doc["mcpServers"].(map[string]any)
	if srv["other"] == nil || srv["sarx"] == nil || doc["extra"] == nil {
		t.Fatalf("lost entries: %v", doc)
	}
}

func TestMergeMCPRefusesBrokenJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".mcp.json")
	os.WriteFile(p, []byte(`{not json`), 0o644)
	if _, err := MergeMCP(p); err == nil {
		t.Fatal("should not overwrite a file it cannot parse")
	}
	if read(t, p) != `{not json` {
		t.Fatal("file was modified")
	}
}

func TestMergeClaudeHookIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(p, []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"mine"}]}]}}`), 0o644)
	if r, err := MergeClaudeHook(p); err != nil || r != Updated {
		t.Fatalf("%v %v", r, err)
	}
	if r, _ := MergeClaudeHook(p); r != Unchanged {
		t.Fatalf("second: %s", r)
	}
	s := read(t, p)
	if !strings.Contains(s, "mine") || strings.Count(s, "hook pretooluse") != 1 {
		t.Fatalf("settings:\n%s", s)
	}
}

func project(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	root, err := store.EnsureRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestHook(t *testing.T) {
	root := project(t)
	run := func(js string) int {
		var e bytes.Buffer
		return HookPreToolUse(strings.NewReader(js), &e, root)
	}
	if run(`{"tool_name":"Agent"}`) != 0 {
		t.Fatal("blocked while mode is off")
	}
	ModeOn(root)
	if !ModeIsOn(root) {
		t.Fatal("mode should be on")
	}
	if run(`{"tool_name":"Agent"}`) != 2 || run(`{"tool_name":"task"}`) != 2 {
		t.Fatal("subagent tools not blocked")
	}
	if run(`{"tool_name":"Bash"}`) != 0 {
		t.Fatal("blocked an unrelated tool")
	}
	if run(`garbage`) != 0 || run(``) != 0 {
		t.Fatal("a broken hook payload must allow the call")
	}
	ModeOff(root)
	if run(`{"tool_name":"Agent"}`) != 0 {
		t.Fatal("blocked after mode off")
	}
}

func TestStatusline(t *testing.T) {
	root := project(t)
	if Statusline(root, time.Now(), false) != "" {
		t.Fatal("empty project should print nothing")
	}
	tk, err := store.NewTask(root, "r", "job", "# Job\n\n## Subtasks\n- [ ] a\n")
	if err != nil {
		t.Fatal(err)
	}
	st := &spec.State{Version: 1, ID: tk.ID, State: spec.Running, Agent: "claude", Role: "builder", Started: time.Now().Unix() - 5}
	if err := store.CreateState(tk, st); err != nil {
		t.Fatal(err)
	}
	line := Statusline(root, time.Now(), false)
	if !strings.Contains(line, "claude") || strings.Contains(line, "\x1b") {
		t.Fatalf("line %q", line)
	}
	st.State = spec.Done
	store.WriteState(tk, st)
	if Statusline(root, time.Now(), false) != "" {
		t.Fatal("finished tasks should not show")
	}
}

func TestInitIsIdempotentAndKeepsConfig(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o755)
	name := "claude"
	if os.PathSeparator == '\\' {
		name += ".exe"
	}
	os.WriteFile(filepath.Join(bin, name), []byte("x"), 0o755)
	t.Setenv("PATH", bin)
	t.Setenv("SARX_CONFIG", filepath.Join(dir, "cfg", "sarx.toml"))
	proj := filepath.Join(dir, "proj")
	os.MkdirAll(proj, 0o755)

	o := InitOptions{Dir: proj, Yes: true, For: []string{"claude", "agents-md", "cursor", "gemini", "codex"}}
	first, err := Init(o)
	if err != nil {
		t.Fatal(err)
	}
	if first.ConfigNote != "written" || first.Roles["builder"] != "claude" {
		t.Fatalf("%+v", first)
	}
	snap := map[string]string{}
	for _, a := range first.Actions {
		snap[a.Path] = read(t, a.Path)
	}
	second, err := Init(o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(second.ConfigNote, "kept") {
		t.Fatalf("config note: %s", second.ConfigNote)
	}
	for _, a := range second.Actions {
		if a.Result != Unchanged {
			t.Errorf("%s: %s on second run", a.Path, a.Result)
		}
		if read(t, a.Path) != snap[a.Path] {
			t.Errorf("%s changed", a.Path)
		}
	}
	for _, f := range []string{"AGENTS.md", "CLAUDE.md", "GEMINI.md", ".mcp.json", ".gemini/settings.json", ".cursor/rules/sarx.mdc"} {
		if _, err := os.Stat(filepath.Join(proj, filepath.FromSlash(f))); err != nil {
			t.Errorf("missing %s", f)
		}
	}
}

func TestInitWithoutAgentsStillWires(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("SARX_CONFIG", filepath.Join(dir, "sarx.toml"))
	res, err := Init(InitOptions{Dir: dir, Yes: true, For: []string{"agents-md"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.ConfigNote, "not written") || len(res.Notes) == 0 {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Fatal("AGENTS.md missing")
	}
}

func TestInitRejectsUnknownTarget(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SARX_CONFIG", filepath.Join(dir, "c.toml"))
	if _, err := Init(InitOptions{Dir: dir, Yes: true, For: []string{"emacs"}}); err == nil {
		t.Fatal("expected an error naming the valid targets")
	}
}
