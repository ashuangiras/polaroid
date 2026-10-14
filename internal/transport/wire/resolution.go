package wire

// Agent resolution (ADR-0050): /mcp/agent/v2's resolve tool. The shapes
// mirror internal/reasoning's Resolution member for member.

// KnowledgeResolveRequest asks what applies to one record of a snapshot.
type KnowledgeResolveRequest struct {
	Goal     string            `json:"goal" jsonschema:"the key of the record to resolve: a procedure, concept or claim"`
	Snapshot string            `json:"snapshot,omitzero" jsonschema:"a snapshot token; omit for the active knowledge"`
	Context  map[string]string `json:"context,omitzero" jsonschema:"facts about your situation as name and value strings, for example repository and concurrency; a rule that needs one you omit reports an unknown, never a no"`
	At       string            `json:"at,omitzero" jsonschema:"evaluation time, RFC 3339 (default now; echoed in the result)"`
}

// KnowledgeDerivation is one independent way an obligation holds.
type KnowledgeDerivation struct {
	ID         string   `json:"id"`
	Source     string   `json:"source"`
	SourceType string   `json:"source_type"`
	Via        string   `json:"via"`
	Path       []string `json:"path,omitzero"`
	Status     string   `json:"status"`
	Exception  string   `json:"exception,omitzero"`
}

// KnowledgeObligation is a requirement with every derivation of it.
type KnowledgeObligation struct {
	Requirement string                `json:"requirement"`
	Status      string                `json:"status"`
	Derivations []KnowledgeDerivation `json:"derivations"`
}

// KnowledgeDerivedFact is a fact a rule derived about the goal.
type KnowledgeDerivedFact struct {
	ID        string   `json:"id"`
	Predicate string   `json:"predicate"`
	Object    string   `json:"object"`
	Rules     []string `json:"rules"`
}

// KnowledgeSpecialization is one ancestor of the goal.
type KnowledgeSpecialization struct {
	Key  string   `json:"key"`
	Path []string `json:"path"`
}

// KnowledgeExceptionResult is one exception that concerns the resolution.
type KnowledgeExceptionResult struct {
	Key         string   `json:"key"`
	Status      string   `json:"status"`
	Reason      string   `json:"reason,omitzero"`
	Derivations []string `json:"derivations,omitzero"`
}

// KnowledgeConflict is information that cannot all hold.
type KnowledgeConflict struct {
	Kind   string   `json:"kind"`
	Keys   []string `json:"keys"`
	Reason string   `json:"reason"`
}

// KnowledgeUnknown is a conclusion the inputs do not decide.
type KnowledgeUnknown struct {
	Kind        string   `json:"kind"`
	Requirement string   `json:"requirement,omitzero"`
	Source      string   `json:"source,omitzero"`
	Context     string   `json:"context,omitzero"`
	Values      []string `json:"values,omitzero"`
	Reason      string   `json:"reason"`
}

// KnowledgeClaimStatus summarises the evidence present for a claim.
type KnowledgeClaimStatus struct {
	Claim         string   `json:"claim"`
	Status        string   `json:"status"`
	Supporting    []string `json:"supporting,omitzero"`
	Contradicting []string `json:"contradicting,omitzero"`
}

// KnowledgeResolution is what applies to a goal.
type KnowledgeResolution struct {
	Version         string                     `json:"version"`
	Snapshot        string                     `json:"snapshot"`
	Goal            string                     `json:"goal"`
	At              string                     `json:"at"`
	Applicable      []string                   `json:"applicable"`
	Specializations []KnowledgeSpecialization  `json:"specializations,omitzero"`
	Obligations     []KnowledgeObligation      `json:"obligations,omitzero"`
	Derived         []KnowledgeDerivedFact     `json:"derived,omitzero"`
	Exceptions      []KnowledgeExceptionResult `json:"exceptions,omitzero"`
	Conflicts       []KnowledgeConflict        `json:"conflicts,omitzero"`
	Unknowns        []KnowledgeUnknown         `json:"unknowns,omitzero"`
	Evidence        []KnowledgeClaimStatus     `json:"evidence,omitzero"`
	Superseded      []string                   `json:"superseded,omitzero"`
}
