package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T, user, project string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir) // no agent CLIs installed unless a test adds one
	t.Setenv("SARX_CONFIG", filepath.Join(dir, "user.toml"))
	if user != "" {
		os.WriteFile(filepath.Join(dir, "user.toml"), []byte(user), 0o644)
	}
	root := filepath.Join(dir, "proj")
	os.MkdirAll(root, 0o755)
	if project != "" {
		os.WriteFile(filepath.Join(root, "sarx.toml"), []byte(project), 0o644)
	}
	return root
}

func TestNoConfigNoAgents(t *testing.T) {
	root := setup(t, "", "")
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Sources) != 0 || c.AutoDetected {
		t.Fatalf("%+v", c)
	}
	if _, err := c.ForRole("builder"); err == nil {
		t.Fatal("expected a helpful error with nothing configured")
	}
}

func TestAutoDetect(t *testing.T) {
	root := setup(t, "", "")
	bin := os.Getenv("PATH")
	name := "codex"
	if os.PathSeparator == '\\' {
		name += ".exe"
	}
	os.WriteFile(filepath.Join(bin, name), []byte("x"), 0o755)
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !c.AutoDetected {
		t.Fatal("not auto detected")
	}
	for _, r := range []string{"operator", "builder", "reviewer", "fallback"} {
		a, err := c.ForRole(r)
		if err != nil || a.Name != "codex" {
			t.Fatalf("%s: %v %+v", r, err, a)
		}
	}
}

func TestOverridesAndProjectWins(t *testing.T) {
	root := setup(t, `
[roles]
builder = "claude"
operator = "claude"
reviewer = "claude"
fallback = "claude"
[agent.claude]
model = "user-model"
timeout = 100
`, `
[agent.claude]
model = "project-model"
`)
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.ForRole("builder")
	if err != nil {
		t.Fatal(err)
	}
	if a.Model != "project-model" || a.Exec != "claude" || a.Timeout != 100 {
		t.Fatalf("%+v", a)
	}
	if a.Name != "claude" || len(a.SessionNew) == 0 {
		t.Fatalf("profile defaults lost: %+v", a)
	}
}

func TestCustomAgent(t *testing.T) {
	root := setup(t, `
[roles]
builder = "mytool"
[agent.mytool]
exec = "mytool"
prompt_via = "stdin"
args = ["--go"]
`, "")
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.ForRole("builder")
	if err != nil || a.PromptVia != "stdin" || a.Exec != "mytool" {
		t.Fatalf("%v %+v", err, a)
	}
	// Roles that were not mapped fall back to a mapped one instead of failing.
	if r, err := c.ForRole("reviewer"); err != nil || r.Name != "mytool" {
		t.Fatalf("partial roles: %v %+v", err, r)
	}
}

func TestUnknownKeyWarnsAndBadFileErrors(t *testing.T) {
	root := setup(t, "[roles]\nbuilder = \"claude\"\n[agent.claude]\nmodle = \"x\"\n", "")
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range c.Warnings {
		if strings.Contains(w, "modle") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no warning for the misspelt key: %v", c.Warnings)
	}
	root = setup(t, "[roles\n", "")
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "user.toml") {
		t.Fatalf("expected the error to name the file: %v", err)
	}
}

func TestRenderRoundTrips(t *testing.T) {
	roles := map[string]string{"operator": "claude", "builder": "claude", "reviewer": "codex", "fallback": "codex"}
	root := setup(t, Render(roles, nil), "")
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	for r, want := range roles {
		if c.Roles[r] != want {
			t.Errorf("%s=%s want %s", r, c.Roles[r], want)
		}
	}
}
