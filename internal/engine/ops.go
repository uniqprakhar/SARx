package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"sarx/internal/brief"
	"sarx/internal/proc"
	"sarx/internal/spec"
	"sarx/internal/store"
	"sarx/internal/util"
)

func marshal(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Row is one line of the task listing.
type Row struct {
	ID       string `json:"id"`
	Run      string `json:"run"`
	Agent    string `json:"agent"`
	Role     string `json:"role"`
	State    string `json:"state"`
	Done     int    `json:"done"`
	Total    int    `json:"total"`
	AgeSecs  int64  `json:"age_seconds"`
	QuietFor int64  `json:"quiet_seconds,omitempty"`
	Quiet    bool   `json:"quiet,omitempty"`
	Step     string `json:"step,omitempty"`
	TaskDir  string `json:"task_dir"`
	Result   string `json:"result"`
	Progress string `json:"progress"`
}

// List returns tasks that are still live, or every task when all is set.
func (e *Engine) List(all bool) []Row {
	var rows []Row
	now := time.Now()
	for _, t := range store.List(e.Root) {
		st, err := store.ReadState(t)
		if err != nil {
			continue
		}
		state := store.Effective(st)
		if !all && !spec.IsLive(state) {
			continue
		}
		done, total := Ticks(t)
		if total == 0 {
			total = st.Subtasks
		}
		r := Row{
			ID: t.ID, Run: t.Run, Agent: st.Agent, Role: st.Role, State: state, Done: done, Total: total,
			AgeSecs: now.Unix() - st.Started, Step: LastStep(t), TaskDir: t.Dir, Result: st.Result, Progress: st.Progress,
		}
		if !spec.IsLive(state) {
			r.AgeSecs = st.Seconds
		}
		if state == spec.Running {
			r.QuietFor = int64(now.Sub(lastActivity(t)).Seconds())
			r.Quiet = r.QuietFor > int64(e.Cfg.Defaults.StallAfter)
		}
		rows = append(rows, r)
	}
	return rows
}

func lastActivity(t store.Task) time.Time {
	var newest time.Time
	for _, f := range []string{t.Journal(), t.Progress(), t.Stdout(), t.Stderr(), t.StateJSON()} {
		if fi, err := os.Stat(f); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	return newest
}

// Stop ends a live task and returns its final state.
func (e *Engine) Stop(t store.Task) (*spec.State, error) {
	st, err := store.ReadState(t)
	if err != nil {
		return nil, fmt.Errorf("%s has not been dispatched", t.ID)
	}
	eff := store.Effective(st)
	if !spec.IsLive(eff) {
		return st, fmt.Errorf("%s is %s, not running", t.ID, eff)
	}
	if eff == spec.Stalled {
		// The supervisor is gone. Make sure the worker is too, then record it.
		if st.PID > 0 && proc.Alive(st.PID) {
			proc.KillTree(st.PID, 3*time.Second)
		}
		st.State, st.Finished, st.Exit, st.Note = spec.Cancelled, util.Now(), 130, "stopped after its supervisor died"
		store.AppendEvent(t, "stop", "stalled task cancelled")
		return st, store.WriteState(t, st)
	}
	if err := util.WriteFileAtomic(t.StopRequest(), []byte("stop\n"), 0o644); err != nil {
		return st, err
	}
	store.AppendEvent(t, "stop", "stop requested")
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := store.ReadState(t)
		if err == nil && !spec.IsLive(cur.State) {
			return cur, nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	// The supervisor ignored the request; interrupt it directly.
	_ = proc.Interrupt(st.SupervisorPID)
	time.Sleep(5 * time.Second)
	cur, _ := store.ReadState(t)
	if cur == nil {
		cur = st
	}
	return cur, nil
}

// Wait blocks until a task finishes or the timeout passes. timeout 0 waits forever.
func (e *Engine) Wait(t store.Task, timeout time.Duration) (*spec.State, bool, error) {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		st, err := store.ReadState(t)
		if err != nil {
			return nil, false, fmt.Errorf("%s has not been dispatched", t.ID)
		}
		if !spec.IsLive(store.Effective(st)) || store.Effective(st) == spec.Stalled {
			return st, true, nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return st, false, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// Verify runs the brief's Done-when checks now and records the outcome in state.json.
func (e *Engine) Verify(t store.Task) (*spec.Verified, error) {
	text, err := os.ReadFile(t.Brief())
	if err != nil {
		return nil, err
	}
	b := brief.Parse(string(text))
	if len(b.Checks) == 0 {
		return nil, errors.New("no fenced checks under '## Done when'")
	}
	dir := e.Root
	st, _ := store.ReadState(t)
	if st != nil && st.Worktree != "" && util.Exists(st.Worktree) {
		dir = st.Worktree
	}
	v, err := RunChecks(dir, b.Checks, e.Cfg.Defaults.Shell)
	if err != nil {
		return nil, err
	}
	if st != nil && !spec.IsLive(st.State) {
		st.Verified = v
		_ = store.WriteState(t, st)
	}
	store.AppendEvent(t, "verify", fmt.Sprintf("%d passed, %d failed", v.Pass, v.Fail))
	return v, nil
}

var standIn = map[string]string{
	spec.Operator: spec.Builder, spec.Builder: spec.Fallback, spec.Fallback: spec.Reviewer,
}

var escalate = map[string]string{
	spec.Operator: spec.Builder, spec.Builder: spec.Reviewer, spec.Fallback: spec.Builder,
}

// retryRole decides which role a failed task goes to next. An empty result means there is no stand-in.
func retryRole(role, state string) string {
	switch state {
	case spec.Crashed:
		return standIn[role]
	case spec.Blocked, spec.Failed:
		return escalate[role]
	default: // QUOTA, TIMEOUT, STALLED, UNMARKED, NO_REPORT, CANCELLED: the same agent is the right place
		return role
	}
}

// Retry writes the next attempt's brief, recording the earlier attempt in it, and dispatches it.
func (e *Engine) Retry(t store.Task, asAgent, asRole, why string, o spec.Options, detach bool) (*spec.State, store.Task, error) {
	st, err := store.ReadState(t)
	if err != nil {
		return nil, t, fmt.Errorf("%s has not been dispatched; use sarx dispatch", t.ID)
	}
	eff := store.Effective(st)
	switch {
	case eff == spec.Done || eff == spec.DoneNotes:
		return nil, t, fmt.Errorf("%s is %s; write a new task for follow-up work instead of retrying", t.ID, eff)
	case eff == spec.Running || eff == spec.Queued:
		return nil, t, fmt.Errorf("%s is %s; stop it first with: sarx stop %s", t.ID, eff, t.ID)
	}

	switch {
	case asAgent != "":
		o.Agent = asAgent
	case asRole != "":
		o.Role = asRole
	default:
		next := retryRole(st.Role, eff)
		if st.Role == "custom" || st.Role == "" {
			o.Agent = st.Agent
		} else if next == "" {
			return nil, t, fmt.Errorf("there is no stand-in for the %s role after %s; bring %s to the user", st.Role, eff, st.Result)
		} else {
			o.Role = next
		}
	}
	if st.Mode == "ro" {
		o.ReadOnly = true
	}

	n := 2
	var newDir string
	base := strings.TrimSuffix(t.ID, "")
	if i := strings.LastIndex(base, "-r"); i > 0 && atoi(base[i+2:]) > 0 && !strings.ContainsAny(base[i+2:], "-") {
		base = base[:i]
	}
	for {
		newDir = filepath.Join(filepath.Dir(t.Dir), fmt.Sprintf("%s-r%d", base, n))
		if !util.Exists(newDir) {
			break
		}
		n++
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		return nil, t, err
	}
	original, err := os.ReadFile(t.Brief())
	if err != nil {
		return nil, t, err
	}
	var sb strings.Builder
	title := st.Title
	if title == "" {
		title = t.ID
	}
	fmt.Fprintf(&sb, "# %s - retry %d\n\n## Retry context\n", title, n)
	fmt.Fprintf(&sb, "This is retry %d of `%s`, handed to you after the previous attempt by `%s` ended `%s`.\n\n", n, t.ID, st.Agent, eff)
	if why != "" {
		fmt.Fprintf(&sb, "Reason: %s\n\n", why)
	}
	fmt.Fprintf(&sb, "- Previous report: %s\n- Previous journal (what it did, step by step): %s\n- Previous checklist (ticked items are already done): %s\n", st.Result, st.Journal, st.Progress)
	if st.Session != "" {
		fmt.Fprintf(&sb, "- Previous session id: %s\n", st.Session)
	}
	sb.WriteString("\nStart by reading the previous report and journal. Do not redo what is already finished; verify it if the brief lets you, then carry on from the first unfinished item.\n\n---\n\n")
	sb.WriteString(stripTitle(string(original)))
	nt := store.TaskAt(e.Root, newDir)
	if err := util.WriteFileAtomic(nt.Brief(), []byte(sb.String()), 0o644); err != nil {
		return nil, t, err
	}
	o.RetryOf = t.ID
	o.Resuming = false
	o.SessionID = ""
	store.AppendEvent(t, "retry", "retried as "+nt.ID)
	e.logf("sarx retry %s: wrote %s", t.ID, nt.ID)
	ns, err := e.Dispatch(nt, o, detach)
	return ns, nt, err
}

func stripTitle(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	inFront := false
	for i, l := range lines {
		if i == 0 && strings.TrimSpace(l) == "---" {
			inFront = true
			continue
		}
		if inFront {
			if strings.TrimSpace(l) == "---" {
				inFront = false
			}
			continue
		}
		if strings.HasPrefix(l, "# ") {
			return strings.Join(append(lines[:i:i], lines[i+1:]...), "\n")
		}
	}
	return s
}

// Apply merges a worktree task's branch into the current branch and removes the worktree.
func (e *Engine) Apply(t store.Task) (string, error) {
	st, err := store.ReadState(t)
	if err != nil {
		return "", err
	}
	if st.Branch == "" {
		return "", fmt.Errorf("%s did not run in a worktree; its changes are already in your project", t.ID)
	}
	if spec.IsLive(store.Effective(st)) {
		return "", fmt.Errorf("%s is still %s", t.ID, st.State)
	}
	commitWorktree(st.Worktree, st.Title)
	out, err := exec.Command("git", "-C", e.Root, "merge", "--no-ff", "-m", "sarx: "+firstNonEmpty(st.Title, t.ID), st.Branch).CombinedOutput()
	if err != nil {
		conflicts, _ := exec.Command("git", "-C", e.Root, "diff", "--name-only", "--diff-filter=U").Output()
		_ = exec.Command("git", "-C", e.Root, "merge", "--abort").Run()
		return "", fmt.Errorf("merge of %s failed and was undone. Conflicts in: %s\n  Resolve by hand with: git merge %s", st.Branch,
			strings.Join(strings.Fields(string(conflicts)), ", "), st.Branch)
	}
	_ = e.removeWorktree(st)
	return strings.TrimSpace(string(out)), nil
}

// Discard throws away a worktree task's changes.
func (e *Engine) Discard(t store.Task) error {
	st, err := store.ReadState(t)
	if err != nil {
		return err
	}
	if st.Branch == "" {
		return fmt.Errorf("%s did not run in a worktree; there is nothing to discard", t.ID)
	}
	if spec.IsLive(store.Effective(st)) {
		return fmt.Errorf("%s is still %s", t.ID, st.State)
	}
	return e.removeWorktree(st)
}

func (e *Engine) removeWorktree(st *spec.State) error {
	_ = exec.Command("git", "-C", e.Root, "worktree", "remove", "--force", st.Worktree).Run()
	_ = exec.Command("git", "-C", e.Root, "branch", "-D", st.Branch).Run()
	return nil
}

// ScaffoldOptions describes a brief to create.
type ScaffoldOptions struct {
	Slug     string
	Title    string
	Role     string
	Goal     string
	Subtasks []string
	Checks   []string
	Scope    []string
	Template bool   // true: leave explanatory text for the author to fill in
	Content  string // when set, written verbatim as task.md
}

// Scaffold writes a new brief into the current run and returns its task.
func (e *Engine) Scaffold(o ScaffoldOptions) (store.Task, error) {
	run, err := store.CurrentRun(e.Root, o.Slug)
	if err != nil {
		return store.Task{}, err
	}
	if o.Content != "" {
		return store.NewTask(e.Root, run, o.Slug, o.Content)
	}
	title := firstNonEmpty(o.Title, o.Slug)
	var sb strings.Builder
	if o.Role != "" {
		fmt.Fprintf(&sb, "---\nrole: %s\n---\n", o.Role)
	}
	fmt.Fprintf(&sb, "# %s\n\n## Goal\n%s\n\n", title, firstNonEmpty(o.Goal, "What to achieve and what it is for."))
	if o.Template {
		sb.WriteString("## Context\nPaths that matter, what has already been decided or ruled out, and earlier result files worth reading first.\n\n")
		sb.WriteString("## Scope\nList the files this task may change, one bullet each, with the path in backticks (for example: - `src/auth/`). Delete this section to skip the scope check.\n\n")
	} else if len(o.Scope) > 0 {
		sb.WriteString("## Scope\n")
		for _, p := range o.Scope {
			fmt.Fprintf(&sb, "- `%s`\n", p)
		}
		sb.WriteString("\n")
	}
	subtasks := o.Subtasks
	if len(subtasks) == 0 {
		subtasks = []string{"First step", "Second step", "Write the report"}
	}
	sb.WriteString("## Subtasks\n")
	for _, s := range subtasks {
		fmt.Fprintf(&sb, "- [ ] %s\n", s)
	}
	sb.WriteString("\n## Done when\n")
	if len(o.Checks) == 0 {
		sb.WriteString("State the observable outcome as commands that prove it. Each fenced block is one check and exit code 0 means it passed. For example:\n")
	}
	for _, c := range o.Checks {
		fmt.Fprintf(&sb, "\n```bash\n%s\n```\n", c)
	}
	sb.WriteString("\n## Report\nAnything specific to bring back beyond the standard report.\n")
	return store.NewTask(e.Root, run, o.Slug, sb.String())
}

// QuickTask creates a brief from one sentence, for `sarx run`.
func (e *Engine) QuickTask(text, role string) (store.Task, string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return store.Task{}, "", errors.New("say what to do, for example: sarx run \"add a retry to the login request\"")
	}
	if role == "" {
		role = GuessRole(text)
	}
	var steps []string
	switch role {
	case spec.Reviewer:
		steps = []string{"Read the relevant code and changes", "Assess them against the request", "Write the report"}
	case spec.Operator:
		steps = []string{"Run what was asked", "Capture exact commands, exit codes and output", "Write the report"}
	default:
		steps = []string{"Understand the request and look at the relevant code", "Do the work", "Check the result", "Write the report"}
	}
	title := text
	if len(title) > 70 {
		title = strings.TrimSpace(title[:70]) + "..."
	}
	t, err := e.Scaffold(ScaffoldOptions{
		Slug: util.Slug(text, 30), Title: title, Role: role, Goal: text, Subtasks: steps, Checks: e.Cfg.Verify,
	})
	return t, role, err
}
