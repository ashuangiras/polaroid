package http_test

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestConcurrentCreatesOfOneCanonicalKey: of simultaneous creations under one
// canonical key exactly one is stored, whole; the others are conflicts.
func TestConcurrentCreatesOfOneCanonicalKey(t *testing.T) {
	s := newTestServer(t)
	const writers = 8
	start := make(chan struct{})
	results := make([]response, writers)
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			<-start
			results[i], errs[i] = s.send(http.MethodPost, "/v1/procedures", procedureBody("c.same", fmt.Sprintf(`"goal":"writer %d",`, i)), nil)
		})
	}
	close(start)
	wg.Wait()

	winner := -1
	for i, resp := range results {
		if errs[i] != nil {
			t.Fatalf("writer %d: %v", i, errs[i])
		}
		switch resp.status {
		case http.StatusCreated:
			if winner >= 0 {
				t.Fatalf("writers %d and %d both created c.same", winner, i)
			}
			winner = i
		case http.StatusConflict:
			if code := errorOf(t, resp).Code; code != "canonical_key_exists" {
				t.Fatalf("writer %d: %s", i, resp.body)
			}
		default:
			t.Fatalf("writer %d: status %d: %s", i, resp.status, resp.body)
		}
	}
	if winner < 0 {
		t.Fatal("no writer created c.same")
	}
	got := s.call(t, http.MethodGet, "/v1/procedures/by-key/c.same", "")
	expect(t, got, http.StatusOK, "")
	if string(got.body) != string(results[winner].body) {
		t.Fatalf("stored history differs from the winner's response:\n%s\n%s", got.body, results[winner].body)
	}
	if list := s.call(t, http.MethodGet, "/v1/procedures", ""); strings.Count(string(list.body), `"id"`) != 1 {
		t.Fatalf("list = %s", list.body)
	}
}

type statisticsCounts struct {
	Records struct {
		Executions int `json:"executions"`
	} `json:"records"`
	Executions struct {
		ByOutcome        map[string]int `json:"by_outcome"`
		ByEvidenceStatus map[string]int `json:"by_evidence_status"`
	} `json:"executions"`
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// TestConcurrentExecutionsAndReads records executions from several writers
// while readers list, read, verify and summarize them. Every read is a
// consistent state that only grows, and afterwards every accepted execution
// reads back exactly as it was returned.
func TestConcurrentExecutionsAndReads(t *testing.T) {
	s := newTestServer(t)
	p := idOf(t, s.mustPost(t, "/v1/procedures", procedureBody("c.run", "")))
	const writers, each, readers = 8, 10, 4
	accepted := make([][]response, writers)
	var writing sync.WaitGroup
	var done atomic.Bool
	for w := range writers {
		writing.Go(func() {
			for n := range each {
				outcome := []string{"succeeded", "failed"}[n%2]
				body := fmt.Sprintf(`{"procedure_id":%q,"version":1,"repository":"github.com/o/a","commit":%q,"environment":{"name":"ci","attributes":{}},`+
					`"inputs":{"writer":%d},"outcome":%q,"evidence":{"writer":%d,"n":%d}}`, p, scopeCommit, w, outcome, w, n)
				resp, err := s.send(http.MethodPost, "/v1/executions", body, nil)
				if err != nil || resp.status != http.StatusCreated {
					t.Errorf("writer %d run %d: %v %d %s", w, n, err, resp.status, resp.body)
					return
				}
				accepted[w] = append(accepted[w], resp)
			}
		})
	}
	var reading sync.WaitGroup
	reads := make([]int, readers)
	for r := range readers {
		reading.Go(func() {
			lastCount, lastListed := 0, 0
			for i := 0; !done.Load() || i < 3; i++ {
				resp, err := s.send(http.MethodGet, "/v1/statistics", "", nil)
				if err != nil || resp.status != http.StatusOK {
					t.Errorf("reader %d: statistics: %v %d %s", r, err, resp.status, resp.body)
					return
				}
				var st statisticsCounts
				if err := json.Unmarshal(resp.body, &st); err != nil {
					t.Errorf("reader %d: %v", r, err)
					return
				}
				n := st.Records.Executions
				if sum(st.Executions.ByOutcome) != n || sum(st.Executions.ByEvidenceStatus) != n || n < lastCount {
					t.Errorf("reader %d: inconsistent statistics after %d executions: %s", r, lastCount, resp.body)
					return
				}
				lastCount = n
				list, err := s.send(http.MethodGet, "/v1/executions", "", nil)
				if err != nil || list.status != http.StatusOK {
					t.Errorf("reader %d: list: %v %d", r, err, list.status)
					return
				}
				ids := executionIDs(t, list.body)
				if len(ids) < lastListed {
					t.Errorf("reader %d: list shrank from %d to %d", r, lastListed, len(ids))
					return
				}
				lastListed = len(ids)
				if len(ids) > 0 {
					id := ids[len(ids)-1]
					for _, path := range []string{"/v1/executions/" + id, "/v1/executions/" + id + "/verification"} {
						if got, err := s.send(http.MethodGet, path, "", nil); err != nil || got.status != http.StatusOK {
							t.Errorf("reader %d: GET %s: %v %d %s", r, path, err, got.status, got.body)
							return
						}
					}
				}
				reads[r]++
			}
		})
	}
	writing.Wait()
	done.Store(true)
	reading.Wait()
	if t.Failed() {
		return
	}

	ids := executionIDs(t, s.call(t, http.MethodGet, "/v1/executions", "").body)
	if len(ids) != writers*each {
		t.Fatalf("%d executions listed, want %d", len(ids), writers*each)
	}
	for w := range writers {
		for _, resp := range accepted[w] {
			id := idOf(t, resp)
			if !slices.Contains(ids, id) {
				t.Fatalf("accepted execution %s is not listed", id)
			}
			if got := s.call(t, http.MethodGet, "/v1/executions/"+id, ""); string(got.body) != string(resp.body) {
				t.Fatalf("execution %s reads back differently:\n%s\n%s", id, got.body, resp.body)
			}
		}
	}
	var st statisticsCounts
	if err := json.Unmarshal(s.call(t, http.MethodGet, "/v1/statistics", "").body, &st); err != nil || st.Records.Executions != writers*each ||
		st.Executions.ByOutcome["succeeded"] != writers*each/2 || st.Executions.ByOutcome["failed"] != writers*each/2 {
		t.Fatalf("final statistics: %+v, %v", st, err)
	}
	t.Logf("reads per reader during the writes: %v", reads)
}

// TestConcurrentFeedbackResolutionConflict: of simultaneous attempts to
// resolve the same report, exactly one succeeds (201); every other gets 409
// feedback_resolved, never a database-lock error escaping as something else
// (_txlock=immediate serializes writers; ADR-0040).
func TestConcurrentFeedbackResolutionConflict(t *testing.T) {
	s := newTestServer(t)
	_, fb := s.report(t, nil)
	path := "/v1/feedback/" + fb.ID + "/resolution"

	const writers = 8
	start := make(chan struct{})
	results := make([]response, writers)
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			<-start
			results[i], errs[i] = s.send(http.MethodPost, path, resolveBody, nil)
		})
	}
	close(start)
	wg.Wait()

	winner := -1
	for i, resp := range results {
		if errs[i] != nil {
			t.Fatalf("writer %d: %v", i, errs[i])
		}
		switch resp.status {
		case http.StatusCreated:
			if winner >= 0 {
				t.Fatalf("writers %d and %d both resolved %s", winner, i, fb.ID)
			}
			winner = i
		case http.StatusConflict:
			if code := errorOf(t, resp).Code; code != "feedback_resolved" {
				t.Fatalf("writer %d: %s", i, resp.body)
			}
		default:
			t.Fatalf("writer %d: status %d: %s", i, resp.status, resp.body)
		}
	}
	if winner < 0 {
		t.Fatal("no writer resolved the report")
	}
	got := s.call(t, http.MethodGet, "/v1/feedback/"+fb.ID, "")
	expect(t, got, http.StatusOK, "")
	decoded := decodeStrict[feedbackJSON](t, got.body)
	if decoded.Status != "resolved" || decoded.Resolution == nil {
		t.Fatalf("Feedback(%s) = %+v; want exactly one stored resolution", fb.ID, decoded)
	}
	won := decodeStrict[resolutionJSON](t, results[winner].body)
	if !reflect.DeepEqual(*decoded.Resolution, won) {
		t.Fatalf("the stored resolution differs from the winner's response:\n%+v\n%+v", *decoded.Resolution, won)
	}
}

func executionIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var list struct {
		Executions []struct {
			ID string `json:"id"`
		} `json:"executions"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("list: %v: %s", err, body)
	}
	ids := make([]string, len(list.Executions))
	for i, e := range list.Executions {
		ids[i] = e.ID
	}
	return ids
}

// TestReadsProceedWhileAnotherProgramHoldsTheWriteLock (#111): graph walks,
// binding resolution and verification read one snapshot without taking the
// write lock, so they answer at once while another connection holds it with
// an uncommitted change, and do not see that change.
func TestReadsProceedWhileAnotherProgramHoldsTheWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polaroid.db")
	s := newTestServerAt(t, path)
	p := idOf(t, s.mustPost(t, "/v1/procedures", procedureBody("c.read", "")))
	b := idOf(t, s.mustPost(t, "/v1/bindings", bindRequest("github.com/o/a", "verify", p, `{"contextual":{}}`)))
	e := idOf(t, s.mustPost(t, "/v1/executions", runBody(p, 1, "github.com/o/a", "")))
	reads := []string{
		"/v1/procedures/" + p + "/versions/1/graph?repository=github.com/o/a&environment=ci",
		"/v1/bindings/" + b + "/resolution?environment=ci",
		"/v1/executions/" + e + "/verification",
		"/v1/procedures/" + p + "/versions/1/verifications",
	}
	before := make([]string, len(reads))
	for i, path := range reads {
		got := s.call(t, http.MethodGet, path, "")
		expect(t, got, http.StatusOK, "")
		before[i] = string(got.body)
	}

	ctx := context.Background()
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO execution_invalidations (execution_id, reason, actor, created_at)
		VALUES (?, 'uncommitted', 'test.writer', '2026-10-11T00:00:00.000000000Z')`, e); err != nil {
		t.Fatal(err)
	}
	for i, path := range reads {
		got := s.call(t, http.MethodGet, path, "")
		expect(t, got, http.StatusOK, "")
		if string(got.body) != before[i] {
			t.Fatalf("GET %s saw the uncommitted invalidation:\n%s\nwant %s", path, got.body, before[i])
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	if got := s.call(t, http.MethodGet, "/v1/executions/"+e+"/verification", ""); string(got.body) == before[2] {
		t.Fatalf("the committed invalidation is not visible: %s", got.body)
	}
}

// TestWriteLockTimeoutStoresNothing: while another connection holds the
// database's write lock longer than the busy timeout, reads still answer, and
// a write fails with the generic internal error, stores nothing, and succeeds
// when retried after the lock is released.
func TestWriteLockTimeoutStoresNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polaroid.db")
	s := newTestServerAt(t, path)
	p := idOf(t, s.mustPost(t, "/v1/procedures", procedureBody("c.locked", "")))
	before := s.call(t, http.MethodGet, "/v1/executions", "")

	ctx := context.Background()
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}

	expect(t, s.call(t, http.MethodGet, "/v1/statistics", ""), http.StatusOK, "")
	run := runBody(p, 1, "github.com/o/a", "")
	locked := s.call(t, http.MethodPost, "/v1/executions", run)
	expect(t, locked, http.StatusInternalServerError, "internal")
	if string(locked.body) != `{"error":{"code":"internal","message":"internal error"}}`+"\n" {
		t.Fatalf("a lock timeout exposed detail: %s", locked.body)
	}
	if !strings.Contains(s.logs.String(), "record execution") {
		t.Fatalf("the cause was not logged: %s", s.logs.String())
	}

	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if after := s.call(t, http.MethodGet, "/v1/executions", ""); string(after.body) != string(before.body) {
		t.Fatalf("the failed write stored something: %s", after.body)
	}
	s.mustPost(t, "/v1/executions", run)
	if n := len(executionIDs(t, s.call(t, http.MethodGet, "/v1/executions", "").body)); n != 1 {
		t.Fatalf("%d executions after the retry, want 1", n)
	}
}
