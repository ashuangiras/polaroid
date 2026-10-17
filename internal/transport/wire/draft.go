package wire

// Interactive, field-by-field proposal construction (ADR-0060,
// /mcp/agent/v5): propose_start and propose_advance. Complements
// single-shot propose (ADR-0059): Polaroid walks an existing Procedure's
// content fields one at a time, requiring an explicit answer -- real,
// citation-grounded content, or an explicit, reasoned decline -- for
// each, instead of allowing an agent to silently omit one. Every draft
// walks all four fields, every time (ADR-0061), including a field that
// already holds published content: its current values are surfaced so
// new entries can be appended to it, not only to one that starts empty.

// DraftStartRequest starts a new draft for an existing Procedure's key,
// resolved and pinned at the active snapshot for the draft's lifetime.
type DraftStartRequest struct {
	Key       string `json:"key" jsonschema:"the existing Procedure's canonical key"`
	Proposer  string `json:"proposer" jsonschema:"who proposes this, canonical-key form, for example copilot.agent"`
	Rationale string `json:"rationale" jsonschema:"why this addition is correct, in your own words"`
}

// DraftOutcomeArg is one field's answer, given to propose_advance: either
// Values (new entries to append to whatever is already published there,
// each grounded by Citations) or ConfirmedAbsent (your own testimony
// that this field needs no further values right now -- whether because
// it genuinely does not apply at all, or because what is already
// published there is already the complete set; never verified by
// Polaroid, the same epistemic category escalate/blocked already are
// for walkthroughs); exactly one of them is required.
type DraftOutcomeArg struct {
	Field           string            `json:"field" jsonschema:"preconditions, postconditions, invariants or informal_conditions"`
	Values          []string          `json:"values,omitzero" jsonschema:"new entries to append to whatever is already there; omit if confirmed_absent"`
	Citations       []ProposeCitation `json:"citations,omitzero" jsonschema:"required when values is given; every quote must appear verbatim in values"`
	ConfirmedAbsent bool              `json:"confirmed_absent,omitzero" jsonschema:"no further values for this field right now -- whether it genuinely does not apply, or what is already published there already stands as complete; exactly one of values or confirmed_absent is required"`
	Reason          string            `json:"reason,omitzero" jsonschema:"why, when confirmed_absent"`
}

// DraftAdvanceRequest reports an outcome for token's next_field, or peeks
// its current state unchanged when outcome is omitted. The moment every
// field is decided, this same call automatically attempts the real
// commit (the same checks single-shot propose already runs); a failure
// leaves the draft open, with every earlier answer intact, for
// correction. close ends the draft immediately, abandoned, whatever its
// status.
type DraftAdvanceRequest struct {
	Token   string           `json:"token" jsonschema:"the token propose_start or an earlier propose_advance returned"`
	Outcome *DraftOutcomeArg `json:"outcome,omitzero" jsonschema:"omit to peek the current state unchanged"`
	Close   bool             `json:"close,omitzero" jsonschema:"end the draft immediately, abandoned, whatever its status"`
}

// DraftResult is propose_start's and propose_advance's result.
// NextField is the next field to answer, in fixed order; every draft
// walks all four, every time (ADR-0061), never skipping one because it
// already has content -- when NextField is non-empty, ExistingValues
// names its own current, already-published values (omitted when it has
// none) and ExistingComplete reports whether it was already marked
// *Complete from an earlier publish, so an answer can add to what is
// there or decide it is already enough. Status is "complete" only as
// the direct result of a commit just succeeding in this call, with
// Snapshot/Key naming what was committed -- the token then behaves
// exactly like a closed or expired one on any later call (not_found, no
// trace). Otherwise Status is "in_progress": either NextField names
// what to answer, or NextField is empty because every field is already
// answered but the commit attempt that answer triggered failed (the
// specific error is returned instead of a result) -- resubmit whichever
// field is actually keeping it from succeeding. A pure peek (no outcome
// given) never attempts a commit, even when nothing is left undecided:
// Status is "in_progress" with NextField empty in that case too,
// reported safely, with no error.
type DraftResult struct {
	Token            string   `json:"token,omitzero"`
	Snapshot         string   `json:"snapshot,omitzero"`
	NextField        string   `json:"next_field,omitzero"`
	ExistingValues   []string `json:"existing_values,omitzero"`
	ExistingComplete bool     `json:"existing_complete,omitzero"`
	Status           string   `json:"status"`
	Key              string   `json:"key,omitzero"`
}
