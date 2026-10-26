# sarx

Send work from one AI coding agent to others, in the background, and keep track of it.
One small program (no runtime, no dependencies) for Linux, macOS and Windows.

Any tool can be the one that **delegates** (Claude Code, Cursor, Codex, Gemini CLI, a shell script, CI) and any
agent CLI can be the one that **does the work** (Claude Code, Command Code, Codex, Gemini CLI, or your own).
They talk through plain files described in [spec/SPEC.md](spec/SPEC.md), so nothing is tied to one vendor.

## Install

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/uniqprakhar/sarx/main/scripts/install.sh | sh
```
```powershell
# Windows (PowerShell)
irm https://raw.githubusercontent.com/uniqprakhar/sarx/main/scripts/install.ps1 | iex
```
Or download the archive for your system from the releases page, or build it: `go build ./cmd/sarx`.
(`uniqprakhar/sarx` is a placeholder until the project is published.)

## Use it in two commands

```sh
cd your-project
sarx init                       # finds your agent CLIs, writes a config, wires your tools
sarx run "add a retry to the login request"
```

`sarx init` needs no answers (`--yes`). It looks for installed agent CLIs, maps the four roles
(operator, builder, reviewer, fallback) to them, and adds a short instruction block to `CLAUDE.md`,
`AGENTS.md`, `GEMINI.md` or `.cursor/rules` plus an MCP entry, so the tool you chat with knows how to delegate.
Re-running it changes nothing that is already set up, and never touches text outside its own marked block.

With only one agent installed, every role uses it. That is fine.

## Everyday commands

| Command | What it does |
|---|---|
| `sarx run "text"` | Create a task from one sentence, route it, run it, print the result. Add `--detach` to return at once. |
| `sarx ps [--all]` | What is running (or everything). |
| `sarx wait <task>` | Block until a task finishes. Exit code tells you how it went. |
| `sarx show <task>` | Read the worker's report (`--logs` for raw output). |
| `sarx stop <task>` | Stop it; `sarx dispatch <task> --resume` continues it. |
| `sarx retry <task>` | Try again on the next role up, with the failure explained to the next worker. |
| `sarx doctor [--probe]` | Check the setup; `--probe` sends each agent a tiny real task. |

For tools that write their own briefs: `sarx new <slug>` prints the path of a template; fill it in and
`sarx dispatch <slug> --detach`. Every command takes `--json`.

**Exit codes:** 0 done, 1 failed, 2 could not start (usage or setup), 3 done with warnings.

## What it checks for you

- Runs the brief's **Done when** commands itself and records the result next to the worker's own claims.
- In a git repository, warns if a worker changed files outside the brief's **Scope**, or changed anything in a read-only task.
- Detects usage-limit refusals (QUOTA), crashes, timeouts, and workers that stop without a report, and says what to do next.
- `--lock name` makes tasks that share a resource (an emulator, a database) queue instead of colliding.
- `--isolate worktree` runs a task on its own git branch; `sarx apply` merges it, `sarx discard` drops it.

## Docs

- [Using it from each tool](docs/works-with.md) — Claude Code, Cursor, Codex, Gemini CLI, scripts and CI
- [Adding or changing an agent](docs/adapters.md) — a few lines of TOML, no code
- [The file format](spec/SPEC.md) — for building another orchestrator or worker
- [Coming from the SAR scripts](docs/migrating-from-sar.md)

## Status

Version 0.1. The core is covered by automated tests that run against a fake agent on Linux. The Windows code
paths are compiled in CI but have not been run by the author; the CI matrix runs the same tests on Windows and macOS.
The Claude and Command Code profiles use the flags the original SAR scripts already used; the Codex and Gemini
profiles are best-effort and marked unverified until `sarx doctor --probe` has been run against the real CLIs.

No license file is included yet; add one before publishing.
