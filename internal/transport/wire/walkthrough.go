package wire

// Guided-procedure walkthroughs (ADR-0053, amended by ADR-0054 for
// multi-level plans): /mcp/agent/v3's start and advance tools. Plan
// reuses KnowledgePlanEntry, resolve's own plan shape (ADR-0052), for
// shape consistency between the two endpoints; unlike resolve's, a
// walkthrough's plan entries may carry Path and the "escalate" status.

// WalkthroughStartRequest starts a new walkthrough for goal, resolved and
// pinned at the currently active snapshot for the walkthrough's lifetime.
type WalkthroughStartRequest struct {
	Goal       string            `json:"goal" jsonschema:"the key of the procedure to walk through"`
	Repository string            `json:"repository,omitzero" jsonschema:"your repository identifier; stored with the walkthrough, not checked against anything"`
	Context    map[string]string `json:"context,omitzero" jsonschema:"facts about your situation as name and value strings; stored with the walkthrough"`
}

// WalkthroughOutcomeArg is one leaf's reported outcome, given to advance:
// exactly one of Key or Path is required (ADR-0054). Key is shorthand
// for a one-element Path, for a leaf that is one of the goal's immediate
// composed children; Path is the full chain of keys from the goal down
// to a leaf found several levels deep. Either way, the named key must be
// an actual leaf (no composed children of its own) reached by following
// composes at every level — a composed node is never a valid target,
// since its status is always derived from its own descendants.
type WalkthroughOutcomeArg struct {
	Key    string   `json:"key,omitzero" jsonschema:"one of the goal's immediate composed children; shorthand for path with this one element. Exactly one of key or path is required"`
	Path   []string `json:"path,omitzero" jsonschema:"the full chain of keys from the goal down to a leaf found several levels deep (ADR-0054); exactly one of key or path is required"`
	Status string   `json:"status" jsonschema:"done, skipped or escalate; escalate freezes plan's next on this leaf until a later advance reports done or skipped for it"`
	Note   string   `json:"note,omitzero"`
}

// WalkthroughAdvanceRequest reports an outcome on token, or peeks its
// current plan unchanged when outcome is omitted. Either way, this
// resets token's sliding expiry.
type WalkthroughAdvanceRequest struct {
	Token   string                 `json:"token" jsonschema:"the token start or an earlier advance returned"`
	Outcome *WalkthroughOutcomeArg `json:"outcome,omitzero" jsonschema:"omit to peek the current plan unchanged"`
	Context map[string]string      `json:"context,omitzero" jsonschema:"facts to add to what this walkthrough has reported so far"`
}

// WalkthroughResult is start's and advance's result: token, the snapshot
// goal was pinned at, and plan — resolve's plan shape (ADR-0052),
// generalized to multiple levels (ADR-0054): done/next/escalate derived
// from every outcome reported for this token so far, not a completed
// list the caller resends.
type WalkthroughResult struct {
	Token    string               `json:"token"`
	Snapshot string               `json:"snapshot"`
	Plan     []KnowledgePlanEntry `json:"plan,omitzero"`
}
