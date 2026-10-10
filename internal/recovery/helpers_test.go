package recovery

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/lifecycle"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	httpapi "github.com/ashuangiras/polaroid/internal/transport/http"
)

// api is a polaroidd stack (HTTP over memory over SQLite) on one catalog file.
type api struct {
	t     *testing.T
	srv   *httptest.Server
	store *sqlite.Store
}

func serve(t *testing.T, path string) *api {
	t.Helper()
	store, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.NewHandler(memory.NewService(store), slog.New(slog.DiscardHandler)))
	a := &api{t: t, srv: srv, store: store}
	t.Cleanup(a.close)
	return a
}

// close stops the server and closes the catalog, as polaroidd does on SIGTERM.
func (a *api) close() {
	if a.srv == nil {
		return
	}
	a.srv.Close()
	if err := a.store.Close(); err != nil {
		a.t.Errorf("close catalog: %v", err)
	}
	a.srv = nil
}

func (a *api) call(method, path, body string) (int, []byte) {
	a.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, a.srv.URL+path, strings.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.srv.Client().Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		a.t.Fatal(err)
	}
	return resp.StatusCode, b
}

var idPattern = regexp.MustCompile(`"id":"([0-9a-f-]{36})"`)

func (a *api) post(path, body string) string {
	a.t.Helper()
	code, b := a.call(http.MethodPost, path, body)
	if code != http.StatusCreated && code != http.StatusOK {
		a.t.Fatalf("POST %s: %d %s", path, code, b)
	}
	m := idPattern.FindSubmatch(b)
	if m == nil {
		return ""
	}
	return string(m[1])
}

func (a *api) get(path string) []byte {
	a.t.Helper()
	code, b := a.call(http.MethodGet, path, "")
	if code != http.StatusOK {
		a.t.Fatalf("GET %s: %d %s", path, code, b)
	}
	return b
}

const (
	fixtureRepo   = "example.com/recovery/service"
	fixtureCommit = "0123456789abcdef0123456789abcdef01234567"
)

func procedureBody(key, references string) string {
	refs := ""
	if references != "" {
		refs = `, "references": ` + references
	}
	return `{"canonical_key": "` + key + `", "version": {"philosophy": "p", "method": "m", "contract": {}, "instructions": {"steps": ["run"]}, "revision_reason": "First."` + refs + `}}`
}

func executionBody(procedureID string, extra string) string {
	return `{"procedure_id": "` + procedureID + `", "version": 1, "repository": "` + fixtureRepo + `", "commit": "` + fixtureCommit +
		`", "environment": {"name": "ci", "attributes": {}}, "inputs": {}, "outcome": "succeeded", "evidence": {"ok": true}` + extra + `}`
}

// fixture writes one of every record kind, including a run that skips a
// conditional reference with a decision, and returns the API reads that
// describe them.
type fixture struct {
	leaf, parent, binding, run, feedback string
}

func populate(a *api) fixture {
	var f fixture
	a.post("/v1/repositories", `{"identifier": "`+fixtureRepo+`", "name": "Recovery fixture"}`)
	f.leaf = a.post("/v1/procedures", procedureBody("recovery.leaf", ""))
	a.post("/v1/procedures/"+f.leaf+"/versions", `{"base_version": 1, "version": {"philosophy": "p", "method": "m2", "contract": {}, "instructions": {}, "revision_reason": "Second."}}`)
	f.parent = a.post("/v1/procedures", procedureBody("recovery.verify", `[
		{"name": "checks", "procedure_id": "`+f.leaf+`", "version_policy": {"pin": 1}, "inputs": {}},
		{"name": "integration", "procedure_id": "`+f.leaf+`", "version_policy": {"pin": 1}, "inputs": {}, "condition": "Only when code changes."}]`))
	f.binding = a.post("/v1/bindings", `{"repository": "`+fixtureRepo+`", "name": "verify", "procedure_id": "`+f.parent+
		`", "revision": {"inputs": {}, "version_policy": {"pin": 1}, "revision_reason": "Bind it."}}`)
	checks := a.post("/v1/executions", executionBody(f.leaf, ""))
	f.run = a.post("/v1/executions", executionBody(f.parent, `, "binding_id": "`+f.binding+`", "binding_revision": 1,
		"children": [{"reference": "checks", "execution_id": "`+checks+`"}],
		"decisions": [{"reference": "integration", "applicable": false, "rationale": "Only README.md changed.", "evidence": {"files": ["README.md"]}}]`))
	f.feedback = a.post("/v1/feedback", `{"kind": "problem", "summary": "A report", "details": "d", "reporter": "recovery.test", "subject": {"type": "execution", "execution_id": "`+f.run+`"}}`)
	return f
}

// reads are the API responses that must survive a backup and restore.
func (f fixture) reads(a *api) map[string]string {
	a.t.Helper()
	target := url.Values{"repository": {fixtureRepo}, "environment": {"ci"}, "commit": {fixtureCommit}, "inputs": {"{}"}, "decisions": {`{"integration":false}`}}
	out := map[string]string{}
	for _, p := range []string{
		"/v1/procedures/" + f.leaf, "/v1/procedures/" + f.parent, "/v1/procedures/" + f.leaf + "/versions/2",
		"/v1/bindings/" + f.binding, "/v1/bindings?repository=" + url.QueryEscape(fixtureRepo),
		"/v1/executions/" + f.run, "/v1/executions/" + f.run + "/verification",
		"/v1/procedures/" + f.parent + "/versions/1/graph?" + target.Encode(),
		"/v1/feedback/" + f.feedback, "/v1/repositories",
	} {
		out[p] = string(a.get(p))
	}
	return out
}

func checkReads(t *testing.T, got, want map[string]string) {
	t.Helper()
	for p, w := range want {
		if got[p] != w {
			t.Errorf("GET %s after the restore:\n got %s\nwant %s", p, got[p], w)
		}
	}
	// The reads must have exercised the decision and the verification.
	for p, w := range want {
		if strings.HasSuffix(p, "/verification") && !strings.Contains(w, `"verified":true`) {
			t.Errorf("fixture run is not verified: %s", w)
		}
		if strings.Contains(p, "/executions/") && !strings.Contains(p, "verification") && !strings.Contains(w, `"applicable":false`) {
			t.Errorf("fixture run has no decision: %s", w)
		}
	}
}

func digest(t *testing.T, path string) string {
	t.Helper()
	_, sum, err := fileDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

// spaced returns a fresh directory whose path has spaces.
func spaced(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name+" with spaces")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// stoppedCatalog writes the fixture into a new catalog and closes it.
func stoppedCatalog(t *testing.T) (string, fixture, map[string]string) {
	t.Helper()
	path := filepath.Join(spaced(t, "catalog"), "polaroid.db")
	a := serve(t, path)
	f := populate(a)
	reads := f.reads(a)
	a.close()
	return path, f, reads
}

func mustBackup(t *testing.T, source, dir string) BackupResult {
	t.Helper()
	r, err := Backup(context.Background(), BackupRequest{Source: source, Dir: dir})
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	return r
}

// fakeService is the managed service, as Restore sees it.
type fakeService struct {
	state     string
	pid       int
	db        string
	conflict  bool
	stopErr   error
	startErrs []error
	onStop    func()
	calls     []string
}

func (s *fakeService) Status(context.Context) lifecycle.Status {
	st := lifecycle.Status{State: s.state, PID: s.pid, Database: s.db, Detail: "fake " + s.state}
	if s.conflict {
		st.Conflict = &lifecycle.Process{PID: 4242, Command: "other"}
	}
	return st
}

func (s *fakeService) Stop(ctx context.Context) (lifecycle.Status, error) {
	s.calls = append(s.calls, "stop")
	if s.stopErr != nil {
		return s.Status(ctx), s.stopErr
	}
	s.state, s.pid = lifecycle.Stopped, 0
	if s.onStop != nil {
		s.onStop()
	}
	return s.Status(ctx), nil
}

func (s *fakeService) Start(ctx context.Context) (lifecycle.Status, error) {
	s.calls = append(s.calls, "start")
	if len(s.startErrs) > 0 {
		err := s.startErrs[0]
		s.startErrs = s.startErrs[1:]
		if err != nil {
			s.state = lifecycle.Failed
			return s.Status(ctx), err
		}
	}
	s.state, s.pid = lifecycle.Running, 777
	return s.Status(ctx), nil
}

// setSchemaVersion rewrites user_version of a database no one has open.
func setSchemaVersion(t *testing.T, path string, v int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), fmt.Sprintf(`PRAGMA user_version = %d`, v)); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // test files
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func replaceInFile(t *testing.T, path, old, new string) {
	t.Helper()
	b := readFile(t, path)
	if !bytes.Contains(b, []byte(old)) {
		t.Fatalf("%s does not contain %q", path, old)
	}
	writeFile(t, path, bytes.Replace(b, []byte(old), []byte(new), 1))
}
