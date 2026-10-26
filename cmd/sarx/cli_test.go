package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var sarxBin, fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sarx-cli-test-")
	if err != nil {
		panic(err)
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	sarxBin = filepath.Join(dir, "sarx"+ext)
	fakeBin = filepath.Join(dir, "fakeagent"+ext)
	for out, pkg := range map[string]string{sarxBin: "sarx/cmd/sarx", fakeBin: "sarx/cmd/sarx-fakeagent"} {
		if b, err := exec.Command("go", "build", "-o", out, pkg).CombinedOutput(); err != nil {
			panic("build " + pkg + ": " + err.Error() + "\n" + string(b))
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// project makes a git repository with a project config that maps every role to the fake agent.
func project(t *testing.T) (dir string, env []string) {
	t.Helper()
	dir = t.TempDir()
	cfg := filepath.Join(dir, "sarx.toml")
	toml := "[roles]\noperator=\"fake\"\nbuilder=\"fake\"\nreviewer=\"fake\"\nfallback=\"fake\"\n" +
		"[agent.fake]\nexec = '" + fakeBin + "'\nprompt_flag = \"-p\"\nsession_new = [\"--session\", \"{id}\"]\nsession_resume = [\"--resume\", \"{id}\"]\n"
	os.WriteFile(cfg, []byte(toml), 0o644)
	for _, c := range [][]string{{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"add", "."}, {"commit", "-qm", "i"}} {
		cmd := exec.Command("git", c...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, b)
		}
	}
	env = append(os.Environ(), "SARX_CONFIG="+filepath.Join(dir, "no-user.toml"), "NO_COLOR=1")
	return dir, env
}

type result struct {
	out, err string
	code     int
}

func sarx(t *testing.T, dir string, env []string, stdin string, args ...string) result {
	t.Helper()
	cmd := exec.Command(sarxBin, args...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdin = strings.NewReader(stdin)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("sarx %v hung\nstdout: %s\nstderr: %s", args, o.String(), e.String())
	}
	return result{o.String(), e.String(), cmd.ProcessState.ExitCode()}
}

func TestUsageAndErrors(t *testing.T) {
	dir, env := project(t)
	if r := sarx(t, dir, env, "", "version"); r.code != 0 || !strings.HasPrefix(r.out, "sarx ") {
		t.Fatalf("%+v", r)
	}
	if r := sarx(t, dir, env, "", "bogus"); r.code != 2 {
		t.Fatalf("unknown command should exit 2: %+v", r)
	}
	if r := sarx(t, dir, env, "", "ps"); r.code != 2 || !strings.Contains(r.err, "sarx init") {
		t.Fatalf("no project should say how to start: %+v", r)
	}
	if r := sarx(t, dir, env, "", "run"); r.code != 2 {
		t.Fatalf("run without text: %+v", r)
	}
	if r := sarx(t, dir, env, "", "run", "--nope", "x"); r.code != 2 || !strings.Contains(r.err, "unknown option") {
		t.Fatalf("%+v", r)
	}
}

func TestRunForegroundAndList(t *testing.T) {
	dir, env := project(t)
	r := sarx(t, dir, env, "", "run", "fix the typo")
	if r.code != 0 || !strings.Contains(r.out, "DONE") {
		t.Fatalf("%+v", r)
	}
	ps := sarx(t, dir, env, "", "ps", "--all", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(ps.out), &rows); err != nil || len(rows) != 1 || rows[0]["state"] != "DONE" {
		t.Fatalf("%v %q", err, ps.out)
	}
	if r := sarx(t, dir, env, "", "ps"); !strings.Contains(r.out, "nothing is running") {
		t.Fatalf("%+v", r)
	}
	id := rows[0]["id"].(string)
	if r := sarx(t, dir, env, "", "show", id); r.code != 0 || !strings.Contains(r.out, "STATUS") {
		t.Fatalf("show: %+v", r)
	}
	// A quick task has no "Done when" checks, so verify says so instead of reporting a hollow pass.
	if r := sarx(t, dir, env, "", "verify", id); r.code != 2 || !strings.Contains(r.err, "no fenced checks") {
		t.Fatalf("verify: %+v", r)
	}
}

func TestExitCodesFollowOutcome(t *testing.T) {
	dir, env := project(t)
	fail := append(env, "FAKE_STATUS=FAILED")
	if r := sarx(t, dir, fail, "", "run", "something hard"); r.code != 1 {
		t.Fatalf("failed task should exit 1: %+v", r)
	}
	crash := append(env, "FAKE_FAIL=1", "FAKE_ERR_TEXT=boom")
	if r := sarx(t, dir, crash, "", "run", "another thing"); r.code != 1 || !strings.Contains(r.out, "CRASHED") {
		t.Fatalf("crash: %+v", r)
	}
}

func TestDetachWaitStop(t *testing.T) {
	dir, env := project(t)
	slow := append(env, "FAKE_SLEEP=1")
	r := sarx(t, dir, slow, "", "run", "--detach", "slow job")
	if r.code != 0 || !strings.Contains(r.out, "started") {
		t.Fatalf("%+v", r)
	}
	ps := sarx(t, dir, env, "", "ps", "--json")
	var rows []map[string]any
	json.Unmarshal([]byte(ps.out), &rows)
	if len(rows) != 1 {
		t.Fatalf("expected the detached task to be listed: %q", ps.out)
	}
	id := rows[0]["id"].(string)
	if w := sarx(t, dir, env, "", "wait", id); w.code != 0 || !strings.Contains(w.out, "DONE") {
		t.Fatalf("wait: %+v", w)
	}

	long := append(env, "FAKE_SLEEP=30")
	sarx(t, dir, long, "", "run", "--detach", "long job")
	ps = sarx(t, dir, env, "", "ps", "--json")
	rows = nil
	json.Unmarshal([]byte(ps.out), &rows)
	if len(rows) != 1 {
		t.Fatalf("%q", ps.out)
	}
	id = rows[0]["id"].(string)
	if s := sarx(t, dir, env, "", "stop", id); s.code != 0 {
		t.Fatalf("stop: %+v", s)
	}
	st := sarx(t, dir, env, "", "show", id, "--json")
	var state map[string]any
	json.Unmarshal([]byte(st.out), &state)
	if state["state"] != "CANCELLED" {
		t.Fatalf("state after stop: %v", state["state"])
	}
}

func TestInitDoctorHookMode(t *testing.T) {
	dir, env := project(t)
	r := sarx(t, dir, env, "", "init", "--yes", "--for", "agents-md,claude", "--enforce")
	if r.code != 0 || !strings.Contains(r.out, "AGENTS.md") {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "settings.json")); err != nil {
		t.Fatal("--enforce should install the hook")
	}
	if d := sarx(t, dir, env, "", "doctor"); d.code != 0 || !strings.Contains(d.out, "All good") && !strings.Contains(d.out, "worth a look") {
		t.Fatalf("doctor: %+v", d)
	}
	if m := sarx(t, dir, env, "", "mode", "on"); m.code != 0 {
		t.Fatalf("%+v", m)
	}
	h := sarx(t, dir, env, `{"tool_name":"Agent"}`, "hook", "pretooluse")
	if h.code != 2 || !strings.Contains(h.err, "sarx mode off") {
		t.Fatalf("hook: %+v", h)
	}
	sarx(t, dir, env, "", "mode", "off")
	if h := sarx(t, dir, env, `{"tool_name":"Agent"}`, "hook", "pretooluse"); h.code != 0 {
		t.Fatalf("hook after off: %+v", h)
	}
}

func TestMCPRunAndWait(t *testing.T) {
	dir, env := project(t)
	call := func(id int, name, args string) map[string]any {
		return map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": json.RawMessage(args)}}
	}
	send := func(msgs ...any) map[float64]map[string]any {
		var in bytes.Buffer
		for _, m := range msgs {
			b, _ := json.Marshal(m)
			in.Write(append(b, '\n'))
		}
		r := sarx(t, dir, env, in.String(), "mcp")
		if r.code != 0 {
			t.Fatalf("mcp exit %d: %s", r.code, r.err)
		}
		out := map[float64]map[string]any{}
		for _, l := range strings.Split(strings.TrimSpace(r.out), "\n") {
			var m map[string]any
			if err := json.Unmarshal([]byte(l), &m); err != nil {
				t.Fatalf("bad output line %q", l)
			}
			id, _ := m["id"].(float64)
			out[id] = m
		}
		return out
	}
	text := func(m map[string]any) string {
		return m["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	}
	out := send(call(1, "sarx_run", `{"text":"change a thing"}`))
	var started struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal([]byte(text(out[1])), &started); err != nil || started.Task == "" {
		t.Fatalf("sarx_run reply: %q (%v)", text(out[1]), err)
	}
	out = send(call(2, "sarx_wait", `{"task":"`+started.Task+`","seconds":30}`))
	if !strings.Contains(text(out[2]), "DONE") {
		t.Fatalf("sarx_wait: %s", text(out[2]))
	}
	out = send(call(3, "sarx_status", `{"all":true}`))
	if !strings.Contains(text(out[3]), started.Task) {
		t.Fatalf("sarx_status: %s", text(out[3]))
	}
}

func TestAgentsSeesCommittedProjectConfigWithoutInit(t *testing.T) {
	dir, env := project(t) // has sarx.toml but no .sarx yet
	r := sarx(t, dir, env, "", "agents")
	if r.code != 0 || !strings.Contains(r.out, "fake") || strings.Contains(r.out, "No config file") {
		t.Fatalf("%+v", r)
	}
}
