# Adding or changing an agent

An agent is a few lines of TOML. There is no code to write. sarx reads, in order:

1. built-in profiles (`claude`, `command-code`, `codex`, `gemini`)
2. your config: `sarx doctor` prints its path (`~/.config/sarx/config.toml` on Linux, `~/Library/Application Support/sarx/` on macOS, `%AppData%\sarx\` on Windows); override with `SARX_CONFIG`
3. the project file `sarx.toml` next to `.sarx/` (shared with your team; later files win)

## Roles

```toml
[roles]
operator = "claude"        # small, mechanical work
builder  = "claude"        # real implementation
reviewer = "codex"         # read-only review and investigation
fallback = "command-code"  # takes over when another agent crashes
```
A role you leave out uses one of the others, so a one-agent setup needs only `builder`.

## Change a built-in agent

```toml
[agent.claude]
model = "your-model-id"    # passed with the profile's model flag
timeout = 3600             # seconds for this agent
max_parallel = 2           # at most 2 of its tasks at once; others wait
```

## Add any other CLI

```toml
[agent.mytool]
exec = "mytool"                     # the command (must be on PATH)
args = ["run", "--headless"]        # always passed first
prompt_flag = "-p"                  # flag before the prompt; leave out to pass it as the last argument
prompt_via = "arg"                  # or "stdin" to pipe the prompt in
auto_approve = ["--yes"]            # flags that let it edit files without asking
model_flag = "--model"
model = "fast-1"
session_new = ["--session", "{id}"]         # how to start a named session ({id} is filled in)
session_resume = ["--resume", "{id}"]       # how to continue it; leave out if unsupported
readonly_args = ["--read-only"]             # asked of the CLI for read-only tasks (sarx also checks)
quota_regex = "(?i)usage limit|429"         # what a usage-limit refusal looks like in its output
images = false                              # true/false: whether it can take screenshots named in a brief

[agent.mytool.env]                  # optional extra environment variables
KEY = "value"
```

Then map a role to it (`reviewer = "mytool"`) and run `sarx doctor --probe`.

Arguments are passed as a list, never through a shell, so quoting and spaces behave identically on every OS.
The worker is given a short prompt that points at a `worker.md` file; no brief text is ever put on the command line.

## Supported TOML

sarx reads a small subset: `[tables]`, dotted tables like `[agent.name]`, strings (`"..."` and `'...'`),
integers, booleans and arrays of strings (which may span lines). Inline tables (`{ }`) and `[[arrays of tables]]`
are rejected with a line-numbered message; use a `[table]` instead. Misspelt keys produce a warning, not silence.

## Verified or not

`claude` and `command-code` use the flags the original SAR scripts drove. `codex` and `gemini` are written from public
documentation and have not been run by the author. If a flag is wrong for your version, override it in `[agent.NAME]`
and tell us. `sarx doctor --probe` is the test: it sends the agent a tiny task and shows what happened.
