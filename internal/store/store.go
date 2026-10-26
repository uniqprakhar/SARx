// Package store owns the on-disk layout under .sarx/ (spec/SPEC.md section 2).
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"sarx/internal/proc"
	"sarx/internal/spec"
	"sarx/internal/util"
)

// DirName is the name of the state directory in a project root.
const DirName = ".sarx"

// Task is one task directory.
type Task struct {
	Root string // project root
	Run  string // run name, empty when the task lives outside .sarx/runs
	ID   string // task directory name
	Dir  string // absolute task directory
}

func (t Task) path(name string) string { return filepath.Join(t.Dir, name) }

// File locations inside a task directory.
func (t Task) Brief() string       { return t.path("task.md") }
func (t Task) Worker() string      { return t.path("worker.md") }
func (t Task) Dispatch() string    { return t.path("dispatch.json") }
func (t Task) Progress() string    { return t.path("progress.md") }
func (t Task) Journal() string     { return t.path("journal.md") }
func (t Task) ResultMD() string    { return t.path("result.md") }
func (t Task) ResultJSON() string  { return t.path("result.json") }
func (t Task) StateJSON() string   { return t.path("state.json") }
func (t Task) Events() string      { return t.path("events.jsonl") }
func (t Task) Stdout() string      { return t.path("stdout.txt") }
func (t Task) Stderr() string      { return t.path("stderr.txt") }
func (t Task) StopRequest() string { return t.path("stop.request") }
func (t Task) Attempt(n int) string {
	return filepath.Join(t.Dir, "attempts", fmt.Sprintf("%d", n))
}

// StateDir is the .sarx directory of a project root.
func StateDir(root string) string { return filepath.Join(root, DirName) }

// LocksDir is where resource locks live.
func LocksDir(root string) string { return filepath.Join(StateDir(root), "locks") }

// RunsDir is where runs live.
func RunsDir(root string) string { return filepath.Join(StateDir(root), "runs") }

// FindRoot walks up from start to the nearest directory containing .sarx. It returns "" if none.
func FindRoot(start string) string {
	d, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		if st, err := os.Stat(filepath.Join(d, DirName)); err == nil && st.IsDir() {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// FindConfigRoot is FindRoot, falling back to the nearest directory holding a committed sarx.toml,
// so read-only commands such as `sarx agents` see a team's shared config in a fresh clone.
func FindConfigRoot(start string) string {
	if r := FindRoot(start); r != "" {
		return r
	}
	d, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		if st, err := os.Stat(filepath.Join(d, "sarx.toml")); err == nil && !st.IsDir() {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// GitRoot returns the git top-level directory containing start, or "".
func GitRoot(start string) string {
	out, err := exec.Command("git", "-C", start, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return filepath.Clean(strings.TrimSpace(string(out)))
}

// EnsureRoot returns the project root for start, creating .sarx if the project has none yet.
// The root is an existing .sarx parent, else the git top-level, else start itself.
func EnsureRoot(start string) (string, error) {
	if r := FindRoot(start); r != "" {
		return r, nil
	}
	root := GitRoot(start)
	if root == "" {
		var err error
		if root, err = filepath.Abs(start); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(StateDir(root), 0o755); err != nil {
		return "", fmt.Errorf("cannot create %s: %v", StateDir(root), err)
	}
	excludeFromGit(root)
	return root, nil
}

// excludeFromGit adds .sarx/ to .git/info/exclude so run files stay out of commits without touching .gitignore.
func excludeFromGit(root string) {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--git-path", "info/exclude").Output()
	if err != nil {
		return
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	data, _ := os.ReadFile(p)
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == DirName+"/" {
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		f.WriteString("\n")
	}
	f.WriteString(DirName + "/\n")
}

// TaskAt builds a Task from a directory path.
func TaskAt(root, dir string) Task {
	dir, _ = filepath.Abs(dir)
	t := Task{Root: root, ID: filepath.Base(dir), Dir: dir}
	parent := filepath.Dir(dir)
	if filepath.Clean(filepath.Dir(parent)) == filepath.Clean(RunsDir(root)) {
		t.Run = filepath.Base(parent)
	}
	return t
}

// ResolveTask finds a task from a directory, a task.md path, a task id, or a unique id prefix.
func ResolveTask(root, ref string) (Task, error) {
	if ref == "" {
		return Task{}, fmt.Errorf("no task given")
	}
	if st, err := os.Stat(ref); err == nil {
		if st.IsDir() && util.Exists(filepath.Join(ref, "task.md")) {
			return TaskAt(root, ref), nil
		}
		if !st.IsDir() && filepath.Base(ref) == "task.md" {
			return TaskAt(root, filepath.Dir(ref)), nil
		}
	}
	runs, _ := os.ReadDir(RunsDir(root))
	var names []string
	for _, r := range runs {
		if r.IsDir() {
			names = append(names, r.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // newest run first
	var exact, prefix []string
	for _, run := range names {
		ents, _ := os.ReadDir(filepath.Join(RunsDir(root), run))
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(RunsDir(root), run, e.Name())
			if !util.Exists(filepath.Join(p, "task.md")) {
				continue
			}
			switch {
			case e.Name() == ref || run+"/"+e.Name() == filepath.ToSlash(ref):
				exact = append(exact, p)
			case strings.HasPrefix(e.Name(), ref):
				prefix = append(prefix, p)
			}
		}
	}
	if len(exact) > 0 {
		return TaskAt(root, exact[0]), nil
	}
	if len(prefix) == 1 {
		return TaskAt(root, prefix[0]), nil
	}
	if len(prefix) > 1 {
		return Task{}, fmt.Errorf("%q matches %d tasks; use a longer id or the full path", ref, len(prefix))
	}
	return Task{}, fmt.Errorf("task not found: %s (looked in %s)", ref, RunsDir(root))
}

// CurrentRun returns the run that new tasks should join, creating a new one when the last is old.
func CurrentRun(root, slugHint string) (string, error) {
	marker := filepath.Join(StateDir(root), "current-run")
	if st, err := os.Stat(marker); err == nil && time.Since(st.ModTime()) < 6*time.Hour {
		if data, err := os.ReadFile(marker); err == nil {
			name := strings.TrimSpace(string(data))
			if name != "" && util.Exists(filepath.Join(RunsDir(root), name)) {
				return name, nil
			}
		}
	}
	name := time.Now().Format("20060102-1504") + "-" + util.Slug(slugHint, 30)
	if err := os.MkdirAll(filepath.Join(RunsDir(root), name), 0o755); err != nil {
		return "", err
	}
	return name, util.WriteFileAtomic(marker, []byte(name+"\n"), 0o644)
}

// SetCurrentRun records the run that new tasks join.
func SetCurrentRun(root, name string) error {
	return util.WriteFileAtomic(filepath.Join(StateDir(root), "current-run"), []byte(name+"\n"), 0o644)
}

var numRe = regexp.MustCompile(`^(\d\d)-`)

// NewTask allocates the next numbered task directory in a run and writes its brief.
func NewTask(root, run, slug, brief string) (Task, error) {
	runDir := filepath.Join(RunsDir(root), run)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return Task{}, err
	}
	ents, _ := os.ReadDir(runDir)
	next := 1
	for _, e := range ents {
		if m := numRe.FindStringSubmatch(e.Name()); m != nil {
			var n int
			fmt.Sscanf(m[1], "%d", &n)
			if n >= next {
				next = n + 1
			}
		}
	}
	id := fmt.Sprintf("%02d-%s", next, util.Slug(slug, 40))
	dir := filepath.Join(runDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Task{}, err
	}
	t := TaskAt(root, dir)
	return t, util.WriteFileAtomic(t.Brief(), []byte(brief), 0o644)
}

// ReadState loads state.json.
func ReadState(t Task) (*spec.State, error) {
	data, err := os.ReadFile(t.StateJSON())
	if err != nil {
		return nil, err
	}
	var s spec.State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %v", t.StateJSON(), err)
	}
	return &s, nil
}

// WriteState replaces state.json atomically.
func WriteState(t Task, s *spec.State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return util.WriteFileAtomic(t.StateJSON(), append(data, '\n'), 0o644)
}

// CreateState writes state.json only if none exists, which is how a dispatch claims a task.
func CreateState(t Task, s *spec.State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(t.StateJSON(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// AppendEvent adds one line to events.jsonl. Failures are ignored: the audit log must never break a run.
func AppendEvent(t Task, typ, msg string) {
	line, err := json.Marshal(spec.Event{TS: util.Now(), Type: typ, Msg: msg})
	if err != nil {
		return
	}
	f, err := os.OpenFile(t.Events(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}

// ReadOptions loads dispatch.json.
func ReadOptions(t Task) (spec.Options, error) {
	var o spec.Options
	data, err := os.ReadFile(t.Dispatch())
	if err != nil {
		return o, err
	}
	return o, json.Unmarshal(data, &o)
}

// WriteOptions saves dispatch.json.
func WriteOptions(t Task, o spec.Options) error {
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	return util.WriteFileAtomic(t.Dispatch(), append(data, '\n'), 0o644)
}

// Effective returns the state to show: a task recorded as live whose engine process is gone is STALLED.
func Effective(s *spec.State) string {
	if (s.State == spec.Running || s.State == spec.Queued) && !proc.Alive(s.SupervisorPID) {
		return spec.Stalled
	}
	return s.State
}

// List returns every task under the project, newest first.
func List(root string) []Task {
	var out []Task
	runs, _ := os.ReadDir(RunsDir(root))
	var names []string
	for _, r := range runs {
		if r.IsDir() {
			names = append(names, r.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, run := range names {
		ents, _ := os.ReadDir(filepath.Join(RunsDir(root), run))
		var ids []string
		for _, e := range ents {
			if e.IsDir() {
				ids = append(ids, e.Name())
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(ids)))
		for _, id := range ids {
			out = append(out, TaskAt(root, filepath.Join(RunsDir(root), run, id)))
		}
	}
	return out
}
