package http_test

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// Discovery and duplicate suggestions (ADR-0032), over a small development
// catalog whose procedures compete for the same words.

type matchJSON struct {
	Field string   `json:"field"`
	Terms []string `json:"terms"`
}

type candidateJSON struct {
	ProcedureID   string         `json:"procedure_id"`
	CanonicalKey  string         `json:"canonical_key"`
	Version       int            `json:"version"`
	Scope         string         `json:"scope"`
	Applicability jsontext.Value `json:"applicability"`
	Goal          string         `json:"goal"`
	Method        string         `json:"method"`
	MatchedTerms  int            `json:"matched_terms"`
	Score         int            `json:"score"`
	Matches       []matchJSON    `json:"matches"`
}

type discoveryJSON struct {
	Terms      []string        `json:"terms"`
	Repository string          `json:"repository"`
	Matched    int             `json:"matched"`
	Candidates []candidateJSON `json:"candidates"`
}

type suggestionJSON struct {
	ProcedureID   string         `json:"procedure_id"`
	CanonicalKey  string         `json:"canonical_key"`
	Version       int            `json:"version"`
	Scope         string         `json:"scope"`
	Applicability jsontext.Value `json:"applicability"`
	Goal          string         `json:"goal"`
	Method        string         `json:"method"`
	Similarity    float64        `json:"similarity"`
	SharedTerms   int            `json:"shared_terms"`
	Matches       []matchJSON    `json:"matches"`
}

type duplicatesJSON struct {
	ProposalTerms int    `json:"proposal_terms"`
	Repository    string `json:"repository"`
	KeyCollision  *struct {
		ProcedureID   string `json:"procedure_id"`
		CanonicalKey  string `json:"canonical_key"`
		LatestVersion int    `json:"latest_version"`
	} `json:"key_collision"`
	Matched     int              `json:"matched"`
	Suggestions []suggestionJSON `json:"suggestions"`
}

type proc struct {
	Key, Goal, Method, Philosophy string
	Applicability                 any
	Contract, Instructions        any
	References                    any
}

func (p proc) body() string {
	version := map[string]any{"philosophy": p.Philosophy, "method": p.Method,
		"contract": orEmpty(p.Contract), "instructions": orEmpty(p.Instructions), "revision_reason": "Seeded as zebrafish fixtures."}
	if p.Goal != "" {
		version["goal"] = p.Goal
	}
	if p.Applicability != nil {
		version["applicability"] = p.Applicability
	}
	if p.References != nil {
		version["references"] = p.References
	}
	b, err := json.Marshal(map[string]any{"canonical_key": p.Key, "version": version})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func orEmpty(v any) any {
	if v == nil {
		return map[string]any{}
	}
	return v
}

type catalog struct {
	ids  map[string]string
	a, b string // repository IDs
}

const (
	discA      = "github.com/o/a"
	discAAlias = "mirror.example/o/a"
	discB      = "github.com/o/b"
)

func newCatalog(t *testing.T, s *testServer) catalog {
	t.Helper()
	c := catalog{ids: map[string]string{}, a: s.register(t, discA), b: s.register(t, discB)}
	s.mustPost(t, "/v1/repositories/"+c.a+"/aliases", `{"identifier":"`+discAAlias+`","reason":"Mirror of A."}`)
	shared := map[string]any{"shared": map[string]any{}}
	add := func(p proc) {
		c.ids[p.Key] = idOf(t, s.mustPost(t, "/v1/procedures", p.body()))
	}
	add(proc{Key: "go.dependency.add", Applicability: shared,
		Goal:       "Add a third-party Go module dependency at a pinned version.",
		Method:     "Justify the new module, check its license against the allowed list, run go get at an exact version, then go mod tidy.",
		Philosophy: "Every dependency is a long-term liability: it must earn its place and be pinned so that builds are reproducible.",
		Contract:   map[string]any{"inputs": map[string]any{"module": "The module path to add."}},
		Instructions: map[string]any{"steps": []any{
			map[string]any{"id": "justify", "action": "Write down why no existing module or the standard library suffices."},
			map[string]any{"id": "pin", "action": "Run go get MODULE@VERSION."}}}})
	add(proc{Key: "go.module.build", Applicability: shared,
		Goal:         "Build every package of a Go module with the toolchain that go.mod declares.",
		Method:       "Confirm the toolchain version, run the repository's build command from the module root, and confirm that the expected binaries exist.",
		Philosophy:   "A build is evidence only when it uses the declared toolchain and produces the artifacts that later steps use.",
		Instructions: map[string]any{"steps": []any{"Run the build command."}}})
	add(proc{Key: "go.module.checks", Applicability: shared,
		Goal:       "Run a Go module's tests, the race detector and the linters.",
		Method:     "Run the repository's check target once and keep its summary lines.",
		Philosophy: "Checks run once per commit; cached results are reported as cached."})
	add(proc{Key: "dev.change.verify", Applicability: shared,
		Goal:       "Verify one commit of a repository through its build and checks, and record the evidence.",
		Method:     "Resolve the selected versions, run the build and then the checks at the commit, and record each run with its children.",
		Philosophy: "Verification is per commit: evidence from another commit is a reason to try a version, not proof.",
		References: []any{
			map[string]any{"name": "build", "procedure_id": c.ids["go.module.build"], "version_policy": map[string]any{"contextual": map[string]any{}}, "inputs": map[string]any{}},
			map[string]any{"name": "checks", "procedure_id": c.ids["go.module.checks"], "version_policy": map[string]any{"contextual": map[string]any{}}, "inputs": map[string]any{}}}})
	add(proc{Key: "sqlite.schema.migrate", Applicability: map[string]any{"repository": c.a},
		Goal:       "Change a SQLite schema with a new numbered migration.",
		Method:     "Add the next migration file, never edit an applied one, and test an upgrade from the previous schema.",
		Philosophy: "Stored rows are append-only history: a migration adds meaning beside old rows and never rewrites them."})
	add(proc{Key: "release.publish", Applicability: map[string]any{"repository": c.b},
		Goal:       "Publish a prerelease build from a tag on main.",
		Method:     "Tag the verified commit, let the release workflow package and test every archive, then check the published assets.",
		Philosophy: "A release is only as good as the archive users download, so the published bytes are tested."})
	add(proc{Key: "docs.links.check",
		Method:     "Check every relative link and anchor in the Markdown files.",
		Philosophy: "Broken links mislead readers."})
	return c
}

func discoveryPath(task string, params ...string) string {
	q := url.Values{"task": {task}}
	for i := 0; i+1 < len(params); i += 2 {
		q.Set(params[i], params[i+1])
	}
	return "/v1/procedures/discovery?" + q.Encode()
}

func (s *testServer) discover(t *testing.T, task string, params ...string) discoveryJSON {
	t.Helper()
	resp := s.call(t, http.MethodGet, discoveryPath(task, params...), "")
	expect(t, resp, http.StatusOK, "")
	return decodeStrict[discoveryJSON](t, resp.body)
}

func keysOf(cs []candidateJSON) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.CanonicalKey
	}
	return out
}

func matchesOf(c candidateJSON) map[string][]string {
	out := map[string][]string{}
	for _, m := range c.Matches {
		out[m.Field] = m.Terms
	}
	return out
}

func TestDiscoveryFindsProceduresForATask(t *testing.T) {
	s := newTestServer(t)
	c := newCatalog(t, s)

	// Other wording: plural and verb forms, against a key and goal that say
	// "add" and "dependency".
	d := s.discover(t, "Adding dependencies to a Go module")
	if !slices.Equal(d.Terms, []string{"adding", "dependencies", "go", "module"}) || d.Repository != "" {
		t.Fatalf("terms %v, repository %q", d.Terms, d.Repository)
	}
	top := d.Candidates[0]
	if top.CanonicalKey != "go.dependency.add" || top.ProcedureID != c.ids["go.dependency.add"] || top.Version != 1 ||
		top.Scope != "shared" || string(top.Applicability) != `{"shared":{}}` || !strings.HasPrefix(top.Goal, "Add a third-party") ||
		!strings.HasPrefix(top.Method, "Justify") || top.MatchedTerms != 4 || top.Score != 12 {
		t.Fatalf("top candidate %+v; all %v", top, keysOf(d.Candidates))
	}
	if got := matchesOf(top); !slices.Equal(got["canonical_key"], []string{"adding", "dependencies", "go"}) ||
		!slices.Equal(got["goal"], []string{"adding", "dependencies", "go", "module"}) || top.Matches[0].Field != "canonical_key" {
		t.Fatalf("explanation %+v", top.Matches)
	}
	if d.Candidates[1].CanonicalKey != "go.module.build" {
		t.Fatalf("competing candidates %v", keysOf(d.Candidates))
	}

	// Case and punctuation do not matter.
	if d := s.discover(t, "GO.Dependency—ADD!!"); d.Candidates[0].CanonicalKey != "go.dependency.add" ||
		!slices.Equal(d.Terms, []string{"go", "dependency", "add"}) {
		t.Fatalf("punctuated task: %v %v", d.Terms, keysOf(d.Candidates))
	}

	// Words found only in the philosophy, then in the method.
	d = s.discover(t, "keep stored history append-only", "repository", discA)
	if top := d.Candidates[0]; top.CanonicalKey != "sqlite.schema.migrate" || len(top.Matches) != 1 ||
		top.Matches[0].Field != "philosophy" || !slices.Equal(top.Matches[0].Terms, []string{"stored", "history", "append", "only"}) {
		t.Fatalf("philosophy match: %+v", d.Candidates)
	}
	d = s.discover(t, "never edit an applied migration", "repository", discA)
	if got := matchesOf(d.Candidates[0]); d.Candidates[0].CanonicalKey != "sqlite.schema.migrate" ||
		!slices.Equal(got["method"], []string{"never", "edit", "applied", "migration"}) {
		t.Fatalf("method match: %+v", d.Candidates[0])
	}

	// A procedure used as a subprocedure is discovered on its own.
	d = s.discover(t, "build the module with its declared toolchain")
	if d.Candidates[0].CanonicalKey != "go.module.build" || !slices.Contains(keysOf(d.Candidates), "dev.change.verify") {
		t.Fatalf("subprocedure: %v", keysOf(d.Candidates))
	}

	// Words in member names, numbers alone and metadata are not searched.
	for _, task := range []string{"action inputs", "zebrafish fixtures seeded", c.ids["go.module.build"]} {
		if d := s.discover(t, task); len(d.Candidates) != 0 {
			t.Fatalf("%q matched %v", task, d.Candidates)
		}
	}
}

func TestDiscoveryHonoursRepositoryApplicability(t *testing.T) {
	s := newTestServer(t)
	c := newCatalog(t, s)
	const task = "publish a prerelease build and change a schema migration"

	// Without a repository: the whole catalog, local procedures labelled.
	all := s.discover(t, task)
	for _, key := range []string{"release.publish", "sqlite.schema.migrate", "go.module.build"} {
		if !slices.Contains(keysOf(all.Candidates), key) {
			t.Fatalf("catalog-wide discovery lacks %s: %v", key, keysOf(all.Candidates))
		}
	}
	for _, cand := range all.Candidates {
		if cand.CanonicalKey == "release.publish" && (cand.Scope != "local" || string(cand.Applicability) != `{"repository":"`+c.b+`"}`) {
			t.Fatalf("local label: %+v", cand)
		}
	}

	for _, tc := range []struct {
		repository string
		with       []string
		without    []string
	}{
		{discA, []string{"sqlite.schema.migrate", "go.module.build", "docs.links.check"}, []string{"release.publish"}},
		{discAAlias, []string{"sqlite.schema.migrate", "go.module.build"}, []string{"release.publish"}},
		{discB, []string{"release.publish", "go.module.build"}, []string{"sqlite.schema.migrate"}},
		{"github.com/o/unregistered", []string{"go.module.build"}, []string{"release.publish", "sqlite.schema.migrate"}},
	} {
		d := s.discover(t, task+" check links", "repository", tc.repository)
		keys := keysOf(d.Candidates)
		if d.Repository != tc.repository {
			t.Fatalf("repository echo %q", d.Repository)
		}
		for _, k := range tc.with {
			if !slices.Contains(keys, k) {
				t.Errorf("%s: %s missing from %v", tc.repository, k, keys)
			}
		}
		for _, k := range tc.without {
			if slices.Contains(keys, k) {
				t.Errorf("%s: %s present in %v", tc.repository, k, keys)
			}
		}
	}
}

func TestDiscoverySearchesTheLatestVersionOnly(t *testing.T) {
	s := newTestServer(t)
	c := newCatalog(t, s)
	id := c.ids["docs.links.check"]
	if d := s.discover(t, "markdown anchors"); len(d.Candidates) != 1 || d.Candidates[0].Version != 1 || d.Candidates[0].Scope != "unspecified" ||
		d.Candidates[0].Applicability != nil || d.Candidates[0].Goal != "" {
		t.Fatalf("before the revision: %+v", d.Candidates)
	}

	// Version 1 is verified in A and a contextual binding resolves to it by
	// evidence; version 2 has no evidence. Discovery still reports version 2:
	// it reads text, not evidence.
	binding := idOf(t, s.mustPost(t, "/v1/bindings", bindRequest(discA, "links", id, `{"contextual":{}}`)))
	s.mustPost(t, "/v1/executions", runBody(id, 1, discA, ""))
	s.mustPost(t, "/v1/procedures/"+id+"/versions", procedureRevision(proc{Goal: "Find broken links in the documentation.",
		Method: "Check every relative link in the documentation.", Philosophy: "Broken links mislead readers."}))

	if d := s.discover(t, "markdown anchors"); len(d.Candidates) != 0 || d.Matched != 0 {
		t.Fatalf("a word only in version 1 still matches: %+v", d)
	}
	d := s.discover(t, "broken documentation links", "repository", discA)
	if top := d.Candidates[0]; top.CanonicalKey != "docs.links.check" || top.Version != 2 || top.Goal != "Find broken links in the documentation." {
		t.Fatalf("after the revision: %+v", d.Candidates)
	}
	resp := s.call(t, http.MethodGet, "/v1/bindings/"+binding+"/resolution?environment=ci", "")
	expect(t, resp, http.StatusOK, "")
	if r := decodeStrict[bindingResolutionJSON](t, resp.body); r.Graph.Version != 1 || r.SelectedBy != "evidence" {
		t.Fatalf("resolution changed: version %d by %s", r.Graph.Version, r.SelectedBy)
	}
}

func procedureRevision(p proc) string {
	b := p.body()
	var m map[string]jsontext.Value
	if err := json.Unmarshal([]byte(b), &m); err != nil {
		panic(err)
	}
	return `{"base_version":1,"version":` + string(m["version"]) + `}`
}

func TestDiscoveryOrderIsStableAndBounded(t *testing.T) {
	s := newTestServer(t)
	newCatalog(t, s)
	for _, key := range []string{"logs.rotate.daily", "logs.rotate.archive"} {
		s.mustPost(t, "/v1/procedures", proc{Key: key, Method: "Rename the file.", Philosophy: "Disks fill up."}.body())
	}
	// Equal score and matched terms: canonical key order.
	if d := s.discover(t, "rotate logs"); !slices.Equal(keysOf(d.Candidates), []string{"logs.rotate.archive", "logs.rotate.daily"}) {
		t.Fatalf("tie order %v", keysOf(d.Candidates))
	}
	// Score first: one term in a key and goal (3) outranks two terms found
	// only in instructions (1 + 1).
	s.mustPost(t, "/v1/procedures", proc{Key: "optics.calibrate", Goal: "Calibrate the instrument.", Method: "m", Philosophy: "p"}.body())
	s.mustPost(t, "/v1/procedures", proc{Key: "observatory.notes", Method: "m", Philosophy: "p",
		Instructions: map[string]any{"steps": []any{"Clean the telescope and its mirrors."}}}.body())
	if d := s.discover(t, "calibrate telescope mirrors"); !slices.Equal(keysOf(d.Candidates), []string{"optics.calibrate", "observatory.notes"}) ||
		d.Candidates[0].Score != 3 || d.Candidates[1].MatchedTerms != 2 {
		t.Fatalf("score order %+v", d.Candidates)
	}

	const task = "run the build and the checks of a module at one commit"
	full := s.discover(t, task)
	if full.Matched != len(full.Candidates) || full.Matched < 4 {
		t.Fatalf("matched %d, %d candidates", full.Matched, len(full.Candidates))
	}
	for i := 1; i < len(full.Candidates); i++ {
		a, b := full.Candidates[i-1], full.Candidates[i]
		if a.Score < b.Score || a.Score == b.Score && (a.MatchedTerms < b.MatchedTerms || a.MatchedTerms == b.MatchedTerms && a.CanonicalKey > b.CanonicalKey) {
			t.Fatalf("out of order at %d: %+v then %+v", i, a, b)
		}
	}
	first := s.call(t, http.MethodGet, discoveryPath(task, "limit", "2"), "")
	again := s.call(t, http.MethodGet, discoveryPath(task, "limit", "2"), "")
	limited := decodeStrict[discoveryJSON](t, first.body)
	if string(first.body) != string(again.body) || limited.Matched != full.Matched ||
		!slices.Equal(keysOf(limited.Candidates), keysOf(full.Candidates[:2])) {
		t.Fatalf("limit 2: %s", first.body)
	}

	// Without a limit, at most 10.
	for i := range 12 {
		s.mustPost(t, "/v1/procedures", proc{Key: "bulk.rotate.p" + string(rune('a'+i)), Method: "Rotate it.", Philosophy: "p"}.body())
	}
	if d := s.discover(t, "rotate"); len(d.Candidates) != 10 || d.Matched != 14 {
		t.Fatalf("default limit: %d candidates of %d", len(d.Candidates), d.Matched)
	}
	if d := s.discover(t, "rotate", "limit", "50"); len(d.Candidates) != 14 {
		t.Fatalf("limit 50: %d candidates", len(d.Candidates))
	}
}

func TestDiscoveryRequestRules(t *testing.T) {
	s := newTestServer(t)
	newCatalog(t, s)
	for _, tc := range []struct {
		path, field string
	}{
		{"/v1/procedures/discovery", "task"},
		{discoveryPath("   "), "task"},
		{discoveryPath("the and of a"), "task"},
		{discoveryPath("?! — …"), "task"},
		{discoveryPath(strings.Repeat("a", 4097)), "task"},
		{discoveryPath("build", "repository", "GitHub.com/O/A"), "repository"},
		{discoveryPath("build", "repository", ""), "repository"},
		{discoveryPath("build", "limit", "0"), "limit"},
		{discoveryPath("build", "limit", "51"), "limit"},
		{discoveryPath("build", "limit", "ten"), "limit"},
	} {
		resp := s.call(t, http.MethodGet, tc.path, "")
		expect(t, resp, http.StatusBadRequest, "invalid_request")
		if got := fieldsOf(t, resp); !slices.Equal(got, []string{tc.field}) {
			t.Errorf("%s: fields %v, want [%s]; %s", tc.path, got, tc.field, resp.body)
		}
	}
	expect(t, s.call(t, http.MethodGet, discoveryPath("build", "q", "x"), ""), http.StatusBadRequest, "invalid_request")
	expect(t, s.call(t, http.MethodPost, discoveryPath("build"), `{}`), http.StatusMethodNotAllowed, "method_not_allowed")

	// Searchable words that match nothing: an empty result, not an error.
	d := s.discover(t, "quantum chromodynamics lattice")
	if len(d.Candidates) != 0 || d.Matched != 0 || !slices.Equal(d.Terms, []string{"quantum", "chromodynamics", "lattice"}) {
		t.Fatalf("no match: %+v", d)
	}
	if resp := s.call(t, http.MethodGet, discoveryPath(strings.Repeat("a", 4096)), ""); resp.status != http.StatusOK {
		t.Fatalf("a 4096-byte task: %d %s", resp.status, resp.body)
	}
}

func (s *testServer) duplicates(t *testing.T, body map[string]any) duplicatesJSON {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp := s.call(t, http.MethodPost, "/v1/procedures/duplicates", string(b))
	expect(t, resp, http.StatusOK, "")
	return decodeStrict[duplicatesJSON](t, resp.body)
}

func suggestedKeys(d duplicatesJSON) []string {
	out := make([]string, len(d.Suggestions))
	for i, s := range d.Suggestions {
		out[i] = s.CanonicalKey
	}
	return out
}

// compileProposal says what go.module.build says, in other words.
var compileProposal = map[string]any{
	"canonical_key": "golang.compile",
	"goal":          "Compile every package of a Go module with the toolchain that go.mod declares.",
	"method":        "Check the toolchain version, run the build command from the module root and confirm the binaries exist.",
	"philosophy":    "Only a build with the declared toolchain that produces the later artifacts is evidence.",
}

func with(base map[string]any, kv ...any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == nil {
			delete(out, kv[i].(string))
			continue
		}
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func TestDuplicateSuggestions(t *testing.T) {
	s := newTestServer(t)
	c := newCatalog(t, s)
	before := s.call(t, http.MethodGet, "/v1/procedures", "").body

	// Overlapping content under another key: advisory, explained.
	d := s.duplicates(t, compileProposal)
	if d.KeyCollision != nil || len(d.Suggestions) != 1 || d.Matched != 1 {
		t.Fatalf("overlap: %+v", d)
	}
	sg := d.Suggestions[0]
	if sg.CanonicalKey != "go.module.build" || sg.ProcedureID != c.ids["go.module.build"] || sg.Version != 1 || sg.Scope != "shared" ||
		sg.Similarity < 0.35 || sg.SharedTerms < 10 || d.ProposalTerms < sg.SharedTerms {
		t.Fatalf("suggestion %+v", sg)
	}
	fields := map[string]bool{}
	for _, m := range sg.Matches {
		fields[m.Field] = true
		if m.Field == "contract" || m.Field == "instructions" {
			t.Fatalf("duplicates compare only the summary: %+v", sg.Matches)
		}
	}
	if !fields["goal"] || !fields["method"] || !fields["philosophy"] {
		t.Fatalf("explanation %+v", sg.Matches)
	}

	// Unrelated content: nothing.
	unrelated := map[string]any{"canonical_key": "guide.translate", "goal": "Translate a user guide into French.",
		"method":     "Split the guide into sections, translate each one and review the terminology with a native speaker.",
		"philosophy": "Readers understand instructions best in their own language."}
	if d := s.duplicates(t, unrelated); len(d.Suggestions) != 0 || d.Matched != 0 || d.KeyCollision != nil {
		t.Fatalf("unrelated: %+v", d)
	}

	// The exact key is an identity collision, reported apart from overlap;
	// creation is still refused by the existing rule.
	d = s.duplicates(t, with(unrelated, "canonical_key", "go.module.build"))
	if d.KeyCollision == nil || d.KeyCollision.ProcedureID != c.ids["go.module.build"] || d.KeyCollision.CanonicalKey != "go.module.build" ||
		d.KeyCollision.LatestVersion != 1 || len(d.Suggestions) != 0 {
		t.Fatalf("key collision: %+v", d)
	}
	expect(t, s.call(t, http.MethodPost, "/v1/procedures", proc{Key: "go.module.build", Method: "m", Philosophy: "p"}.body()),
		http.StatusConflict, "canonical_key_exists")

	// Checking a revision of go.module.build: it is excluded, so neither its
	// key nor its content is reported.
	revision := with(compileProposal, "canonical_key", "go.module.build", "exclude_procedure_id", c.ids["go.module.build"])
	if d := s.duplicates(t, revision); d.KeyCollision != nil || slices.Contains(suggestedKeys(d), "go.module.build") {
		t.Fatalf("excluded procedure reported: %+v", d)
	}
	if d := s.duplicates(t, with(revision, "exclude_procedure_id", nil)); d.KeyCollision == nil || suggestedKeys(d)[0] != "go.module.build" {
		t.Fatalf("without the exclusion: %+v", d)
	}

	// Repository applicability: a local procedure of A is a candidate in A
	// only; a shared one everywhere, so it is offered for reuse in B too.
	migrate := map[string]any{"goal": "Change a SQLite schema with a numbered migration file.",
		"method":     "Add the next migration file and never edit an applied one; test an upgrade from the previous schema.",
		"philosophy": "Rows are append-only history, so a migration never rewrites them."}
	if d := s.duplicates(t, with(migrate, "repository", discA)); !slices.Equal(suggestedKeys(d), []string{"sqlite.schema.migrate"}) || d.Repository != discA {
		t.Fatalf("in A: %+v", d)
	}
	if d := s.duplicates(t, with(migrate, "repository", discB)); len(d.Suggestions) != 0 {
		t.Fatalf("in B: %+v", d)
	}
	if d := s.duplicates(t, with(compileProposal, "repository", discB)); !slices.Equal(suggestedKeys(d), []string{"go.module.build"}) {
		t.Fatalf("shared in B: %+v", d)
	}

	// Nothing was written.
	if after := s.call(t, http.MethodGet, "/v1/procedures", "").body; string(after) != string(before) {
		t.Fatalf("the catalog changed:\n%s\n%s", before, after)
	}
}

// TestDuplicateThresholdIsExact: with 20 terms on each side, 7 shared terms
// are a similarity of exactly 0.35 and suggested; 6 are 0.30 and not, even
// though the candidate's instructions hold every other word of the proposal.
func TestDuplicateThresholdIsExact(t *testing.T) {
	s := newTestServer(t)
	words := strings.Fields("alpha bravo charlie delta echo foxtrot golf hotel india juliett kilo lima mike november oscar papa quebec romeo")
	others := strings.Fields("sierra tango uniform victor whiskey xray yankee zulu amber basil cedar dune fjord glacier")
	s.mustPost(t, "/v1/procedures", proc{Key: "nato.one", Method: strings.Join(words[:9], " "), Philosophy: strings.Join(words[9:], " "),
		Instructions: map[string]any{"steps": []any{strings.Join(others, " ")}}}.body())
	at := func(shared int) duplicatesJSON {
		return s.duplicates(t, map[string]any{"method": strings.Join(words[:shared], " "), "philosophy": strings.Join(others[:20-shared], " ")})
	}
	if d := at(7); d.ProposalTerms != 20 || len(d.Suggestions) != 1 || d.Suggestions[0].Similarity != 0.35 || d.Suggestions[0].SharedTerms != 7 {
		t.Fatalf("7 of 20 shared: %+v", d)
	}
	if d := at(6); len(d.Suggestions) != 0 {
		t.Fatalf("6 of 20 shared: %+v", d)
	}
}

func TestDuplicateRequestRules(t *testing.T) {
	s := newTestServer(t)
	newCatalog(t, s)
	for _, tc := range []struct {
		body   string
		fields []string
	}{
		{`{}`, []string{"method", "philosophy"}},
		{`{"method":"  ","philosophy":"p"}`, []string{"method"}},
		{`{"method":"the and","philosophy":"of a"}`, []string{"method"}},
		{`{"canonical_key":"Go.Build","method":"m build","philosophy":"p"}`, []string{"canonical_key"}},
		{`{"goal":"two\nlines","method":"build","philosophy":"p"}`, []string{"goal"}},
		{`{"method":"build","philosophy":"p","repository":"https://x"}`, []string{"repository"}},
		{`{"method":"build","philosophy":"p","limit":51}`, []string{"limit"}},
		{`{"method":"build","philosophy":"p","exclude_procedure_id":"01a00000-0000-7000-8000-000000000000"}`, []string{"exclude_procedure_id"}},
	} {
		resp := s.call(t, http.MethodPost, "/v1/procedures/duplicates", tc.body)
		expect(t, resp, http.StatusBadRequest, "invalid_request")
		if got := fieldsOf(t, resp); !slices.Equal(got, tc.fields) {
			t.Errorf("%s: fields %v, want %v", tc.body, got, tc.fields)
		}
	}
	expect(t, s.call(t, http.MethodPost, "/v1/procedures/duplicates", `{"method":"m","philosophy":"p","contract":{}}`), http.StatusBadRequest, "invalid_request")
	expect(t, s.call(t, http.MethodGet, "/v1/procedures/duplicates", ""), http.StatusNotFound, "not_found")
}
