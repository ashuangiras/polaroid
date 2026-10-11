package main

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func fieldOf(t *testing.T, body, name string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	s, _ := m[name].(string)
	return s
}

func TestLifecycleCommands(t *testing.T) {
	server := newServer(t)
	created := cli(createJSON, nil, "-server", server, "create")
	mustSucceed(t, created)
	id := fieldOf(t, created.stdout, "id")
	recorded := cli(`{"procedure_id":"`+id+`","version":1,"repository":"github.com/o/a","commit":"0123456789abcdef0123456789abcdef01234567",`+
		`"environment":{"name":"ci","attributes":{}},"inputs":{},"outcome":"succeeded","evidence":{"exit":0}}`, nil, "-server", server, "record")
	mustSucceed(t, recorded)
	run := fieldOf(t, recorded.stdout, "id")

	if r := cli("", nil, "-server", server, "get-execution-invalidation", run); r.code != exitOK || !strings.Contains(r.stdout, `"evidence_status":"valid"`) {
		t.Fatalf("get-execution-invalidation: exit %d, %s", r.code, r.stdout)
	}
	body := `{"reason":"wrong version","actor":"copilot.cli"}`
	if r := cli(body, nil, "-server", server, "invalidate-execution", run); r.code != exitOK || !strings.Contains(r.stdout, `"evidence_status":"invalidated"`) {
		t.Fatalf("invalidate-execution: exit %d, %s", r.code, r.stdout)
	}
	if r := cli("", nil, "-server", server, "invalidate-execution", run, writeFile(t, body)); r.code != exitFailure || !strings.Contains(r.stderr, "execution_invalidated") {
		t.Fatalf("second invalidate-execution: exit %d, %s", r.code, r.stderr)
	}
	if r := cli("", nil, "-server", server, "executions", "evidence_status=invalidated"); r.code != exitOK || !strings.Contains(r.stdout, run) {
		t.Fatalf("executions evidence_status=invalidated: exit %d, %s", r.code, r.stdout)
	}

	if r := cli("", nil, "-server", server, "get-procedure-lifecycle", id); r.code != exitOK || !strings.Contains(r.stdout, `"status":"active","latest_event":0`) {
		t.Fatalf("get-procedure-lifecycle: exit %d, %s", r.code, r.stdout)
	}
	change := writeFile(t, `{"base_event":0,"reason":"superseded","actor":"copilot.cli"}`)
	if r := cli("", nil, "-server", server, "retire-procedure", id, change); r.code != exitOK || !strings.Contains(r.stdout, `"status":"retired","latest_event":1`) {
		t.Fatalf("retire-procedure: exit %d, %s", r.code, r.stdout)
	}
	if r := cli("", nil, "-server", server, "retire-procedure", id, change); r.code != exitFailure || !strings.Contains(r.stdout, `"latest_event":1`) {
		t.Fatalf("stale retire-procedure: exit %d, %s", r.code, r.stdout)
	}
	if r := cli("", nil, "-server", server, "list"); r.code != exitOK || strings.Contains(r.stdout, id) {
		t.Fatalf("list shows a retired procedure: %s", r.stdout)
	}
	if r := cli("", nil, "-server", server, "list", "lifecycle=retired"); r.code != exitOK || !strings.Contains(r.stdout, id) {
		t.Fatalf("list lifecycle=retired: %s", r.stdout)
	}
	if r := cli("", nil, "-server", server, "discover", "dependency", "lifecycle=all"); r.code != exitOK || !strings.Contains(r.stdout, `"lifecycle_status":"retired"`) {
		t.Fatalf("discover lifecycle=all: %s", r.stdout)
	}
	if r := cli(`{"base_event":1,"reason":"needed","actor":"copilot.cli"}`, nil, "-server", server, "reactivate-procedure", id); r.code != exitOK ||
		!strings.Contains(r.stdout, `"status":"active","latest_event":2`) {
		t.Fatalf("reactivate-procedure: exit %d, %s", r.code, r.stdout)
	}
	if r := cli("", nil, "-server", server, "retire-procedure"); r.code != exitUsage {
		t.Fatalf("retire-procedure without an ID: exit %d", r.code)
	}
}
