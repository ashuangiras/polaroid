package knowledgeops

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// Drafter answers /mcp/agent/v5's propose_start and propose_advance
// (ADR-0060): interactive, field-by-field construction of a single-shot
// propose (ADR-0059) request for an existing Procedure, one content
// field at a time, requiring an explicit answer -- real, citation-grounded
// content, or an explicit, reasoned decline -- for every one of its four
// fields, every draft, every time (ADR-0061): a field that already holds
// published content is still offered, with its current values surfaced,
// so new entries can be appended to it, not only to one that starts
// empty. Each Values answer's own citations are checked immediately
// (reusing Proposer's own checkQuotesGrounded), so a missing or
// unverifiable citation is rejected on the spot, not deferred to the
// final commit; the moment every field is decided, Drafter delegates the
// actual commit to Proposer.Propose unchanged -- completeness, the
// non-vacuous composition gate and the novelty-diff are Proposer's,
// reused, not duplicated.
type Drafter struct {
	kernel   *knowledge.Kernel
	proposer *Proposer
	drafts   *memory.Drafts
}

// NewDrafter builds a Drafter. proposer is the same Proposer /mcp/agent/v4
// already uses for single-shot propose; Drafter's own final commit step
// is nothing more than assembling one wire.ProposeRequest from the
// draft's accumulated answers and calling proposer.Propose with it.
func NewDrafter(kernel *knowledge.Kernel, proposer *Proposer, drafts *memory.Drafts) *Drafter {
	return &Drafter{kernel: kernel, proposer: proposer, drafts: drafts}
}

// draftableFields names, in the fixed order propose_start/propose_advance
// always offer them, each field and its accessor on a knowledge.Record:
// the values already published there (shown to the agent as context,
// never a reason to skip the field -- ADR-0061) and whether it is
// already marked *Complete (ADR-0060) from an earlier publish (also
// shown, also never a reason to skip).
type draftableField struct {
	field    memory.DraftField
	existing []string
	complete bool
}

func draftableFields(rec *knowledge.Record) []draftableField {
	return []draftableField{
		{memory.DraftFieldPreconditions, rec.Preconditions, rec.PreconditionsComplete},
		{memory.DraftFieldPostconditions, rec.Postconditions, rec.PostconditionsComplete},
		{memory.DraftFieldInvariants, rec.Invariants, rec.InvariantsComplete},
		{memory.DraftFieldInformalConditions, rec.InformalConditions, rec.InformalConditionsComplete},
	}
}

// nextField returns the first field, in draftableFields' fixed order,
// not yet answered in this draft's own accumulated answers; "" once all
// four have been. Unlike an earlier design, this never looks at the
// record's own existing content or *Complete flag to decide what to
// skip (ADR-0061): every draft walks all four fields, every time, so
// that a field already holding real content can still be extended
// through this guided tool, not only through a hand-assembled
// single-shot propose call.
func nextField(answers map[memory.DraftField]memory.DraftFieldAnswer) memory.DraftField {
	for _, f := range memory.DraftFields {
		if _, answered := answers[f]; answered {
			continue
		}
		return f
	}
	return ""
}

// Start resolves in.Key at the active snapshot (must be an existing
// Procedure), pins it for the new draft's lifetime, and returns
// {token, snapshot, next_field, existing_values, existing_complete,
// status}.
func (d *Drafter) Start(ctx context.Context, in wire.DraftStartRequest) (wire.DraftResult, error) {
	if in.Key == "" {
		return wire.DraftResult{}, requiredField("key")
	}
	if in.Proposer == "" {
		return wire.DraftResult{}, requiredField("proposer")
	}
	cat, err := d.kernel.Active(ctx)
	if err != nil {
		return wire.DraftResult{}, classify(err)
	}
	rec, err := existingProcedure(cat, in.Key)
	if err != nil {
		return wire.DraftResult{}, err
	}
	st, err := d.drafts.Start(ctx, in.Key, string(cat.Snapshot()), in.Proposer, in.Rationale)
	if err != nil {
		return wire.DraftResult{}, classify(err)
	}
	return d.resultOrCommit(ctx, rec, st, true)
}

// Advance reports in.Outcome (nil for a pure peek) on in.Token. The
// moment every field is decided -- by this call's own answer -- it
// attempts the real commit in the same call, delegating entirely to
// Proposer.Propose; a failure (still vacuous, or redundant) is returned
// as the specific error, and the draft stays open, every earlier answer
// intact, for correction. A pure peek never attempts a commit, even if
// every field already happens to be decided (found while testing, not
// assumed: re-attempting on every peek would otherwise make a plain
// peek capable of erroring, breaking the same "a peek is always safe"
// convention start/advance already established for walkthroughs) -- it
// reports NextField "" with Status still "in_progress", the one
// deliberate exception to NextField "" otherwise always meaning
// "complete": signalling "answered, but not yet committed; resubmit the
// field actually keeping it from succeeding." in.Close ends the draft
// immediately, abandoned, whatever its status, before anything else is
// attempted.
func (d *Drafter) Advance(ctx context.Context, in wire.DraftAdvanceRequest) (wire.DraftResult, error) {
	if in.Token == "" {
		return wire.DraftResult{}, requiredField("token")
	}
	if in.Close {
		if err := d.drafts.Close(ctx, in.Token); err != nil {
			return wire.DraftResult{}, classify(err)
		}
		return wire.DraftResult{Status: "closed"}, nil
	}
	answer, err := decodeDraftOutcome(in.Outcome)
	if err != nil {
		return wire.DraftResult{}, err
	}
	st, err := d.drafts.Advance(ctx, in.Token, answer)
	if err != nil {
		return wire.DraftResult{}, classify(err)
	}
	cat, err := d.kernel.At(ctx, knowledge.Snapshot(st.Snapshot))
	if err != nil {
		return wire.DraftResult{}, classify(err)
	}
	rec, err := existingProcedure(cat, st.Key)
	if err != nil {
		return wire.DraftResult{}, err
	}
	return d.resultOrCommit(ctx, rec, st, answer != nil)
}

// resultOrCommit computes st's next field to answer (ADR-0061: never
// gated on the record's own existing content, only on what this draft
// has already answered). If none remains and attemptCommit is true, it
// attempts the real commit now, through Proposer.Propose, leaving the
// draft open on failure. attemptCommit is false only for a pure peek
// (Advance with no outcome): a peek never causes a side-effecting commit
// attempt, even when nothing is left undecided.
func (d *Drafter) resultOrCommit(ctx context.Context, rec *knowledge.Record, st memory.DraftState, attemptCommit bool) (wire.DraftResult, error) {
	next := nextField(st.Answers)
	if next != "" || !attemptCommit {
		res := wire.DraftResult{Token: st.Token, Snapshot: st.Snapshot, NextField: string(next), Status: "in_progress"}
		if next != "" {
			for _, f := range draftableFields(rec) {
				if f.field == next {
					res.ExistingValues, res.ExistingComplete = f.existing, f.complete
					break
				}
			}
		}
		return res, nil
	}
	res, err := d.proposer.Propose(ctx, draftProposeRequest(st))
	if err != nil {
		return wire.DraftResult{}, err
	}
	_ = d.drafts.Close(ctx, st.Token) // committed: no trace kept from here on, the same as an explicit close
	return wire.DraftResult{Snapshot: res.Snapshot, Key: res.Key, Status: "complete"}, nil
}

// existingProcedure reads key from cat, requiring it to already exist as
// a Procedure (a Draft only ever adds to one that is already published;
// a wholly new Procedure is single-shot propose's new_orchestration, or
// operator-published, not this).
func existingProcedure(cat *knowledge.Catalog, key string) (*knowledge.Record, error) {
	rec, err := cat.Record(key)
	if err != nil {
		return nil, classify(err)
	}
	if rec.Type != knowledge.TypeProcedure {
		return nil, invalidField("key", fmt.Sprintf("%s is a %s, not a Procedure", key, rec.Type))
	}
	return rec, nil
}

// decodeDraftOutcome validates in's shape: nil is a pure peek; otherwise
// exactly one of Values or ConfirmedAbsent is required, and Field must
// be one of memory.DraftFields. A Values answer's citations are checked
// immediately, against this one field's own values (checkQuotesGrounded,
// the same check Proposer.Propose itself uses) -- so a missing or
// unverifiable citation is rejected right away, on the field it belongs
// to, rather than deferred to the final commit several calls later; the
// combined check Propose still runs at commit time is unaffected and
// necessarily passes again, since every field's own values were already
// individually grounded here.
func decodeDraftOutcome(in *wire.DraftOutcomeArg) (*memory.DraftFieldAnswer, error) {
	if in == nil {
		return nil, nil
	}
	field := memory.DraftField(in.Field)
	if !slices.Contains(memory.DraftFields, field) {
		return nil, invalidField("outcome.field", "must be preconditions, postconditions, invariants or informal_conditions")
	}
	if (len(in.Values) > 0) == in.ConfirmedAbsent {
		return nil, invalidField("outcome", "exactly one of values or confirmed_absent is required")
	}
	if len(in.Values) > 0 {
		if err := checkQuotesGrounded("outcome", in.Citations, strings.Join(in.Values, "\n")); err != nil {
			return nil, err
		}
	}
	citations := make([]memory.DraftCitation, len(in.Citations))
	for i, c := range in.Citations {
		citations[i] = memory.DraftCitation{Source: c.Source, Quote: c.Quote}
	}
	return &memory.DraftFieldAnswer{Field: field, Values: in.Values, Citations: citations, ConfirmedAbsent: in.ConfirmedAbsent, Reason: in.Reason}, nil
}

// draftProposeRequest assembles the one wire.ProposeRequest st's
// accumulated answers describe: every answered field sets its own
// *Complete flag (ADR-0060) -- true whether answered with real values or
// an explicit decline, since either way the field has now been reviewed
// -- and contributes its own citations to the combined list
// Proposer.Propose's existing citation check already validates against
// the combined new content.
func draftProposeRequest(st memory.DraftState) wire.ProposeRequest {
	fa := &wire.ProposeFieldAddition{Key: st.Key}
	for _, field := range memory.DraftFields {
		ans, ok := st.Answers[field]
		if !ok {
			continue
		}
		switch field {
		case memory.DraftFieldPreconditions:
			fa.Preconditions, fa.PreconditionsComplete = ans.Values, true
		case memory.DraftFieldPostconditions:
			fa.Postconditions, fa.PostconditionsComplete = ans.Values, true
		case memory.DraftFieldInvariants:
			fa.Invariants, fa.InvariantsComplete = ans.Values, true
		case memory.DraftFieldInformalConditions:
			fa.InformalConditions, fa.InformalConditionsComplete = ans.Values, true
		}
		for _, c := range ans.Citations {
			fa.Citations = append(fa.Citations, wire.ProposeCitation{Source: c.Source, Quote: c.Quote})
		}
	}
	return wire.ProposeRequest{Proposer: st.Proposer, Rationale: st.Rationale, ExistingProcedure: fa}
}
