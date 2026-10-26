package adapter

import (
	"reflect"
	"testing"

	"sarx/internal/config"
)

func TestBuildOrderAndSession(t *testing.T) {
	a := config.Agent{
		Exec: "x", Args: []string{"-p"}, AutoApprove: []string{"--yes"}, ReadonlyArgs: []string{"--ro"},
		Model: "m1", ModelFlag: "--model", SessionNew: []string{"--sid", "{id}"}, SessionResume: []string{"--resume", "{id}"},
		Env: map[string]string{"K": "V"},
	}
	c := Build(Request{Agent: a, Prompt: "do it", Session: "S1", ReadOnly: true, Extra: []string{"--x"}})
	want := []string{"-p", "--yes", "--ro", "--model", "m1", "--sid", "S1", "--x", "do it"}
	if !reflect.DeepEqual(c.Args, want) {
		t.Fatalf("got %v", c.Args)
	}
	if len(c.Env) != 1 || c.Env[0] != "K=V" {
		t.Fatalf("env %v", c.Env)
	}
	c = Build(Request{Agent: a, Prompt: "p", Session: "S1", Resume: true})
	if !reflect.DeepEqual(c.Args, []string{"-p", "--yes", "--model", "m1", "--resume", "S1", "p"}) {
		t.Fatalf("resume: %v", c.Args)
	}
}

func TestPromptVariants(t *testing.T) {
	c := Build(Request{Agent: config.Agent{Exec: "x", PromptVia: "stdin"}, Prompt: "hello"})
	if c.Stdin != "hello" || len(c.Args) != 0 {
		t.Fatalf("stdin: %+v", c)
	}
	c = Build(Request{Agent: config.Agent{Exec: "x", PromptFlag: "-n"}, Prompt: "hello"})
	if !reflect.DeepEqual(c.Args, []string{"-n", "hello"}) {
		t.Fatalf("flag: %v", c.Args)
	}
}

func TestPromptNeverSplitOrQuoted(t *testing.T) {
	p := `has "quotes", $VARS and spaces; & more`
	c := Build(Request{Agent: config.Agent{Exec: "x"}, Prompt: p})
	if len(c.Args) != 1 || c.Args[0] != p {
		t.Fatalf("%v", c.Args)
	}
}
