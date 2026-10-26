package config

// Profile is a built-in adapter recipe. Users override any field in [agent.NAME] of their config.
//
// Verified means the flags were taken from a CLI the project has run or that this project's origin
// (the SAR scripts) already drove. Unverified profiles are best effort from public documentation:
// `sarx doctor --probe` tests them against the real CLI, and any flag can be overridden in config.
type Profile struct {
	Agent
	Verified bool
}

// profileOrder is the order used when auto-detecting installed CLIs.
var profileOrder = []string{"claude", "codex", "gemini", "command-code"}

var profiles = map[string]Profile{
	"claude": {
		Verified: true,
		Agent: Agent{
			Exec:          "claude",
			PromptFlag:    "-p",
			ModelFlag:     "--model",
			AutoApprove:   []string{"--dangerously-skip-permissions"},
			SessionNew:    []string{"--session-id", "{id}"},
			SessionResume: []string{"--resume", "{id}"},
			ReadonlyArgs:  []string{"--disallowedTools", "Edit,Write,NotebookEdit"},
			Notes:         "Claude Code headless mode.",
		},
	},
	"command-code": {
		Verified: true,
		Agent: Agent{
			Exec:          "command-code",
			Args:          []string{"--trust", "--skip-onboarding", "--no-auto-update", "--no-skills"},
			PromptFlag:    "-p",
			ModelFlag:     "-m",
			AutoApprove:   []string{"--yolo"},
			SessionNew:    []string{"-n", "sarx-{id}"},
			SessionResume: []string{"--resume", "sarx-{id}"},
			Notes:         "Command Code headless mode. Read-only is enforced by sarx's own check; set readonly_args = [\"--permission-mode\", \"plan\"] to also ask the CLI to enforce it.",
		},
	},
	"codex": {
		Verified: false,
		Agent: Agent{
			Exec:        "codex",
			Args:        []string{"exec"},
			ModelFlag:   "-m",
			AutoApprove: []string{"--full-auto"},
			Notes:       "OpenAI Codex CLI, non-interactive exec. No session reuse: a resume restarts from the journal.",
		},
	},
	"gemini": {
		Verified: false,
		Agent: Agent{
			Exec:        "gemini",
			PromptFlag:  "-p",
			ModelFlag:   "-m",
			AutoApprove: []string{"--yolo"},
			Notes:       "Gemini CLI headless mode. No session reuse: a resume restarts from the journal.",
		},
	},
}

// ProfileNames lists the built-in adapter profiles.
func ProfileNames() []string {
	out := make([]string, len(profileOrder))
	copy(out, profileOrder)
	return out
}

// LookupProfile returns a built-in profile.
func LookupProfile(name string) (Profile, bool) {
	p, ok := profiles[name]
	return p, ok
}
