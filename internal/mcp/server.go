// Package mcp serves sarx's operations as Model Context Protocol tools over stdio, so any MCP
// capable tool can drive it without a shell. Messages are newline-delimited JSON-RPC 2.0.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"sarx/internal/engine"
	"sarx/internal/spec"
	"sarx/internal/store"
)

// Server holds what a session needs.
type Server struct {
	// Engine builds an engine for the project; it is called per request so config edits take effect.
	Engine  func() (*engine.Engine, error)
	Version string
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Serve reads requests from in and writes responses to out until in closes.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	var mu sync.Mutex
	var wg sync.WaitGroup
	send := func(r response) {
		r.JSONRPC = "2.0"
		data, err := json.Marshal(r)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		out.Write(append(data, '\n'))
	}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			send(response{ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			continue
		}
		if len(req.ID) == 0 { // a notification: nothing to answer
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			send(s.handle(req))
		}()
	}
	wg.Wait()
	return sc.Err()
}

func (s *Server) handle(req request) response {
	r := response{ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2024-11-05"
		}
		r.Result = map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "sarx", "version": s.Version},
			"instructions":    "Delegate work to other agents. Typical flow: sarx_run (one sentence) or sarx_new + sarx_dispatch, then sarx_status / sarx_wait, then sarx_show.",
		}
	case "ping":
		r.Result = map[string]any{}
	case "tools/list":
		r.Result = map[string]any{"tools": tools}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			r.Error = &rpcError{-32602, "invalid params"}
			return r
		}
		text, err := s.call(p.Name, p.Arguments)
		res := map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
		if err != nil {
			res["content"] = []any{map[string]any{"type": "text", "text": err.Error()}}
			res["isError"] = true
		}
		r.Result = res
	default:
		r.Error = &rpcError{-32601, "method not found: " + req.Method}
	}
	return r
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func schema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func flag(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
func num(desc string) map[string]any  { return map[string]any{"type": "number", "description": desc} }
func strs(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

var tools = []tool{
	{"sarx_agents", "Show which agent fills each role (operator, builder, reviewer, fallback) and which agents are configured.", schema(map[string]any{})},
	{"sarx_new", "Create a task brief (task.md) in the current run and return its path. Pass content to write the whole brief yourself, or fill the template fields.",
		schema(map[string]any{
			"slug": str("short name for the task, e.g. add-retry"), "title": str("task title"), "role": str("operator, builder or reviewer"),
			"goal": str("what to achieve and what it is for"), "subtasks": strs("checklist items, three to eight"),
			"checks": strs("shell commands whose exit code 0 proves the task is done"), "scope": strs("paths the task may change"),
			"content": str("the complete brief as markdown; overrides the other fields"),
		}, "slug")},
	{"sarx_run", "Create a brief from one sentence, route it to the right agent and start it in the background.",
		schema(map[string]any{"text": str("what to do"), "role": str("optional: operator, builder or reviewer"), "agent": str("optional: a specific agent"), "readonly": flag("do not let the worker change files")}, "text")},
	{"sarx_dispatch", "Start a task brief in the background. Use resume to continue a stopped task in its own session.",
		schema(map[string]any{
			"task": str("task id or path"), "agent": str("a specific agent"), "role": str("operator, builder or reviewer"), "readonly": flag("reviews and investigations"),
			"timeout": num("seconds the worker may run"), "lock": str("name of a shared resource such as an emulator; tasks with the same name queue"),
			"isolate": str("none or worktree"), "resume": flag("continue the previous attempt's session"),
		}, "task")},
	{"sarx_status", "List tasks that are running or queued, or all tasks with all=true.", schema(map[string]any{"all": flag("include finished tasks")})},
	{"sarx_wait", "Wait for a task to finish, for at most 'seconds' (default 30, maximum 50). Returns finished=false if it is still running.",
		schema(map[string]any{"task": str("task id or path"), "seconds": num("how long to wait")}, "task")},
	{"sarx_show", "Return a task's final report and state.", schema(map[string]any{"task": str("task id or path")}, "task")},
	{"sarx_stop", "Stop a running task.", schema(map[string]any{"task": str("task id or path")}, "task")},
	{"sarx_verify", "Run the brief's Done-when checks now.", schema(map[string]any{"task": str("task id or path")}, "task")},
	{"sarx_retry", "Write the next attempt of a failed task and start it. Escalates to the next role unless agent or role is given.",
		schema(map[string]any{"task": str("task id or path"), "agent": str("a specific agent"), "role": str("operator, builder or reviewer"), "why": str("what to tell the next worker")}, "task")},
	{"sarx_apply", "Merge a worktree task's changes into the project.", schema(map[string]any{"task": str("task id or path")}, "task")},
}

func (s *Server) call(name string, raw json.RawMessage) (string, error) {
	eng, err := s.Engine()
	if err != nil {
		return "", err
	}
	var a struct {
		Task     string   `json:"task"`
		Slug     string   `json:"slug"`
		Title    string   `json:"title"`
		Role     string   `json:"role"`
		Agent    string   `json:"agent"`
		Goal     string   `json:"goal"`
		Text     string   `json:"text"`
		Why      string   `json:"why"`
		Content  string   `json:"content"`
		Lock     string   `json:"lock"`
		Isolate  string   `json:"isolate"`
		Subtasks []string `json:"subtasks"`
		Checks   []string `json:"checks"`
		Scope    []string `json:"scope"`
		ReadOnly bool     `json:"readonly"`
		Resume   bool     `json:"resume"`
		All      bool     `json:"all"`
		Timeout  float64  `json:"timeout"`
		Seconds  float64  `json:"seconds"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", fmt.Errorf("invalid arguments: %v", err)
		}
	}
	task := func() (store.Task, error) { return eng.Task(a.Task) }

	switch name {
	case "sarx_agents":
		return jsonText(map[string]any{"roles": eng.Cfg.Roles, "agents": agentNames(eng), "auto_detected": eng.Cfg.AutoDetected})
	case "sarx_new":
		t, err := eng.Scaffold(engine.ScaffoldOptions{Slug: a.Slug, Title: a.Title, Role: a.Role, Goal: a.Goal,
			Subtasks: a.Subtasks, Checks: a.Checks, Scope: a.Scope, Template: a.Content == "" && a.Goal == "" && len(a.Subtasks) == 0, Content: a.Content})
		if err != nil {
			return "", err
		}
		return jsonText(map[string]any{"task": t.ID, "path": t.Brief()})
	case "sarx_run":
		t, role, err := eng.QuickTask(a.Text, a.Role)
		if err != nil {
			return "", err
		}
		st, err := eng.Dispatch(t, spec.Options{Agent: a.Agent, Role: a.Role, ReadOnly: a.ReadOnly}, true)
		if err != nil {
			return "", err
		}
		return view(t, st, role)
	case "sarx_dispatch":
		t, err := task()
		if err != nil {
			return "", err
		}
		st, err := eng.Dispatch(t, spec.Options{Agent: a.Agent, Role: a.Role, ReadOnly: a.ReadOnly, Timeout: int(a.Timeout),
			Lock: a.Lock, Isolation: a.Isolate, Resuming: a.Resume}, true)
		if err != nil {
			return "", err
		}
		return view(t, st, "")
	case "sarx_status":
		return jsonText(eng.List(a.All))
	case "sarx_wait":
		t, err := task()
		if err != nil {
			return "", err
		}
		secs := a.Seconds
		if secs <= 0 {
			secs = 30
		}
		if secs > 50 {
			secs = 50
		}
		st, finished, err := eng.Wait(t, time.Duration(secs*float64(time.Second)))
		if err != nil {
			return "", err
		}
		out := stateMap(t, st)
		out["finished"] = finished
		return jsonText(out)
	case "sarx_show":
		t, err := task()
		if err != nil {
			return "", err
		}
		st, err := store.ReadState(t)
		if err != nil {
			return "", err
		}
		out := stateMap(t, st)
		if data, err := os.ReadFile(t.ResultMD()); err == nil {
			if len(data) > 20000 {
				data = append(data[:20000], []byte("\n[truncated; read the file for the rest]")...)
			}
			out["report"] = string(data)
		}
		return jsonText(out)
	case "sarx_stop":
		t, err := task()
		if err != nil {
			return "", err
		}
		st, err := eng.Stop(t)
		if err != nil {
			return "", err
		}
		return jsonText(stateMap(t, st))
	case "sarx_verify":
		t, err := task()
		if err != nil {
			return "", err
		}
		v, err := eng.Verify(t)
		if err != nil {
			return "", err
		}
		return jsonText(v)
	case "sarx_retry":
		t, err := task()
		if err != nil {
			return "", err
		}
		st, nt, err := eng.Retry(t, a.Agent, a.Role, a.Why, spec.Options{}, true)
		if err != nil {
			return "", err
		}
		return view(nt, st, "")
	case "sarx_apply":
		t, err := task()
		if err != nil {
			return "", err
		}
		out, err := eng.Apply(t)
		if err != nil {
			return "", err
		}
		return jsonText(map[string]any{"merged": true, "git": out})
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

func agentNames(e *engine.Engine) []string {
	var out []string
	for n := range e.Cfg.Agents {
		out = append(out, n)
	}
	return out
}

func stateMap(t store.Task, st *spec.State) map[string]any {
	return map[string]any{
		"task": st.ID, "state": store.Effective(st), "agent": st.Agent, "role": st.Role, "summary": engine.Summary(t, st),
		"result": st.Result, "warnings": st.Warnings, "verified": st.Verified, "note": st.Note,
	}
}

func view(t store.Task, st *spec.State, role string) (string, error) {
	m := stateMap(t, st)
	if role != "" {
		m["routed_as"] = role
	}
	return jsonText(m)
}

func jsonText(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	return string(data), err
}
