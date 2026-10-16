package wire

// Guided-procedure walkthroughs (ADR-0053): /mcp/agent/v3's start and
// advance tools. Plan reuses KnowledgePlanEntry, resolve's own plan shape
// (ADR-0052), for shape consistency between the two endpoints.

// WalkthroughStartRequest starts a new walkthrough for goal, resolved and
// pinned at the currently active snapshot for the walkthrough's lifetime.
type WalkthroughStartRequest struct {
	Goal       string            `json:"goal" jsonschema:"the key of the procedure to walk through"`
	Repository string            `json:"repository,omitzero" jsonschema:"your repository identifier; stored with the walkthrough, not checked against anything"`
	Context    map[string]string `json:"context,omitzero" jsonschema:"facts about your situation as name and value strings; stored with the walkthrough"`
}

// WalkthroughOutcomeArg is one leaf's reported outcome, given to advance.
type WalkthroughOutcomeArg struct {
	Key    string `json:"key" jsonschema:"one of the goal's immediate composed children"`
	Status string `json:"status" jsonschema:"done, skipped or escalate; escalate freezes plan's next on this key until a later advance reports done or skipped for it"`
	Note   string `json:"note,omitzero"`
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
// goal was pinned at, and plan — exactly resolve's plan member
// (ADR-0052), with done/next derived from every outcome reported for
// this token so far, not a completed list the caller resends.
type WalkthroughResult struct {
	Token    string               `json:"token"`
	Snapshot string               `json:"snapshot"`
	Plan     []KnowledgePlanEntry `json:"plan,omitzero"`
}
