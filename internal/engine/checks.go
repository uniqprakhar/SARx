package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"sarx/internal/proc"
	"sarx/internal/spec"
	"sarx/internal/util"
)

// checkTimeout caps how long a single Done-when block may run.
const checkTimeout = 10 * time.Minute

// RunChecks runs each Done-when block in dir and reports pass or fail per block.
// shell overrides the platform default shell when set.
func RunChecks(dir string, checks []string, shell string) (*spec.Verified, error) {
	v := &spec.Verified{Ran: true}
	tmp, err := os.MkdirTemp("", "sarx-verify-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	for i, snippet := range checks {
		exe, args, err := shellCommand(tmp, i+1, snippet, shell)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
		cmd := exec.CommandContext(ctx, exe, args...)
		cmd.Dir = dir
		cmd.SysProcAttr = proc.WorkerAttrs()
		cmd.Cancel = func() error { proc.KillTree(cmd.Process.Pid, time.Second); return nil }
		out, runErr := cmd.CombinedOutput()
		timedOut := ctx.Err() == context.DeadlineExceeded
		cancel()
		if runErr == nil {
			v.Pass++
			continue
		}
		code := -1
		if ee, ok := runErr.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		tail := util.Tail(string(out), 5)
		if timedOut {
			tail = "timed out after " + checkTimeout.String()
		} else if len(out) == 0 {
			tail = runErr.Error()
		}
		v.Fail++
		v.Details = append(v.Details, spec.CheckDetail{Index: i + 1, Exit: code, Tail: tail})
	}
	return v, nil
}

// shellCommand writes a snippet to a temp script and returns how to run it on this platform.
func shellCommand(tmp string, n int, snippet, shell string) (string, []string, error) {
	lower := strings.ToLower(shell)
	switch {
	case strings.Contains(lower, "powershell") || strings.Contains(lower, "pwsh"):
		p := filepath.Join(tmp, fmt.Sprintf("%02d.ps1", n))
		if err := os.WriteFile(p, []byte(snippet+"\n"), 0o644); err != nil {
			return "", nil, err
		}
		return shell, []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", p}, nil
	case runtime.GOOS == "windows" && shell == "":
		p := filepath.Join(tmp, fmt.Sprintf("%02d.cmd", n))
		body := "@echo off\r\n" + strings.ReplaceAll(strings.ReplaceAll(snippet, "\r\n", "\n"), "\n", "\r\n") + "\r\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return "", nil, err
		}
		return "cmd", []string{"/C", p}, nil
	default:
		p := filepath.Join(tmp, fmt.Sprintf("%02d.sh", n))
		if err := os.WriteFile(p, []byte(snippet+"\n"), 0o644); err != nil {
			return "", nil, err
		}
		sh := shell
		if sh == "" {
			sh = "sh"
			if _, err := exec.LookPath("bash"); err == nil {
				sh = "bash"
			}
		}
		return sh, []string{p}, nil
	}
}

// Snapshot fingerprints the files git considers changed, by content hash.
type Snapshot map[string]string

const bigFile = 50 << 20

// TakeSnapshot returns the fingerprint of the working tree at dir. ok is false outside a git repository.
func TakeSnapshot(dir string) (Snapshot, bool) {
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain=v1", "-z", "--untracked-files=all").Output()
	if err != nil {
		return nil, false
	}
	snap := Snapshot{}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		status, path := f[:2], f[3:]
		if status[0] == 'R' || status[0] == 'C' {
			i++ // the next field is the original path
		}
		path = filepath.ToSlash(path)
		if strings.HasPrefix(path, ".sarx/") {
			continue
		}
		snap[path] = fingerprint(filepath.Join(dir, filepath.FromSlash(path)))
	}
	return snap, true
}

func fingerprint(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return "absent"
	}
	if st.IsDir() {
		return "dir"
	}
	if st.Size() > bigFile {
		return fmt.Sprintf("big:%d:%d", st.Size(), st.ModTime().UnixNano())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "unreadable"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Changed lists files whose state differs between two snapshots.
func Changed(before, after Snapshot) []string {
	set := map[string]bool{}
	for p, h := range after {
		if before[p] != h {
			set[p] = true
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			set[p] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// scopeWarnings turns a change list into warnings for a run.
func scopeWarnings(changed []string, readOnly bool, inScope func(string) bool) []string {
	var bad []string
	for _, p := range changed {
		if readOnly || (inScope != nil && !inScope(p)) {
			bad = append(bad, p)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	list := bad
	more := ""
	if len(list) > 8 {
		more = fmt.Sprintf(" and %d more", len(list)-8)
		list = list[:8]
	}
	what := "changed outside the brief's Scope"
	if readOnly {
		what = "changed by a read-only run"
	}
	return []string{fmt.Sprintf("%d file(s) %s: %s%s (this can include edits made by you or by other tasks during the run)",
		len(bad), what, strings.Join(list, ", "), more)}
}
