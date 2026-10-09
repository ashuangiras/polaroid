package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	httptransport "github.com/ashuangiras/polaroid/internal/transport/http"
)

const createJSON = `{"canonical_key":"go.dependency.add","version":{"philosophy":"p","method":"m",` +
	`"contract":{},"instructions":{"steps":["one"]},"revision_reason":"Initial version."}}`

const reviseJSON = `{"base_version":1,"version":{"philosophy":"p","method":"m",` +
	`"contract":{},"instructions":{"steps":["corrected"]},"revision_reason":"Corrected step one."}}`

// newServer runs the real API over a real SQLite file and returns its URL.
func newServer(t *testing.T) string {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "polaroid.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := httptest.NewServer(httptransport.NewHandler(memory.NewService(store), slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	return srv.URL
}

type result struct {
	code           int
	stdout, stderr string
}

func cli(stdin string, env map[string]string, args ...string) result {
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(stdin), &stdout, &stderr, func(k string) string { return env[k] })
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustSucceed(t *testing.T, r result) {
	t.Helper()
	if r.code != exitOK {
		t.Fatalf("exit %d; stdout: %s; stderr: %s", r.code, r.stdout, r.stderr)
	}
}

func TestCommandsReachTheAPI(t *testing.T) {
	server := newServer(t)

	created := cli("", nil, "-server", server, "create", writeFile(t, createJSON))
	mustSucceed(t, created)
	var h struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(created.stdout), &h); err != nil || h.ID == "" {
		t.Fatalf("create output is not a procedure: %q (%v)", created.stdout, err)
	}

	revised := cli(reviseJSON, nil, "-server", server, "revise", h.ID)
	mustSucceed(t, revised)
	if !strings.Contains(revised.stdout, `"version":2`) {
		t.Fatalf("revise output = %s", revised.stdout)
	}

	for _, args := range [][]string{
		{"health"},
		{"list"},
		{"get", h.ID},
		{"get-by-key", "go.dependency.add"},
		{"get-version", h.ID, "2"},
		{"graph", h.ID, "2"},
	} {
		r := cli("", nil, append([]string{"-server", server}, args...)...)
		mustSucceed(t, r)
		if !jsontext.Value(r.stdout).IsValid() || r.stderr != "" {
			t.Fatalf("%v: stdout %q, stderr %q", args, r.stdout, r.stderr)
		}
	}

	// The server can also come from the environment.
	mustSucceed(t, cli("", map[string]string{"POLAROID_URL": server}, "get", h.ID))
}

func TestBindingCommands(t *testing.T) {
	server := newServer(t)
	created := cli(createJSON, nil, "-server", server, "create")
	mustSucceed(t, created)
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(created.stdout), &p); err != nil {
		t.Fatal(err)
	}

	bindJSON := `{"repository":"github.com/ashuangiras/polaroid","name":"add-dependency","procedure_id":"` + p.ID +
		`","revision":{"inputs":{},"version_policy":{"pin":1},"revision_reason":"Bind."}}`
	bound := cli("", nil, "-server", server, "bind", writeFile(t, bindJSON))
	mustSucceed(t, bound)
	var b struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(bound.stdout), &b); err != nil || b.ID == "" {
		t.Fatalf("bind output is not a binding: %q (%v)", bound.stdout, err)
	}

	const reviseBindingJSON = `{"base_revision":1,"revision":{"inputs":{"x":1},"version_policy":{"contextual":{}},"revision_reason":"Go contextual."}}`
	revised := cli(reviseBindingJSON, nil, "-server", server, "revise-binding", b.ID)
	mustSucceed(t, revised)
	if !strings.Contains(revised.stdout, `"revision":2`) {
		t.Fatalf("revise-binding output = %s", revised.stdout)
	}

	list := cli("", nil, "-server", server, "bindings", "github.com/ashuangiras/polaroid")
	mustSucceed(t, list)
	if !strings.Contains(list.stdout, b.ID) {
		t.Fatalf("bindings output = %s", list.stdout)
	}
	for _, args := range [][]string{
		{"get-binding", b.ID},
		{"get-binding-revision", b.ID, "2"},
	} {
		r := cli("", nil, append([]string{"-server", server}, args...)...)
		mustSucceed(t, r)
		if !jsontext.Value(r.stdout).IsValid() || r.stderr != "" {
			t.Fatalf("%v: stdout %q, stderr %q", args, r.stdout, r.stderr)
		}
	}

	stale := cli(reviseBindingJSON, nil, "-server", server, "revise-binding", b.ID)
	if stale.code != exitFailure || !strings.Contains(stale.stdout, `"code":"revision_conflict"`) {
		t.Fatalf("stale revise-binding: exit %d, stdout %s", stale.code, stale.stdout)
	}
	duplicate := cli(bindJSON, nil, "-server", server, "bind")
	if duplicate.code != exitFailure || !strings.Contains(duplicate.stdout, `"code":"binding_exists"`) {
		t.Fatalf("duplicate bind: exit %d, stdout %s", duplicate.code, duplicate.stdout)
	}
}

func TestExecutionCommands(t *testing.T) {
	server := newServer(t)
	created := cli(createJSON, nil, "-server", server, "create")
	mustSucceed(t, created)
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(created.stdout), &p); err != nil {
		t.Fatal(err)
	}

	recordJSON := `{"procedure_id":"` + p.ID + `","version":1,"repository":"scratch",` +
		`"commit":"0123456789abcdef0123456789abcdef01234567","environment":{"name":"laptop","attributes":{}},` +
		`"inputs":{},"outcome":"succeeded","evidence":{"exit":0}}`
	recorded := cli(recordJSON, nil, "-server", server, "record")
	mustSucceed(t, recorded)
	var e struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(recorded.stdout), &e); err != nil || e.ID == "" {
		t.Fatalf("record output is not an execution: %q (%v)", recorded.stdout, err)
	}

	const commit = "0123456789abcdef0123456789abcdef01234567"
	for args, want := range map[[7]string]string{
		{"get-execution", e.ID}:                                   `"evidence":{"exit":0}`,
		{"executions", p.ID}:                                      e.ID,
		{"executions", p.ID, "scratch"}:                           e.ID,
		{"executions", p.ID, "github.com/o/r"}:                    `{"executions":[]}`,
		{"verification", e.ID}:                                    `"verified":true`,
		{"verifications", p.ID, "1"}:                              `"latest_execution_id":"` + e.ID + `"`,
		{"verifications", p.ID, "1", "scratch", commit, "laptop"}: `"execution_ids":["` + e.ID + `"]`,
		{"verifications", p.ID, "1", "scratch", commit, "other"}:  `{"verifications":[]}`,
		{"graph", p.ID, "1", "scratch", "laptop"}:                 `"verified_by":"` + e.ID + `"`,
		{"graph", p.ID, "1", "scratch", "laptop", commit, "{}"}:   `"target_verification":{"combination":{"repository":"scratch","commit":"` + commit + `","environment":{"name":"laptop"},"inputs":{}},"verified":true,"latest_execution_id":"` + e.ID + `"`,
	} {
		r := cli("", nil, append([]string{"-server", server}, slices.DeleteFunc(args[:], func(s string) bool { return s == "" })...)...)
		mustSucceed(t, r)
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("%v: stdout %s, want %s", args, r.stdout, want)
		}
	}

	bound := cli(`{"repository":"scratch","name":"follow","procedure_id":"`+p.ID+`",`+
		`"revision":{"inputs":{},"version_policy":{"contextual":{}},"revision_reason":"r"}}`, nil, "-server", server, "bind")
	mustSucceed(t, bound)
	var b struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(bound.stdout), &b); err != nil {
		t.Fatal(err)
	}
	resolved := cli("", nil, "-server", server, "resolve", b.ID, "laptop")
	mustSucceed(t, resolved)
	if !strings.Contains(resolved.stdout, `"selected_by":"evidence"`) {
		t.Fatalf("resolve: %s", resolved.stdout)
	}
	elsewhere := cli("", nil, "-server", server, "resolve", b.ID, "laptop", strings.Repeat("b", 40), `{}`)
	mustSucceed(t, elsewhere)
	if !strings.Contains(elsewhere.stdout, `"verified":false,"execution_ids":[]`) || !strings.Contains(elsewhere.stdout, `"commit":"`+commit+`"`) {
		t.Fatalf("resolve at an unseen commit: %s", elsewhere.stdout)
	}

	invalid := cli(strings.Replace(recordJSON, `"version":1`, `"version":5`, 1), nil, "-server", server, "record")
	if invalid.code != exitFailure || !strings.Contains(invalid.stdout, `"field":"version"`) {
		t.Fatalf("recording a missing version: exit %d, stdout %s", invalid.code, invalid.stdout)
	}
}

func TestFeedbackCommands(t *testing.T) {
	server := newServer(t)
	const reportJSON = `{"kind":"problem","summary":"The CLI hides the error code","details":"Only stdout has it.",` +
		`"reporter":"copilot.cli","context":{"command":"record"}}`
	reported := cli("", nil, "-server", server, "feedback", writeFile(t, reportJSON))
	mustSucceed(t, reported)
	var f struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(reported.stdout), &f); err != nil || f.ID == "" {
		t.Fatalf("feedback output is not a report: %q (%v)", reported.stdout, err)
	}
	suggested := cli(`{"kind":"suggestion","summary":"s","details":"d","reporter":"copilot.cli"}`, nil, "-server", server, "feedback")
	mustSucceed(t, suggested)

	got := cli("", nil, "-server", server, "get-feedback", f.ID)
	mustSucceed(t, got)
	if got.stdout != reported.stdout {
		t.Fatalf("get-feedback = %s, want %s", got.stdout, reported.stdout)
	}
	all := cli("", nil, "-server", server, "feedbacks")
	mustSucceed(t, all)
	if want := `{"feedback":[` + strings.TrimSpace(reported.stdout) + `,` + strings.TrimSpace(suggested.stdout) + `]}`; strings.TrimSpace(all.stdout) != want {
		t.Fatalf("feedbacks = %s, want %s", all.stdout, want)
	}
	problems := cli("", nil, "-server", server, "feedbacks", "problem")
	mustSucceed(t, problems)
	if want := `{"feedback":[` + strings.TrimSpace(reported.stdout) + `]}`; strings.TrimSpace(problems.stdout) != want {
		t.Fatalf("feedbacks problem = %s, want %s", problems.stdout, want)
	}

	if r := cli("", nil, "-server", server, "feedbacks", "bug"); r.code != exitFailure || !strings.Contains(r.stdout, `"field":"kind"`) {
		t.Fatalf("unknown kind: exit %d, stdout %s", r.code, r.stdout)
	}
	if r := cli(strings.Replace(reportJSON, `"problem"`, `"bug"`, 1), nil, "-server", server, "feedback"); r.code != exitFailure || !strings.Contains(r.stdout, `"field":"kind"`) {
		t.Fatalf("reporting an unknown kind: exit %d, stdout %s", r.code, r.stdout)
	}
	page := cli("", nil, "-server", server, "feedbacks", "subject_type=service", "limit=1")
	mustSucceed(t, page)
	if !strings.Contains(page.stdout, f.ID) || !strings.Contains(page.stdout, `"next":"`) {
		t.Fatalf("feedbacks subject_type=service limit=1 = %s, want the first report and a next cursor", page.stdout)
	}
}

func TestRepositoryAndDiscoveryCommands(t *testing.T) {
	server := newServer(t)
	run := func(stdin string, args ...string) string {
		t.Helper()
		r := cli(stdin, nil, append([]string{"-server", server}, args...)...)
		mustSucceed(t, r)
		return r.stdout
	}
	var repo struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(run(`{"identifier":"github.com/o/a","name":"A"}`, "register")), &repo); err != nil || repo.ID == "" {
		t.Fatalf("register output is not a repository: %v", err)
	}
	aliased := run(`{"identifier":"mirror.example/o/a","reason":"The mirror of A."}`, "alias", repo.ID)
	if !strings.Contains(aliased, `"aliases":[{"identifier":"mirror.example/o/a"`) {
		t.Fatalf("alias = %s", aliased)
	}
	for _, args := range [][]string{{"repository", repo.ID}, {"repository-by-identifier", "mirror.example/o/a"}} {
		if got := run("", args...); got != aliased {
			t.Fatalf("%v = %s, want %s", args, got, aliased)
		}
	}
	if got := run("", "repositories", "limit=1"); !strings.Contains(got, repo.ID) || strings.Contains(got, `"next"`) {
		t.Fatalf("repositories limit=1 = %s", got)
	}

	local := strings.Replace(createJSON, `"version":{`, `"version":{"goal":"Add a Go dependency.","applicability":{"repository":"`+repo.ID+`"},`, 1)
	created := run(local, "create")
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(created), &p); err != nil {
		t.Fatal(err)
	}
	for args, want := range map[[3]string]string{
		{"list", "repository=mirror.example/o/a"}: `"scope":"local"`,
		{"list", "repository=github.com/o/b"}:     `{"procedures":[]}`,
		{"list", "q=GO DEP", "scope=local"}:       p.ID,
	} {
		if got := run("", slices.DeleteFunc(args[:], func(s string) bool { return s == "" })...); !strings.Contains(got, want) {
			t.Fatalf("%v = %s, want %s", args, got, want)
		}
	}
	origin := run(`{"repository_id":"`+repo.ID+`","reason":"Written for A."}`, "origin", p.ID)
	if !strings.Contains(origin, `"origin":{"repository_id":"`+repo.ID+`","reason":"Written for A."`) {
		t.Fatalf("origin = %s", origin)
	}
	if r := cli(`{"repository_id":"`+repo.ID+`","reason":"Again."}`, nil, "-server", server, "origin", p.ID); r.code != exitFailure || !strings.Contains(r.stdout, `"code":"origin_exists"`) {
		t.Fatalf("second origin: exit %d, stdout %s", r.code, r.stdout)
	}
}

func TestFailedRequestsExitOne(t *testing.T) {
	server := newServer(t)
	created := cli("", nil, "-server", server, "create", writeFile(t, createJSON))
	mustSucceed(t, created)
	var h struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(created.stdout), &h); err != nil {
		t.Fatal(err)
	}
	mustSucceed(t, cli(reviseJSON, nil, "-server", server, "revise", h.ID, "-"))

	unreachable := func() string {
		ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		return "http://" + addr
	}()

	cases := []struct {
		name      string
		stdin     string
		args      []string
		errorCode string // expected API error code on stdout, if any
	}{
		{"stale revision", reviseJSON, []string{"-server", server, "revise", h.ID}, "version_conflict"},
		{"duplicate canonical key", createJSON, []string{"-server", server, "create"}, "canonical_key_exists"},
		{"unknown procedure", "", []string{"-server", server, "get", "no-such-id"}, "not_found"},
		{"unknown version", "", []string{"-server", server, "get-version", h.ID, "9"}, "not_found"},
		{"invalid JSON", `{"canonical_key":`, []string{"-server", server, "create"}, "invalid_request"},
		{"missing input file", "", []string{"-server", server, "create", filepath.Join(t.TempDir(), "absent.json")}, ""},
		{"server unreachable", "", []string{"-server", unreachable, "list"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := cli(tc.stdin, nil, tc.args...)
			if r.code != exitFailure {
				t.Fatalf("exit %d, want %d; stdout: %s; stderr: %s", r.code, exitFailure, r.stdout, r.stderr)
			}
			if !strings.HasPrefix(r.stderr, "polaroid: ") {
				t.Fatalf("stderr = %q, want a diagnostic", r.stderr)
			}
			if tc.errorCode == "" {
				return
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(r.stdout), &body); err != nil || body.Error.Code != tc.errorCode {
				t.Fatalf("stdout = %s, want API error %s", r.stdout, tc.errorCode)
			}
		})
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"frobnicate"},
		{"get"},
		{"get", "a", "b"},
		{"get-version", "a"},
		{"graph", "a"},
		{"graph", "a", "1", "repo"},
		{"graph", "a", "1", "repo", "env", "x"},
		{"graph", "a", "1", "repo", "env", "c", "{}", "x"},
		{"resolve", "a"},
		{"resolve", "a", "env", "c"},
		{"resolve", "a", "env", "c", "{}", "x"},
		{"revise"},
		{"bindings"},
		{"get-binding"},
		{"get-binding-revision", "a"},
		{"revise-binding"},
		{"bind", "a", "b"},
		{"record", "a", "b"},
		{"get-execution"},
		{"executions", "limit=1", "a"},
		{"executions", "a", "b", "c"},
		{"list", "=x"},
		{"list", "a"},
		{"verification"},
		{"verification", "a", "b"},
		{"verifications", "a"},
		{"verifications", "a", "1", "r", "c", "e", "x"},
		{"feedback", "a", "b"},
		{"feedbacks", "problem", "x"},
		{"get-feedback"},
		{"get-feedback", "a", "b"},
		{"-server", "ftp://example.com", "list"},
		{"-server", "127.0.0.1:7417", "list"},
		{"-no-such-flag", "list"},
	} {
		r := cli("", nil, args...)
		if r.code != exitUsage || r.stderr == "" {
			t.Errorf("%v: exit %d, stderr %q; want exit %d with a message", args, r.code, r.stderr, exitUsage)
		}
	}
}

func TestHelp(t *testing.T) {
	r := cli("", nil, "help")
	if r.code != exitOK || !strings.Contains(r.stdout, "get-by-key KEY") {
		t.Fatalf("help: exit %d, stdout %q", r.code, r.stdout)
	}
	if r := cli("", nil, "-h"); r.code != exitOK || !strings.Contains(r.stderr, "Commands:") {
		t.Fatalf("-h: exit %d, stderr %q", r.code, r.stderr)
	}
}
