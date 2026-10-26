# Coming from the SAR scripts

`sar.sh` and the `/sar` skill keep working; sarx is a separate program with its own `.sarx/` folder, so you can try it
side by side. What maps to what:

| SAR | sarx |
|---|---|
| `sar.sh dispatch task.md` | `sarx dispatch <task>` (or `sarx run "one sentence"`) |
| `sar.sh ps` / `status` | `sarx ps [--all]` |
| `sar.sh stop` | `sarx stop` |
| `sar.sh verify` | `sarx verify` |
| `sar.sh retry` | `sarx retry` (escalates by role) |
| `sar.sh agents` | `sarx agents` |
| `--ro`, `--timeout`, `--lock`, `--resume`, `--allow-images` | same flag names |
| `.sar/<run>/NN-slug.{task,progress,report,result}.md` | `.sarx/runs/<run>/NN-slug/{task.md,progress.md,result.md,result.json,state.json}` |
| `models.env`, `laude` aliases | `[agent.NAME]` in the config file; no shell aliases needed |
| zsh and bash required | none: a single binary, also on Windows |

Differences to know about:

- Briefs use the same sections (Goal, Subtasks, Scope, Done when). A fenced block under **Done when** is now **run by sarx**, not just asked of the worker.
- Worker `PASS`/`FAIL` lines are kept as *claims*; sarx's own check results are reported separately.
- Scope and read-only violations are warnings (exit code 3), not failures.
- Worktree isolation is opt-in (`--isolate worktree`): a fresh worktree lacks `node_modules` and similar, so it is not the default.
- The binary is called `sarx` because `sar` already exists on Linux (the sysstat tool).
