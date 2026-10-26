package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"sarx/internal/brief"
	"sarx/internal/lock"
	"sarx/internal/proc"
	"sarx/internal/spec"
	"sarx/internal/store"
	"sarx/internal/util"
)

// ErrAlreadyDispatched is returned when a task directory has been dispatched before.
var ErrAlreadyDispatched = errors.New("this task was already dispatched; use --resume to continue it, or `sarx retry` for a fresh attempt")

// Dispatch validates and accepts a task, then supervises it in this process (foreground) or hands
// it to a detached supervisor (detach) and returns as soon as it has been accepted.
func (e *Engine) Dispatch(t store.Task, o spec.Options, detach bool) (*spec.State, error) {
	if os.Getenv("SARX_WORKER") != "" {
		return nil, errors.New("refusing to dispatch from inside a sarx worker")
	}
	text, err := os.ReadFile(t.Brief())
	if err != nil {
		return nil, fmt.Errorf("cannot read the brief: %v", err)
	}
	b := brief.Parse(string(text))

	agent, role, err := e.Route(b, &o)
	if err != nil {
		return nil, err
	}
	if err := agent.Validate(); err != nil {
		return nil, err
	}
	if strings.EqualFold(b.Front["readonly"], "true") {
		o.ReadOnly = true
	}
	if _, err := exec.LookPath(agent.Exec); err != nil {
		return nil, fmt.Errorf("agent %q needs %q, which is not on PATH. Install it, or run `sarx doctor` for help", agent.Name, agent.Exec)
	}
	if !agent.AllowsImages() && !o.AllowImages {
		if imgs := brief.Images(string(text)); len(imgs) > 0 {
			return nil, fmt.Errorf("refusing to send images to %s (images = false): %s\n  Send the images to an agent that can read them (--agent), or pass --allow-images if the brief only names the paths",
				agent.Name, strings.Join(imgs, " "))
		}
	}

	// Settings: command line, then the brief's front matter, then the agent, then defaults.
	o.Agent = agent.Name
	o.Role = role
	if o.Timeout == 0 {
		o.Timeout = atoi(b.Front["timeout"])
	}
	if o.Timeout == 0 {
		o.Timeout = agent.Timeout
	}
	if o.Timeout == 0 {
		o.Timeout = e.Cfg.Defaults.Timeout
	}
	if o.Lock == "" {
		o.Lock = b.Front["lock"]
	}
	if o.Lock != "" && !lock.ValidName(o.Lock) {
		return nil, fmt.Errorf("lock name %q may only contain letters, digits, dot, dash and underscore", o.Lock)
	}
	if o.Isolation == "" {
		o.Isolation = b.Front["isolate"]
	}
	if o.Isolation == "" {
		o.Isolation = e.Cfg.Defaults.Isolation
	}
	if o.Isolation != "none" && o.Isolation != "worktree" {
		return nil, fmt.Errorf("isolation must be none or worktree, not %q", o.Isolation)
	}
	if o.Isolation == "worktree" && store.GitRoot(e.Root) == "" {
		return nil, errors.New("worktree isolation needs a git repository")
	}

	// Claim the task, archiving an earlier attempt when resuming.
	prev, _ := store.ReadState(t)
	attempt := 1
	session := o.SessionID
	resumedSession := false
	if o.Resuming {
		if prev == nil {
			return nil, errors.New("nothing to resume: this task has not been dispatched")
		}
		if spec.IsLive(store.Effective(prev)) && store.Effective(prev) != spec.Stalled {
			return nil, fmt.Errorf("%s is %s; stop it first with: sarx stop %s", t.ID, prev.State, t.ID)
		}
		if err := archiveAttempt(t, prev); err != nil {
			return nil, err
		}
		attempt = prev.Attempt + 1
		if agent.CanResume() {
			session = firstNonEmpty(o.Resume, prev.Session)
			resumedSession = session != ""
		}
		store.AppendEvent(t, "resume", fmt.Sprintf("attempt %d, session %s", attempt, session))
	} else if prev != nil {
		return nil, ErrAlreadyDispatched
	}
	if session == "" {
		session = util.UUID()
	}
	o.SessionID = session
	o.Resuming = resumedSession
	o.Resume = ""
	if resumedSession {
		o.Resume = session
	}

	st := &spec.State{
		Version:       1,
		ID:            t.ID,
		Run:           t.Run,
		Title:         b.Title,
		State:         spec.Queued,
		Agent:         agent.Name,
		Role:          role,
		Adapter:       agent.Adapter,
		Mode:          mode(o.ReadOnly),
		Started:       util.Now(),
		SupervisorPID: os.Getpid(),
		Session:       session,
		Attempt:       attempt,
		Subtasks:      len(b.Subtasks),
		TaskDir:       t.Dir,
		Task:          t.Brief(),
		Result:        t.ResultMD(),
		Progress:      t.Progress(),
		Journal:       t.Journal(),
		Stdout:        t.Stdout(),
		Stderr:        t.Stderr(),
		Lock:          o.Lock,
		Isolation:     o.Isolation,
		RetryOf:       o.RetryOf,
	}
	if resumedSession {
		st.Resumed = session
	}
	if err := store.CreateState(t, st); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, ErrAlreadyDispatched
		}
		return nil, err
	}

	// Seed the run files. A resumed attempt keeps the checklist and journal exactly as they were.
	if prev == nil || !util.Exists(t.Progress()) {
		if err := util.WriteFileAtomic(t.Progress(), []byte(progressSeed(t.ID, agent.Name, b.Subtasks)), 0o644); err != nil {
			return nil, err
		}
	} else {
		resetProgressState(t)
	}
	if !util.Exists(t.Journal()) {
		head := fmt.Sprintf("# %s journal\nagent=%s session=%s\n\n", t.ID, agent.Name, session)
		if err := util.WriteFileAtomic(t.Journal(), []byte(head), 0o644); err != nil {
			return nil, err
		}
	}
	if err := util.WriteFileAtomic(t.Worker(), []byte(workerDoc(t, o.ReadOnly, prev != nil, b.Scope, len(b.Subtasks))), 0o644); err != nil {
		return nil, err
	}
	if err := store.WriteOptions(t, o); err != nil {
		return nil, err
	}
	store.AppendEvent(t, "dispatched", fmt.Sprintf("agent=%s role=%s mode=%s attempt=%d", agent.Name, role, st.Mode, attempt))

	if !detach {
		return e.Supervise(t)
	}
	return e.spawnSupervisor(t)
}

func mode(ro bool) string {
	if ro {
		return "ro"
	}
	return "rw"
}

func atoi(s string) int {
	n := 0
	fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// archiveAttempt moves the run-specific files of a finished attempt into attempts/<N>/.
func archiveAttempt(t store.Task, prev *spec.State) error {
	dir := t.Attempt(prev.Attempt)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range []string{t.StateJSON(), t.ResultMD(), t.ResultJSON(), t.Stdout(), t.Stderr()} {
		if !util.Exists(f) {
			continue
		}
		if err := os.Rename(f, filepath.Join(dir, filepath.Base(f))); err != nil {
			return err
		}
	}
	os.Remove(t.StopRequest())
	return nil
}

// resetProgressState sets the checklist header back to RUNNING, leaving the ticks alone.
func resetProgressState(t store.Task) {
	data, err := os.ReadFile(t.Progress())
	if err != nil {
		return
	}
	s := blockedRe.ReplaceAllStringFunc(string(data), func(m string) string {
		return strings.Replace(m, "state=BLOCKED", "state=RUNNING", 1)
	})
	_ = util.WriteFileAtomic(t.Progress(), []byte(s), 0o644)
}

// spawnSupervisor starts a detached `sarx _supervise` and waits until it has taken over the task.
func (e *Engine) spawnSupervisor(t store.Task) (*spec.State, error) {
	cmd := exec.Command(e.Self, "_supervise", "--root", e.Root, t.Dir)
	cmd.SysProcAttr = proc.DetachAttrs()
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		st, _ := store.ReadState(t)
		if st != nil {
			st.State = spec.Crashed
			st.Note = "could not start the supervisor: " + err.Error()
			st.Finished = util.Now()
			_ = store.WriteState(t, st)
		}
		return st, fmt.Errorf("could not start the supervisor: %v", err)
	}
	_ = cmd.Process.Release()
	me := os.Getpid()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st, err := store.ReadState(t)
		if err == nil && (st.SupervisorPID != me || !spec.IsLive(st.State)) {
			return st, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	st, _ := store.ReadState(t)
	return st, errors.New("the supervisor did not start within 15 seconds")
}
