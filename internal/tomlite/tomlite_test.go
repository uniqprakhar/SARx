package tomlite

import (
	"strings"
	"testing"
)

func TestParseBasics(t *testing.T) {
	m, err := Parse(`
# comment
top = "x"   # trailing
[defaults]
timeout = 900
auto_verify = true
[agent.claude]
exec = "claude"
args = ["-p", "--flag"]
multi = [
  "a",
  "b", # note
]
path = 'C:\Users\me'
`)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := String(m, "top"); v != "x" {
		t.Fatalf("top=%q", v)
	}
	d, _ := Table(m, "defaults")
	if n, _ := Int(d, "timeout"); n != 900 {
		t.Fatalf("timeout=%d", n)
	}
	if b, _ := Bool(d, "auto_verify"); !b {
		t.Fatal("auto_verify")
	}
	ag, _ := Table(m, "agent")
	cl, ok := Table(ag, "claude")
	if !ok {
		t.Fatalf("agent.claude missing: %v", m)
	}
	if a, _ := Strings(cl, "args"); strings.Join(a, ",") != "-p,--flag" {
		t.Fatalf("args=%v", a)
	}
	if a, _ := Strings(cl, "multi"); strings.Join(a, ",") != "a,b" {
		t.Fatalf("multi=%v", a)
	}
	if v, _ := String(cl, "path"); v != `C:\Users\me` {
		t.Fatalf("literal string: %q", v)
	}
}

func TestParseErrorsHaveLineNumbers(t *testing.T) {
	for name, src := range map[string]string{
		"unterminated": "a = \"x\n",
		"no equals":    "a\n",
		"inline table": "a = {b = 1}\n",
		"array table":  "[[x]]\n",
		"bad table":    "[x\n",
	} {
		_, err := Parse(src)
		if err == nil {
			t.Errorf("%s: expected an error", name)
			continue
		}
		if !strings.Contains(err.Error(), "line") {
			t.Errorf("%s: error has no line number: %v", name, err)
		}
	}
}

func TestEscapes(t *testing.T) {
	m, err := Parse(`a = "tab\there \"q\" \\"`)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := String(m, "a"); v != "tab\there \"q\" \\" {
		t.Fatalf("got %q", v)
	}
}
