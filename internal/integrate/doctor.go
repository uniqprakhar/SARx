package integrate

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"sarx/internal/config"
	"sarx/internal/engine"
	"sarx/internal/spec"
	"sarx/internal/store"
)

// Check statuses.
const (
	OK   = "ok"
	Warn = "warn"
	Fail = "fail"
)

// Check is one line of the health report.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// Doctor inspects the install. With probe set it also sends a tiny task to every agent in use,
// which uses a few tokens of each provider's quota.
func Doctor(root string, cfg *config.Config, cfgErr error, version string, probe bool, log io.Writer) []Check {
	var cs []Check
	add := func(name, status, detail, fix string) { cs = append(cs, Check{name, status, detail, fix}) }

	add("platform", OK, fmt.Sprintf("sarx %s on %s/%s", version, runtime.GOOS, runtime.GOARCH), "")
	if root == "" {
		add("project", Warn, "no .sarx directory here or above", "run `sarx init` in your project")
	} else if err := writable(store.StateDir(root)); err != nil {
		add("project", Fail, store.StateDir(root)+" is not writable: "+err.Error(), "check the folder's permissions")
	} else {
		add("project", OK, root, "")
	}
	if _, err := exec.LookPath("git"); err != nil {
		add("git", Warn, "git is not installed", "install git to get scope and read-only checks and worktree isolation")
	} else if root != "" && store.GitRoot(root) == "" {
		add("git", Warn, "this project is not a git repository", "run `git init` to get scope and read-only checks")
	} else {
		add("git", OK, "found", "")
	}
	if cfgErr != nil {
		add("config", Fail, cfgErr.Error(), "fix the file named above (it uses a small TOML subset; see docs/adapters.md)")
		return cs
	}
	switch {
	case cfg.AutoDetected:
		add("config", OK, "no config file; every role uses the first agent found ("+cfg.Roles[spec.Builder]+")", "run `sarx init` to choose roles")
	case len(cfg.Sources) == 0:
		add("config", Fail, "no config file and no agent CLI on PATH", "install an agent CLI ("+strings.Join(config.ProfileNames(), ", ")+") and run `sarx init`")
		return cs
	default:
		add("config", OK, strings.Join(cfg.Sources, ", "), "")
	}
	for _, w := range cfg.Warnings {
		add("config", Warn, w, "check the spelling against docs/adapters.md")
	}

	used := map[string]bool{}
	var names []string
	for _, r := range spec.Roles {
		if n := cfg.Roles[r]; n != "" && !used[n] {
			used[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	add("roles", OK, formatRoles(cfg.Roles), "")
	for _, n := range names {
		a, err := cfg.Get(n)
		if err != nil {
			add("agent "+n, Fail, err.Error(), "")
			continue
		}
		if err := a.Validate(); err != nil {
			add("agent "+n, Fail, err.Error(), "")
			continue
		}
		path, err := exec.LookPath(a.Exec)
		if err != nil {
			add("agent "+n, Fail, a.Exec+" is not on PATH", "install it, or point the agent at another command with exec = \"...\"")
			continue
		}
		detail := path
		if p, ok := config.LookupProfile(a.Adapter); ok && !p.Verified {
			add("agent "+n, Warn, detail+" (built-in "+a.Adapter+" profile is unverified)", "run `sarx doctor --probe`; override any flag in [agent."+n+"]")
			continue
		}
		add("agent "+n, OK, detail, "")
	}

	if root != "" {
		var wired []string
		for _, rel := range []string{"AGENTS.md", "CLAUDE.md", "GEMINI.md", ".cursor/rules/sarx.mdc"} {
			if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))); err == nil && strings.Contains(string(data), BeginMarker) {
				wired = append(wired, rel)
			}
		}
		if len(wired) == 0 {
			add("integrations", Warn, "no instruction file mentions sarx yet", "run `sarx init --for <claude|cursor|codex|gemini|agents-md>`")
		} else {
			add("integrations", OK, strings.Join(wired, ", "), "")
		}
		if ModeIsOn(root) {
			add("mode", OK, "delegation mode is on", "")
		} else {
			add("mode", OK, "delegation mode is off (`sarx mode on` to turn it on)", "")
		}
	}

	if probe && root != "" {
		eng := engine.New(root, cfg)
		eng.Log = io.Discard
		for _, n := range names {
			if log != nil {
				fmt.Fprintf(log, "probing %s (up to 3 minutes)...\n", n)
			}
			cs = append(cs, probeAgent(root, eng, n))
		}
	}
	return cs
}

func probeAgent(root string, eng *engine.Engine, name string) Check {
	body := "# Probe " + name + "\n\n## Goal\nThis is a connectivity probe. Do not look at or change any project file.\n\n## Subtasks\n- [ ] Write the report\n\n## Done when\nWrite the report with STATUS: DONE as its first line and nothing else of substance.\n"
	run := "probe-" + time.Now().Format("150405")
	t, err := store.NewTask(root, run, "probe-"+name, body)
	if err != nil {
		return Check{"probe " + name, Fail, err.Error(), ""}
	}
	st, err := eng.Dispatch(t, spec.Options{Agent: name, Timeout: 180, NoVerify: true}, false)
	if err != nil {
		return Check{"probe " + name, Fail, err.Error(), ""}
	}
	detail := fmt.Sprintf("%s in %ds", st.State, st.Seconds)
	if spec.IsSuccess(st.State) {
		return Check{"probe " + name, OK, detail, ""}
	}
	fix := engine.Hint(st)
	if st.State == spec.Crashed {
		fix = "see " + st.Stderr + " (is the CLI signed in? is the model id valid?)"
	}
	if st.Note != "" {
		detail += ": " + st.Note
	}
	return Check{"probe " + name, Fail, detail, fix}
}

func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// RenderChecks prints checks in plain ASCII so every terminal shows them the same way.
func RenderChecks(w io.Writer, cs []Check) (fails, warns int) {
	for _, c := range cs {
		tag := "[ok]  "
		switch c.Status {
		case Warn:
			tag, warns = "[warn]", warns+1
		case Fail:
			tag, fails = "[FAIL]", fails+1
		}
		fmt.Fprintf(w, "%s %-14s %s\n", tag, c.Name, c.Detail)
		if c.Fix != "" && c.Status != OK {
			fmt.Fprintf(w, "       %-14s fix: %s\n", "", c.Fix)
		}
	}
	return
}
