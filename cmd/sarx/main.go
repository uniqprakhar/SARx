// sarx sends work to other AI coding agents, headless and in the background, and keeps track of it.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"sarx/internal/config"
	"sarx/internal/engine"
	"sarx/internal/integrate"
	"sarx/internal/mcp"
	"sarx/internal/spec"
	"sarx/internal/store"
	"sarx/internal/util"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.1.0"

const usage = `sarx - send work to other AI agents, in the background, and keep track of it

Start here:
  sarx init                      set up this project (finds your agent CLIs, wires your tools)
  sarx run "what to do"          do a task now: picks an agent, runs it, shows the result

Working with tasks:
  sarx ps [--all]                what is running (or everything, with --all)
  sarx wait <task>               wait for a task to finish
  sarx show <task> [--logs]      read a task's report
  sarx stop <task>               stop a running task
  sarx retry <task>              try again, escalating to the next agent role
  sarx apply <task>              merge a worktree task's changes (see: dispatch --isolate worktree)

For tools and scripts that write their own briefs:
  sarx new <slug>                create a brief template and print its path
  sarx dispatch <task>           start a brief (add --detach to return at once)
  sarx verify <task>             run the brief's "Done when" checks

Setup and diagnosis:
  sarx doctor [--probe]          check the install; --probe sends each agent a tiny test task
  sarx agents                    show which agent fills each role
  sarx mode on|off|status        delegation mode (used with the Claude Code hook)
  sarx mcp                       serve these operations as MCP tools over stdio
  sarx statusline                one line of live task progress for a status bar
  sarx version

Every command that prints results accepts --json. Tasks are the directories under .sarx/runs/.
Exit codes: 0 done, 1 failed, 2 could not start (usage or setup), 3 done but with warnings.
`

type app struct {
	cwd string
	out io.Writer
	err io.Writer
}

var errNoProject = errors.New("no sarx project here. Run `sarx init` in your project first (`sarx run \"...\"` also sets one up)")

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sarx:", err)
		os.Exit(2)
	}
	a := &app{cwd: cwd, out: os.Stdout, err: os.Stderr}
	os.Exit(a.run(os.Args[1:]))
}

func (a *app) run(argv []string) int {
	if len(argv) == 0 {
		fmt.Fprint(a.out, usage)
		return 0
	}
	cmd, rest := argv[0], argv[1:]
	var code int
	var err error
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(a.out, usage)
		return 0
	case "version", "--version", "-v":
		fmt.Fprintln(a.out, "sarx", version)
		return 0
	case "init":
		code, err = a.cmdInit(rest)
	case "doctor":
		code, err = a.cmdDoctor(rest)
	case "run":
		code, err = a.cmdRun(rest)
	case "new":
		code, err = a.cmdNew(rest)
	case "dispatch":
		code, err = a.cmdDispatch(rest)
	case "ps", "status":
		code, err = a.cmdPs(rest)
	case "wait":
		code, err = a.cmdWait(rest)
	case "show":
		code, err = a.cmdShow(rest)
	case "stop":
		code, err = a.cmdStop(rest)
	case "verify":
		code, err = a.cmdVerify(rest)
	case "retry":
		code, err = a.cmdRetry(rest)
	case "apply":
		code, err = a.cmdApply(rest, false)
	case "discard":
		code, err = a.cmdApply(rest, true)
	case "agents":
		code, err = a.cmdAgents(rest)
	case "mode":
		code, err = a.cmdMode(rest)
	case "hook":
		return a.cmdHook(rest)
	case "statusline":
		return a.cmdStatusline()
	case "mcp":
		code, err = a.cmdMCP()
	case "_supervise":
		code, err = a.cmdSupervise(rest)
	default:
		fmt.Fprintf(a.err, "sarx: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(a.err, "sarx:", err)
		if code == 0 {
			code = 2
		}
	}
	return code
}

// engine loads the project and its configuration. With create set, a missing project is created.
func (a *app) engine(create bool) (*engine.Engine, error) {
	root := store.FindRoot(a.cwd)
	if root == "" {
		if !create {
			return nil, errNoProject
		}
		var err error
		if root, err = store.EnsureRoot(a.cwd); err != nil {
			return nil, err
		}
	}
	return a.engineAt(root)
}

func (a *app) engineAt(root string) (*engine.Engine, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	for _, w := range cfg.Warnings {
		fmt.Fprintln(a.err, "sarx: warning:", w)
	}
	e := engine.New(root, cfg)
	e.Log = a.err
	return e, nil
}

func (a *app) printJSON(v any) {
	data, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(a.out, string(data))
}

// exitFor maps a finished task to the process exit code.
func exitFor(st *spec.State) int {
	switch {
	case st == nil:
		return 1
	case spec.IsSuccess(st.State):
		if len(st.Warnings) > 0 || (st.Verified != nil && st.Verified.Fail > 0) {
			return 3
		}
		return 0
	default:
		return 1
	}
}

func (a *app) needTask(p *args, eng *engine.Engine, cmd string) (store.Task, error) {
	if len(p.pos) == 0 {
		return store.Task{}, fmt.Errorf("which task? usage: sarx %s <task>   (see ids with: sarx ps --all)", cmd)
	}
	return eng.Task(p.pos[0])
}

// ---------------------------------------------------------------- init / doctor

func (a *app) cmdInit(argv []string) (int, error) {
	f := flags{bools: []string{"yes", "reconfigure", "enforce", "probe", "json"}, lists: []string{"for"}, split: []string{"for"}, alias: map[string]string{"y": "yes"}}
	p, err := f.parse(argv)
	if err != nil {
		return 2, err
	}
	res, err := integrate.Init(integrate.InitOptions{
		Dir: a.cwd, For: p.l["for"], Yes: p.b["yes"] || p.b["json"], Reconfigure: p.b["reconfigure"], Enforce: p.b["enforce"],
		In: stdinIfInteractive(), Out: a.err,
	})
	if err != nil {
		return 2, err
	}
	if p.b["json"] {
		a.printJSON(res)
		return 0, nil
	}
	fmt.Fprintf(a.out, "sarx init\n  project   %s\n", res.Root)
	if len(res.Found) > 0 {
		fmt.Fprintf(a.out, "  agents    found %s\n", strings.Join(res.Found, ", "))
	} else {
		fmt.Fprintf(a.out, "  agents    none found on PATH\n")
	}
	fmt.Fprintf(a.out, "  config    %s (%s)\n", res.ConfigPath, res.ConfigNote)
	if len(res.Roles) > 0 {
		var parts []string
		for _, r := range spec.Roles {
			parts = append(parts, r+"="+res.Roles[r])
		}
		fmt.Fprintf(a.out, "  roles     %s\n", strings.Join(parts, " "))
	}
	fmt.Fprintf(a.out, "  tools     %s\n", strings.Join(res.Targets, ", "))
	for _, act := range res.Actions {
		rel, err := filepath.Rel(res.Root, act.Path)
		if err != nil {
			rel = act.Path
		}
		line := fmt.Sprintf("    %-34s %s", rel, act.Result)
		if act.Note != "" {
			line += "  (" + act.Note + ")"
		}
		fmt.Fprintln(a.out, line)
	}
	for _, n := range res.Notes {
		fmt.Fprintf(a.out, "  note      %s\n", n)
	}
	if p.b["probe"] {
		fmt.Fprintln(a.out)
		return a.runDoctor(true, false)
	}
	fmt.Fprintf(a.out, "\nNext: sarx doctor --probe   (optional: sends each agent a tiny test task)\n      sarx run \"add a retry to the login request\"\n")
	return 0, nil
}

func stdinIfInteractive() io.Reader {
	if util.IsTerminal(os.Stdin) {
		return os.Stdin
	}
	return nil
}

func (a *app) cmdDoctor(argv []string) (int, error) {
	f := flags{bools: []string{"probe", "json"}}
	p, err := f.parse(argv)
	if err != nil {
		return 2, err
	}
	return a.runDoctor(p.b["probe"], p.b["json"])
}

func (a *app) runDoctor(probe, asJSON bool) (int, error) {
	root := store.FindRoot(a.cwd)
	croot := root
	if croot == "" {
		croot = store.FindConfigRoot(a.cwd)
	}
	cfg, cfgErr := config.Load(croot)
	checks := integrate.Doctor(root, cfg, cfgErr, version, probe, a.err)
	if asJSON {
		a.printJSON(checks)
	} else {
		fails, warns := integrate.RenderChecks(a.out, checks)
		switch {
		case fails > 0:
			fmt.Fprintf(a.out, "\n%d problem(s) to fix.\n", fails)
		case warns > 0:
			fmt.Fprintf(a.out, "\nWorking, with %d thing(s) worth a look.\n", warns)
		default:
			fmt.Fprintln(a.out, "\nAll good.")
		}
	}
	for _, c := range checks {
		if c.Status == integrate.Fail {
			return 1, nil
		}
	}
	return 0, nil
}

// ---------------------------------------------------------------- creating and starting tasks

func (a *app) cmdRun(argv []string) (int, error) {
	f := flags{
		bools: []string{"ro", "detach", "json", "no-verify", "allow-images"},
		strs:  []string{"agent", "role", "timeout", "lock", "isolate"},
		lists: []string{"verify"},
		alias: map[string]string{"d": "detach"},
	}
	p, err := f.parse(argv)
	if err != nil {
		return 2, err
	}
	eng, err := a.engine(true)
	if err != nil {
		return 2, err
	}
	if v := p.l["verify"]; len(v) > 0 {
		eng.Cfg.Verify = v
	}
	t, _, err := eng.QuickTask(strings.Join(p.pos, " "), p.s["role"])
	if err != nil {
		return 2, err
	}
	o := spec.Options{
		Agent: p.s["agent"], Role: p.s["role"], ReadOnly: p.b["ro"], Timeout: atoi(p.s["timeout"]), Lock: p.s["lock"],
		Isolation: p.s["isolate"], AllowImages: p.b["allow-images"], NoVerify: p.b["no-verify"], Extra: p.rest,
	}
	return a.start(eng, t, o, p.b["detach"], p.b["json"], true)
}

func (a *app) cmdNew(argv []string) (int, error) {
	f := flags{bools: []string{"json"}, strs: []string{"role", "title", "goal"}}
	p, err := f.parse(argv)
	if err != nil {
		return 2, err
	}
	if len(p.pos) == 0 {
		return 2, errors.New("give the task a short name, for example: sarx new add-retry --role builder")
	}
	eng, err := a.engine(true)
	if err != nil {
		return 2, err
	}
	t, err := eng.Scaffold(engine.ScaffoldOptions{Slug: p.pos[0], Title: p.s["title"], Role: p.s["role"], Goal: p.s["goal"], Template: true})
	if err != nil {
		return 2, err
	}
	if p.b["json"] {
		a.printJSON(map[string]string{"task": t.ID, "path": t.Brief(), "dir": t.Dir})
	} else {
		fmt.Fprintln(a.out, t.Brief())
	}
	return 0, nil
}

func (a *app) cmdDispatch(argv []string) (int, error) {
	f := flags{
		bools: []string{"ro", "detach", "json", "resume", "allow-images", "no-verify"},
		strs:  []string{"agent", "role", "timeout", "lock", "isolate", "session"},
		alias: map[string]string{"d": "detach"},
	}
	p, err := f.parse(argv)
	if err != nil {
		return 2, err
	}
	eng, err := a.engine(false)
	if err != nil {
		return 2, err
	}
	t, err := a.needTask(p, eng, "dispatch")
	if err != nil {
		return 2, err
	}
	o := spec.Options{
		Agent: p.s["agent"], Role: p.s["role"], ReadOnly: p.b["ro"], Timeout: atoi(p.s["timeout"]), Lock: p.s["lock"],
		Isolation: p.s["isolate"], AllowImages: p.b["allow-images"], NoVerify: p.b["no-verify"], Extra: p.rest,
		Resuming: p.b["resume"], Resume: p.s["session"], SessionID: p.s["session"],
	}
	if p.b["resume"] {
		o.SessionID = ""
	}
	return a.start(eng, t, o, p.b["detach"], p.b["json"], false)
}

// start dispatches a task and reports on it. friendly selects the beginner-oriented output.
func (a *app) start(eng *engine.Engine, t store.Task, o spec.Options, detach, asJSON, friendly bool) (int, error) {
	stop := func() {}
	if !detach && !asJSON && util.IsTerminal(os.Stderr) {
		stop = a.progressLine(t)
	}
	st, err := eng.Dispatch(t, o, detach)
	stop()
	if err != nil {
		return 2, err
	}
	if asJSON {
		a.printJSON(st)
		return exitFor(st), nil
	}
	if detach {
		fmt.Fprintf(a.out, "started %s on %s (%s). Check with: sarx ps    Wait with: sarx wait %s\n", t.ID, st.Agent, st.Role, t.ID)
		return 0, nil
	}
	if friendly {
		fmt.Fprint(a.out, humanSummary(t, st))
	} else {
		fmt.Fprintln(a.out, engine.Summary(t, st))
	}
	return exitFor(st), nil
}

// progressLine redraws one status line on stderr while a foreground task runs.
func (a *app) progressLine(t store.Task) func() {
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(1500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-quit:
				fmt.Fprint(a.err, "\r\x1b[2K")
				return
			case <-tick.C:
				st, err := store.ReadState(t)
				if err != nil {
					continue
				}
				d, n := engine.Ticks(t)
				step := engine.LastStep(t)
				if len(step) > 40 {
					step = step[:40]
				}
				fmt.Fprintf(a.err, "\r\x1b[2K  %s %s %d/%d %s %s", st.State, st.Agent, d, n, step, util.Age(util.Now()-st.Started))
			}
		}
	}()
	return func() { close(quit); <-done }
}

func humanSummary(t store.Task, st *spec.State) string {
	done, total := engine.Ticks(t)
	var b strings.Builder
	state := store.Effective(st)
	fmt.Fprintf(&b, "%s in %s by %s (%s), %d/%d steps\n", state, util.Age(st.Seconds), st.Agent, st.Role, done, total)
	if st.Verified != nil && st.Verified.Ran {
		fmt.Fprintf(&b, "  checks run by sarx: %d passed, %d failed\n", st.Verified.Pass, st.Verified.Fail)
		for _, d := range st.Verified.Details {
			fmt.Fprintf(&b, "    check %d failed (exit %d): %s\n", d.Index, d.Exit, strings.ReplaceAll(d.Tail, "\n", "\n      "))
		}
	}
	for _, w := range st.Warnings {
		fmt.Fprintf(&b, "  warning: %s\n", w)
	}
	if st.Note != "" && !spec.IsSuccess(state) {
		fmt.Fprintf(&b, "  note: %s\n", st.Note)
	}
	fmt.Fprintf(&b, "  report: %s\n", st.Result)
	if st.Worktree != "" && spec.IsSuccess(state) {
		fmt.Fprintf(&b, "  changes are on branch %s; merge them with: sarx apply %s   (or throw away: sarx discard %s)\n", st.Branch, st.ID, st.ID)
	}
	if h := engine.Hint(st); h != "" {
		fmt.Fprintf(&b, "  %s\n", h)
	}
	return b.String()
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// ---------------------------------------------------------------- looking at tasks

func (a *app) cmdPs(argv []string) (int, error) {
	f := flags{bools: []string{"all", "json"}, alias: map[string]string{"a": "all"}}
	p, err := f.parse(argv)
	if err != nil {
		return 2, err
	}
	eng, err := a.engine(false)
	if err != nil {
		return 2, err
	}
	rows := eng.List(p.b["all"])
	if p.b["json"] {
		if rows == nil {
			rows = []engine.Row{}
		}
		a.printJSON(rows)
		return 0, nil
	}
	if len(rows) == 0 {
		if p.b["all"] {
			fmt.Fprintln(a.out, "no tasks yet. Try: sarx run \"what you want done\"")
		} else {
			fmt.Fprintln(a.out, "nothing is running. (sarx ps --all shows finished tasks too)")
		}
		return 0, nil
	}
	w := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TASK\tAGENT\tROLE\tSTATE\tSTEPS\tAGE\tNOW")
	for _, r := range rows {
		state := r.State
		if r.Quiet {
			state += " (quiet " + util.Age(r.QuietFor) + ")"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d/%d\t%s\t%s\n", r.ID, r.Agent, r.Role, state, r.Done, r.Total, util.Age(r.AgeSecs), r.Step)
	}
	w.Flush()
	return 0, nil
}

func (a *app) cmdWait(argv []string) (int, error) {
	f := flags{bools: []string{"json"}, strs: []string{"timeout"}}
	p, err := f.parse(argv)
	if err != nil {
		return 2, err
	}
	eng, err := a.engine(false)
	if err != nil {
		return 2, err
	}
	t, err := a.needTask(p, eng, "wait")
	if err != nil {
		return 2, err
	}
	st, finished, err := eng.Wait(t, time.Duration(atoi(p.s["timeout"]))*time.Second)
	if err != nil {
		return 2, err
	}
	if !finished {
		fmt.Fprintf(a.out, "%s is still %s\n", t.ID, st.State)
		return 1, nil
	}
	if p.b["json"] {
		a.printJSON(st)
	} else {
		fmt.Fprintln(a.out, engine.Summary(t, st))
	}
	return exitFor(st), nil
}

func (a *app) cmdShow(argv []string) (int, error) {
	f := flags{bools: []string{"json", "logs"}}
	p, err := f.parse(argv)
	if err != nil {
		return 2, err
	}
	eng, err := a.engine(false)
	if err != nil {
		return 2, err
	}
	t, err := a.needTask(p, eng, "show")
	if err != nil {
		return 2, err
	}
	st, err := store.ReadState(t)
	if err != nil {
		return 2, fmt.Errorf("%s has not been dispatched yet", t.ID)
	}
	if p.b["json"] {
		a.printJSON(st)
		return 0, nil
	}
	fmt.Fprintln(a.out, engine.Summary(t, st))
	if p.b["logs"] {
		for _, f := range []string{t.Stdout(), t.Stderr()} {
			if data, err := os.ReadFile(f); err == nil && len(strings.TrimSpace(string(data))) > 0 {
				fmt.Fprintf(a.out, "\n--- %s (last lines) ---\n%s\n", filepath.Base(f), util.Tail(string(data), 30))
			}
		}
		return 0, nil
	}
	if data, err := os.ReadFile(t.ResultMD()); err == nil {
		fmt.Fprintf(a.out, "\n%s\n", strings.TrimRight(string(data), "\n"))
	}
	return 0, nil
}

func (a *app) cmdStop(argv []string) (int, error) {
	f := flags{bools: []string{"json"}}
	p, err := f.parse(argv)
	if err != nil {
		return 2, err
	}
	eng, err := a.engine(false)
	if err != nil {
		return 2, err
	}
	t, err := a.needTask(p, eng, "stop")
	if err != nil {
		return 2, err
	}
	st, err := eng.Stop(t)
	if err != nil {
		return 2, err
	}
	if p.b["json"] {
		a.printJSON(st)
	} else {
		fmt.Fprintf(a.out, "sarx stop %s: %s. Continue it with: sarx dispatch %s --resume\n", t.ID, st.State, t.ID)
	}
	return 0, nil
}

func (a *app) cmdVerify(argv []string) (int, error) {
	f := flags{bools: []string{"json"}}
	p, err := f.parse(argv)
	if err != nil {
		return 2, err
	}
	eng, err := a.engine(false)
	if err != nil {
		return 2, err
	}
	t, err := a.needTask(p, eng, "verify")
	if err != nil {
		return 2, err
	}
	v, err := eng.Verify(t)
	if err != nil {
		return 2, err
	}
	if p.b["json"] {
		a.printJSON(v)
	} else {
		for _, d := range v.Details {
			fmt.Fprintf(a.out, "check %d FAILED (exit %d)\n    %s\n", d.Index, d.Exit, strings.ReplaceAll(d.Tail, "\n", "\n    "))
		}
		fmt.Fprintf(a.out, "sarx verify %s: %d passed, %d failed\n", t.ID, v.Pass, v.Fail)
	}
	if v.Fail > 0 {
		return 1, nil
	}
	return 0, nil
}

func (a *app) cmdRetry(argv []string) (int, error) {
	f := flags{
		bools: []string{"detach", "json", "ro"}, strs: []string{"as", "role", "why", "timeout"},
		alias: map[string]string{"d": "detach"},
	}
	p, err := f.parse(argv)
	if err != nil {
		return 2, err
	}
	eng, err := a.engine(false)
	if err != nil {
		return 2, err
	}
	t, err := a.needTask(p, eng, "retry")
	if err != nil {
		return 2, err
	}
	o := spec.Options{ReadOnly: p.b["ro"], Timeout: atoi(p.s["timeout"]), Extra: p.rest}
	st, nt, err := eng.Retry(t, p.s["as"], p.s["role"], p.s["why"], o, p.b["detach"])
	if err != nil {
		return 2, err
	}
	if p.b["json"] {
		a.printJSON(st)
		return exitFor(st), nil
	}
	if p.b["detach"] {
		fmt.Fprintf(a.out, "started %s on %s (%s)\n", nt.ID, st.Agent, st.Role)
		return 0, nil
	}
	fmt.Fprintln(a.out, engine.Summary(nt, st))
	return exitFor(st), nil
}

func (a *app) cmdApply(argv []string, discard bool) (int, error) {
	f := flags{}
	p, err := f.parse(argv)
	if err != nil {
		return 2, err
	}
	eng, err := a.engine(false)
	if err != nil {
		return 2, err
	}
	name := "apply"
	if discard {
		name = "discard"
	}
	t, err := a.needTask(p, eng, name)
	if err != nil {
		return 2, err
	}
	if discard {
		if err := eng.Discard(t); err != nil {
			return 2, err
		}
		fmt.Fprintf(a.out, "discarded the changes of %s\n", t.ID)
		return 0, nil
	}
	out, err := eng.Apply(t)
	if err != nil {
		return 2, err
	}
	fmt.Fprintf(a.out, "merged %s into your current branch\n%s\n", t.ID, out)
	return 0, nil
}

// ---------------------------------------------------------------- configuration views

func (a *app) cmdAgents(argv []string) (int, error) {
	f := flags{bools: []string{"json"}}
	p, err := f.parse(argv)
	if err != nil {
		return 2, err
	}
	cfg, err := config.Load(store.FindConfigRoot(a.cwd))
	if err != nil {
		return 2, err
	}
	type row struct {
		Role, Agent, Command, Model string
		Installed                   bool
	}
	var rows []row
	for _, r := range spec.Roles {
		ag, err := cfg.ForRole(r)
		if err != nil {
			rows = append(rows, row{Role: r})
			continue
		}
		_, lerr := exec.LookPath(ag.Exec)
		rows = append(rows, row{Role: r, Agent: ag.Name, Command: ag.Exec, Model: ag.Model, Installed: lerr == nil})
	}
	if p.b["json"] {
		a.printJSON(map[string]any{"roles": rows, "auto_detected": cfg.AutoDetected, "config": cfg.Sources})
		return 0, nil
	}
	w := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ROLE\tAGENT\tCOMMAND\tMODEL\tINSTALLED")
	for _, r := range rows {
		if r.Agent == "" {
			fmt.Fprintf(w, "%s\t-\t-\t-\tno agent (run: sarx init)\n", r.Role)
			continue
		}
		model := r.Model
		if model == "" {
			model = "(CLI default)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\n", r.Role, r.Agent, r.Command, model, r.Installed)
	}
	w.Flush()
	if cfg.AutoDetected {
		fmt.Fprintln(a.out, "\nNo config file: every role uses the first agent found. Run `sarx init` to choose.")
	}
	var others []string
	for n := range cfg.Agents {
		others = append(others, n)
	}
	sort.Strings(others)
	fmt.Fprintf(a.out, "\nKnown agents: %s\n", strings.Join(others, ", "))
	return 0, nil
}

func (a *app) cmdMode(argv []string) (int, error) {
	root := store.FindRoot(a.cwd)
	if root == "" {
		return 2, errNoProject
	}
	sub := "status"
	if len(argv) > 0 {
		sub = argv[0]
	}
	switch sub {
	case "on":
		if err := integrate.ModeOn(root); err != nil {
			return 2, err
		}
		fmt.Fprintln(a.out, "delegation mode is on: delegate with sarx, do not do the work directly")
	case "off":
		if err := integrate.ModeOff(root); err != nil {
			return 2, err
		}
		fmt.Fprintln(a.out, "delegation mode is off")
	case "status":
		if integrate.ModeIsOn(root) {
			fmt.Fprintln(a.out, "delegation mode is on")
		} else {
			fmt.Fprintln(a.out, "delegation mode is off")
		}
	default:
		return 2, errors.New("usage: sarx mode on|off|status")
	}
	return 0, nil
}

// ---------------------------------------------------------------- integrations

func (a *app) cmdHook(argv []string) int {
	if len(argv) == 0 || argv[0] != "pretooluse" {
		fmt.Fprintln(a.err, "usage: sarx hook pretooluse   (reads the hook's JSON on stdin)")
		return 2
	}
	return integrate.HookPreToolUse(os.Stdin, a.err, a.cwd)
}

func (a *app) cmdStatusline() int {
	cwd := a.cwd
	if !util.IsTerminal(os.Stdin) {
		var in struct {
			Cwd       string `json:"cwd"`
			Workspace struct {
				CurrentDir string `json:"current_dir"`
			} `json:"workspace"`
		}
		if data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20)); err == nil && json.Unmarshal(data, &in) == nil {
			switch {
			case in.Cwd != "":
				cwd = in.Cwd
			case in.Workspace.CurrentDir != "":
				cwd = in.Workspace.CurrentDir
			}
		}
	}
	root := store.FindRoot(cwd)
	if root == "" {
		return 0
	}
	if line := integrate.Statusline(root, time.Now(), os.Getenv("NO_COLOR") == ""); line != "" {
		fmt.Fprintln(a.out, line)
	}
	return 0
}

func (a *app) cmdMCP() (int, error) {
	s := &mcp.Server{Version: version, Engine: func() (*engine.Engine, error) {
		e, err := a.engine(true)
		if e != nil {
			e.Log = io.Discard
		}
		return e, err
	}}
	if err := s.Serve(os.Stdin, os.Stdout); err != nil {
		return 1, err
	}
	return 0, nil
}

// cmdSupervise is the detached half of `dispatch --detach`; it is not meant to be run by hand.
func (a *app) cmdSupervise(argv []string) (int, error) {
	f := flags{strs: []string{"root"}}
	p, err := f.parse(argv)
	if err != nil {
		return 2, err
	}
	if p.s["root"] == "" || len(p.pos) != 1 {
		return 2, errors.New("_supervise is internal: sarx _supervise --root <project> <task dir>")
	}
	eng, err := a.engineAt(p.s["root"])
	if err != nil {
		return 2, err
	}
	eng.Log = io.Discard
	if _, err := eng.Supervise(store.TaskAt(p.s["root"], p.pos[0])); err != nil {
		return 1, err
	}
	return 0, nil
}
