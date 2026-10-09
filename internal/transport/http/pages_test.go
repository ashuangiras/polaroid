package http_test

import (
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"testing"
)

type pageJSON struct {
	Procedures []struct {
		ID            string `json:"id"`
		CanonicalKey  string `json:"canonical_key"`
		LatestVersion int    `json:"latest_version"`
		Scope         string `json:"scope"`
	} `json:"procedures"`
	Bindings []struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		LatestRevision int    `json:"latest_revision"`
	} `json:"bindings"`
	Next string `json:"next"`
}

func (s *testServer) page(t *testing.T, path, after string) pageJSON {
	t.Helper()
	if after != "" {
		path += "&after=" + url.QueryEscape(after)
	}
	resp := s.call(t, http.MethodGet, path, "")
	expect(t, resp, http.StatusOK, "")
	var page pageJSON
	if err := json.Unmarshal(resp.body, &page); err != nil {
		t.Fatal(err)
	}
	return page
}

// items follows a list from path to its end, calling between once after the
// first page, and returns each item as "key@version" (procedures) or
// "name@revision" (bindings).
func (s *testServer) items(t *testing.T, path string, between func()) []string {
	t.Helper()
	var out []string
	for after, first := "", true; ; first = false {
		page := s.page(t, path, after)
		for _, p := range page.Procedures {
			out = append(out, fmt.Sprintf("%s@%d", p.CanonicalKey, p.LatestVersion))
		}
		for _, b := range page.Bindings {
			out = append(out, fmt.Sprintf("%s@%d", b.Name, b.LatestRevision))
		}
		if page.Next == "" {
			return out
		}
		if first && between != nil {
			between()
		}
		after = page.Next
	}
}

// TestSnapshotPagesAreFixedAtTheFirstPage covers ADR-0023 on the procedure
// list: with snapshot, every procedure that existed at the first page is
// returned once, as it was then, whatever is written while the client
// pages. Without it, the documented live behavior holds.
func TestSnapshotPagesAreFixedAtTheFirstPage(t *testing.T) {
	s := newTestServer(t)
	ids := map[string]string{}
	create := func(key string) { ids[key] = idOf(t, s.mustPost(t, "/v1/procedures", procedureBody(key, ""))) }
	share := func(key string, base int) {
		s.mustPost(t, "/v1/procedures/"+ids[key]+"/versions", reviseWith(base, `"applicability":{"shared":{}},`))
	}
	for _, key := range []string{"k.b", "k.d", "k.f", "k.h"} {
		create(key)
	}
	const unspecified = "/v1/procedures?scope=unspecified&limit=1"

	// After the first page (k.b): a.5 sorts before the cursor, k.c0 after it,
	// and k.f leaves the filter.
	got := s.items(t, unspecified+"&snapshot=true", func() { create("a.5"); create("k.c0"); share("k.f", 1) })
	if want := []string{"k.b@1", "k.d@1", "k.f@1", "k.h@1"}; !slices.Equal(got, want) {
		t.Fatalf("snapshot traversal = %v, want %v", got, want)
	}

	// The same kinds of writes during a live traversal, after its first page
	// (a.5): a.1 sorts before the cursor and is never returned, k.c1 is, and
	// k.h leaves the filter before its page.
	got = s.items(t, unspecified, func() { create("a.1"); create("k.c1"); share("k.h", 1) })
	if want := []string{"a.5@1", "k.b@1", "k.c0@1", "k.c1@1", "k.d@1"}; !slices.Equal(got, want) {
		t.Fatalf("live traversal = %v, want %v", got, want)
	}

	// Starting again shows what the earlier traversals could not.
	got = s.items(t, unspecified+"&snapshot=true", nil)
	if want := []string{"a.1@1", "a.5@1", "k.b@1", "k.c0@1", "k.c1@1", "k.d@1"}; !slices.Equal(got, want) {
		t.Fatalf("a new snapshot = %v, want %v", got, want)
	}
	if got := s.items(t, "/v1/procedures?scope=shared&limit=1&snapshot=true", nil); !slices.Equal(got, []string{"k.f@2", "k.h@2"}) {
		t.Fatalf("shared after the revisions = %v", got)
	}
}

// TestSnapshotPagesUnderConcurrentWrites pages a snapshot one item at a time
// while writers concurrently create procedures before and after the cursor
// and revise listed ones: the traversal is exactly the procedures within the
// first page's boundary, in key order, at their versions then.
func TestSnapshotPagesUnderConcurrentWrites(t *testing.T) {
	s := newTestServer(t)
	listed := []string{"c.b", "c.d", "c.f", "c.h", "c.j", "c.l"}
	ids := map[string]string{}
	var want []string
	for _, key := range listed {
		ids[key] = idOf(t, s.mustPost(t, "/v1/procedures", procedureBody(key, "")))
		want = append(want, key+"@1")
	}
	const path = "/v1/procedures?limit=1&snapshot=true"
	page := s.page(t, path, "")
	seen := []string{fmt.Sprintf("%s@%d", page.Procedures[0].CanonicalKey, page.Procedures[0].LatestVersion)}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, key := range []string{"c.a", "c.c", "c.e", "c.g", "c.i", "c.k"} {
		wg.Go(func() {
			<-start
			if r, err := s.send(http.MethodPost, "/v1/procedures", procedureBody(key, ""), nil); err != nil || r.status != http.StatusCreated {
				t.Errorf("create %s: %v %+v", key, err, r)
			}
			r, err := s.send(http.MethodPost, "/v1/procedures/"+ids[listed[i]]+"/versions", reviseWith(1, ""), nil)
			if err != nil || r.status != http.StatusCreated {
				t.Errorf("revise %s: %v %+v", listed[i], err, r)
			}
		})
	}
	close(start)
	for after := page.Next; after != ""; {
		page = s.page(t, path, after)
		for _, p := range page.Procedures {
			seen = append(seen, fmt.Sprintf("%s@%d", p.CanonicalKey, p.LatestVersion))
		}
		after = page.Next
	}
	wg.Wait()
	if !slices.Equal(seen, want) {
		t.Fatalf("snapshot under concurrent writes = %v, want %v", seen, want)
	}
	if got := s.items(t, path, nil); len(got) != 12 || got[0] != "c.a@1" || got[1] != "c.b@2" {
		t.Fatalf("a new snapshot after the writes = %v", got)
	}
}

// TestBindingSnapshotsAndRepositoryIdentity pages a repository's bindings
// as a snapshot while an alias is registered and a revision is appended:
// the alias's binding joins only a traversal that starts after it.
func TestBindingSnapshotsAndRepositoryIdentity(t *testing.T) {
	s := newTestServer(t)
	a := s.register(t, "github.com/o/a")
	p := idOf(t, s.mustPost(t, "/v1/procedures", procedureBody("b.p", "")))
	s.mustPost(t, "/v1/bindings", bindRequest("github.com/o/a", "b1", p, `{"contextual":{}}`))
	b3 := idOf(t, s.mustPost(t, "/v1/bindings", bindRequest("github.com/o/a", "b3", p, `{"contextual":{}}`)))
	s.mustPost(t, "/v1/bindings", bindRequest("old.example/o/a", "b2", p, `{"contextual":{}}`))
	const path = "/v1/bindings?repository=github.com/o/a&limit=1&snapshot=true"
	got := s.items(t, path, func() {
		s.mustPost(t, "/v1/repositories/"+a+"/aliases", `{"identifier":"old.example/o/a","reason":"The old host."}`)
		s.mustPost(t, "/v1/bindings/"+b3+"/revisions", `{"base_revision":1,"revision":{"inputs":{},"version_policy":{"contextual":{}},"revision_reason":"r"}}`)
	})
	if want := []string{"b1@1", "b3@1"}; !slices.Equal(got, want) {
		t.Fatalf("binding snapshot = %v, want %v", got, want)
	}
	if got, want := s.items(t, path, nil), []string{"b1@1", "b2@1", "b3@2"}; !slices.Equal(got, want) {
		t.Fatalf("binding snapshot after the alias = %v, want %v", got, want)
	}
}

// TestCursorsAreBoundToTheirListAndParameters covers cursor validation.
func TestCursorsAreBoundToTheirListAndParameters(t *testing.T) {
	s := newTestServer(t)
	p := ""
	for _, key := range []string{"v.a", "v.b", "v.c"} {
		p = idOf(t, s.mustPost(t, "/v1/procedures", procedureBody(key, "")))
	}
	for _, name := range []string{"x", "y"} {
		s.mustPost(t, "/v1/bindings", bindRequest("github.com/o/a", name, p, `{"contextual":{}}`))
	}
	s.mustPost(t, "/v1/feedback", `{"kind":"problem","summary":"s","details":"d","reporter":"copilot.vscode"}`)
	s.mustPost(t, "/v1/feedback", `{"kind":"problem","summary":"s","details":"d","reporter":"copilot.vscode"}`)
	next := func(path string) string {
		page := s.page(t, path, "")
		if page.Next == "" {
			t.Fatalf("%s: no next cursor", path)
		}
		return url.QueryEscape(page.Next)
	}
	live := next("/v1/procedures?limit=1")
	snapshot := next("/v1/procedures?limit=1&snapshot=true")
	unspecified := next("/v1/procedures?limit=1&scope=unspecified")
	refused := map[string]string{
		"malformed":                "/v1/procedures?limit=1&after=not-a-cursor",
		"another list":             "/v1/bindings?repository=github.com/o/a&limit=1&after=" + live,
		"other filters":            "/v1/procedures?limit=1&scope=shared&after=" + unspecified,
		"a filter dropped":         "/v1/procedures?limit=1&after=" + unspecified,
		"snapshot cursor, live":    "/v1/procedures?limit=1&after=" + snapshot,
		"live cursor, snapshot":    "/v1/procedures?limit=1&snapshot=true&after=" + live,
		"a key list's cursor":      "/v1/feedback?limit=1&after=" + live,
		"a time list's cursor":     "/v1/procedures?limit=1&after=" + next("/v1/feedback?limit=1"),
		"another repository":       "/v1/bindings?repository=github.com/o/b&limit=1&after=" + next("/v1/bindings?repository=github.com/o/a&limit=1"),
		"a forged snapshot bounds": "/v1/procedures?limit=1&snapshot=true&after=" + forge(t, snapshot),
	}
	for name, path := range refused {
		resp := s.call(t, http.MethodGet, path, "")
		expect(t, resp, http.StatusBadRequest, "invalid_request")
		if got := fieldsOf(t, resp); !slices.Equal(got, []string{"after"}) {
			t.Errorf("%s: fields %v", name, got)
		}
	}
	for path, field := range map[string]string{
		"/v1/procedures?snapshot=true":                           "snapshot",
		"/v1/procedures?limit=1&snapshot=yes":                    "snapshot",
		"/v1/bindings?repository=github.com/o/a&snapshot=true":   "snapshot",
		"/v1/procedures?limit=1&snapshot=false&after=" + live:    "",
		"/v1/procedures?limit=2&after=" + live:                   "",
		"/v1/procedures?limit=1&after=" + legacyCursor("v.a", p): "",
	} {
		resp := s.call(t, http.MethodGet, path, "")
		if field == "" {
			expect(t, resp, http.StatusOK, "")
			continue
		}
		expect(t, resp, http.StatusBadRequest, "invalid_request")
		if got := fieldsOf(t, resp); !slices.Equal(got, []string{field}) {
			t.Errorf("%s: fields %v, want %s", path, got, field)
		}
	}
	// The time-ordered lists do not offer snapshots.
	expect(t, s.call(t, http.MethodGet, "/v1/executions?limit=1&snapshot=true", ""), http.StatusBadRequest, "invalid_request")
}

// forge returns an escaped snapshot cursor with a boundary of the wrong size.
func forge(t *testing.T, escaped string) string {
	t.Helper()
	cursor, err := url.QueryUnescape(escaped)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	c["s"] = []int{1}
	out, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return url.QueryEscape(base64.RawURLEncoding.EncodeToString(out))
}

// legacyCursor is a cursor as issued before ADR-0023: a bare position.
func legacyCursor(key, id string) string {
	raw, err := json.Marshal([]string{key, id})
	if err != nil {
		panic(err)
	}
	return url.QueryEscape(base64.RawURLEncoding.EncodeToString(raw))
}
