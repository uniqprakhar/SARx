// Package engine implements dispatching, supervising, verifying, stopping and retrying tasks.
package engine

import (
	"fmt"
	"io"
	"os"
	"strings"

	"sarx/internal/brief"
	"sarx/internal/config"
	"sarx/internal/spec"
	"sarx/internal/store"
)

// Engine ties a project root to its configuration.
type Engine struct {
	Root string
	Cfg  *config.Config
	Log  io.Writer // progress and waiting messages; defaults to stderr
	Self string    // path of the sarx binary, used to start detached supervisors
}

// New creates an engine.
func New(root string, cfg *config.Config) *Engine {
	self, _ := os.Executable()
	return &Engine{Root: root, Cfg: cfg, Log: os.Stderr, Self: self}
}

func (e *Engine) logf(format string, a ...any) {
	if e.Log != nil {
		fmt.Fprintf(e.Log, format+"\n", a...)
	}
}

var operatorVerbs = map[string]bool{
	"run": true, "test": true, "build": true, "install": true, "lint": true, "capture": true,
	"reproduce": true, "execute": true, "benchmark": true, "rename": true, "format": true,
}

var reviewerWords = []string{"review", "audit", "assess", "judge", "critique", "evaluate"}

// GuessRole picks a role from a task title. It is deliberately simple; override with --role or --agent.
func GuessRole(title string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	for _, w := range reviewerWords {
		if strings.Contains(t, w) {
			return spec.Reviewer
		}
	}
	first, _, _ := strings.Cut(t, " ")
	if operatorVerbs[first] {
		return spec.Operator
	}
	return spec.Builder
}

// Route decides which agent and role handle a brief, and whether the run is read-only.
func (e *Engine) Route(b brief.Brief, o *spec.Options) (config.Agent, string, error) {
	agentName := o.Agent
	if agentName == "" {
		agentName = b.Front["agent"]
	}
	role := o.Role
	if role == "" {
		role = b.Front["role"]
	}
	if agentName != "" {
		a, err := e.Cfg.Get(agentName)
		if err != nil {
			return a, "", err
		}
		if role == "" {
			role = e.roleOf(agentName)
		}
		if role == spec.Reviewer {
			o.ReadOnly = true
		}
		return a, role, nil
	}
	if role == "" {
		role = GuessRole(b.Title)
	}
	a, err := e.Cfg.ForRole(role)
	if err != nil {
		return a, role, err
	}
	if role == spec.Reviewer {
		o.ReadOnly = true
	}
	return a, role, nil
}

// roleOf finds the first role mapped to an agent, or "custom".
func (e *Engine) roleOf(agent string) string {
	for _, r := range spec.Roles {
		if e.Cfg.Roles[r] == agent {
			return r
		}
	}
	return "custom"
}

// Task resolves a reference to a task in this project.
func (e *Engine) Task(ref string) (store.Task, error) { return store.ResolveTask(e.Root, ref) }
