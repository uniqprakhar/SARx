package integrate

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"sarx/internal/store"
	"sarx/internal/util"
)

func modeFile(root string) string { return filepath.Join(store.StateDir(root), "mode") }

// ModeOn turns delegation mode on for the project.
func ModeOn(root string) error {
	return util.WriteFileAtomic(modeFile(root), []byte("on\n"), 0o644)
}

// ModeOff turns delegation mode off.
func ModeOff(root string) error {
	err := os.Remove(modeFile(root))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ModeIsOn reports whether delegation mode is on.
func ModeIsOn(root string) bool {
	data, err := os.ReadFile(modeFile(root))
	return err == nil && strings.TrimSpace(string(data)) == "on"
}

var blockedTools = map[string]bool{"agent": true, "task": true, "workflow": true}

// HookPreToolUse implements `sarx hook pretooluse` for Claude Code. It reads the hook's JSON from
// in and returns the exit code: 2 blocks the tool call (the message on errOut is shown to the
// model), 0 allows it. Anything unexpected allows the call, so a broken hook never locks a session.
func HookPreToolUse(in io.Reader, errOut io.Writer, cwd string) int {
	var ev struct {
		ToolName string `json:"tool_name"`
		Cwd      string `json:"cwd"`
	}
	data, err := io.ReadAll(io.LimitReader(in, 4<<20))
	if err != nil || json.Unmarshal(data, &ev) != nil {
		return 0
	}
	if !blockedTools[strings.ToLower(ev.ToolName)] {
		return 0
	}
	dir := ev.Cwd
	if dir == "" {
		dir = cwd
	}
	root := store.FindRoot(dir)
	if root == "" || !ModeIsOn(root) {
		return 0
	}
	fmt.Fprintf(errOut, "sarx delegation mode is on, so built-in subagents are blocked. Delegate with `sarx dispatch` instead (see the sarx instructions). If the user asked for a built-in subagent by name, tell them delegation mode has to be turned off first with `sarx mode off`.\n")
	return 2
}
