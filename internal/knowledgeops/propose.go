package knowledgeops

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/reasoning"
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// ProposeTimeout bounds one propose call: it resolves every record in the
// candidate catalog once, for the novelty-diff, on top of the usual
// candidate validation.
const ProposeTimeout = 30 * time.Second

// Proposer answers /mcp/agent/v4's propose tool (ADR-0059 Parts 2-3):
// agent-proposed knowledge, grounded by citation or by composing only
// already-existing Procedures (Tier 1), committed immediately through the
// same Kernel.Publish the knowledge-publish CLI already uses -- no draft
// state, no queue, no operator step, a deliberate decision (see
// ADR-0059's Context for why one was considered and rejected).
type Proposer struct {
	kernel *knowledge.Kernel
	ev     reasoning.Evaluator
	trust  policy.Trust
	now    func() time.Time
}

// NewProposer builds a Proposer. ev and trust are the same reasoning
// evaluator and operator-held trust material Resolver and Guide already
// use: the non-vacuous composition gate (#173) and the novelty-diff both
// need a real Evaluator to resolve against the candidate catalog.
func NewProposer(kernel *knowledge.Kernel, ev reasoning.Evaluator, trust policy.Trust) *Proposer {
	return &Proposer{kernel: kernel, ev: ev, trust: trust, now: time.Now}
}

// Propose validates in's shape, builds the candidate it describes, and
// checks it before committing anything (ADR-0059): citation
// self-consistency (skipped when there is no new value content to
// ground at all — a bare Definition change, or an ADR-0060 decline with
// only *Complete flags set, needs no citation), ADR-0057's completeness
// (run by Kernel.Validate's own candidate construction, reused here
// rather than duplicated), the non-vacuous composition gate (#173,
// scoped to pairs this proposal's own key touches, never the whole
// catalog — skipped only for a bare Definition change, which Compose
// never reads at all; a *Complete-flag-only decline still runs it,
// since Compose's own verdict for an already-vacuous pair is genuinely
// unchanged by one), and a novelty-diff (skipped whenever no new,
// non-empty value is given at all, flags or not — a flag-only
// decline's value is in being stored for later audit, not in changing
// any resolution today, so it is never asked to pass that standard).
// Passing every applicable check, it commits immediately through
// Kernel.Publish and returns the new snapshot.
func (p *Proposer) Propose(ctx context.Context, in wire.ProposeRequest) (wire.ProposeResult, error) {
	if in.Proposer == "" {
		return wire.ProposeResult{}, requiredField("proposer")
	}
	target, err := classifyPropose(in)
	if err != nil {
		return wire.ProposeResult{}, err
	}
	base, err := p.kernel.Active(ctx)
	if err != nil {
		return wire.ProposeResult{}, classify(err)
	}
	prop, key, err := target.buildProposal(base, in)
	if err != nil {
		return wire.ProposeResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, ProposeTimeout)
	defer cancel()
	candidate, err := p.kernel.Validate(ctx, prop)
	if err != nil {
		return wire.ProposeResult{}, classify(err)
	}
	if target.touchesComposition() {
		if problems, err := p.checkOwnComposition(ctx, candidate, key); err != nil {
			return wire.ProposeResult{}, err
		} else if len(problems) > 0 {
			return wire.ProposeResult{}, classify(&knowledge.ValidationError{Problems: problems})
		}
	}
	if target.hasNewValues() {
		novel, err := p.isNovel(ctx, base, candidate)
		if err != nil {
			return wire.ProposeResult{}, err
		}
		if !novel {
			return wire.ProposeResult{}, invalidField("redundant", fmt.Sprintf(
				"%s changes nothing any resolution in the catalog already concludes: not accepted as new knowledge", key))
		}
	}
	snap, err := p.kernel.Publish(ctx, prop)
	if err != nil {
		return wire.ProposeResult{}, classify(err)
	}
	return wire.ProposeResult{Snapshot: string(snap), Key: key}, nil
}

// checkOwnComposition runs #173's non-vacuous composition check, scoped
// to only the pairs this proposal's own key touches, as a producer or a
// consumer -- never the whole catalog. Found while implementing, not
// assumed: the live corpus already has pre-existing vacuous pairs
// unrelated to any one proposal (#176's own authoring backlog); checking
// every pair in the catalog would make propose unusable for anything,
// including fixing those very pairs, until every one of them happened to
// be fixed first -- the same bootstrapping deadlock #173/#176 already
// named for Kernel.publish itself, reproduced one layer up if this
// reused CheckComposition's whole-catalog sweep unscoped.
func (p *Proposer) checkOwnComposition(ctx context.Context, candidate *knowledge.Catalog, key string) ([]knowledge.Problem, error) {
	var problems []knowledge.Problem
	for _, pair := range reasoning.PrecedesAdjacentPairs(candidate) {
		if pair.Producer != key && pair.Consumer != key {
			continue
		}
		res, err := reasoning.Compose(ctx, candidate, p.ev, pair.Producer, pair.Consumer, reasoning.Limits{})
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(res.Findings, func(f reasoning.Finding) bool { return f.Status == "established" || f.Status == "contradicted" }) {
			continue
		}
		problems = append(problems, knowledge.Problem{
			Field: fmt.Sprintf("procedures[%s].composes[%s->%s]", pair.Parent, pair.Producer, pair.Consumer),
			Message: fmt.Sprintf(
				"is vacuous: %s's own postconditions, outputs and invariants establish or contradict none of %s's preconditions", pair.Producer, pair.Consumer),
		})
	}
	return problems, nil
}

// isNovel reports whether candidate's resolution of any record differs
// from base's (a brand new key, absent from base, counts as different
// without needing to resolve it there). Stops at the first difference
// found: proving non-redundancy needs only one, not an exhaustive scan.
//
// Plain Resolve alone is not enough: Obligations/Unknowns derive from
// Requires and Rules, never directly from Preconditions, Postconditions
// or Invariants (found while implementing this check, not assumed —
// those three only ever surface through Compose, reasoning/compose.go's
// own established/contradicted/missing findings). Adding one of them to
// an existing Procedure, exactly #176's own motivating case, would
// therefore show no difference in that key's own plain Resolve at all,
// and be wrongly refused as redundant. So isNovel also diffs every
// PrecedesAdjacentPairs (#173's own enumeration) Compose result, which
// does read those three fields directly.
func (p *Proposer) isNovel(ctx context.Context, base, candidate *knowledge.Catalog) (bool, error) {
	at := p.now().UTC().Truncate(time.Second)
	for _, key := range candidate.RecordKeys() {
		after, err := reasoning.Resolve(ctx, candidate, p.ev, p.trust, reasoning.Request{Goal: key, At: at})
		if err != nil {
			return false, err
		}
		if _, err := base.Record(key); err != nil {
			return true, nil // a brand new key: nothing to compare against
		}
		before, err := reasoning.Resolve(ctx, base, p.ev, p.trust, reasoning.Request{Goal: key, At: at})
		if err != nil {
			return false, err
		}
		if !resolutionsEqual(before, after) {
			return true, nil
		}
	}
	for _, pair := range reasoning.PrecedesAdjacentPairs(candidate) {
		if _, err := base.Record(pair.Producer); err != nil {
			return true, nil // a brand new producer or consumer: nothing to compare against
		}
		if _, err := base.Record(pair.Consumer); err != nil {
			return true, nil
		}
		after, err := reasoning.Compose(ctx, candidate, p.ev, pair.Producer, pair.Consumer, reasoning.Limits{})
		if err != nil {
			return false, err
		}
		before, err := reasoning.Compose(ctx, base, p.ev, pair.Producer, pair.Consumer, reasoning.Limits{})
		if err != nil {
			return false, err
		}
		if !compositionsEqual(before, after) {
			return true, nil
		}
	}
	return false, nil
}

// resolutionsEqual compares two Resolutions for everything except
// Snapshot and every Derivation.ID (ExplanationID is a hash of Version,
// Snapshot, Goal and the derivation's own parts — found while
// implementing, not assumed: it differs between base and candidate even
// when nothing substantive does, purely because Snapshot feeds the hash
// and Kernel.Validate's own candidate always has an empty one. Source,
// SourceType, Via and Status already capture everything the ID itself
// would, so dropping it loses no real comparison).
// resolutionsEqual compares two Resolutions for everything except
// Snapshot and every Derivation/DerivedFact.ID (ExplanationID is a hash
// of Version, Snapshot, Goal and the derivation's own parts — found
// while implementing, not assumed: it differs between base and
// candidate even when nothing substantive does, purely because Snapshot
// feeds the hash and Kernel.Validate's own candidate always has an
// empty one. Source, SourceType, Via and Status already capture
// everything the ID itself would, so dropping it loses no real
// comparison). Mutates a's and b's own Obligations/Derived slices in
// place to clear those IDs; harmless here, since isNovel never reuses
// before/after after comparing them.
func resolutionsEqual(a, b *reasoning.Resolution) bool {
	a2, b2 := *a, *b
	a2.Snapshot, b2.Snapshot = "", ""
	clearDerivedIDs(a2.Obligations, a2.Derived)
	clearDerivedIDs(b2.Obligations, b2.Derived)
	return fmt.Sprintf("%+v", a2) == fmt.Sprintf("%+v", b2)
}

func clearDerivedIDs(obligations []reasoning.Obligation, derived []reasoning.DerivedFact) {
	for i := range obligations {
		for j := range obligations[i].Derivations {
			obligations[i].Derivations[j].ID = ""
		}
	}
	for i := range derived {
		derived[i].ID = ""
	}
}

// compositionsEqual compares two CompositionResults for everything except
// Snapshot, the same reason resolutionsEqual does.
func compositionsEqual(a, b *reasoning.CompositionResult) bool {
	a2, b2 := *a, *b
	a2.Snapshot, b2.Snapshot = "", ""
	return fmt.Sprintf("%+v", a2) == fmt.Sprintf("%+v", b2)
}

// checkQuotesGrounded requires at least one citation and every citation's
// quote to appear verbatim in content (the new prose this submission
// adds, concatenated) -- the one check Polaroid can enforce without
// filesystem access to any consuming repository (ADR-0059). content
// empty (ADR-0060: a bare *Complete declaration with no real values at
// all) needs no citation: there is nothing to ground.
func checkQuotesGrounded(field string, citations []wire.ProposeCitation, content string) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	if len(citations) == 0 {
		return requiredField(field + ".citations")
	}
	for i, c := range citations {
		if c.Quote == "" {
			return requiredField(fmt.Sprintf("%s.citations[%d].quote", field, i))
		}
		if c.Source == "" {
			return requiredField(fmt.Sprintf("%s.citations[%d].source", field, i))
		}
		if !strings.Contains(content, c.Quote) {
			return invalidField(fmt.Sprintf("%s.citations[%d].quote", field, i),
				"does not appear verbatim in the new content being proposed")
		}
	}
	return nil
}
