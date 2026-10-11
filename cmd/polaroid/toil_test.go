package main

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestToilItemCommands(t *testing.T) {
	server := newServer(t)
	created := cli("", nil, "-server", server, "create", writeFile(t, createJSON))
	mustSucceed(t, created)
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(created.stdout), &p); err != nil {
		t.Fatal(err)
	}
	content := `"activity":"Add a dependency by hand","applicability":{"shared":{}},"sources":[{"procedure_id":"` + p.ID + `","version":1,"steps":["one"]}],` +
		`"assessment":{"repetition":{"status":"observed"},"burden":{"status":"hypothesis"}},"status_rationale":"Seen twice.","revision_reason":"r","author":"copilot.cli"`
	made := cli("", nil, "-server", server, "create-toil-item", writeFile(t, `{"canonical_key":"toil.dependency","revision":{"status":"observed",`+content+`}}`))
	mustSucceed(t, made)
	var item struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(made.stdout), &item); err != nil || item.ID == "" {
		t.Fatalf("create-toil-item output: %s (%v)", made.stdout, err)
	}
	revised := cli(`{"base_revision":1,"revision":{"status":"candidate","proposal":{"inputs":1,"outputs":2,"remaining_judgment":3,`+
		`"failure_and_recovery":4,"acceptance_criteria":["c"]},`+content+`}}`, nil, "-server", server, "revise-toil-item", item.ID)
	mustSucceed(t, revised)
	if !strings.Contains(revised.stdout, `"revision":2`) || !strings.Contains(revised.stdout, `"status":"candidate"`) {
		t.Fatalf("revise-toil-item output = %s", revised.stdout)
	}
	byID := cli("", nil, "-server", server, "get-toil-item", item.ID)
	mustSucceed(t, byID)
	byKey := cli("", nil, "-server", server, "get-toil-item-by-key", "toil.dependency")
	mustSucceed(t, byKey)
	if byID.stdout != byKey.stdout || !strings.Contains(byID.stdout, `"latest_revision":2`) {
		t.Fatalf("get-toil-item = %s, by key %s", byID.stdout, byKey.stdout)
	}
	first := cli("", nil, "-server", server, "get-toil-revision", item.ID, "1")
	mustSucceed(t, first)
	if !strings.Contains(first.stdout, `"revision":1,`) || !strings.Contains(first.stdout, `"status":"observed"`) {
		t.Fatalf("get-toil-revision = %s", first.stdout)
	}
	listed := cli("", nil, "-server", server, "toil-items", "status=candidate", "procedure_id="+p.ID, "limit=1")
	mustSucceed(t, listed)
	if !strings.Contains(listed.stdout, `"canonical_key":"toil.dependency"`) || strings.Contains(listed.stdout, `"revisions"`) {
		t.Fatalf("toil-items = %s", listed.stdout)
	}
	if r := cli("", nil, "-server", server, "toil-items", "status=verified"); r.code != exitFailure || !strings.Contains(r.stdout, `"field":"status"`) {
		t.Fatalf("an unknown status: exit %d, stdout %s", r.code, r.stdout)
	}
	stale := cli(`{"base_revision":1,"revision":{"status":"observed",`+content+`}}`, nil, "-server", server, "revise-toil-item", item.ID)
	if stale.code != exitFailure || !strings.Contains(stale.stdout, `"code":"revision_conflict"`) || !strings.Contains(stale.stderr, "revision_conflict") {
		t.Fatalf("a stale base: exit %d, stdout %s, stderr %s", stale.code, stale.stdout, stale.stderr)
	}
	if r := cli("", nil, "-server", server, "get-toil-revision", item.ID); r.code != exitUsage {
		t.Fatalf("get-toil-revision without N: exit %d", r.code)
	}
}
