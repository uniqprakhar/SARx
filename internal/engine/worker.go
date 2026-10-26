package engine

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"sarx/internal/spec"
	"sarx/internal/store"
)

// workerPrompt is the short prompt handed to the worker CLI. It carries no quotes or shell
// metacharacters, so it survives any launcher on any operating system.
func workerPrompt(t store.Task) string {
	return "You are a worker agent dispatched by sarx. Read the file " + t.Worker() +
		" and follow it exactly. Nobody can answer questions during this run."
}

// workerDoc renders worker.md for one attempt.
func workerDoc(t store.Task, readOnly, resumed bool, scope []string, subtasks int) string {
	var b strings.Builder
	b.WriteString("# sarx worker instructions\n\n")
	b.WriteString("You are a worker agent dispatched by an orchestrator. The orchestrator reads only your report, not this conversation. Nobody can answer questions during this run, so if something is ambiguous take the most reasonable reading, say which reading you took in the report, and keep going. Do not delegate to other agents and do not run `sarx dispatch`.\n\n")
	b.WriteString("## Files\n")
	fmt.Fprintf(&b, "- Brief (read it first and follow it exactly): %s\n", t.Brief())
	fmt.Fprintf(&b, "- Checklist: %s\n", t.Progress())
	fmt.Fprintf(&b, "- Journal: %s\n", t.Journal())
	fmt.Fprintf(&b, "- Report: %s\n\n", t.ResultMD())
	b.WriteString("## While you work\n")
	b.WriteString("Keep the run observable. After each subtask, change its line in the checklist from `- [ ]` to `- [x]` and append a short section to the journal (a `## heading`, what you did, what you verified, what comes next). Never delete anything already in the journal. Even inside one long subtask, append a journal section at least every few minutes so the orchestrator can see you are alive. If you have to stop early, change `state=RUNNING` to `state=BLOCKED` on the line under the heading of the checklist and say why in the journal.\n\n")
	if subtasks == 0 {
		b.WriteString("The brief has no subtask checklist. Keep the journal anyway.\n\n")
	}
	if resumed {
		b.WriteString("This is a resumed run of the same task. Read the journal and the checklist first to see what earlier attempts did, then continue from the first unfinished subtask. Do not redo finished subtasks.\n\n")
	}
	if len(scope) > 0 {
		b.WriteString("Only change files inside the brief's Scope. Everything else is off limits.\n\n")
	}
	if readOnly {
		fmt.Fprintf(&b, "This is a read-only task. Do not create, edit or delete any file under the project. The only exception is the two run files above, the checklist and the journal: you may edit those and nothing else. Give your full report as your final message, starting with the STATUS line.\n\n")
	}
	b.WriteString("## Report\n")
	if readOnly {
		b.WriteString("Your final message is the report, in exactly this shape.\n\n")
	} else {
		b.WriteString("When finished, write your report to the report file above in exactly this shape, then make your final message a single line: the status and that path.\n\n")
	}
	b.WriteString("The first line must be exactly one of:\n")
	b.WriteString("- `STATUS: DONE` - the work is finished and verified.\n")
	b.WriteString("- `STATUS: DONE_WITH_NOTES` - the work is finished but something needs a human decision; you MUST include a `## Resolution` section saying what.\n")
	b.WriteString("- `STATUS: BLOCKED` - you could not finish; include `## Resolution` saying what is needed to unblock.\n")
	b.WriteString("- `STATUS: FAILED` - the work is wrong or impossible as briefed.\n\n")
	b.WriteString("Then these sections:\n")
	b.WriteString("- `## Checks` - one line per Done-when command, each starting with PASS or FAIL, followed by the command and what it printed.\n")
	b.WriteString("- `## Resolution` - required for DONE_WITH_NOTES and BLOCKED.\n")
	b.WriteString("- `## Files` - the files you changed.\n")
	b.WriteString("- `## Notes` - anything left open.\n")
	return b.String()
}

// progressSeed builds the initial checklist file.
func progressSeed(id, agent string, subtasks []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s progress\n", id)
	fmt.Fprintf(&b, "agent=%s state=RUNNING subtasks=%d\n", agent, len(subtasks))
	if len(subtasks) == 0 {
		b.WriteString("No subtask checklist in the brief; see the journal for the running record.\n")
	}
	for _, s := range subtasks {
		fmt.Fprintf(&b, "- [ ] %s\n", s)
	}
	return b.String()
}

var (
	tickedRe  = regexp.MustCompile(`(?m)^- \[[xX]\]`)
	itemRe    = regexp.MustCompile(`(?m)^- \[[ xX]\]`)
	headingRe = regexp.MustCompile(`(?m)^## (.+)$`)
	blockedRe = regexp.MustCompile(`(?m)^(?:agent=\S+ )?state=BLOCKED`)
)

// Ticks reads the checklist and returns how many items are ticked and how many exist.
func Ticks(t store.Task) (done, total int) {
	data, err := os.ReadFile(t.Progress())
	if err != nil {
		return 0, 0
	}
	return len(tickedRe.FindAll(data, -1)), len(itemRe.FindAll(data, -1))
}

// WorkerBlocked reports whether the worker marked its own checklist BLOCKED.
func WorkerBlocked(t store.Task) bool {
	data, err := os.ReadFile(t.Progress())
	return err == nil && blockedRe.Match(data)
}

// LastStep returns the last journal heading, which says what the worker is on.
func LastStep(t store.Task) string {
	data, err := os.ReadFile(t.Journal())
	if err != nil {
		return ""
	}
	m := headingRe.FindAllSubmatch(data, -1)
	if len(m) == 0 {
		return ""
	}
	return strings.TrimSpace(string(m[len(m)-1][1]))
}

// Summary renders the one-line outcome of a task.
func Summary(t store.Task, st *spec.State) string {
	done, total := Ticks(t)
	where := fmt.Sprintf("%d/%d subtasks", done, total)
	if total == 0 {
		where = "no checklist"
	}
	extra := ""
	if st.Claimed.Pass+st.Claimed.Fail > 0 {
		extra += fmt.Sprintf(", worker claims: %d PASS %d FAIL", st.Claimed.Pass, st.Claimed.Fail)
	}
	if st.Verified != nil && st.Verified.Ran {
		extra += fmt.Sprintf(", verified: %d PASS %d FAIL", st.Verified.Pass, st.Verified.Fail)
	}
	state := store.Effective(st)
	line := fmt.Sprintf("sarx %s %s: %s (%ds, %s%s) result: %s", st.Agent, st.ID, state, st.Seconds, where, extra, st.Result)
	if st.Worktree != "" && spec.IsSuccess(state) {
		line += fmt.Sprintf("  changes are on branch %s; merge with: sarx apply %s", st.Branch, st.ID)
	}
	for _, w := range st.Warnings {
		line += "\n  warning: " + w
	}
	if st.Note != "" && !spec.IsSuccess(state) {
		line += "\n  note: " + st.Note
	}
	if h := Hint(st); h != "" {
		line += "\n  " + h
	}
	return line
}

// Hint says what to do next after a task that did not succeed.
func Hint(st *spec.State) string {
	switch store.Effective(st) {
	case spec.Quota:
		return fmt.Sprintf("the provider hit a usage limit. Wait for it to reset, then: sarx dispatch %s --resume", st.ID)
	case spec.Timeout:
		return fmt.Sprintf("hit the time limit. Check what it left, then split the task, or: sarx dispatch %s --resume --timeout <seconds>", st.ID)
	case spec.Blocked, spec.Failed, spec.Crashed, spec.Stalled, spec.NoReport, spec.Unmarked:
		return fmt.Sprintf("then: sarx retry %s   or: sarx dispatch %s --resume   (logs: %s)", st.ID, st.ID, st.Stderr)
	case spec.Cancelled:
		return fmt.Sprintf("continue with: sarx dispatch %s --resume", st.ID)
	}
	return ""
}
