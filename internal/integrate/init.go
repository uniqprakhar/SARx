package integrate

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"sarx/internal/config"
	"sarx/internal/spec"
	"sarx/internal/store"
	"sarx/internal/util"
)

// Targets sarx can wire itself into.
var Targets = []string{"claude", "cursor", "codex", "gemini", "agents-md"}

// InitOptions controls `sarx init`.
type InitOptions struct {
	Dir         string
	For         []string // targets; empty means detect
	Yes         bool     // accept every default without asking
	Reconfigure bool     // rewrite an existing user config
	Enforce     bool     // install the Claude Code hook that blocks built-in subagents while delegation mode is on
	In          io.Reader
	Out         io.Writer
}

// Action is one file sarx wrote, updated, or left alone.
type Action struct {
	Path   string
	Result string
	Note   string
}

// InitResult is what init did.
type InitResult struct {
	Root       string
	Found      []string
	ConfigPath string
	ConfigNote string
	Roles      map[string]string
	Targets    []string
	Actions    []Action
	Notes      []string
}

// Init prepares a project: state directory, per-user config, and the instruction files and MCP
// entries for each tool in use. It is safe to run again.
func Init(o InitOptions) (*InitResult, error) {
	if o.Out == nil {
		o.Out = io.Discard
	}
	in := bufio.NewReader(o.In)
	interactive := !o.Yes && o.In != nil
	ask := func(q string, def bool) bool {
		if !interactive {
			return def
		}
		d := "Y/n"
		if !def {
			d = "y/N"
		}
		fmt.Fprintf(o.Out, "%s [%s] ", q, d)
		line, _ := in.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			return def
		case "y", "yes":
			return true
		}
		return false
	}

	root, err := store.EnsureRoot(o.Dir)
	if err != nil {
		return nil, err
	}
	res := &InitResult{Root: root, Found: config.DetectInstalled(), ConfigPath: config.UserConfigPath()}

	// Roles: keep an existing config unless asked, otherwise propose a mapping from what is installed.
	_, statErr := os.Stat(res.ConfigPath)
	switch {
	case statErr == nil && !o.Reconfigure:
		res.ConfigNote = "kept your existing config"
		if cfg, err := config.Load(root); err == nil {
			res.Roles = cfg.Roles
		}
	case len(res.Found) == 0:
		res.ConfigNote = "not written: no agent CLI found on PATH"
		res.Notes = append(res.Notes, fmt.Sprintf("No agent CLI found (looked for: %s). Install one, or describe your own in %s (see docs/adapters.md), then run `sarx doctor`.",
			strings.Join(config.ProfileNames(), ", "), res.ConfigPath))
	default:
		roles := proposeRoles(res.Found)
		fmt.Fprintf(o.Out, "Found: %s\nProposed roles: %s\n", strings.Join(res.Found, ", "), formatRoles(roles))
		if !ask("Use this mapping?", true) && interactive {
			roles = chooseRoles(in, o.Out, res.Found, roles)
		}
		if statErr == nil {
			_ = copyFile(res.ConfigPath, res.ConfigPath+".bak")
		}
		if err := util.WriteFileAtomic(res.ConfigPath, []byte(config.Render(roles, nil)), 0o644); err != nil {
			return nil, fmt.Errorf("cannot write %s: %v", res.ConfigPath, err)
		}
		res.Roles = roles
		res.ConfigNote = "written"
		if statErr == nil {
			res.ConfigNote = "rewritten (previous copy saved as .bak)"
		}
		if len(res.Found) == 1 {
			res.Notes = append(res.Notes, "Only one agent was found, so every role uses it. That works; add more agents later and run `sarx init --reconfigure`.")
		} else {
			res.Notes = append(res.Notes, "The operator role uses the same agent as the builder. To use a cheaper model for it, add an [agent.NAME] entry with model = \"...\" and map operator to it in the config file.")
		}
	}

	// Which tools to wire into.
	targets := o.For
	if len(targets) == 0 {
		targets = DetectTargets(root)
	}
	targets, err = normaliseTargets(targets)
	if err != nil {
		return nil, err
	}
	res.Targets = targets
	for _, t := range targets {
		acts, notes := wire(root, t, o.Enforce)
		res.Actions = append(res.Actions, acts...)
		res.Notes = append(res.Notes, notes...)
	}
	return res, nil
}

func proposeRoles(found []string) map[string]string {
	second := found[0]
	if len(found) > 1 {
		second = found[1]
	}
	return map[string]string{
		spec.Operator: found[0], spec.Builder: found[0], spec.Reviewer: second, spec.Fallback: second,
	}
}

func formatRoles(r map[string]string) string {
	var parts []string
	for _, role := range spec.Roles {
		parts = append(parts, role+"="+r[role])
	}
	return strings.Join(parts, " ")
}

func chooseRoles(in *bufio.Reader, out io.Writer, found []string, def map[string]string) map[string]string {
	roles := map[string]string{}
	for _, role := range spec.Roles {
		for {
			fmt.Fprintf(out, "Agent for %s (%s) [%s]: ", role, strings.Join(found, ", "), def[role])
			line, _ := in.ReadString('\n')
			line = strings.TrimSpace(line)
			if line == "" {
				roles[role] = def[role]
				break
			}
			if contains(found, line) {
				roles[role] = line
				break
			}
			fmt.Fprintf(out, "  %q is not one of the agents found.\n", line)
		}
	}
	return roles
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// DetectTargets guesses which tools a project uses.
func DetectTargets(root string) []string {
	var out []string
	has := func(rel string) bool { return util.Exists(filepath.Join(root, rel)) }
	onPath := func(n string) bool { _, err := exec.LookPath(n); return err == nil }
	if has(".claude") || has("CLAUDE.md") || onPath("claude") {
		out = append(out, "claude")
	}
	if has(".cursor") {
		out = append(out, "cursor")
	}
	if has("GEMINI.md") || has(".gemini") || onPath("gemini") {
		out = append(out, "gemini")
	}
	if has("AGENTS.md") || onPath("codex") {
		out = append(out, "codex")
	}
	if len(out) == 0 {
		out = []string{"agents-md"}
	}
	return out
}

func normaliseTargets(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, t := range in {
		for _, p := range strings.Split(t, ",") {
			p = strings.ToLower(strings.TrimSpace(p))
			if p == "" {
				continue
			}
			if p == "all" {
				for _, a := range Targets {
					if !seen[a] {
						seen[a], out = true, append(out, a)
					}
				}
				continue
			}
			if !contains(Targets, p) {
				return nil, fmt.Errorf("unknown tool %q for --for (choose from: %s, all)", p, strings.Join(Targets, ", "))
			}
			if !seen[p] {
				seen[p], out = true, append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func wire(root, target string, enforce bool) ([]Action, []string) {
	var acts []Action
	var notes []string
	add := func(rel string, res string, err error, note string) {
		a := Action{Path: filepath.Join(root, filepath.FromSlash(rel)), Result: res, Note: note}
		if err != nil {
			a.Result, a.Note = "failed", err.Error()
		}
		acts = append(acts, a)
	}
	block := func(rel, header string, withMCP bool) {
		res, err := UpsertBlock(filepath.Join(root, filepath.FromSlash(rel)), header, Policy(withMCP))
		add(rel, res, err, "")
	}
	mcp := func(rel string) {
		res, err := MergeMCP(filepath.Join(root, filepath.FromSlash(rel)))
		note := ""
		if err != nil {
			note = "add this by hand: " + MCPSnippet()
			acts = append(acts, Action{Path: filepath.Join(root, filepath.FromSlash(rel)), Result: "failed", Note: err.Error() + "; " + note})
			return
		}
		add(rel, res, nil, "MCP server entry")
	}
	switch target {
	case "claude":
		block("CLAUDE.md", "", true)
		mcp(".mcp.json")
		res, err := UpsertBlock(filepath.Join(root, ".claude", "skills", "sarx", "SKILL.md"), "---\nname: sarx\ndescription: Delegate work to other agents through sarx. Use when the user says use sarx, delegate, fan out, route this to the agents, or runs /sarx.\n---\n\n# sarx\n\n", Policy(true))
		add(".claude/skills/sarx/SKILL.md", res, err, "skill")
		if enforce {
			res, err := MergeClaudeHook(filepath.Join(root, ".claude", "settings.json"))
			add(".claude/settings.json", res, err, "hook: blocks built-in subagents while `sarx mode on`")
		} else {
			notes = append(notes, "Claude Code: to make the rule \"no built-in subagents while delegating\" binding rather than advisory, run `sarx init --for claude --enforce`.")
		}
	case "cursor":
		block(".cursor/rules/sarx.mdc", CursorRule(), true)
		mcp(".cursor/mcp.json")
	case "gemini":
		block("GEMINI.md", "", true)
		mcp(".gemini/settings.json")
	case "codex":
		block("AGENTS.md", "", false)
		notes = append(notes, "Codex: to add the MCP server, put this in ~/.codex/config.toml:\n    [mcp_servers.sarx]\n    command = \""+SelfExec()+"\"\n    args = [\"mcp\"]")
	case "agents-md":
		block("AGENTS.md", "", false)
	}
	return acts, notes
}

func copyFile(from, to string) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, data, 0o644)
}
