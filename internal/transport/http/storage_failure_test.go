package http_test

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"
)

// TestStorageFailureMidWriteStoresNothing makes the database fail the second
// statement of multi-statement writes, as a full disk or I/O error would: the
// client gets the generic internal error, nothing of the write is stored, and
// the same write succeeds once storage works again.
func TestStorageFailureMidWriteStoresNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polaroid.db")
	s := newTestServerAt(t, path)
	h := s.create(t)
	beforeList := s.call(t, http.MethodGet, "/v1/procedures", "")
	beforeHistory := s.call(t, http.MethodGet, "/v1/procedures/"+h.ID, "")

	ctx := context.Background()
	raw, err := sql.Open("sqlite", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	exec := func(stmt string) {
		t.Helper()
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TRIGGER injected_failure BEFORE INSERT ON procedure_versions BEGIN SELECT RAISE(FAIL, 'injected storage failure'); END`)

	create := procedureBody("f.new", "")
	revise := reviseBody(1, "never stored")
	for _, w := range []struct{ path, body string }{{"/v1/procedures", create}, {"/v1/procedures/" + h.ID + "/versions", revise}} {
		resp := s.call(t, http.MethodPost, w.path, w.body)
		expect(t, resp, http.StatusInternalServerError, "internal")
		if e := errorOf(t, resp); e.Message != "internal error" {
			t.Fatalf("POST %s exposed detail: %s", w.path, resp.body)
		}
	}
	if got := s.call(t, http.MethodGet, "/v1/procedures", ""); string(got.body) != string(beforeList.body) {
		t.Fatalf("a failed creation left an identity behind: %s", got.body)
	}
	if got := s.call(t, http.MethodGet, "/v1/procedures/by-key/f.new", ""); got.status != http.StatusNotFound {
		t.Fatalf("f.new after the failed creation: %d %s", got.status, got.body)
	}
	if got := s.call(t, http.MethodGet, "/v1/procedures/"+h.ID, ""); string(got.body) != string(beforeHistory.body) {
		t.Fatalf("a failed revision changed the history: %s", got.body)
	}

	exec(`DROP TRIGGER injected_failure`)
	s.mustPost(t, "/v1/procedures", create)
	s.mustPost(t, "/v1/procedures/"+h.ID+"/versions", revise)
}
