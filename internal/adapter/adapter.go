// Package adapter turns an agent definition into the command line that starts it.
//
// Arguments are built as a list and never pass through a shell, so there is no quoting to get
// wrong on any operating system.
package adapter

import (
	"strings"

	"sarx/internal/config"
)

// Request describes one worker launch.
type Request struct {
	Agent    config.Agent
	Prompt   string
	Session  string // session id (new or resumed)
	Resume   bool   // continue Session instead of starting it
	ReadOnly bool
	Extra    []string // passed through after the adapter's own flags
	TaskDir  string
}

// Command is what to execute.
type Command struct {
	Exec  string
	Args  []string
	Stdin string
	Env   []string // extra KEY=VALUE entries
}

// Build assembles the command for a request. The order is: fixed args, auto-approve flags,
// read-only flags, model, session, extra args, then the prompt.
func Build(r Request) Command {
	a := r.Agent
	vars := map[string]string{
		"id":       r.Session,
		"model":    a.Model,
		"task_dir": r.TaskDir,
	}
	var args []string
	args = append(args, expand(a.Args, vars)...)
	args = append(args, expand(a.AutoApprove, vars)...)
	if r.ReadOnly {
		args = append(args, expand(a.ReadonlyArgs, vars)...)
	}
	if a.Model != "" && a.ModelFlag != "" {
		args = append(args, a.ModelFlag, a.Model)
	}
	if r.Resume && a.CanResume() {
		args = append(args, expand(a.SessionResume, vars)...)
	} else {
		args = append(args, expand(a.SessionNew, vars)...)
	}
	args = append(args, r.Extra...)

	cmd := Command{Exec: a.Exec}
	if a.PromptVia == "stdin" {
		cmd.Stdin = r.Prompt
	} else {
		if a.PromptFlag != "" {
			args = append(args, a.PromptFlag)
		}
		args = append(args, r.Prompt)
	}
	cmd.Args = args
	for k, v := range a.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd
}

func expand(in []string, vars map[string]string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		for k, v := range vars {
			s = strings.ReplaceAll(s, "{"+k+"}", v)
		}
		out = append(out, s)
	}
	return out
}
