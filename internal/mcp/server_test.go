package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"sarx/internal/engine"
)

func roundTrip(t *testing.T, s *Server, lines ...string) map[float64]map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := s.Serve(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	got := map[float64]map[string]any{}
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not JSON: %q", l)
		}
		id, _ := m["id"].(float64)
		got[id] = m
	}
	return got
}

func TestHandshakeAndToolList(t *testing.T) {
	s := &Server{Version: "9", Engine: func() (*engine.Engine, error) { return nil, errors.New("no project") }}
	got := roundTrip(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":4,"method":"nope"}`,
	)
	if len(got) != 4 {
		t.Fatalf("a notification must not be answered; got %d replies", len(got))
	}
	res := got[1]["result"].(map[string]any)
	if res["protocolVersion"] != "2025-03-26" || res["serverInfo"].(map[string]any)["version"] != "9" {
		t.Fatalf("%v", res)
	}
	names := map[string]bool{}
	for _, tl := range got[2]["result"].(map[string]any)["tools"].([]any) {
		m := tl.(map[string]any)
		names[m["name"].(string)] = true
		if m["inputSchema"] == nil {
			t.Errorf("%v has no schema", m["name"])
		}
	}
	for _, n := range []string{"sarx_run", "sarx_dispatch", "sarx_status", "sarx_wait", "sarx_show", "sarx_stop", "sarx_retry", "sarx_agents"} {
		if !names[n] {
			t.Errorf("missing tool %s", n)
		}
	}
	if got[4]["error"] == nil {
		t.Fatal("unknown method should be a JSON-RPC error")
	}
}

func TestToolErrorsAreReportedNotFatal(t *testing.T) {
	s := &Server{Engine: func() (*engine.Engine, error) { return nil, errors.New("no project here") }}
	got := roundTrip(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sarx_status","arguments":{}}}`,
		`not json`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
	)
	res := got[1]["result"].(map[string]any)
	if res["isError"] != true || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "no project") {
		t.Fatalf("%v", res)
	}
	if got[2]["result"] == nil {
		t.Fatal("server stopped answering after a bad line")
	}
}
