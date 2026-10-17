package knowledgeops_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/knowledgeops"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/reasoning"
	"github.com/ashuangiras/polaroid/internal/reasoning/datalog"
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// drafterFixture publishes a small base corpus over a real Kernel and
// SQLite-backed Drafts store: comp.d-producer (precedes
// comp.d-consumer, the same vacuous shape #173's own tests use) and
// comp.d-standalone (no composed neighbor, every one of its four
// draftable fields genuinely empty, for walking the full field order).
// Returns a Drafter over it, plus the Kernel to read back committed
// results.
func drafterFixture(t *testing.T) (*knowledgeops.Drafter, *knowledge.Kernel) {
	t.Helper()
	k := knowledge.New(knowledgetest.NewMemoryStore(), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: datalog.Evaluator{}}))
	obligation := &knowledge.Record{Type: knowledge.TypeConcept, Key: "test.obligation", Label: "done", Definition: "d"}
	precondition := &knowledge.Record{Type: knowledge.TypeConcept, Key: "draft.some-precondition", Label: "x", Definition: "d"}
	standalone := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "draft.standalone", Label: "s", Definition: "d", Requires: []string{"test.obligation"}}
	producer := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "draft.producer", Label: "p", Definition: "d",
		Precedes: []string{"draft.consumer"}, Requires: []string{"test.obligation"}}
	consumer := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "draft.consumer", Label: "c", Definition: "d",
		Preconditions: []string{"draft.some-precondition"}, Requires: []string{"test.obligation"}}
	parent := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "draft.parent", Label: "parent", Definition: "d",
		Composes: []string{"draft.producer", "draft.consumer"}, Requires: []string{"test.obligation"}}
	changes := []knowledge.Change{
		{Key: "draft.standalone", Procedure: knowledgetest.SharedProc("draft.standalone", "draft.standalone")},
		{Key: "draft.producer", Procedure: knowledgetest.SharedProc("draft.producer", "draft.producer")},
		{Key: "draft.consumer", Procedure: knowledgetest.SharedProc("draft.consumer", "draft.consumer")},
		{Key: "draft.parent", Procedure: knowledgetest.SharedProc("draft.parent", "draft.parent")},
	}
	records := []knowledge.RecordChange{
		{Key: obligation.Key, Record: obligation}, {Key: precondition.Key, Record: precondition},
		{Key: standalone.Key, Record: standalone}, {Key: producer.Key, Record: producer}, {Key: consumer.Key, Record: consumer}, {Key: parent.Key, Record: parent},
	}
	if _, err := k.Publish(context.Background(), knowledge.Proposal{Changes: changes, Records: records,
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "s", Rationale: "r"}}); err != nil {
		t.Fatal(err)
	}
	proposer := knowledgeops.NewProposer(k, datalog.Evaluator{}, policy.Trust{})
	store, err := sqlite.Open(context.Background(), t.TempDir()+"/polaroid.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return knowledgeops.NewDrafter(k, proposer, memory.NewDrafts(store)), k
}

func TestDrafterWalksEveryFieldInFixedOrder(t *testing.T) {
	d, _ := drafterFixture(t)
	ctx := context.Background()

	start, err := d.Start(ctx, wire.DraftStartRequest{Key: "draft.producer", Proposer: "test.agent", Rationale: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if start.Status != "in_progress" || start.NextField != "preconditions" || start.Token == "" {
		t.Fatalf("start = %+v, want next_field preconditions", start)
	}

	res, err := d.Advance(ctx, wire.DraftAdvanceRequest{Token: start.Token,
		Outcome: &wire.DraftOutcomeArg{Field: "preconditions", ConfirmedAbsent: true, Reason: "genuinely none"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "in_progress" || res.NextField != "postconditions" {
		t.Fatalf("after preconditions decline: %+v, want next_field postconditions", res)
	}

	// The real, grounding answer: draft.consumer already declares
	// draft.some-precondition as its own precondition (checked at
	// drafterFixture's own publish); citing it here is what finally
	// makes this pair's composition non-vacuous.
	res, err = d.Advance(ctx, wire.DraftAdvanceRequest{Token: start.Token,
		Outcome: &wire.DraftOutcomeArg{Field: "postconditions", Values: []string{"draft.some-precondition"},
			Citations: []wire.ProposeCitation{{Source: "draft.consumer", Quote: "draft.some-precondition"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "in_progress" || res.NextField != "invariants" {
		t.Fatalf("after postconditions answer: %+v, want next_field invariants", res)
	}

	res, err = d.Advance(ctx, wire.DraftAdvanceRequest{Token: start.Token,
		Outcome: &wire.DraftOutcomeArg{Field: "invariants", ConfirmedAbsent: true, Reason: "genuinely none"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "in_progress" || res.NextField != "informal_conditions" {
		t.Fatalf("after invariants decline: %+v, want next_field informal_conditions", res)
	}

	// The last field: this call must auto-commit in the same round trip.
	res, err = d.Advance(ctx, wire.DraftAdvanceRequest{Token: start.Token,
		Outcome: &wire.DraftOutcomeArg{Field: "informal_conditions", ConfirmedAbsent: true, Reason: "genuinely none"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "complete" || res.Key != "draft.producer" || res.Snapshot == "" || res.NextField != "" {
		t.Fatalf("final answer must auto-commit: %+v", res)
	}
}

// TestDrafterCommittedRecordReflectsEveryAnswer confirms the committed
// record carries exactly what was answered: real content where given,
// empty-but-Complete where declined.
func TestDrafterCommittedRecordReflectsEveryAnswer(t *testing.T) {
	d, k := drafterFixture(t)
	ctx := context.Background()
	start, err := d.Start(ctx, wire.DraftStartRequest{Key: "draft.producer", Proposer: "test.agent", Rationale: "r"})
	if err != nil {
		t.Fatal(err)
	}
	token := start.Token
	for _, outcome := range []wire.DraftOutcomeArg{
		{Field: "preconditions", ConfirmedAbsent: true, Reason: "none"},
		{Field: "postconditions", Values: []string{"draft.some-precondition"}, Citations: []wire.ProposeCitation{{Source: "draft.consumer", Quote: "draft.some-precondition"}}},
		{Field: "invariants", ConfirmedAbsent: true, Reason: "none"},
		{Field: "informal_conditions", ConfirmedAbsent: true, Reason: "none"},
	} {
		if _, err := d.Advance(ctx, wire.DraftAdvanceRequest{Token: token, Outcome: &outcome}); err != nil {
			t.Fatal(err)
		}
	}
	cat, err := k.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := cat.Record("draft.producer")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.PreconditionsComplete || len(rec.Preconditions) != 0 {
		t.Fatalf("preconditions = %v, complete = %v, want empty and complete", rec.Preconditions, rec.PreconditionsComplete)
	}
	if !rec.PostconditionsComplete || len(rec.Postconditions) != 1 || rec.Postconditions[0] != "draft.some-precondition" {
		t.Fatalf("postconditions = %v, complete = %v", rec.Postconditions, rec.PostconditionsComplete)
	}
	if !rec.InvariantsComplete || !rec.InformalConditionsComplete {
		t.Fatalf("invariants complete = %v, informal_conditions complete = %v, want both true", rec.InvariantsComplete, rec.InformalConditionsComplete)
	}
}

// TestDrafterTokenNotFoundAfterCommit proves the token behaves exactly
// like a closed or expired one once it commits: no trace kept.
func TestDrafterTokenNotFoundAfterCommit(t *testing.T) {
	d, _ := drafterFixture(t)
	ctx := context.Background()
	start, err := d.Start(ctx, wire.DraftStartRequest{Key: "draft.standalone", Proposer: "test.agent", Rationale: "r"})
	if err != nil {
		t.Fatal(err)
	}
	token := start.Token
	for _, field := range []string{"preconditions", "postconditions", "invariants", "informal_conditions"} {
		if _, err := d.Advance(ctx, wire.DraftAdvanceRequest{Token: token,
			Outcome: &wire.DraftOutcomeArg{Field: field, ConfirmedAbsent: true, Reason: "none"}}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = d.Advance(ctx, wire.DraftAdvanceRequest{Token: token})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "not_found" {
		t.Fatalf("err = %v, want not_found after commit", err)
	}
}

// TestDrafterOffersAlreadyNonEmptyFieldsWithExistingValues proves
// (ADR-0061) that a field already non-empty (draft.consumer's own
// preconditions) is still offered, not skipped, with its current values
// and *Complete state surfaced in the result -- closing the gap where an
// already-published field could never be extended through the guided
// walk, only through a hand-assembled single-shot propose call.
func TestDrafterOffersAlreadyNonEmptyFieldsWithExistingValues(t *testing.T) {
	d, _ := drafterFixture(t)
	ctx := context.Background()
	start, err := d.Start(ctx, wire.DraftStartRequest{Key: "draft.consumer", Proposer: "test.agent", Rationale: "r"})
	if err != nil {
		t.Fatal(err)
	}
	// draft.consumer already has Preconditions = ["draft.some-precondition"]:
	// every draft still starts at preconditions, first in fixed order,
	// surfacing what is already there instead of skipping past it.
	if start.NextField != "preconditions" {
		t.Fatalf("next_field = %q, want preconditions (every field is offered, ADR-0061)", start.NextField)
	}
	if len(start.ExistingValues) != 1 || start.ExistingValues[0] != "draft.some-precondition" {
		t.Fatalf("existing_values = %v, want [draft.some-precondition]", start.ExistingValues)
	}
	if start.ExistingComplete {
		t.Fatalf("existing_complete = true, want false (never marked *Complete in the fixture)")
	}
}

// TestDrafterStillVacuousCommitFailsAndDraftStaysOpen proves a
// still-vacuous composition (#173) is returned as the specific error,
// and the draft remains open for correction -- confirmed by then
// supplying the real, grounding value and succeeding.
func TestDrafterStillVacuousCommitFailsAndDraftStaysOpen(t *testing.T) {
	d, k := drafterFixture(t)
	ctx := context.Background()
	start, err := d.Start(ctx, wire.DraftStartRequest{Key: "draft.producer", Proposer: "test.agent", Rationale: "r"})
	if err != nil {
		t.Fatal(err)
	}
	token := start.Token
	// Decline every field: draft.producer -> draft.consumer stays
	// exactly as vacuous as before (nothing establishes
	// draft.some-precondition), so the auto-commit attempt on the last
	// decline must fail, not silently succeed.
	for i, field := range []string{"preconditions", "postconditions", "invariants"} {
		res, err := d.Advance(ctx, wire.DraftAdvanceRequest{Token: token,
			Outcome: &wire.DraftOutcomeArg{Field: field, ConfirmedAbsent: true, Reason: "none"}})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "in_progress" {
			t.Fatalf("step %d: %+v", i, res)
		}
	}
	_, err = d.Advance(ctx, wire.DraftAdvanceRequest{Token: token,
		Outcome: &wire.DraftOutcomeArg{Field: "informal_conditions", ConfirmedAbsent: true, Reason: "none"}})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || !strings.Contains(err.Error(), "vacuous") {
		t.Fatalf("err = %v, want it naming the still-vacuous composition", err)
	}
	// The draft must still be alive and correctable: peek confirms it.
	peek, err := d.Advance(ctx, wire.DraftAdvanceRequest{Token: token})
	if err != nil {
		t.Fatalf("draft must stay open after a failed commit attempt, got %v", err)
	}
	if peek.Status != "in_progress" {
		t.Fatalf("peek after failed commit = %+v, want still in_progress", peek)
	}
	// Correct the field that was actually keeping it vacuous.
	res, err := d.Advance(ctx, wire.DraftAdvanceRequest{Token: token,
		Outcome: &wire.DraftOutcomeArg{Field: "postconditions", Values: []string{"draft.some-precondition"},
			Citations: []wire.ProposeCitation{{Source: "draft.consumer", Quote: "draft.some-precondition"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "complete" {
		t.Fatalf("after correcting the vacuous field: %+v, want complete", res)
	}
	cat, err := k.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	problems, err := reasoning.CheckComposition(ctx, cat, datalog.Evaluator{}, reasoning.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		if strings.Contains(p.Field, "draft.producer->draft.consumer") {
			t.Fatalf("the pair is still reported vacuous after correction: %+v", p)
		}
	}
}

func TestDrafterInvalidFieldNameRejected(t *testing.T) {
	d, _ := drafterFixture(t)
	ctx := context.Background()
	start, err := d.Start(ctx, wire.DraftStartRequest{Key: "draft.standalone", Proposer: "test.agent", Rationale: "r"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Advance(ctx, wire.DraftAdvanceRequest{Token: start.Token, Outcome: &wire.DraftOutcomeArg{Field: "bogus_field", ConfirmedAbsent: true}})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "invalid_request" {
		t.Fatalf("err = %v, want invalid_request", err)
	}
}

func TestDrafterNeitherOrBothValuesAndConfirmedAbsentRejected(t *testing.T) {
	d, _ := drafterFixture(t)
	ctx := context.Background()
	start, err := d.Start(ctx, wire.DraftStartRequest{Key: "draft.standalone", Proposer: "test.agent", Rationale: "r"})
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []wire.DraftOutcomeArg{
		{Field: "preconditions"}, // neither
		{Field: "preconditions", Values: []string{"x"}, ConfirmedAbsent: true}, // both
	} {
		_, err := d.Advance(ctx, wire.DraftAdvanceRequest{Token: start.Token, Outcome: &outcome})
		var we *wire.KnowledgeError
		if !errors.As(err, &we) || we.Detail.Code != "invalid_request" {
			t.Fatalf("outcome %+v: err = %v, want invalid_request", outcome, err)
		}
	}
}

func TestDrafterCloseEndsItImmediately(t *testing.T) {
	d, _ := drafterFixture(t)
	ctx := context.Background()
	start, err := d.Start(ctx, wire.DraftStartRequest{Key: "draft.standalone", Proposer: "test.agent", Rationale: "r"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.Advance(ctx, wire.DraftAdvanceRequest{Token: start.Token, Close: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "closed" {
		t.Fatalf("close result = %+v", res)
	}
	_, err = d.Advance(ctx, wire.DraftAdvanceRequest{Token: start.Token})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "not_found" {
		t.Fatalf("err = %v, want not_found after close", err)
	}
}

func TestDrafterStartUnknownKeyIsNotFound(t *testing.T) {
	d, _ := drafterFixture(t)
	_, err := d.Start(context.Background(), wire.DraftStartRequest{Key: "nope", Proposer: "test.agent", Rationale: "r"})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "not_found" {
		t.Fatalf("err = %v, want not_found", err)
	}
}

func TestDrafterStartRequiresKeyAndProposer(t *testing.T) {
	d, _ := drafterFixture(t)
	ctx := context.Background()
	if _, err := d.Start(ctx, wire.DraftStartRequest{Proposer: "test.agent"}); err == nil {
		t.Fatal("missing key must be rejected")
	}
	if _, err := d.Start(ctx, wire.DraftStartRequest{Key: "draft.standalone"}); err == nil {
		t.Fatal("missing proposer must be rejected")
	}
}

// TestDrafterAdvanceRejectsMissingCitationImmediately proves the
// citation check happens right away, on the field it belongs to, not
// deferred to the final commit several calls later.
func TestDrafterAdvanceRejectsMissingCitationImmediately(t *testing.T) {
	d, _ := drafterFixture(t)
	ctx := context.Background()
	start, err := d.Start(ctx, wire.DraftStartRequest{Key: "draft.producer", Proposer: "test.agent", Rationale: "r"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Advance(ctx, wire.DraftAdvanceRequest{Token: start.Token,
		Outcome: &wire.DraftOutcomeArg{Field: "preconditions", Values: []string{"draft.some-precondition"}}})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "invalid_request" || !strings.Contains(err.Error(), "citations") {
		t.Fatalf("err = %v, want invalid_request naming citations, rejected immediately", err)
	}
	// Confirm the draft is unaffected by the rejected answer: still on
	// the same field.
	peek, err := d.Advance(ctx, wire.DraftAdvanceRequest{Token: start.Token})
	if err != nil {
		t.Fatal(err)
	}
	if peek.NextField != "preconditions" {
		t.Fatalf("after a rejected answer, next_field = %q, want still preconditions", peek.NextField)
	}
}
