package wire

// Propose (ADR-0059 Parts 2-3, /mcp/agent/v4): agent-proposed knowledge,
// grounded either by citation (new content on an existing record, or a
// new supporting Concept/Claim) or by composing only already-existing
// Procedures (Tier 1 orchestration, which needs no citation at all).
// Deliberately independent of feedback (a different purpose: feedback is
// an append-only report that never itself changes served knowledge;
// propose commits). No draft state, no queue, no operator step: passing
// every check commits immediately.

// ProposeRequest is propose's argument. Exactly one of ExistingProcedure,
// NewConcept, NewClaim or NewOrchestration is required.
type ProposeRequest struct {
	ExistingProcedure *ProposeFieldAddition `json:"existing_procedure,omitzero" jsonschema:"add definition/preconditions/postconditions/invariants/informal_conditions to an existing Procedure; each new piece of prose must be citation-grounded"`
	NewConcept        *ProposeNewRecord     `json:"new_concept,omitzero" jsonschema:"a new, citation-grounded, supporting Concept"`
	NewClaim          *ProposeNewRecord     `json:"new_claim,omitzero" jsonschema:"a new, citation-grounded, supporting Claim"`
	NewOrchestration  *ProposeOrchestration `json:"new_orchestration,omitzero" jsonschema:"a wholly new Procedure whose only content is composing Procedures that already exist, in order (Tier 1, ADR-0059 Part 3): no citation needed"`
	Proposer          string                `json:"proposer" jsonschema:"who proposes this, canonical-key form, for example copilot.agent; recorded as this publication's author"`
	Rationale         string                `json:"rationale" jsonschema:"why this addition is correct, in your own words"`
}

// ProposeCitation grounds one piece of new prose: Quote must appear
// verbatim somewhere in the content it is attached to -- Polaroid's only
// enforceable check. This does not prove Source is accurate against any
// real file (Polaroid has no filesystem access to a consuming
// repository); Source is retained for later, independent, human audit,
// never itself re-verified automatically.
type ProposeCitation struct {
	Source string `json:"source" jsonschema:"where this was mirrored from: a file path, or an existing record's key"`
	Quote  string `json:"quote" jsonschema:"the exact text mirrored; must appear verbatim in the content it grounds"`
}

// ProposeFieldAddition adds new formal content to an existing Procedure.
// Every given field's new entries are appended to whatever the record
// already has. At least one Citation is required whenever any field is
// given, and every Citation's Quote must appear verbatim in the combined
// new content.
type ProposeFieldAddition struct {
	Key                string            `json:"key" jsonschema:"the existing Procedure's canonical key"`
	Definition         string            `json:"definition,omitzero"`
	Preconditions      []string          `json:"preconditions,omitzero"`
	Postconditions     []string          `json:"postconditions,omitzero"`
	Invariants         []string          `json:"invariants,omitzero"`
	InformalConditions []string          `json:"informal_conditions,omitzero"`
	Citations          []ProposeCitation `json:"citations" jsonschema:"one or more; every quote must appear verbatim in the new content above"`
}

// ProposeNewRecord is a new Concept (Definition) or Claim (Statement); the
// key must not already exist. At least one Citation is required, and
// every Citation's Quote must appear verbatim in Definition/Statement.
type ProposeNewRecord struct {
	Key        string            `json:"key"`
	Label      string            `json:"label"`
	Definition string            `json:"definition,omitzero" jsonschema:"a Concept's definition"`
	Statement  string            `json:"statement,omitzero" jsonschema:"a Claim's own statement"`
	Citations  []ProposeCitation `json:"citations" jsonschema:"one or more; every quote must appear verbatim in definition or statement above"`
}

// ProposeOrchestration is a wholly new orchestration Procedure (Tier 1):
// its only content is Composes, in order, plus an optional Requires (a
// reference to an already-existing Concept, not new prose, so it needs
// no citation either) to satisfy ADR-0057's completeness bar, which
// Composes alone does not. No freeform instructions, no citation needed
// at all. The key must not already exist; every composed key must
// already exist, as a Procedure.
type ProposeOrchestration struct {
	Key        string   `json:"key"`
	Label      string   `json:"label"`
	Definition string   `json:"definition" jsonschema:"a short description of what this orchestrates, for example the order it composes"`
	Composes   []string `json:"composes" jsonschema:"already-existing Procedure keys, in the order they are followed; precedes edges are derived from this order automatically"`
	Requires   []string `json:"requires,omitzero" jsonschema:"already-existing Concept keys this orchestration itself requires, to satisfy completeness; a reference, not new prose, so it needs no citation"`
}

// ProposeResult is propose's result: the new snapshot and the key it
// committed.
type ProposeResult struct {
	Snapshot string `json:"snapshot"`
	Key      string `json:"key"`
}
