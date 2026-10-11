package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestDiscoveryCommands(t *testing.T) {
	server := newServer(t)
	mustSucceed(t, cli(createJSON, nil, "-server", server, "create"))

	found := cli("", nil, "-server", server, "discover", "Adding a dependency", "repository=github.com/o/a", "limit=1")
	mustSucceed(t, found)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		server+"/v1/procedures/discovery?"+url.Values{"task": {"Adding a dependency"}, "repository": {"github.com/o/a"}, "limit": {"1"}}.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	direct, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if found.stdout != string(direct) || !strings.Contains(found.stdout, `"canonical_key":"go.dependency.add"`) {
		t.Fatalf("discover = %s, GET = %s", found.stdout, direct)
	}

	dup := cli(`{"canonical_key":"go.dependency.add","method":"Justify and pin.","philosophy":"p"}`, nil, "-server", server, "duplicates")
	mustSucceed(t, dup)
	if !strings.Contains(dup.stdout, `"key_collision":{"procedure_id":`) {
		t.Fatalf("duplicates = %s", dup.stdout)
	}

	if r := cli("", nil, "-server", server, "discover", "the and"); r.code != exitFailure || !strings.Contains(r.stdout, `"field":"task"`) {
		t.Fatalf("a task without searchable words: exit %d, %s", r.code, r.stdout)
	}
	if r := cli("", nil, "-server", server, "discover"); r.code != exitUsage {
		t.Fatalf("discover without a task: exit %d", r.code)
	}
}
