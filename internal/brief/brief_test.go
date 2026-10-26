package brief

import "testing"

const sample = "---\nrole: builder\n---\n# Add retry\n\n## Goal\nMake login retry.\n\n## Subtasks\n- [ ] one\n- [x] two\n\n## Scope\n- `src/**`\n- docs/*.md\n\n## Done when\n```\ngo test ./...\n```\nprose\n```bash\nnpm run lint\n```\n"

func TestParse(t *testing.T) {
	b := Parse(sample)
	if b.Title != "Add retry" || b.Front["role"] != "builder" {
		t.Fatalf("title/front: %+v", b)
	}
	if len(b.Subtasks) != 2 || b.Subtasks[1] != "two" {
		t.Fatalf("subtasks %v", b.Subtasks)
	}
	if len(b.Checks) != 2 || b.Checks[0] != "go test ./..." {
		t.Fatalf("checks %q", b.Checks)
	}
	if len(b.Scope) != 2 || b.Scope[0] != "src/**" {
		t.Fatalf("scope %v", b.Scope)
	}
}

func TestCRLF(t *testing.T) {
	b := Parse("# T\r\n\r\n## Subtasks\r\n- [ ] a\r\n\r\n## Done when\r\n```\r\necho hi\r\n```\r\n")
	if len(b.Subtasks) != 1 || len(b.Checks) != 1 || b.Checks[0] != "echo hi" {
		t.Fatalf("%+v", b)
	}
}

func TestScopeMatcher(t *testing.T) {
	m := Parse(sample).ScopeMatcher()
	for p, want := range map[string]bool{
		"src/a/b.go": true, "src/x.go": true, "docs/readme.md": true,
		"docs/sub/readme.md": false, "other.txt": false,
	} {
		if m(p) != want {
			t.Errorf("%s: want %v", p, want)
		}
	}
	empty := Parse("# T\n").ScopeMatcher()
	if empty != nil && !empty("anything") {
		t.Error("no scope should allow everything")
	}
}

func TestImages(t *testing.T) {
	got := Images("see `shots/a.PNG` and b.jpeg but not c.go")
	if len(got) != 2 {
		t.Fatalf("%v", got)
	}
}
