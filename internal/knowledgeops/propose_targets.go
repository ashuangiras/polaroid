package knowledgeops

import (
	json "encoding/json/v2"
	"fmt"
	"strings"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// proposeTarget builds the Proposal one propose request describes, read
// against base (the active catalog at the time of the call).
type proposeTarget interface {
	buildProposal(base *knowledge.Catalog, in wire.ProposeRequest) (knowledge.Proposal, string, error)
	// affectsReasoning reports whether this target's content could ever
	// show up in a Resolve or Compose result at all. False only for an
	// existingProcedureTarget whose only new content is Definition —
	// Corpus Logic never reads it (found while implementing: Definition
	// appears in no Resolution or CompositionResult field at all), so
	// the non-vacuous composition gate (#173) and the novelty-diff
	// (which can only ever say "no difference found" for such content)
	// would both wrongly refuse it — the gate for an unrelated,
	// pre-existing vacuous pair this change could not possibly affect
	// either way, the diff as redundant, since "changes no resolution"
	// would otherwise be true of every one of them, by construction, not
	// because any particular one is actually redundant. ADR-0057's
	// completeness bar and the citation check already govern Definition
	// fully on their own.
	affectsReasoning() bool
}

// classifyPropose picks in's one target, or reports exactly the shape
// problem: none given, or more than one.
func classifyPropose(in wire.ProposeRequest) (proposeTarget, error) {
	var targets []proposeTarget
	if in.ExistingProcedure != nil {
		targets = append(targets, existingProcedureTarget{in.ExistingProcedure})
	}
	if in.NewConcept != nil {
		targets = append(targets, newRecordTarget{knowledge.TypeConcept, in.NewConcept})
	}
	if in.NewClaim != nil {
		targets = append(targets, newRecordTarget{knowledge.TypeClaim, in.NewClaim})
	}
	if in.NewOrchestration != nil {
		targets = append(targets, newOrchestrationTarget{in.NewOrchestration})
	}
	switch len(targets) {
	case 0:
		return nil, requiredField("existing_procedure, new_concept, new_claim or new_orchestration")
	case 1:
		return targets[0], nil
	default:
		return nil, invalidField("propose", "exactly one of existing_procedure, new_concept, new_claim or new_orchestration is required, not more than one")
	}
}

func baseProvenance(in wire.ProposeRequest, summary string) knowledge.Provenance {
	return knowledge.Provenance{Author: in.Proposer, Summary: summary, Rationale: in.Rationale}
}

// existingProcedureTarget adds new formal content to an existing
// Procedure: every given field's new entries are appended, each
// citation-grounded (ADR-0059 Part 2).
type existingProcedureTarget struct{ f *wire.ProposeFieldAddition }

// affectsReasoning is false exactly when only Definition is given: see
// proposeTarget's own doc comment for why.
func (t existingProcedureTarget) affectsReasoning() bool {
	f := t.f
	return len(f.Preconditions)+len(f.Postconditions)+len(f.Invariants)+len(f.InformalConditions) > 0
}

func (t existingProcedureTarget) buildProposal(base *knowledge.Catalog, in wire.ProposeRequest) (knowledge.Proposal, string, error) {
	f := t.f
	if f.Key == "" {
		return knowledge.Proposal{}, "", requiredField("existing_procedure.key")
	}
	old, err := base.Record(f.Key)
	if err != nil {
		return knowledge.Proposal{}, "", invalidField("existing_procedure.key", fmt.Sprintf("%s is not in the catalog", f.Key))
	}
	if old.Type != knowledge.TypeProcedure {
		return knowledge.Proposal{}, "", invalidField("existing_procedure.key", fmt.Sprintf("%s is a %s, not a Procedure", f.Key, old.Type))
	}
	content := strings.Join(slicesConcat(f.Definition, f.Preconditions, f.Postconditions, f.Invariants, f.InformalConditions), "\n")
	if err := checkQuotesGrounded("existing_procedure", f.Citations, content); err != nil {
		return knowledge.Proposal{}, "", err
	}
	updated := old.Clone()
	if f.Definition != "" {
		updated.Definition = f.Definition
	}
	updated.Preconditions = append(append([]string{}, updated.Preconditions...), f.Preconditions...)
	updated.Postconditions = append(append([]string{}, updated.Postconditions...), f.Postconditions...)
	updated.Invariants = append(append([]string{}, updated.Invariants...), f.Invariants...)
	updated.InformalConditions = append(append([]string{}, updated.InformalConditions...), f.InformalConditions...)
	prop := knowledge.Proposal{
		Base:       base.Snapshot(),
		Records:    []knowledge.RecordChange{{Key: f.Key, Record: updated}},
		Provenance: baseProvenance(in, "propose: new formal content on "+f.Key),
	}
	return prop, f.Key, nil
}

// slicesConcat joins a scalar and several string slices into one slice,
// for the combined content a citation's quote is checked against.
func slicesConcat(scalar string, lists ...[]string) []string {
	out := []string{scalar}
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// newRecordTarget is a new Concept or Claim (ADR-0059 Part 2): the key
// must not already exist, and its Definition/Statement must be
// citation-grounded.
type newRecordTarget struct {
	typ knowledge.RecordType
	n   *wire.ProposeNewRecord
}

// affectsReasoning is always true: the checks run and trivially pass for
// a brand new key either way (nothing to compare against), via isNovel's
// and checkOwnComposition's own shortcuts for it.
func (t newRecordTarget) affectsReasoning() bool { return true }

func (t newRecordTarget) buildProposal(base *knowledge.Catalog, in wire.ProposeRequest) (knowledge.Proposal, string, error) {
	n := t.n
	field := "new_concept"
	content := n.Definition
	if t.typ == knowledge.TypeClaim {
		field, content = "new_claim", n.Statement
	}
	if n.Key == "" {
		return knowledge.Proposal{}, "", requiredField(field + ".key")
	}
	if _, err := base.Record(n.Key); err == nil {
		return knowledge.Proposal{}, "", invalidField(field+".key", fmt.Sprintf("%s already exists", n.Key))
	}
	if err := checkQuotesGrounded(field, n.Citations, content); err != nil {
		return knowledge.Proposal{}, "", err
	}
	record := &knowledge.Record{Type: t.typ, Key: n.Key, Label: n.Label, Definition: n.Definition, Statement: n.Statement}
	prop := knowledge.Proposal{
		Base:       base.Snapshot(),
		Records:    []knowledge.RecordChange{{Key: n.Key, Record: record}},
		Provenance: baseProvenance(in, fmt.Sprintf("propose: new %s %s", t.typ, n.Key)),
	}
	return prop, n.Key, nil
}

// newOrchestrationTarget is a wholly new Procedure whose only content is
// composing Procedures that already exist, in order (Tier 1, ADR-0059
// Part 3): no citation needed at all, since it asserts no new operational
// claim beyond that ordering. Requires, if given, is a reference to an
// already-existing Concept (not new prose), to satisfy ADR-0057's
// completeness bar, which Composes alone does not.
//
// Deliberately touches only the new key: it never adds a precedes edge
// to an existing composed child's own record (that would modify other,
// unrelated records as a side effect of this proposal) -- an agent that
// wants those children strictly ordered proposes that separately, on
// each child, through existingProcedureTarget.
type newOrchestrationTarget struct{ o *wire.ProposeOrchestration }

// affectsReasoning is always true: the checks run and trivially pass for
// a brand new key either way (nothing to compare against), via isNovel's
// and checkOwnComposition's own shortcuts for it.
func (t newOrchestrationTarget) affectsReasoning() bool { return true }

func (t newOrchestrationTarget) buildProposal(base *knowledge.Catalog, in wire.ProposeRequest) (knowledge.Proposal, string, error) {
	o := t.o
	if o.Key == "" {
		return knowledge.Proposal{}, "", requiredField("new_orchestration.key")
	}
	if o.Definition == "" {
		return knowledge.Proposal{}, "", requiredField("new_orchestration.definition")
	}
	if len(o.Composes) == 0 {
		return knowledge.Proposal{}, "", requiredField("new_orchestration.composes")
	}
	if _, err := base.Record(o.Key); err == nil {
		return knowledge.Proposal{}, "", invalidField("new_orchestration.key", fmt.Sprintf("%s already exists", o.Key))
	}
	for i, k := range o.Composes {
		rec, err := base.Record(k)
		if err != nil {
			return knowledge.Proposal{}, "", invalidField(fmt.Sprintf("new_orchestration.composes[%d]", i), fmt.Sprintf("%s is not in the catalog", k))
		}
		if rec.Type != knowledge.TypeProcedure {
			return knowledge.Proposal{}, "", invalidField(fmt.Sprintf("new_orchestration.composes[%d]", i), fmt.Sprintf("%s is a %s, not a Procedure", k, rec.Type))
		}
	}
	record := &knowledge.Record{Type: knowledge.TypeProcedure, Key: o.Key, Label: o.Label, Definition: o.Definition, Composes: o.Composes, Requires: o.Requires}
	doc, err := orchestrationProcedure(o)
	if err != nil {
		return knowledge.Proposal{}, "", err
	}
	prop := knowledge.Proposal{
		Base:       base.Snapshot(),
		Changes:    []knowledge.Change{{Key: o.Key, Procedure: doc}},
		Records:    []knowledge.RecordChange{{Key: o.Key, Record: record}},
		Provenance: baseProvenance(in, "propose: new orchestration procedure "+o.Key),
	}
	return prop, o.Key, nil
}

// orchestrationProcedure builds o's matching legacy Procedure document
// (every typed Procedure record needs one, ADR-0047): its instructions
// are generated directly from Composes, in order, so they say exactly
// what the typed record's own composition already asserts, nothing more.
func orchestrationProcedure(o *wire.ProposeOrchestration) (*knowledge.Procedure, error) {
	steps := make([]string, len(o.Composes))
	for i, k := range o.Composes {
		steps[i] = fmt.Sprintf("Follow %s.", k)
	}
	instructions, err := json.Marshal(struct {
		Steps []string `json:"steps"`
	}{Steps: steps})
	if err != nil {
		return nil, err
	}
	return &knowledge.Procedure{
		Key: o.Key, Goal: o.Definition, Philosophy: "Reuse what already works, in the order that already works.",
		Method:        "Follow each composed procedure, in order: " + strings.Join(o.Composes, ", ") + ".",
		Applicability: knowledge.Applicability{Scope: knowledge.ScopeShared},
		Contract:      []byte("{}"), Instructions: instructions,
	}, nil
}
