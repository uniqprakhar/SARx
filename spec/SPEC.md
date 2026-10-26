# sarx Open Specification, v1

sarx is an engine that lets any orchestrator (an AI coding tool, a script, a CI job or a person) hand work to any worker agent, headless and in the background. Everything that matters travels through plain files, so any tool that can read and write a file can take part. This document is the contract. The `sarx` binary is one implementation of it.

Words in **bold** are defined terms. MUST / SHOULD follow RFC 2119.

## 1. Concepts

| Term | Meaning |
|---|---|
| **Orchestrator** | Whatever plans the work and writes briefs. Claude Code, Cursor, Codex, a shell script, a person. |
| **Worker** | An agent that does one task. Any program that can run headless, read a file and write a file. |
| **Task** | One unit of work with one owner and one checkable outcome. A task is a directory containing `task.md`. |
| **Run** | A group of tasks created together (`.sarx/runs/<run>/`). |
| **Role** | What a task needs from a worker: `operator`, `builder`, `reviewer`, `fallback`. Users map roles to concrete agents. |
| **Agent** | A configured worker: an adapter plus a model plus options. |
| **Adapter** | The recipe for starting an agent's CLI (executable, flags, how the prompt and session are passed). |

### Roles

| Role | Send it | Do not send it |
|---|---|---|
| `operator` | Running things and reporting what happened (tests, builds, installs, captures). Fully specified edits. | Anything that asks for an opinion or judgement. |
| `builder` | Work that needs judgement: multi-file features, refactors, root-causing, text where someone must decide what matters. | Long checklists in one brief. |
| `reviewer` | Verdicts: reviews, design decisions, security-sensitive checks. Always read-only. | Large changes. |
| `fallback` | A stand-in when another agent is down, or a fully specified edit nobody else can take. | Anything the other roles can do. |

A user with one agent maps all four roles to it. That is a valid setup.

## 2. Directory layout

All state lives under `.sarx/` in the project root (the git top-level, or the directory where `sarx init` ran).

```
.sarx/
  mode                    optional flag file; contains "on" while delegation mode is on
  current-run             name of the run that new tasks join
  locks/<name>.lock       advisory resource locks (section 7)
  runs/<run>/<NN-slug>/   one directory per task
    task.md               the brief (written by the orchestrator)
    worker.md             instructions for the worker (written by sarx at dispatch)
    dispatch.json         the options the task was dispatched with
    progress.md           checklist the worker ticks as it goes
    journal.md            the worker's running journal
    result.md             the worker's final report
    result.json           the report, parsed (written by sarx)
    state.json            the state of the run (written only by sarx)
    events.jsonl          audit log, one JSON object per line (written only by sarx)
    stdout.txt            worker standard output
    stderr.txt            worker standard error
    stop.request          present when a stop was requested
    attempts/<N>/         earlier attempts archived by a resume
```

Rules:

- `state.json` and `events.jsonl` are written only by the engine. Workers and orchestrators MUST NOT write them.
- `progress.md`, `journal.md` and `result.md` are written by the worker. Orchestrators MUST NOT write them while the task is running.
- `state.json` MUST be replaced atomically (write a temporary file, then rename).
- `.sarx/` SHOULD be excluded from version control. `sarx init` adds it to `.git/info/exclude`.
- Paths inside every file written by the engine are absolute, so a worker running in another working directory can use them.
- Write files with one line per paragraph or list item. Do not hard-wrap prose.

## 3. The brief (`task.md`)

Markdown, with an optional front-matter block.

```markdown
---
role: builder
timeout: 1800
lock: emulator
---
# Add a retry to the login request

## Goal
What to achieve and what it is for.

## Context
Paths that matter, decisions already made, earlier `result.md` files worth reading.

## Scope
- `src/auth/`
- `tests/auth/**`

## Subtasks
- [ ] Read the existing request code
- [ ] Add the retry
- [ ] Write the report

## Done when
```bash
npm test -- auth
```

## Report
Anything specific to bring back beyond the standard report.
```

Front-matter keys (all optional): `role`, `agent`, `readonly` (`true`/`false`), `timeout` (seconds), `lock`, `isolate` (`none`/`worktree`). Command-line options override front matter.

Sections are matched by their `##` heading, case-insensitively.

- **Subtasks**: lines `- [ ] text`. Copied into `progress.md` at dispatch. Three to eight is right.
- **Done when**: fenced code blocks. Each block is one check. A check passes when the block exits 0. Blocks run in order, from the project root (or the task's worktree).
- **Scope**: bullet lines naming files the task may change. Backticked text is used when present, otherwise the whole bullet. `dir/` matches everything below it, `*` matches within a path segment, `**` matches across segments. If there is no Scope section, nothing is checked.
- **Title**: the first `# ` line.

## 4. The worker contract

A worker receives a short prompt that names `worker.md`. It MUST:

1. Read `worker.md`, then `task.md`, and follow them.
2. After each subtask, change that line in `progress.md` from `- [ ]` to `- [x]` and append a short section (`## heading`) to `journal.md`. Append at least every few minutes during long subtasks. Never delete earlier journal text.
3. If it must stop early, change `state=RUNNING` to `state=BLOCKED` in `progress.md` and say why in the journal.
4. Finish by writing `result.md` (read-only tasks put the same text in the final message instead).
5. Not delegate to other agents and not run `sarx dispatch`. The engine sets `SARX_WORKER=1`, and `sarx` refuses to dispatch when it is set.

### Report shape

The first line of `result.md` MUST be exactly one of:

```
STATUS: DONE
STATUS: DONE_WITH_NOTES
STATUS: BLOCKED
STATUS: FAILED
```

followed by these sections:

- `## Checks`: one line per Done-when block, starting with `PASS` or `FAIL`, then the command and what it printed.
- `## Resolution`: required for `DONE_WITH_NOTES` and `BLOCKED`; says what a human must decide or provide.
- `## Files`: the files changed.
- `## Notes`: anything left open.

A `DONE_WITH_NOTES` report with no `## Resolution` section is treated as `UNMARKED`.

A worker that cannot write files still conforms if its final stdout message is a valid report: the engine copies stdout to `result.md` when the worker wrote none.

## 5. Task states

| State | Meaning |
|---|---|
| `QUEUED` | Accepted, waiting for a lock or for a free slot on its agent. |
| `RUNNING` | The worker is running. |
| `DONE` | The worker reported `STATUS: DONE`. |
| `DONE_WITH_NOTES` | Finished, but a human decision is needed; see `## Resolution`. |
| `BLOCKED` / `FAILED` | The worker reported it could not finish. |
| `QUOTA` | The provider refused with a usage or rate limit. The agent is not broken; wait and resume. |
| `CRASHED` | The worker exited non-zero for any other reason. |
| `TIMEOUT` | The time limit was reached and the worker was killed. |
| `NO_REPORT` | Exited cleanly and wrote nothing. |
| `UNMARKED` | A report exists but its first lines carry no valid `STATUS:`. |
| `CANCELLED` | Stopped on request or by an interrupt. |
| `STALLED` | Derived, never stored: `state.json` says `RUNNING` or `QUEUED` but the engine process is gone. |

Terminal states: everything except `QUEUED`, `RUNNING` and `STALLED`.

### Escalation

| Outcome | Next |
|---|---|
| `QUOTA`, `TIMEOUT` | Same agent. Wait, then resume or split the task. |
| `CRASHED` | The stand-in: `operator`→`builder`→`fallback`→`reviewer`. |
| `BLOCKED`, `FAILED` | One level up: `operator`→`builder`→`reviewer`; `fallback`→`builder`. |
| `reviewer` fails | Stop and bring the report to the human. |

When the target role maps to the same agent (a single-agent setup), the retry goes to that agent again in a fresh session. Escalate once per level.

## 6. Engine behaviour

### Dispatch

1. Resolve the agent: `--agent`, else `--role` or front matter, else routing by keywords in the brief (review, audit, judge → `reviewer`; run, test, build, install, lint, capture, reproduce → `operator`; otherwise `builder`).
2. Refuse (before creating anything) when the brief names an image file and the agent is configured `images = false`.
3. Claim the task atomically by creating `state.json` exclusively. A task directory is dispatched once. A second dispatch is refused; use resume or retry.
4. Seed `progress.md` from `## Subtasks`, write `worker.md` and `dispatch.json`.
5. Wait for resource locks and the agent's `max_parallel` slot (state `QUEUED`).
6. Start the worker in its own process group with the working directory set to the project root (or its worktree), `SARX_WORKER=1` in its environment, standard input closed, output redirected to `stdout.txt` and `stderr.txt`.
7. Supervise until it exits, times out, is stopped, or the supervisor is interrupted. Stopping kills the worker's whole process tree.
8. Classify the outcome (section 5), write `result.json`, run checks (section 6.2), compare file changes to Scope or read-only (section 6.3), write `state.json`, release locks.

A dispatch may be foreground (the call returns when the task ends) or detached (the call returns once the task is accepted; use `wait`).

### 6.1 Resume

Resuming reuses the worker's session when the adapter supports it, keeps `journal.md` and the ticks in `progress.md`, and archives the previous attempt under `attempts/<N>/`. Adapters without session support restart in a fresh session with the journal and ticks noted in the prompt.

### 6.2 Verification

`## Done when` checks are run by the engine after the worker finishes (auto-verify, on by default) and on demand. The results in `state.json.verified` are the engine's own; the worker's `PASS`/`FAIL` lines are recorded separately in `claimed_checks` and are claims only.

### 6.3 Scope and read-only checks

In a git repository the engine fingerprints the working tree (modified and untracked files, by content hash) before and after the run, ignoring `.sarx/`. Files that changed outside `## Scope`, or any file at all in a read-only run, are recorded in `state.json.warnings`. Outside git, these checks are skipped and `warnings` says so.

### 6.4 Isolation

`none` (default): the worker edits the project directly. `worktree`: the worker runs in `.sarx/worktrees/<id>` on branch `sarx/<id>`; `apply` merges it, `discard` removes it. A worktree has none of the project's untracked dependencies (for example `node_modules`), so tasks that run tests may need an install step.

## 7. Locks

A lock is the file `.sarx/locks/<name>.lock` created exclusively and containing the owner's process id. Names use letters, digits, dot, dash and underscore. A second claimant waits. A lock whose owner process is gone is reclaimed. A lock file that exists but is empty is given about two seconds before it is suspected. Locks are advisory and best effort.

Use a lock for anything the file scope cannot model: an emulator, a device, a database, a build daemon.

## 8. `state.json`

See `state.schema.json`. Key fields: `state`, `agent`, `role`, `mode`, `attempt`, `session`, `started`, `finished`, `seconds`, `subtasks`, `claimed_checks`, `verified`, `warnings`, `isolation`, `worktree`, `supervisor_pid`, `pid`.

## 9. `events.jsonl`

One JSON object per line: `{"ts": <unix seconds>, "type": "...", "msg": "..."}`. See `event.schema.json`. Types: `dispatched`, `queued`, `started`, `exited`, `state`, `verify`, `warning`, `stop`, `resume`, `retry`.

## 10. `result.json`

See `result.schema.json`: `status`, `resolution`, `claimed_checks`, `files`, `notes`, `source` (`file` or `stdout`).

## 11. Integrations

An orchestrator drives the engine through any of:

1. **CLI**: every command accepts `--json`.
2. **MCP**: `sarx mcp` serves the same operations as typed tools over stdio.
3. **Instruction files**: `sarx init --for <tool>` writes the delegation policy into `AGENTS.md`, `CLAUDE.md`, `GEMINI.md` or `.cursor/rules/`, between `<!-- sarx:begin -->` and `<!-- sarx:end -->` markers so re-running updates it in place.

Enforcement of "the orchestrator does not do the work itself" is only possible where the orchestrator has hooks. Elsewhere it is a written policy. What the engine enforces for every orchestrator is what it controls: scope, read-only, verification, locks, isolation.

## 12. Adapters

An adapter is configuration, not code. Fields are listed in `docs/adapters.md`. A conforming adapter, run against the reference fake worker (`cmd/sarx-fakeagent`), MUST produce a `DONE` task with every subtask ticked.

## 13. Versioning

`state.json` carries `"version": 1`. Additive changes keep the version. Readers MUST ignore unknown fields.
