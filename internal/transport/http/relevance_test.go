package http_test

import (
	"net/http"
	"slices"
	"testing"
)

// Relevance (ADR-0033): rarity over the candidates, purpose first, long prose
// weighs less, detail capped, distinct terms only.

func (s *testServer) relevanceCatalog(t *testing.T) map[string]string {
	t.Helper()
	ids := map[string]string{}
	for _, p := range []proc{
		{Key: "release.publish", Goal: "Publish a prerelease from a tag.",
			Method: "Tag the commit and let the workflow package the archives.", Philosophy: "Users test what they download."},
		{Key: "repo.onboard",
			Goal: "Onboard a repository onto the procedure catalog: register the repository, connect the agent, find procedures, " +
				"record executions, write new procedures, review records and leave short guidance for later work in the repository.",
			Method: "Register the repository, list its procedures and bindings, run one task through recorded executions, " +
				"and write a new procedure only when no record covers the task.",
			Philosophy:   "Knowledge belongs in procedure records, not in a repository's own files.",
			Instructions: map[string]any{"steps": []any{"When a release is due, publish a prerelease and tag it.", "Write the prerelease notes."}}},
		{Key: "records.audit", Goal: "Audit the procedure records of a repository.", Method: "List the records and compare them.", Philosophy: "p"},
		{Key: "bindings.review", Goal: "Review the procedure bindings of a repository.", Method: "Read each binding.", Philosophy: "p"},
		{Key: "archives.rotate.once", Goal: "Rotate old archives.", Method: "Move archives aside.", Philosophy: "p"},
		{Key: "archives.rotate.twice", Goal: "Rotate old archives. Rotate old archives, rotate them.", Method: "Move archives aside, move archives.", Philosophy: "p"},
	} {
		ids[p.Key] = idOf(t, s.mustPost(t, "/v1/procedures", p.body()))
	}
	return ids
}

func contributionOf(c candidateJSON, term string) (string, float64) {
	for _, k := range c.Contributions {
		if k.Term == term {
			return k.Field, k.Points
		}
	}
	return "", 0
}

func candidateNamed(d discoveryJSON, key string) candidateJSON {
	for _, c := range d.Candidates {
		if c.CanonicalKey == key {
			return c
		}
	}
	return candidateJSON{}
}

func TestDiscoveryPrefersStatedPurpose(t *testing.T) {
	s := newTestServer(t)
	s.relevanceCatalog(t)

	// The broad procedure mentions publishing a prerelease in its
	// instructions and the repository and procedures throughout its long
	// goal; the specific one states the task as its goal.
	d := s.discover(t, "publish a prerelease of the repository procedures")
	if got := keysOf(d.Candidates); len(got) < 2 || got[0] != "release.publish" {
		t.Fatalf("order %v", got)
	}
	broad := candidateNamed(d, "repo.onboard")
	if f, _ := contributionOf(broad, "publish"); f != "instructions" || broad.DetailPoints == 0 || broad.Score >= d.Candidates[0].Score {
		t.Fatalf("broad candidate %+v", broad)
	}

	// A term most candidates contain counts less than a distinguishing one,
	// in the same field.
	rarity := map[string]float64{}
	for _, r := range d.Rarity {
		rarity[r.Term] = r.Rarity
	}
	if d.Considered != 6 || rarity["repository"] >= rarity["prerelease"] {
		t.Fatalf("considered %d, rarity %+v", d.Considered, d.Rarity)
	}
	audit := s.discover(t, "audit repository records")
	top := audit.Candidates[0]
	_, generic := contributionOf(top, "repository")
	field, specific := contributionOf(top, "audit")
	if top.CanonicalKey != "records.audit" || generic >= specific || field != "canonical_key" {
		t.Fatalf("generic %v vs distinguishing %v (credited to %s) in %+v", generic, specific, field, top)
	}

	// Instruction-only matches are still found, with their points capped.
	only := s.discover(t, "prerelease notes")
	if c := candidateNamed(only, "repo.onboard"); c.CanonicalKey == "" || c.Score > only.DetailCap || c.DetailPoints <= 0 {
		t.Fatalf("instruction-only match %+v, cap %v", only.Candidates, only.DetailCap)
	}

	// Many rare words in a long instruction document add up to the cap only,
	// so one of them in another procedure's goal ranks first.
	s.mustPost(t, "/v1/procedures", proc{Key: "manual.everything", Method: "Read the manual.", Philosophy: "p",
		Instructions: map[string]any{"steps": []any{"Check the lantern, the quiver, the saddle, the tundra map, the violet flag and the walnut box."}}}.body())
	s.mustPost(t, "/v1/procedures", proc{Key: "gear.check", Goal: "Check the lantern.", Method: "Look at it.", Philosophy: "p"}.body())
	capped := s.discover(t, "lantern quiver saddle tundra violet walnut")
	manual := candidateNamed(capped, "manual.everything")
	if keysOf(capped.Candidates)[0] != "gear.check" || manual.DetailPoints <= capped.DetailCap || manual.Score > capped.DetailCap+0.01 {
		t.Fatalf("detail cap %v: %+v", capped.DetailCap, capped.Candidates)
	}
}

func TestDiscoveryCountsDistinctTermsOnly(t *testing.T) {
	s := newTestServer(t)
	s.relevanceCatalog(t)
	// Repeating words in a procedure changes nothing: both have the same
	// distinct terms per field, so they tie and order by canonical key.
	d := s.discover(t, "rotate archives")
	once, twice := candidateNamed(d, "archives.rotate.once"), candidateNamed(d, "archives.rotate.twice")
	if once.Score == 0 || once.Score != twice.Score || !slices.Equal(keysOf(d.Candidates)[:2], []string{"archives.rotate.once", "archives.rotate.twice"}) {
		t.Fatalf("repetition changed the ranking: %+v", d.Candidates)
	}
	// Repeating words in the task changes nothing either.
	again := s.call(t, http.MethodGet, discoveryPath("rotate rotate archives ARCHIVES rotating"), "")
	plain := s.call(t, http.MethodGet, discoveryPath("rotate archives"), "")
	a, p := decodeStrict[discoveryJSON](t, again.body), decodeStrict[discoveryJSON](t, plain.body)
	for i := range p.Candidates {
		if a.Candidates[i].CanonicalKey != p.Candidates[i].CanonicalKey || a.Candidates[i].Score != p.Candidates[i].Score {
			t.Fatalf("a repeated task word changed the ranking:\n%s\n%s", again.body, plain.body)
		}
	}
}

func TestScopedDiscoveryIgnoresOtherRepositoriesProcedures(t *testing.T) {
	s := newTestServer(t)
	s.relevanceCatalog(t)
	a, b := s.register(t, discA), s.register(t, discB)
	const task = "rotate the archives of a repository"
	path := discoveryPath(task, "repository", discA)
	before := s.call(t, http.MethodGet, path, "").body
	unscoped := decodeStrict[discoveryJSON](t, s.call(t, http.MethodGet, discoveryPath(task), "").body)

	// A procedure local to B, full of the task's words, is not a candidate in
	// A and changes neither the corpus nor any score there.
	s.mustPost(t, "/v1/procedures", proc{Key: "b.archives.rotate", Goal: "Rotate the archives of the repository.", Method: "Rotate archives.",
		Philosophy: "p", Applicability: map[string]any{"repository": b}}.body())
	if after := s.call(t, http.MethodGet, path, "").body; string(after) != string(before) {
		t.Fatalf("a procedure local to B changed A's discovery:\n%s\n%s", before, after)
	}
	if d := decodeStrict[discoveryJSON](t, s.call(t, http.MethodGet, discoveryPath(task), "").body); d.Considered != unscoped.Considered+1 ||
		slices.Equal(d.Rarity[0:1], unscoped.Rarity[0:1]) {
		t.Fatalf("without a repository the new procedure must count: %+v vs %+v", d.Rarity, unscoped.Rarity)
	}
	// A procedure local to A does.
	s.mustPost(t, "/v1/procedures", proc{Key: "a.archives.rotate", Goal: "Rotate the archives.", Method: "m", Philosophy: "p",
		Applicability: map[string]any{"repository": a}}.body())
	if d := decodeStrict[discoveryJSON](t, s.call(t, http.MethodGet, path, "").body); d.Considered != 7 {
		t.Fatalf("A's corpus: %d", d.Considered)
	}
}
