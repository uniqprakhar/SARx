package engine

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"sarx/internal/adapter"
	"sarx/internal/brief"
	"sarx/internal/config"
	"sarx/internal/lock"
	"sarx/internal/proc"
	"sarx/internal/spec"
	"sarx/internal/store"
	"sarx/internal/util"
)

var (
	statusRe     = regexp.MustCompile(`(?m)^STATUS: *(DONE_WITH_NOTES|DONE|BLOCKED|FAILED)\b`)
	resolutionRe = regexp.MustCompile(`(?mi)^##\s*Resolution`)
	passRe       = regexp.MustCompile(`(?m)^\s*(?:[*-]\s*)?PASS\b`)
	failRe       = regexp.MustCompile(`(?m)^\s*(?:[*-]\s*)?FAIL\b`)
)

// Supervise runs a task that Dispatch has accepted, to completion, in this process.
func (e *Engine) Supervise(t store.Task) (*spec.State, error) {
	st, err := store.ReadState(t)
	if err != nil {
		return nil, err
	}
	o, err := store.ReadOptions(t)
	if err != nil {
		return nil, err
	}
	agent, err := e.Cfg.Get(o.Agent)
	if err != nil {
		return e.fail(t, st, spec.Crashed, err.Error())
	}
	text, _ := os.ReadFile(t.Brief())
	b := brief.Parse(string(text))

	st.SupervisorPID = os.Getpid()
	_ = store.WriteState(t, st)

	// Stop signals: Ctrl-C, SIGTERM, or the stop.request file (which works on every platform).
	stop := make(chan struct{})
	var once sync.Once
	var why atomic.Value
	requestStop := func(reason string) { once.Do(func() { why.Store(reason); close(stop) }) }
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	quit := make(chan struct{})
	defer func() { signal.Stop(sigs); close(quit) }()
	go func() {
		tick := time.NewTicker(300 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-sigs:
				requestStop("interrupted")
			case <-tick.C:
				if util.Exists(t.StopRequest()) {
					requestStop("stop requested")
				}
			case <-quit:
				return
			}
		}
	}()

	// Resource lock first, then the agent's parallel slot: a fixed order cannot deadlock.
	var held []*lock.Lock
	defer func() {
		for _, l := range held {
			l.Release()
		}
	}()
	locks := store.LocksDir(e.Root)
	if o.Lock != "" {
		l, err := lock.Acquire(locks, o.Lock, func(holder int) {
			e.logf("sarx: waiting for lock '%s' held by %s (another task is using it)", o.Lock, pidText(holder))
			store.AppendEvent(t, "queued", "waiting for lock "+o.Lock)
		}, stop)
		if err != nil {
			return e.cancelled(t, st, "stopped while waiting for lock "+o.Lock)
		}
		held = append(held, l)
	}
	if agent.MaxParallel > 0 {
		var names []string
		for i := 1; i <= agent.MaxParallel; i++ {
			names = append(names, fmt.Sprintf("agent-%s-%d", util.Slug(agent.Name, 40), i))
		}
		l, err := lock.AcquireAny(locks, names, func(int) {
			e.logf("sarx: %s is at its limit of %d parallel tasks; waiting for a free slot", agent.Name, agent.MaxParallel)
			store.AppendEvent(t, "queued", "waiting for a free slot on "+agent.Name)
		}, stop)
		if err != nil {
			return e.cancelled(t, st, "stopped while waiting for a free slot")
		}
		held = append(held, l)
	}

	workdir := e.Root
	if o.Isolation == "worktree" {
		wt, branch, err := createWorktree(e.Root, t.ID)
		if err != nil {
			return e.fail(t, st, spec.Crashed, "could not create the worktree: "+err.Error())
		}
		workdir, st.Worktree, st.Branch = wt, wt, branch
	}
	before, snapOK := TakeSnapshot(workdir)

	outF, err := os.Create(t.Stdout())
	if err != nil {
		return e.fail(t, st, spec.Crashed, err.Error())
	}
	defer outF.Close()
	errF, err := os.Create(t.Stderr())
	if err != nil {
		return e.fail(t, st, spec.Crashed, err.Error())
	}
	defer errF.Close()

	built := adapter.Build(adapter.Request{
		Agent: agent, Prompt: workerPrompt(t), Session: o.SessionID, Resume: o.Resuming,
		ReadOnly: o.ReadOnly, Extra: o.Extra, TaskDir: t.Dir,
	})
	exe, err := exec.LookPath(built.Exec)
	if err != nil {
		return e.fail(t, st, spec.Crashed, fmt.Sprintf("%q is not on PATH", built.Exec))
	}
	cmd := exec.Command(exe, built.Args...)
	cmd.Dir = workdir
	cmd.Stdout, cmd.Stderr = outF, errF
	if built.Stdin != "" {
		cmd.Stdin = strings.NewReader(built.Stdin)
	}
	cmd.Env = append(os.Environ(), "SARX_WORKER=1", "SARX_TASK_DIR="+t.Dir)
	cmd.Env = append(cmd.Env, built.Env...)
	cmd.SysProcAttr = proc.WorkerAttrs()

	started := time.Now()
	if err := cmd.Start(); err != nil {
		return e.fail(t, st, spec.Crashed, fmt.Sprintf("cannot start %s: %v", exe, err))
	}
	st.PID = cmd.Process.Pid
	st.State = spec.Running
	st.Started = started.Unix()
	_ = store.WriteState(t, st)
	store.AppendEvent(t, "started", fmt.Sprintf("pid %d via %s", st.PID, exe))

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(time.Duration(o.Timeout) * time.Second)
	defer timer.Stop()

	var waitErr error
	timedOut, stopped := false, false
	select {
	case waitErr = <-done:
	case <-timer.C:
		timedOut = true
		proc.KillTree(st.PID, 10*time.Second)
		waitErr = <-done
	case <-stop:
		stopped = true
		proc.KillTree(st.PID, 3*time.Second)
		waitErr = <-done
	}
	code := 0
	if waitErr != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			code = ee.ExitCode()
		}
	}
	st.Seconds = int64(time.Since(started).Seconds())
	st.Exit = code
	st.Finished = util.Now()
	store.AppendEvent(t, "exited", fmt.Sprintf("exit %d after %ds", code, st.Seconds))

	// A worker that never wrote its report still said something on stdout.
	resultSource := "file"
	if fi, err := os.Stat(t.ResultMD()); err != nil || fi.Size() == 0 {
		if data, err := os.ReadFile(t.Stdout()); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			_ = util.WriteFileAtomic(t.ResultMD(), data, 0o644)
			resultSource = "stdout"
		} else {
			resultSource = "none"
		}
	}

	var res spec.Result
	switch {
	case stopped:
		st.State = spec.Cancelled
		st.Note, _ = why.Load().(string)
		st.Exit = 130
	case timedOut:
		st.State = spec.Timeout
		st.Note = fmt.Sprintf("hit the %ds time limit and was killed", o.Timeout)
	case code != 0:
		tail := readTail(t.Stdout(), 8192) + "\n" + readTail(t.Stderr(), 8192)
		if quotaRe(agent).MatchString(tail) {
			st.State = spec.Quota
			st.Note = "the provider refused with a usage or rate limit"
		} else {
			st.State = spec.Crashed
			st.Note = fmt.Sprintf("exit %d. %s", code, util.Tail(strings.TrimSpace(readTail(t.Stderr(), 4096)), 5))
		}
	default:
		res, st.State = classify(t, resultSource)
	}
	if res.Status == "" {
		res = spec.Result{Status: st.State, Source: resultSource}
		if res.Source == "none" || res.Source == "" {
			res.Source = "none"
		}
	}
	res.Claimed = countClaims(t)
	st.Claimed = res.Claimed
	if data, err := marshal(res); err == nil {
		_ = util.WriteFileAtomic(t.ResultJSON(), data, 0o644)
	}

	// Engine-side checks: what the worker claims is recorded, what the engine sees is what counts.
	if snapOK && st.State != spec.Cancelled {
		if after, ok := TakeSnapshot(workdir); ok {
			st.Warnings = append(st.Warnings, scopeWarnings(Changed(before, after), o.ReadOnly, b.ScopeMatcher())...)
		}
	} else if !snapOK && (o.ReadOnly || len(b.Scope) > 0) {
		st.Warnings = append(st.Warnings, "scope and read-only checks were skipped: the project is not a git repository")
	}
	if spec.IsSuccess(st.State) && !o.NoVerify && e.Cfg.Defaults.AutoVerify && len(b.Checks) > 0 {
		if v, err := RunChecks(workdir, b.Checks, e.Cfg.Defaults.Shell); err == nil {
			st.Verified = v
			store.AppendEvent(t, "verify", fmt.Sprintf("%d passed, %d failed", v.Pass, v.Fail))
			if v.Fail > 0 {
				st.Warnings = append(st.Warnings, fmt.Sprintf("%d of %d Done-when checks failed when sarx ran them (see: sarx verify %s)", v.Fail, v.Pass+v.Fail, t.ID))
			}
		}
	}
	for _, w := range st.Warnings {
		store.AppendEvent(t, "warning", w)
	}
	if st.Worktree != "" {
		commitWorktree(st.Worktree, st.Title)
	}
	store.AppendEvent(t, "state", st.State)
	if err := store.WriteState(t, st); err != nil {
		return st, err
	}
	return st, nil
}

// classify reads the worker's report and decides the task state.
func classify(t store.Task, source string) (spec.Result, string) {
	res := spec.Result{Source: source}
	data, err := os.ReadFile(t.ResultMD())
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		res.Status, res.Source = spec.NoReport, "none"
		return res, spec.NoReport
	}
	text := string(data)
	head := strings.Join(firstLines(text, 5), "\n")
	m := statusRe.FindStringSubmatch(head)
	if m == nil {
		res.Status = spec.Unmarked
		return res, spec.Unmarked
	}
	state := m[1]
	if state == spec.DoneNotes && !resolutionRe.MatchString(text) {
		res.Status = spec.Unmarked
		res.Notes = "claimed DONE_WITH_NOTES without a '## Resolution' section"
		return res, spec.Unmarked
	}
	res.Status = state
	res.Resolution = section(text, "Resolution")
	res.Notes = firstNonEmpty(res.Notes, section(text, "Notes"))
	for _, l := range strings.Split(section(text, "Files"), "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "-*"))
		if l != "" {
			res.Files = append(res.Files, l)
		}
	}
	return res, state
}

func countClaims(t store.Task) spec.Checks {
	data, err := os.ReadFile(t.ResultMD())
	if err != nil {
		return spec.Checks{}
	}
	return spec.Checks{Pass: len(passRe.FindAll(data, -1)), Fail: len(failRe.FindAll(data, -1))}
}

func firstLines(s string, n int) []string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return lines
}

// section returns the body of a "## name" section.
func section(text, name string) string {
	var out []string
	in := false
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(l, "#") {
			in = strings.EqualFold(strings.TrimSpace(strings.TrimLeft(l, "#")), name)
			continue
		}
		if in {
			out = append(out, l)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func quotaRe(a config.Agent) *regexp.Regexp {
	re, err := regexp.Compile(a.QuotaRegex)
	if err != nil {
		return regexp.MustCompile(config.DefaultQuotaRegex)
	}
	return re
}

func readTail(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	off := st.Size() - n
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	nn, _ := f.ReadAt(buf, off)
	return string(buf[:nn])
}

func pidText(pid int) string {
	if pid <= 0 {
		return "an unknown process"
	}
	return fmt.Sprintf("process %d", pid)
}

// fail records a terminal state for a task that never got as far as running a worker.
func (e *Engine) fail(t store.Task, st *spec.State, state, note string) (*spec.State, error) {
	st.State, st.Note = state, note
	st.Finished = util.Now()
	st.Exit = -1
	store.AppendEvent(t, "state", state+": "+note)
	_ = store.WriteState(t, st)
	return st, nil
}

func (e *Engine) cancelled(t store.Task, st *spec.State, note string) (*spec.State, error) {
	st, err := e.fail(t, st, spec.Cancelled, note)
	st.Exit = 130
	_ = store.WriteState(t, st)
	return st, err
}

// createWorktree makes (or reuses) .sarx/worktrees/<id> on branch sarx/<id>.
func createWorktree(root, id string) (path, branch string, err error) {
	path = filepath.Join(store.StateDir(root), "worktrees", id)
	branch = "sarx/" + id
	if util.Exists(path) {
		return path, branch, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", "", err
	}
	args := []string{"-C", root, "worktree", "add", "-b", branch, path, "HEAD"}
	if exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil {
		args = []string{"-C", root, "worktree", "add", path, branch}
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return path, branch, nil
}

// commitWorktree commits whatever the worker left in its worktree so nothing is lost.
func commitWorktree(wt, title string) {
	if title == "" {
		title = "task"
	}
	_ = exec.Command("git", "-C", wt, "add", "-A").Run()
	if exec.Command("git", "-C", wt, "diff", "--cached", "--quiet").Run() == nil {
		return // nothing to commit
	}
	_ = exec.Command("git", "-C", wt, "-c", "user.name=sarx", "-c", "user.email=sarx@localhost",
		"commit", "-q", "-m", "sarx: "+title).Run()
}
