package engine

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"sarx/internal/config"
	"sarx/internal/spec"
	"sarx/internal/store"
)

var fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sarx-test-bin-")
	if err != nil {
		panic(err)
	}
	name := "sarx-fakeagent"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	fakeBin = filepath.Join(dir, name)
	if out, err := exec.Command("go", "build", "-o", fakeBin, "sarx/cmd/sarx-fakeagent").CombinedOutput(); err != nil {
		panic("cannot build the fake agent: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type testEnv struct {
	t    *testing.T
	root string
	eng  *Engine
}

func fakeAgent(name string, env map[string]string) config.Agent {
	return config.Agent{
		Name: name, Adapter: "generic", Exec: fakeBin, PromptVia: "arg", PromptFlag: "-p",
		SessionNew: []string{"--session", "{id}"}, SessionResume: []string{"--resume", "{id}"},
		QuotaRegex: config.DefaultQuotaRegex, Env: env,
	}
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sarx"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Roles:    map[string]string{},
		Agents:   map[string]config.Agent{"fake": fakeAgent("fake", nil)},
		Defaults: config.Defaults{Timeout: 60, Isolation: "none", AutoVerify: true, StallAfter: 600},
	}
	for _, r := range spec.Roles {
		cfg.Roles[r] = "fake"
	}
	e := New(root, cfg)
	e.Log = io.Discard
	return &testEnv{t: t, root: root, eng: e}
}

func (te *testEnv) git() {
	te.t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		te.t.Skip("git is not installed")
	}
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", te.root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			te.t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(te.root, "README.txt"), []byte("hi\n"), 0o644); err != nil {
		te.t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	// the .sarx directory must stay out of the tree, as `sarx init` arranges
	if err := os.WriteFile(filepath.Join(te.root, ".git", "info", "exclude"), []byte(".sarx/\n"), 0o644); err != nil {
		te.t.Fatal(err)
	}
}

// task writes a brief with the given extra sections and returns its task.
func (te *testEnv) task(slug string, extra string, subtasks ...string) store.Task {
	te.t.Helper()
	var b strings.Builder
	b.WriteString("# Demo " + slug + "\n\n## Goal\nProve tracking.\n\n")
	b.WriteString(extra)
	b.WriteString("## Subtasks\n")
	for _, s := range subtasks {
		b.WriteString("- [ ] " + s + "\n")
	}
	if !strings.Contains(extra, "## Done when") {
		b.WriteString("\n## Done when\n```bash\ntrue\n```\n")
	}
	tk, err := store.NewTask(te.root, "t", slug, b.String())
	if err != nil {
		te.t.Fatal(err)
	}
	return tk
}

func (te *testEnv) run(tk store.Task, o spec.Options) *spec.State {
	te.t.Helper()
	st, err := te.eng.Dispatch(tk, o, false)
	if err != nil {
		te.t.Fatalf("dispatch: %v", err)
	}
	return st
}

func TestDispatchDone(t *testing.T) {
	te := newEnv(t)
	tk := te.task("basic", "", "first", "second", "third")
	st := te.run(tk, spec.Options{})
	if st.State != spec.Done {
		t.Fatalf("state = %s (%s), want DONE", st.State, st.Note)
	}
	done, total := Ticks(tk)
	if done != 3 || total != 3 {
		t.Errorf("ticks = %d/%d, want 3/3", done, total)
	}
	if data, _ := os.ReadFile(tk.Journal()); !strings.Contains(string(data), "progress proven") {
		t.Errorf("journal was not written")
	}
	if st.Verified == nil || st.Verified.Pass != 1 || st.Verified.Fail != 0 {
		t.Errorf("verified = %+v, want 1 pass", st.Verified)
	}
	if st.Claimed.Pass != 1 {
		t.Errorf("claimed = %+v, want 1 pass", st.Claimed)
	}
	sum := Summary(tk, st)
	for _, want := range []string{"DONE", "3/3 subtasks", "verified: 1 PASS 0 FAIL", "result:"} {
		if !strings.Contains(sum, want) {
			t.Errorf("summary %q lacks %q", sum, want)
		}
	}
	// state.json must carry every key the schema requires
	data, _ := os.ReadFile(tk.StateJSON())
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile(filepath.Join("..", "..", "spec", "state.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sch struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &sch); err != nil {
		t.Fatal(err)
	}
	for _, k := range sch.Required {
		if _, ok := got[k]; !ok {
			t.Errorf("state.json lacks required key %q", k)
		}
	}
	// every event line must be valid JSON with ts and type
	ev, _ := os.ReadFile(tk.Events())
	for _, line := range strings.Split(strings.TrimSpace(string(ev)), "\n") {
		var e spec.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil || e.TS == 0 || e.Type == "" {
			t.Errorf("bad event line %q", line)
		}
	}
}

func TestRedispatchRefused(t *testing.T) {
	te := newEnv(t)
	tk := te.task("twice", "", "one")
	te.run(tk, spec.Options{})
	if _, err := te.eng.Dispatch(tk, spec.Options{}, false); err == nil || !strings.Contains(err.Error(), "already dispatched") {
		t.Fatalf("second dispatch error = %v, want 'already dispatched'", err)
	}
}

func TestWorkerCannotDispatch(t *testing.T) {
	te := newEnv(t)
	tk := te.task("guard", "", "one")
	t.Setenv("SARX_WORKER", "1")
	if _, err := te.eng.Dispatch(tk, spec.Options{}, false); err == nil || !strings.Contains(err.Error(), "inside a sarx worker") {
		t.Fatalf("error = %v", err)
	}
}

func TestResumeKeepsSessionAndJournal(t *testing.T) {
	te := newEnv(t)
	log := filepath.Join(t.TempDir(), "calls.txt")
	t.Setenv("FAKE_LOG", log)
	tk := te.task("resume", "", "alpha", "beta")
	t.Setenv("FAKE_STATUS", "BLOCKED")
	st := te.run(tk, spec.Options{})
	if st.State != spec.Blocked || st.Attempt != 1 {
		t.Fatalf("first attempt: %s attempt %d", st.State, st.Attempt)
	}
	sess := st.Session
	t.Setenv("FAKE_STATUS", "DONE")
	st2 := te.run(tk, spec.Options{Resuming: true})
	if st2.State != spec.Done || st2.Attempt != 2 {
		t.Fatalf("resumed attempt: %s attempt %d (%s)", st2.State, st2.Attempt, st2.Note)
	}
	if st2.Session != sess || st2.Resumed != sess {
		t.Errorf("session %q resumed %q, want %q", st2.Session, st2.Resumed, sess)
	}
	if !fileExists(filepath.Join(tk.Attempt(1), "state.json")) || !fileExists(filepath.Join(tk.Attempt(1), "result.md")) {
		t.Errorf("attempt 1 was not archived")
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "--resume "+sess) || !strings.Contains(string(calls), "--session "+sess) {
		t.Errorf("calls should start then resume session %s:\n%s", sess, calls)
	}
	if data, _ := os.ReadFile(tk.Journal()); strings.Count(string(data), "progress proven") < 2 {
		t.Errorf("journal did not survive the resume:\n%s", data)
	}
}

func TestStdoutReportBecomesResult(t *testing.T) {
	te := newEnv(t)
	t.Setenv("FAKE_STDOUT_REPORT", "1")
	tk := te.task("stdout", "", "one")
	st := te.run(tk, spec.Options{})
	if st.State != spec.Done {
		t.Fatalf("state = %s", st.State)
	}
	var res spec.Result
	data, _ := os.ReadFile(tk.ResultJSON())
	json.Unmarshal(data, &res)
	if res.Source != "stdout" {
		t.Errorf("source = %q, want stdout", res.Source)
	}
}

func TestNoReportAndUnmarked(t *testing.T) {
	te := newEnv(t)
	t.Setenv("FAKE_NO_REPORT", "1")
	if st := te.run(te.task("noreport", "", "one"), spec.Options{}); st.State != spec.NoReport {
		t.Errorf("state = %s, want NO_REPORT", st.State)
	}
	os.Unsetenv("FAKE_NO_REPORT")
	bad := filepath.Join(t.TempDir(), "r.md")
	os.WriteFile(bad, []byte("I did the thing.\n"), 0o644)
	t.Setenv("FAKE_RESULT_FROM", bad)
	if st := te.run(te.task("unmarked", "", "one"), spec.Options{}); st.State != spec.Unmarked {
		t.Errorf("state = %s, want UNMARKED", st.State)
	}
}

func TestDoneWithNotesNeedsResolution(t *testing.T) {
	te := newEnv(t)
	dir := t.TempDir()
	no := filepath.Join(dir, "no.md")
	yes := filepath.Join(dir, "yes.md")
	os.WriteFile(no, []byte("STATUS: DONE_WITH_NOTES\n\n## Notes\nnothing\n"), 0o644)
	os.WriteFile(yes, []byte("STATUS: DONE_WITH_NOTES\n\n## Resolution\npick a name\n\n## Checks\nPASS: true\n"), 0o644)
	t.Setenv("FAKE_RESULT_FROM", no)
	if st := te.run(te.task("notes-no", "", "one"), spec.Options{}); st.State != spec.Unmarked {
		t.Errorf("without Resolution: %s, want UNMARKED", st.State)
	}
	t.Setenv("FAKE_RESULT_FROM", yes)
	st := te.run(te.task("notes-yes", "", "one"), spec.Options{})
	if st.State != spec.DoneNotes {
		t.Errorf("with Resolution: %s, want DONE_WITH_NOTES", st.State)
	}
}

func TestQuotaIsNotCrash(t *testing.T) {
	te := newEnv(t)
	t.Setenv("FAKE_FAIL", "1")
	t.Setenv("FAKE_OUT_TEXT", "429 The usage limit has been reached")
	tk := te.task("quota", "", "one")
	st := te.run(tk, spec.Options{})
	if st.State != spec.Quota {
		t.Fatalf("state = %s, want QUOTA", st.State)
	}
	if !strings.Contains(Summary(tk, st), "usage limit") {
		t.Errorf("hint should mention the limit: %s", Summary(tk, st))
	}
	// a retry after a quota refusal keeps the same agent
	os.Unsetenv("FAKE_FAIL")
	os.Unsetenv("FAKE_OUT_TEXT")
	ns, nt, err := te.eng.Retry(tk, "", "", "limit reset", spec.Options{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if ns.Agent != "fake" || ns.State != spec.Done || !strings.HasSuffix(nt.ID, "-r2") {
		t.Errorf("retry: agent %s state %s id %s", ns.Agent, ns.State, nt.ID)
	}
	brief, _ := os.ReadFile(nt.Brief())
	for _, want := range []string{"Retry context", "limit reset", "## Subtasks"} {
		if !strings.Contains(string(brief), want) {
			t.Errorf("retry brief lacks %q", want)
		}
	}
}

func TestCrashRecordsStderr(t *testing.T) {
	te := newEnv(t)
	t.Setenv("FAKE_FAIL", "1")
	t.Setenv("FAKE_ERR_TEXT", "boom: auth failed")
	st := te.run(te.task("crash", "", "one"), spec.Options{})
	if st.State != spec.Crashed || !strings.Contains(st.Note, "boom: auth failed") {
		t.Errorf("state %s note %q", st.State, st.Note)
	}
}

func TestTimeoutKillsWorker(t *testing.T) {
	te := newEnv(t)
	t.Setenv("FAKE_SLEEP", "30")
	start := time.Now()
	st := te.run(te.task("slow", "", "one"), spec.Options{Timeout: 2})
	if st.State != spec.Timeout {
		t.Fatalf("state = %s, want TIMEOUT", st.State)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("took %s; the worker was not killed promptly", d)
	}
}

func TestStopCancelsRunningTask(t *testing.T) {
	te := newEnv(t)
	t.Setenv("FAKE_SLEEP", "60")
	tk := te.task("stopme", "", "one")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		te.eng.Dispatch(tk, spec.Options{}, false)
	}()
	waitFor(t, 10*time.Second, func() bool {
		st, err := store.ReadState(tk)
		return err == nil && st.State == spec.Running
	})
	st, err := te.eng.Stop(tk)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != spec.Cancelled {
		t.Errorf("state = %s, want CANCELLED", st.State)
	}
	wg.Wait()
	if _, err := te.eng.Stop(tk); err == nil {
		t.Errorf("stopping a finished task should be an error")
	}
}

func TestImagesRefusedForTextOnlyAgent(t *testing.T) {
	te := newEnv(t)
	no := false
	a := te.eng.Cfg.Agents["fake"]
	a.Images = &no
	te.eng.Cfg.Agents["fake"] = a
	tk := te.task("images", "## Context\nLook at shots/login.png and judge it.\n\n", "one")
	_, err := te.eng.Dispatch(tk, spec.Options{}, false)
	if err == nil || !strings.Contains(err.Error(), "shots/login.png") {
		t.Fatalf("error = %v", err)
	}
	if fileExists(tk.StateJSON()) {
		t.Errorf("a refused dispatch must not create run files")
	}
	if st := te.run(tk, spec.Options{AllowImages: true}); st.State != spec.Done {
		t.Errorf("--allow-images should override: %s", st.State)
	}
}

func TestLockSerialisesTasks(t *testing.T) {
	te := newEnv(t)
	te.eng.Cfg.Agents["slow"] = fakeAgent("slow", map[string]string{"FAKE_SLEEP": "3"})
	a := te.task("lock-a", "", "one")
	b := te.task("lock-b", "", "one")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); te.eng.Dispatch(a, spec.Options{Agent: "slow", Lock: "emulator"}, false) }()
	waitFor(t, 10*time.Second, func() bool {
		st, err := store.ReadState(a)
		return err == nil && st.State == spec.Running
	})
	go func() { defer wg.Done(); te.eng.Dispatch(b, spec.Options{Agent: "fake", Lock: "emulator"}, false) }()
	waitFor(t, 5*time.Second, func() bool { return fileExists(b.StateJSON()) })
	time.Sleep(700 * time.Millisecond)
	if sb, _ := store.ReadState(b); sb.State != spec.Queued {
		t.Errorf("second task should be QUEUED while the lock is held, got %s", sb.State)
	}
	wg.Wait()
	sa, _ := store.ReadState(a)
	sb, _ := store.ReadState(b)
	if sa.State != spec.Done || sb.State != spec.Done {
		t.Fatalf("states %s / %s", sa.State, sb.State)
	}
	if sb.Started < sa.Finished {
		t.Errorf("b started at %d before a finished at %d", sb.Started, sa.Finished)
	}
	if fileExists(filepath.Join(store.LocksDir(te.root), "emulator.lock")) {
		t.Errorf("lock was not released")
	}
}

func TestMaxParallelQueues(t *testing.T) {
	te := newEnv(t)
	a := fakeAgent("limited", map[string]string{"FAKE_SLEEP": "3"})
	a.MaxParallel = 1
	te.eng.Cfg.Agents["limited"] = a
	t1 := te.task("par-a", "", "one")
	t2 := te.task("par-b", "", "one")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); te.eng.Dispatch(t1, spec.Options{Agent: "limited"}, false) }()
	waitFor(t, 10*time.Second, func() bool {
		st, err := store.ReadState(t1)
		return err == nil && st.State == spec.Running
	})
	go func() { defer wg.Done(); te.eng.Dispatch(t2, spec.Options{Agent: "limited"}, false) }()
	waitFor(t, 5*time.Second, func() bool { return fileExists(t2.StateJSON()) })
	time.Sleep(700 * time.Millisecond)
	if s2, _ := store.ReadState(t2); s2.State != spec.Queued {
		t.Errorf("second task should wait for a slot, got %s", s2.State)
	}
	wg.Wait()
}

func TestVerifyReportsPassAndFail(t *testing.T) {
	te := newEnv(t)
	fail := "exit 1"
	if runtime.GOOS == "windows" {
		fail = "exit /b 1"
	}
	extra := "## Done when\n```bash\ntrue\n```\n```bash\n" + fail + "\n```\n\n"
	tk := te.task("verify", extra, "one")
	st := te.run(tk, spec.Options{})
	if st.Verified == nil || st.Verified.Pass != 1 || st.Verified.Fail != 1 {
		t.Fatalf("verified = %+v", st.Verified)
	}
	if len(st.Warnings) == 0 || !strings.Contains(strings.Join(st.Warnings, " "), "Done-when checks failed") {
		t.Errorf("a failing check should raise a warning: %v", st.Warnings)
	}
	v, err := te.eng.Verify(tk)
	if err != nil || v.Pass != 1 || v.Fail != 1 {
		t.Errorf("Verify = %+v, %v", v, err)
	}
	empty := te.task("nochecks", "## Done when\nNothing fenced here.\n\n", "one")
	if _, err := te.eng.Verify(empty); err == nil {
		t.Errorf("a brief with no fenced checks should be an error")
	}
}

func TestScopeAndReadOnlyWarnings(t *testing.T) {
	te := newEnv(t)
	te.git()
	scope := "## Scope\n- `src/`\n\n"

	t.Setenv("FAKE_WRITE", "src/ok.txt")
	if st := te.run(te.task("scope-ok", scope, "one"), spec.Options{}); len(st.Warnings) != 0 {
		t.Errorf("a file inside Scope should not warn: %v", st.Warnings)
	}
	t.Setenv("FAKE_WRITE", "elsewhere.txt")
	st := te.run(te.task("scope-bad", scope, "one"), spec.Options{})
	if len(st.Warnings) == 0 || !strings.Contains(st.Warnings[0], "elsewhere.txt") {
		t.Errorf("a file outside Scope should warn: %v", st.Warnings)
	}
	t.Setenv("FAKE_WRITE", "ro-edit.txt")
	st = te.run(te.task("ro-bad", "", "one"), spec.Options{ReadOnly: true})
	if len(st.Warnings) == 0 || !strings.Contains(st.Warnings[0], "read-only") {
		t.Errorf("a read-only run that writes should warn: %v", st.Warnings)
	}
	os.Unsetenv("FAKE_WRITE")
	st = te.run(te.task("ro-ok", "", "one"), spec.Options{ReadOnly: true})
	if len(st.Warnings) != 0 {
		t.Errorf("a clean read-only run should not warn: %v", st.Warnings)
	}
}

func TestNoGitSkipsScopeChecks(t *testing.T) {
	te := newEnv(t)
	st := te.run(te.task("nogit", "## Scope\n- `src/`\n\n", "one"), spec.Options{})
	if len(st.Warnings) != 1 || !strings.Contains(st.Warnings[0], "not a git repository") {
		t.Errorf("warnings = %v", st.Warnings)
	}
}

func TestRetryEscalatesByRole(t *testing.T) {
	te := newEnv(t)
	te.eng.Cfg.Agents["op"] = fakeAgent("op", nil)
	te.eng.Cfg.Agents["build"] = fakeAgent("build", nil)
	te.eng.Cfg.Roles[spec.Operator] = "op"
	te.eng.Cfg.Roles[spec.Builder] = "build"
	t.Setenv("FAKE_STATUS", "FAILED")
	tk := te.task("escalate", "---\nrole: operator\n---\n", "one")
	// front matter must come first, so write the brief by hand
	os.WriteFile(tk.Brief(), []byte("---\nrole: operator\n---\n# Escalate\n\n## Subtasks\n- [ ] one\n"), 0o644)
	st := te.run(tk, spec.Options{})
	if st.Agent != "op" || st.State != spec.Failed {
		t.Fatalf("first run: agent %s state %s", st.Agent, st.State)
	}
	os.Unsetenv("FAKE_STATUS")
	ns, _, err := te.eng.Retry(tk, "", "", "", spec.Options{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if ns.Agent != "build" || ns.Role != spec.Builder {
		t.Errorf("a failed operator task should escalate to the builder, got %s/%s", ns.Agent, ns.Role)
	}
	done := te.task("done", "", "one")
	te.run(done, spec.Options{})
	if _, _, err := te.eng.Retry(done, "", "", "", spec.Options{}, false); err == nil || !strings.Contains(err.Error(), "DONE") {
		t.Errorf("retrying a DONE task should be refused: %v", err)
	}
}

func TestRetryRoleTable(t *testing.T) {
	cases := []struct{ role, state, want string }{
		{spec.Operator, spec.Crashed, spec.Builder},
		{spec.Builder, spec.Crashed, spec.Fallback},
		{spec.Fallback, spec.Crashed, spec.Reviewer},
		{spec.Reviewer, spec.Crashed, ""},
		{spec.Operator, spec.Failed, spec.Builder},
		{spec.Builder, spec.Blocked, spec.Reviewer},
		{spec.Reviewer, spec.Failed, ""},
		{spec.Builder, spec.Quota, spec.Builder},
		{spec.Reviewer, spec.Timeout, spec.Reviewer},
	}
	for _, c := range cases {
		if got := retryRole(c.role, c.state); got != c.want {
			t.Errorf("retryRole(%s, %s) = %q, want %q", c.role, c.state, got, c.want)
		}
	}
}

func TestGuessRole(t *testing.T) {
	cases := map[string]string{
		"Review the auth change":      spec.Reviewer,
		"Run the test suite":          spec.Operator,
		"Rename the config key":       spec.Operator,
		"Add token refresh to login":  spec.Builder,
		"Implement retry and run it":  spec.Builder,
		"Security audit of the proxy": spec.Reviewer,
	}
	for title, want := range cases {
		if got := GuessRole(title); got != want {
			t.Errorf("GuessRole(%q) = %s, want %s", title, got, want)
		}
	}
}

func TestWorktreeIsolationAndApply(t *testing.T) {
	te := newEnv(t)
	te.git()
	t.Setenv("FAKE_WRITE", "feature.txt")
	tk := te.task("wt", "", "one")
	st := te.run(tk, spec.Options{Isolation: "worktree"})
	if st.State != spec.Done || st.Worktree == "" || st.Branch == "" {
		t.Fatalf("state %s worktree %q branch %q (%s)", st.State, st.Worktree, st.Branch, st.Note)
	}
	if fileExists(filepath.Join(te.root, "feature.txt")) {
		t.Fatalf("the worker's change must not touch the main tree before apply")
	}
	if !fileExists(filepath.Join(st.Worktree, "feature.txt")) {
		t.Fatalf("the worker's change should be in its worktree")
	}
	if _, err := te.eng.Apply(tk); err != nil {
		t.Fatal(err)
	}
	if !fileExists(filepath.Join(te.root, "feature.txt")) {
		t.Errorf("apply should merge the change into the project")
	}
	if fileExists(st.Worktree) {
		t.Errorf("apply should remove the worktree")
	}
}

func TestWorktreeDiscard(t *testing.T) {
	te := newEnv(t)
	te.git()
	t.Setenv("FAKE_WRITE", "scratch.txt")
	tk := te.task("wt-discard", "", "one")
	st := te.run(tk, spec.Options{Isolation: "worktree"})
	if err := te.eng.Discard(tk); err != nil {
		t.Fatal(err)
	}
	if fileExists(st.Worktree) || fileExists(filepath.Join(te.root, "scratch.txt")) {
		t.Errorf("discard should leave nothing behind")
	}
}

func TestListShowsLiveThenAll(t *testing.T) {
	te := newEnv(t)
	te.run(te.task("listed", "", "one", "two"), spec.Options{})
	if rows := te.eng.List(false); len(rows) != 0 {
		t.Errorf("finished tasks should be hidden: %v", rows)
	}
	rows := te.eng.List(true)
	if len(rows) != 1 || rows[0].State != spec.Done || rows[0].Done != 2 || rows[0].Total != 2 {
		t.Errorf("rows = %+v", rows)
	}
}

func TestStalledWhenSupervisorIsGone(t *testing.T) {
	te := newEnv(t)
	tk := te.task("stalled", "", "one")
	st := &spec.State{Version: 1, ID: tk.ID, State: spec.Running, Agent: "fake", Role: "builder", Mode: "rw",
		Attempt: 1, Started: time.Now().Unix(), SupervisorPID: 999999, TaskDir: tk.Dir, Task: tk.Brief(),
		Result: tk.ResultMD(), Progress: tk.Progress(), Journal: tk.Journal()}
	if err := store.CreateState(tk, st); err != nil {
		t.Fatal(err)
	}
	rows := te.eng.List(false)
	if len(rows) != 1 || rows[0].State != spec.Stalled {
		t.Fatalf("rows = %+v, want one STALLED", rows)
	}
	got, err := te.eng.Stop(tk)
	if err != nil || got.State != spec.Cancelled {
		t.Errorf("stop of a stalled task: %v %v", got, err)
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}
