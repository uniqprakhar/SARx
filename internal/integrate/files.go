package integrate

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"

	"sarx/internal/util"
)

// Result of writing one file.
const (
	Created   = "created"
	Updated   = "updated"
	Unchanged = "unchanged"
)

// UpsertBlock puts body between the sarx markers in path. Text outside the markers is never touched.
// header is used only when the file does not exist yet.
func UpsertBlock(path, header, body string) (string, error) {
	block := BeginMarker + "\n" + strings.TrimRight(body, "\n") + "\n" + EndMarker
	old, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Created, write(path, header+block+"\n")
	}
	if err != nil {
		return "", err
	}
	s := string(old)
	var next string
	b, e := strings.Index(s, BeginMarker), strings.Index(s, EndMarker)
	if b >= 0 && e > b {
		next = s[:b] + block + s[e+len(EndMarker):]
	} else {
		next = strings.TrimRight(s, "\r\n")
		if next != "" {
			next += "\n\n"
		}
		next += block + "\n"
	}
	if next == s {
		return Unchanged, nil
	}
	return Updated, write(path, next)
}

func write(path, content string) error {
	return util.WriteFileAtomic(path, []byte(content), 0o644)
}

// SelfExec is how a tool should start sarx: the bare name if it resolves to this binary, else the full path.
func SelfExec() string {
	exe, err := os.Executable()
	if err != nil {
		return "sarx"
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	if p, err := exec.LookPath("sarx"); err == nil {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		if a, err := os.Stat(p); err == nil {
			if b, err := os.Stat(exe); err == nil && os.SameFile(a, b) {
				return "sarx"
			}
		}
	}
	return exe
}

// SelfCommand is SelfExec quoted for use inside a shell command string.
func SelfCommand() string {
	e := SelfExec()
	if strings.ContainsAny(e, " \t") {
		return `"` + e + `"`
	}
	return e
}

func readJSONObject(path string) (map[string]any, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return map[string]any{}, true, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, true, fmt.Errorf("%s is not plain JSON (%v)", path, err)
	}
	return doc, true, nil
}

func writeJSON(path string, doc map[string]any) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return write(path, string(data)+"\n")
}

// MCPSnippet is the server entry, for tools whose config sarx does not edit.
func MCPSnippet() string {
	return fmt.Sprintf(`{"mcpServers": {"sarx": {"command": %q, "args": ["mcp"]}}}`, SelfExec())
}

// MergeMCP adds the sarx server to an mcpServers JSON file without disturbing other entries.
func MergeMCP(path string) (string, error) {
	doc, existed, err := readJSONObject(path)
	if err != nil {
		return "", err
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	want := map[string]any{"command": SelfExec(), "args": []any{"mcp"}}
	if reflect.DeepEqual(servers["sarx"], want) {
		return Unchanged, nil
	}
	servers["sarx"] = want
	doc["mcpServers"] = servers
	res := Created
	if existed {
		res = Updated
	}
	return res, writeJSON(path, doc)
}

// MergeClaudeHook adds the PreToolUse hook that blocks built-in subagents while delegation mode is on.
func MergeClaudeHook(path string) (string, error) {
	doc, existed, err := readJSONObject(path)
	if err != nil {
		return "", err
	}
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	pre, _ := hooks["PreToolUse"].([]any)
	for _, entry := range pre {
		m, _ := entry.(map[string]any)
		inner, _ := m["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if c, _ := hm["command"].(string); strings.Contains(c, "hook pretooluse") {
				return Unchanged, nil
			}
		}
	}
	pre = append(pre, map[string]any{
		"matcher": "Agent|Task|Workflow",
		"hooks":   []any{map[string]any{"type": "command", "command": SelfCommand() + " hook pretooluse"}},
	})
	hooks["PreToolUse"] = pre
	doc["hooks"] = hooks
	res := Created
	if existed {
		res = Updated
	}
	return res, writeJSON(path, doc)
}
