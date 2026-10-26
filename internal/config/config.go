// Package config loads sarx configuration: built-in adapter profiles, the user's config file, and
// an optional project file. With no config at all it detects installed agent CLIs, so a first run
// needs no setup.
package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"sarx/internal/spec"
	"sarx/internal/tomlite"
)

// DefaultQuotaRegex recognises a provider's usage-limit refusal in a worker's output.
const DefaultQuotaRegex = `(?i)(^|[^0-9])429([^0-9]|$)|usage limit|rate.?limit|quota|too many requests`

// Agent is a fully merged agent definition: a built-in profile with the user's overrides applied.
type Agent struct {
	Name          string
	Adapter       string
	Exec          string
	Args          []string
	PromptVia     string // "arg" (default) or "stdin"
	PromptFlag    string // flag placed before the prompt; empty means the prompt is positional
	Model         string
	ModelFlag     string
	AutoApprove   []string
	SessionNew    []string
	SessionResume []string
	ReadonlyArgs  []string
	QuotaRegex    string
	Images        *bool
	MaxParallel   int
	Timeout       int
	Env           map[string]string
	Notes         string
}

// AllowsImages reports whether briefs that name image files may be sent to this agent.
func (a Agent) AllowsImages() bool { return a.Images == nil || *a.Images }

// CanResume reports whether the agent's CLI can continue an earlier session.
func (a Agent) CanResume() bool { return len(a.SessionResume) > 0 }

// Validate reports a configuration mistake that would stop the agent from starting.
func (a Agent) Validate() error {
	if a.Exec == "" {
		return fmt.Errorf("agent %q has no exec: set exec = \"<command>\" in [agent.%s], or use adapter = \"%s\"",
			a.Name, a.Name, strings.Join(ProfileNames(), "\", \""))
	}
	return nil
}

// Defaults are the settings that apply to every task unless overridden.
type Defaults struct {
	Timeout    int    // seconds a worker may run
	Isolation  string // "none" or "worktree"
	AutoVerify bool   // run the brief's Done-when checks when a worker finishes
	StallAfter int    // seconds without any sign of life before ps flags a task as quiet
	Shell      string // shell used for checks; empty means the platform default
}

// Config is the merged configuration.
type Config struct {
	Roles        map[string]string
	Agents       map[string]Agent
	Defaults     Defaults
	Verify       []string // default Done-when commands for tasks created by `sarx run`
	Sources      []string // config files that were read
	Warnings     []string
	AutoDetected bool
}

type raw struct {
	roles    map[string]string
	agents   map[string]map[string]any
	defaults map[string]any
	verify   []string
	hasVer   bool
}

// UserConfigPath is where the per-user config lives. SARX_CONFIG overrides it.
func UserConfigPath() string {
	if p := os.Getenv("SARX_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "sarx", "config.toml")
}

// ProjectConfigPath is the optional per-project override file.
func ProjectConfigPath(root string) string { return filepath.Join(root, "sarx.toml") }

// Load reads the user config, then the project config, over the built-in defaults.
func Load(root string) (*Config, error) {
	c := &Config{
		Roles:  map[string]string{},
		Agents: map[string]Agent{},
		Defaults: Defaults{
			Timeout:    1800,
			Isolation:  "none",
			AutoVerify: true,
			StallAfter: 600,
		},
	}
	r := &raw{roles: map[string]string{}, agents: map[string]map[string]any{}, defaults: map[string]any{}}
	paths := []string{UserConfigPath()}
	if root != "" {
		paths = append(paths, ProjectConfigPath(root))
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		m, err := tomlite.Parse(string(data))
		if err != nil {
			return nil, fmt.Errorf("%s: %v", p, err)
		}
		if err := r.apply(c, p, m); err != nil {
			return nil, fmt.Errorf("%s: %v", p, err)
		}
		c.Sources = append(c.Sources, p)
	}
	if err := c.finish(r); err != nil {
		return nil, err
	}
	return c, nil
}

var agentKeys = map[string]bool{
	"adapter": true, "exec": true, "args": true, "prompt_via": true, "prompt_flag": true, "model": true,
	"model_flag": true, "auto_approve": true, "session_new": true, "session_resume": true,
	"readonly_args": true, "quota_regex": true, "images": true, "max_parallel": true, "timeout": true,
	"env": true, "notes": true,
}

var defaultKeys = map[string]bool{"timeout": true, "isolation": true, "auto_verify": true, "stall_after": true, "shell": true}

func (r *raw) apply(c *Config, path string, m map[string]any) error {
	for key := range m {
		switch key {
		case "roles", "agent", "defaults", "project":
		default:
			c.Warnings = append(c.Warnings, fmt.Sprintf("%s: unknown section [%s]", path, key))
		}
	}
	if t, ok := tomlite.Table(m, "roles"); ok {
		for k, v := range t {
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("[roles] %s must be a string naming an agent", k)
			}
			if !isRole(k) {
				c.Warnings = append(c.Warnings, fmt.Sprintf("%s: unknown role %q (roles are %s)", path, k, strings.Join(spec.Roles, ", ")))
				continue
			}
			r.roles[k] = s
		}
	}
	if t, ok := tomlite.Table(m, "agent"); ok {
		for name, v := range t {
			at, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("[agent.%s] must be a table", name)
			}
			if r.agents[name] == nil {
				r.agents[name] = map[string]any{}
			}
			for k, val := range at {
				if !agentKeys[k] {
					c.Warnings = append(c.Warnings, fmt.Sprintf("%s: [agent.%s] unknown key %q", path, name, k))
					continue
				}
				r.agents[name][k] = val
			}
		}
	}
	if t, ok := tomlite.Table(m, "defaults"); ok {
		for k, v := range t {
			if !defaultKeys[k] {
				c.Warnings = append(c.Warnings, fmt.Sprintf("%s: [defaults] unknown key %q", path, k))
				continue
			}
			r.defaults[k] = v
		}
	}
	if t, ok := tomlite.Table(m, "project"); ok {
		if v, ok := tomlite.Strings(t, "verify"); ok {
			r.verify, r.hasVer = v, true
		}
	}
	return nil
}

func isRole(s string) bool {
	for _, r := range spec.Roles {
		if r == s {
			return true
		}
	}
	return false
}

func (c *Config) finish(r *raw) error {
	d := &c.Defaults
	if n, ok := tomlite.Int(r.defaults, "timeout"); ok && n > 0 {
		d.Timeout = n
	}
	if s, ok := tomlite.String(r.defaults, "isolation"); ok {
		if s != "none" && s != "worktree" {
			return fmt.Errorf("[defaults] isolation must be \"none\" or \"worktree\", not %q", s)
		}
		d.Isolation = s
	}
	if b, ok := tomlite.Bool(r.defaults, "auto_verify"); ok {
		d.AutoVerify = b
	}
	if n, ok := tomlite.Int(r.defaults, "stall_after"); ok && n > 0 {
		d.StallAfter = n
	}
	if s, ok := tomlite.String(r.defaults, "shell"); ok {
		d.Shell = s
	}
	if r.hasVer {
		c.Verify = r.verify
	}

	names := map[string]bool{}
	for n := range profiles {
		names[n] = true
	}
	for n := range r.agents {
		names[n] = true
	}
	for name := range names {
		a, err := buildAgent(name, r.agents[name])
		if err != nil {
			return err
		}
		c.Agents[name] = a
	}

	for k, v := range r.roles {
		c.Roles[k] = v
	}
	if len(c.Roles) == 0 {
		if found := DetectInstalled(); len(found) > 0 {
			for _, role := range spec.Roles {
				c.Roles[role] = found[0]
			}
			c.AutoDetected = true
		}
		return nil
	}
	// Fill roles the user left out with the first role they did set, so a partial config still works.
	var fallback string
	for _, role := range []string{spec.Builder, spec.Operator, spec.Reviewer, spec.Fallback} {
		if c.Roles[role] != "" {
			fallback = c.Roles[role]
			break
		}
	}
	for _, role := range spec.Roles {
		if c.Roles[role] == "" {
			c.Roles[role] = fallback
		}
	}
	for role, name := range c.Roles {
		if _, ok := c.Agents[name]; !ok {
			return fmt.Errorf("role %q points at agent %q, which is not defined. Define [agent.%s] or use one of: %s",
				role, name, name, strings.Join(c.agentNames(), ", "))
		}
	}
	return nil
}

func (c *Config) agentNames() []string {
	var out []string
	for n := range c.Agents {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func buildAgent(name string, m map[string]any) (Agent, error) {
	adapter, _ := tomlite.String(m, "adapter")
	if adapter == "" {
		if _, ok := profiles[name]; ok {
			adapter = name
		} else {
			adapter = "generic"
		}
	}
	var a Agent
	if p, ok := profiles[adapter]; ok {
		a = p.Agent
	} else if adapter != "generic" {
		return a, fmt.Errorf("agent %q: unknown adapter %q (built-in: %s, or \"generic\" with your own exec)", name, adapter, strings.Join(ProfileNames(), ", "))
	}
	a.Name, a.Adapter = name, adapter
	if v, ok := tomlite.String(m, "exec"); ok {
		a.Exec = v
	}
	if v, ok := tomlite.Strings(m, "args"); ok {
		a.Args = v
	}
	if v, ok := tomlite.String(m, "prompt_via"); ok {
		if v != "arg" && v != "stdin" {
			return a, fmt.Errorf("agent %q: prompt_via must be \"arg\" or \"stdin\"", name)
		}
		a.PromptVia = v
	}
	if v, ok := tomlite.String(m, "prompt_flag"); ok {
		a.PromptFlag = v
	}
	if v, ok := tomlite.String(m, "model"); ok {
		a.Model = v
	}
	if v, ok := tomlite.String(m, "model_flag"); ok {
		a.ModelFlag = v
	}
	if v, ok := tomlite.Strings(m, "auto_approve"); ok {
		a.AutoApprove = v
	}
	if v, ok := tomlite.Strings(m, "session_new"); ok {
		a.SessionNew = v
	}
	if v, ok := tomlite.Strings(m, "session_resume"); ok {
		a.SessionResume = v
	}
	if v, ok := tomlite.Strings(m, "readonly_args"); ok {
		a.ReadonlyArgs = v
	}
	if v, ok := tomlite.String(m, "quota_regex"); ok {
		a.QuotaRegex = v
	}
	if v, ok := tomlite.Bool(m, "images"); ok {
		a.Images = &v
	}
	if v, ok := tomlite.Int(m, "max_parallel"); ok {
		a.MaxParallel = v
	}
	if v, ok := tomlite.Int(m, "timeout"); ok {
		a.Timeout = v
	}
	if v, ok := tomlite.String(m, "notes"); ok {
		a.Notes = v
	}
	if t, ok := tomlite.Table(m, "env"); ok {
		a.Env = map[string]string{}
		for k, v := range t {
			s, ok := v.(string)
			if !ok {
				return a, fmt.Errorf("agent %q: env.%s must be a string", name, k)
			}
			a.Env[k] = s
		}
	}
	if a.PromptVia == "" {
		a.PromptVia = "arg"
	}
	if a.QuotaRegex == "" {
		a.QuotaRegex = DefaultQuotaRegex
	}
	return a, nil
}

// DetectInstalled lists the built-in profiles whose CLI is on PATH, in preference order.
func DetectInstalled() []string {
	var out []string
	for _, n := range profileOrder {
		if _, err := exec.LookPath(profiles[n].Exec); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// Get returns an agent by name.
func (c *Config) Get(name string) (Agent, error) {
	a, ok := c.Agents[name]
	if !ok {
		return Agent{}, fmt.Errorf("unknown agent %q (known: %s)", name, strings.Join(c.agentNames(), ", "))
	}
	return a, nil
}

// ForRole returns the agent mapped to a role.
func (c *Config) ForRole(role string) (Agent, error) {
	if !isRole(role) {
		return Agent{}, fmt.Errorf("unknown role %q (roles are %s)", role, strings.Join(spec.Roles, ", "))
	}
	name := c.Roles[role]
	if name == "" {
		return Agent{}, fmt.Errorf("no agent is mapped to the %s role and no agent CLI was found on PATH. Install one (%s) or run: sarx init",
			role, strings.Join(ProfileNames(), ", "))
	}
	return c.Get(name)
}

// Decl is one agent entry to write into a config file.
type Decl struct {
	Name    string
	Adapter string
	Model   string
}

// Render produces the text of a user config file.
func Render(roles map[string]string, decls []Decl) string {
	var b strings.Builder
	b.WriteString("# sarx configuration. Edit freely; run `sarx doctor` to check it.\n")
	b.WriteString("# Roles say which agent does which kind of work. One agent in every role is fine.\n\n[roles]\n")
	for _, r := range spec.Roles {
		if roles[r] != "" {
			fmt.Fprintf(&b, "%s = %q\n", r, roles[r])
		}
	}
	for _, d := range decls {
		if d.Model == "" && (d.Adapter == "" || d.Adapter == d.Name) {
			continue
		}
		fmt.Fprintf(&b, "\n[agent.%s]\n", d.Name)
		if d.Adapter != "" {
			fmt.Fprintf(&b, "adapter = %q\n", d.Adapter)
		}
		if d.Model != "" {
			fmt.Fprintf(&b, "model = %q\n", d.Model)
		}
	}
	b.WriteString("\n# Other things you can set:\n")
	b.WriteString("# [defaults]\n# timeout = 1800        # seconds a worker may run\n# isolation = \"none\"   # or \"worktree\" (see docs)\n# auto_verify = true\n")
	b.WriteString("# [agent.fast]\n# adapter = \"command-code\"\n# model = \"your-model-id\"\n# images = false       # this model cannot read images\n# max_parallel = 2\n")
	return b.String()
}
