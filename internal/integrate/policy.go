// Package integrate wires sarx into the tools people already use: instruction files, MCP
// configuration, hooks, the setup command and the health check.
package integrate

import "strings"

// Markers delimit the part of a shared file that sarx owns, so re-running init updates it in place.
const (
	BeginMarker = "<!-- sarx:begin -->"
	EndMarker   = "<!-- sarx:end -->"
)

// Policy is the delegation policy written into each tool's instruction file.
// withMCP adds a line about the MCP tools for tools that load them.
func Policy(withMCP bool) string {
	var b strings.Builder
	b.WriteString(`## Delegating work with sarx

This project can hand work to other agents through the ` + "`sarx`" + ` command. Use it when the user asks to delegate, fan out, route or parallelize work, says "use sarx", or when ` + "`sarx mode status`" + ` reports that delegation mode is on. While delegation mode is on, do not do the work yourself and do not use built-in subagents: plan, brief, dispatch, read reports and verify.

How it works:

1. Plan tasks that each have one owner and one checkable outcome. Tasks may run in parallel only if they touch different files. Give tasks that share an emulator, device, database or build daemon the same ` + "`--lock <name>`" + `.
2. Create a brief with ` + "`sarx new <slug> --role <operator|builder|reviewer>`" + `, which prints the path of a task.md. Fill in Goal, Context, Scope, Subtasks (three to eight) and Done when (fenced commands whose exit code 0 proves the task is done).
3. Dispatch it in the background with ` + "`sarx dispatch <task> --detach`" + ` (add ` + "`--ro`" + ` for reviews and investigations). Dispatch independent tasks together. Use ` + "`sarx ps`" + ` to see progress and ` + "`sarx wait <task>`" + ` when you have nothing else to do.
4. Read the one-line summary and the result file it names. Keep reports in files; do not paste them into the conversation.
5. Verify before accepting. sarx runs each brief's Done-when checks itself (see "verified:" in the summary) and warns about files changed outside Scope. For work that needed judgement, dispatch a read-only review to the reviewer role.
6. If a task fails, use ` + "`sarx retry <task>`" + ` (it escalates to the next role) or ` + "`sarx dispatch <task> --resume`" + ` to continue the same session. A QUOTA result means wait for the limit to reset, then resume.

Roles: operator runs things and reports what happened (tests, builds, mechanical edits); builder does work that needs judgement (features, refactors, root-causing bugs); reviewer gives verdicts and is always read-only; fallback is a stand-in. ` + "`sarx agents`" + ` shows which agent fills each role. Ask an operator for facts (what ran, what the output was), never for opinions.

Rules: briefs go in files, not on the command line. Reports come back in files. Never run an agent CLI directly. The one exception is when the user asks for a built-in subagent by name. ` + "`sarx mode off`" + ` ends delegation mode when the user says to stop.
`)
	if withMCP {
		b.WriteString("\nIf the sarx MCP server is connected, the same operations are available as tools named `sarx_*` (for example `sarx_run`, `sarx_dispatch`, `sarx_status`, `sarx_wait`).\n")
	}
	return b.String()
}

// SkillDoc is the Claude Code skill that points at the same policy.
func SkillDoc() string {
	return "---\nname: sarx\ndescription: Delegate work to other agents through sarx. Use when the user says use sarx, delegate, fan out, route this to the agents, or runs /sarx.\n---\n\n# sarx\n\n" +
		Policy(true)
}

// CursorRule is the Cursor rule file header.
func CursorRule() string {
	return "---\ndescription: Delegate work to other agents through sarx\nalwaysApply: true\n---\n\n"
}
