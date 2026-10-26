# Using sarx from each tool

Run `sarx init` once per project. It detects which of these you use (or pass `--for claude,cursor,...`).
What it writes is the same short policy everywhere: *delegate with sarx; read the report; do not trust a worker's claim, run the checks.*

| Tool | What `sarx init` adds | How it delegates |
|---|---|---|
| Claude Code | `CLAUDE.md` block, `.claude/skills/sarx/SKILL.md`, `.mcp.json` entry | shell commands or MCP tools. `--enforce` also adds a hook that blocks built-in subagents while `sarx mode on`. |
| Cursor | `.cursor/rules/sarx.mdc`, `.cursor/mcp.json` | MCP tools or terminal commands |
| Codex CLI | `AGENTS.md` block | terminal commands |
| Gemini CLI | `GEMINI.md` block, `.gemini/settings.json` MCP entry | MCP tools or shell commands |
| Anything with `AGENTS.md` | `AGENTS.md` block | terminal commands |
| Scripts / CI | nothing needed | `sarx run`, `sarx wait`, exit codes, `--json` |

## From a script or CI

```sh
sarx run "update the changelog for v2" --json > result.json   # waits; exit code 0/1/3
```
or without waiting:
```sh
sarx run "regenerate the API docs" --detach
sarx wait <task-id> --timeout 600
```

## Over MCP

`sarx mcp` serves these tools on stdio: `sarx_agents`, `sarx_new`, `sarx_run`, `sarx_dispatch`, `sarx_status`,
`sarx_wait` (at most 50 s per call, call again), `sarx_show`, `sarx_stop`, `sarx_verify`, `sarx_retry`, `sarx_apply`.
For any MCP client without auto-wiring, add: `{"mcpServers": {"sarx": {"command": "sarx", "args": ["mcp"]}}}`.

## What has and has not been checked

The MCP server is tested with a JSON-RPC round trip and the policy blocks are idempotent. The exact settings-file
shapes that Cursor and Gemini CLI expect, and Claude Code's hook format, were written from their public documentation
and have not been tried inside those tools. If one does not pick sarx up, `sarx doctor` shows what was written and where;
the `AGENTS.md` block alone is enough for any tool that can run a terminal command.
