package http_test

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"net/http"
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// TestChildInputsFollowTheReferenceMapping covers ADR-0024 end to end: a
// child linked to a parent must have run with the inputs the parent's
// reference maps, on write and, for stored links, on every derived read.
func TestChildInputsFollowTheReferenceMapping(t *testing.T) {
	s := newTestServer(t)
	const repo, env = "github.com/o/r", "ci"
	c1, c2 := scopeCommit, "fedcba9876543210fedcba9876543210fedcba98"
	build := idOf(t, s.mustPost(t, "/v1/procedures", procedureBody("m.build", "")))
	lint := idOf(t, s.mustPost(t, "/v1/procedures", procedureBody("m.lint", "")))
	refs := fmt.Sprintf(`"references":[`+
		`{"name":"build","procedure_id":%q,"version_policy":{"contextual":{}},"inputs":`+
		`{"service":{"input":"service"},"mode":{"value":"release"},"retries":{"value":1.0},"big":{"value":9007199254740993},"flags":{"input":"flags"}}},`+
		`{"name":"lint","procedure_id":%q,"version_policy":{"contextual":{}},"inputs":{}}],`, build, lint)
	release := idOf(t, s.mustPost(t, "/v1/procedures", procedureBody("m.release", refs)))
	s.mustPost(t, "/v1/procedures/"+release+"/versions", reviseWith(1, refs))
	children := func(b, l string) string {
		return `,"children":[{"reference":"build","execution_id":"` + b + `"},{"reference":"lint","execution_id":"` + l + `"}]`
	}
	child := func(procedureID, commit, inputs string) string {
		return idOf(t, s.mustPost(t, "/v1/executions", run(procedureID, 1, repo, commit, env, inputs, "succeeded", "")))
	}

	// Correct links: members in another order, 1 for the literal 1.0, the
	// unmapped "flags" omitted, and {} for the empty mapping.
	goodBuild := child(build, c1, `{"retries":1,"big":9007199254740993,"mode":"release","service":"a"}`)
	goodLint := child(lint, c1, `{}`)
	goodParent := idOf(t, s.mustPost(t, "/v1/executions", run(release, 1, repo, c1, env, `{"service":"a"}`, "succeeded", children(goodBuild, goodLint))))
	if v, _ := s.verification(t, goodParent); !v.Verified {
		t.Fatalf("a parent whose children match the mapping is not verified: %+v", v)
	}

	// New mismatched links are refused, each naming its child.
	for name, c := range map[string]struct{ build, lint, field string }{
		"another service":      {`{"retries":1,"big":9007199254740993,"mode":"release","service":"b"}`, `{}`, "children[0].execution_id"},
		"an unmapped member":   {`{"retries":1,"big":9007199254740993,"mode":"release","service":"a","flags":"-v"}`, `{}`, "children[0].execution_id"},
		"a missing member":     {`{"retries":1,"big":9007199254740993,"service":"a"}`, `{}`, "children[0].execution_id"},
		"a distinct large int": {`{"retries":1,"big":9007199254740992,"mode":"release","service":"a"}`, `{}`, "children[0].execution_id"},
		"an empty mapping":     {`{"retries":1,"big":9007199254740993,"mode":"release","service":"a"}`, `{"x":1}`, "children[1].execution_id"},
	} {
		b, l := child(build, c1, c.build), child(lint, c1, c.lint)
		resp := s.call(t, http.MethodPost, "/v1/executions", run(release, 1, repo, c1, env, `{"service":"a"}`, "succeeded", children(b, l)))
		expect(t, resp, http.StatusBadRequest, "invalid_request")
		if got := errorOf(t, resp).Fields; len(got) != 1 || got[0].Field != c.field {
			t.Errorf("%s: fields %+v, want %s", name, got, c.field)
		}
	}
	resp := s.call(t, http.MethodPost, "/v1/executions", run(release, 1, repo, c1, env, `{"service":"a"}`, "succeeded",
		children(child(build, c1, `{"service":"b"}`), goodLint)))
	expect(t, resp, http.StatusBadRequest, "invalid_request")
	if got, want := errorOf(t, resp).Fields[0].Message,
		`ran with inputs {"service":"b"}, but reference "build" maps the parent's inputs to {"big":9007199254740993,"mode":"release","retries":1,"service":"a"} (ADR-0024)`; got != want {
		t.Fatalf("message = %s\nwant      %s", got, want)
	}

	// A historical record, written before the rule: version 2 of the parent
	// at c2 linked a successful build of service b. It is stored through the
	// store, below the service's validation, as an older build would have.
	wrongBuild := child(build, c2, `{"retries":1,"big":9007199254740993,"mode":"release","service":"b"}`)
	lint2 := child(lint, c2, `{}`)
	historical := memory.Execution{ID: "historical-parent", ExecutionRecord: memory.ExecutionRecord{
		ProcedureID: release, Version: 2, Repository: repo, Commit: c2,
		Environment: memory.Environment{Name: env, Attributes: jsontext.Value(`{}`)},
		Inputs:      jsontext.Value(`{"service":"a"}`), Outcome: memory.OutcomeSucceeded, Evidence: jsontext.Value(`{"exit":0}`),
		Children: []memory.ChildExecution{{Reference: "build", ExecutionID: wrongBuild}, {Reference: "lint", ExecutionID: lint2}},
	}}
	if err := s.store.CreateExecution(context.Background(), &historical); err != nil {
		t.Fatal(err)
	}

	// It stays readable and unchanged, but does not verify its parent.
	stored := decodeStrict[executionJSON](t, s.call(t, http.MethodGet, "/v1/executions/historical-parent", "").body)
	if stored.Outcome != "succeeded" || len(stored.Children) != 2 || stored.Children[0].ExecutionID != wrongBuild {
		t.Fatalf("the historical execution reads back as %+v", stored)
	}
	v, _ := s.verification(t, "historical-parent")
	if v.Verified || len(v.Problems) != 1 || v.Problems[0].Code != "child_inputs_mismatch" ||
		v.Problems[0].Reference != "build" || v.Problems[0].ExecutionID != wrongBuild {
		t.Fatalf("historical verification = %+v, want one child_inputs_mismatch on build", v)
	}
	if list := s.combinations(t, release, 2, repo); len(list.Verifications) != 1 || list.Verifications[0].Verified {
		t.Fatalf("the historical combination: %+v", list)
	}

	// Contextual selection skips it: version 1, verified by the correct
	// parent, is the highest verified version, not version 2.
	binding := idOf(t, s.mustPost(t, "/v1/bindings", bindRequest(repo, "release", release, `{"contextual":{}}`)))
	res := decodeStrict[bindingResolutionJSON](t, s.call(t, http.MethodGet, "/v1/bindings/"+binding+"/resolution?environment="+env, "").body)
	if res.Graph.Version != 1 || res.Graph.VerifiedBy != goodParent {
		t.Fatalf("contextual selection = version %d verified by %q, want version 1 by %s", res.Graph.Version, res.Graph.VerifiedBy, goodParent)
	}

	// Target verification at c2 reports the parent unverified, like the
	// build its mapping implies.
	tv := s.target(t, release, 2, repo, env, c2, `{"service":"a"}`)
	if tv.TargetVerification.Verified || tv.TargetVerification.LatestExecutionID != "historical-parent" {
		t.Fatalf("target verification of the historical parent: %+v", tv.TargetVerification)
	}
	if b := tv.References[0].Node.TargetVerification; b.Verified {
		t.Fatalf("the mapped build is verified at c2: %+v", b)
	}

	// Nested: a grandparent whose own link is correct is still unverified,
	// because its child is.
	ship := idOf(t, s.mustPost(t, "/v1/procedures", procedureBody("m.ship",
		`"references":[{"name":"release","procedure_id":"`+release+`","version_policy":{"contextual":{}},"inputs":{"service":{"input":"service"}}}],`)))
	grandparent := idOf(t, s.mustPost(t, "/v1/executions", run(ship, 1, repo, c2, env, `{"service":"a"}`, "succeeded",
		`,"children":[{"reference":"release","execution_id":"historical-parent"}]`)))
	if g, _ := s.verification(t, grandparent); g.Verified || len(g.Problems) != 1 || g.Problems[0].Code != "child_not_verified" {
		t.Fatalf("grandparent verification = %+v, want child_not_verified", g)
	}
}
