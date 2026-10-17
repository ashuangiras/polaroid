package wire

// Agent resolution (ADR-0050): /mcp/agent/v2's resolve tool. The shapes
// mirror internal/reasoning's Resolution member for member.

// KnowledgeResolveRequest asks what applies to one record of a snapshot.
type KnowledgeResolveRequest struct {
	Goal       string               `json:"goal" jsonschema:"the key of the record to resolve: a procedure, concept or claim"`
	Snapshot   string               `json:"snapshot,omitzero" jsonschema:"a snapshot token; omit for the active knowledge"`
	Context    map[string]string    `json:"context,omitzero" jsonschema:"facts about your situation as name and value strings, for example concurrency; they can add obligations and decide unknowns, but never authorise an exception: a rule that needs one you omit reports an unknown, never a no"`
	Then       string               `json:"then,omitzero" jsonschema:"a procedure key to compose after goal (itself a procedure): the result's composition says compatible, incompatible or undetermined from both formal contracts; nothing is executed"`
	At         string               `json:"at,omitzero" jsonschema:"evaluation time, RFC 3339 (default now; echoed in the result)"`
	Assertions []KnowledgeAssertion `json:"assertions,omitzero" jsonschema:"signed context assertions you were given, for example by your CI or operator; only these establish trusted context, which can authorise project-scoped and conditional exceptions"`
	Completed  []string             `json:"completed,omitzero" jsonschema:"keys of goal's immediate composed children you already consider done; the result's plan marks them done and names the next one, in precedes order"`
}

// KnowledgeAssertion is a signed context assertion (ADR-0050): facts about
// one repository during a validity period, signed by an attestation key
// the operator trusts.
type KnowledgeAssertion struct {
	Reference  string            `json:"reference" jsonschema:"the assertion's identifier, as issued"`
	Key        string            `json:"key" jsonschema:"the id of the attestation key that signed it"`
	Repository string            `json:"repository" jsonschema:"the repository the facts are about"`
	Facts      map[string]string `json:"facts,omitzero" jsonschema:"context names and values asserted, besides the repository"`
	ValidFrom  string            `json:"valid_from" jsonschema:"RFC 3339"`
	ValidUntil string            `json:"valid_until" jsonschema:"RFC 3339"`
	Signature  string            `json:"signature" jsonschema:"base64 Ed25519 signature, as issued"`
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

// KnowledgeContextValue is one context name and value.
type KnowledgeContextValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// KnowledgeUnknown is a conclusion the inputs do not decide: it holds when
// the context also has When.
type KnowledgeUnknown struct {
	Kind        string                  `json:"kind"`
	Requirement string                  `json:"requirement,omitzero"`
	Predicate   string                  `json:"predicate,omitzero"`
	Object      string                  `json:"object,omitzero"`
	Source      string                  `json:"source"`
	When        []KnowledgeContextValue `json:"when"`
	Reason      string                  `json:"reason"`
}

// KnowledgePort is a procedure input or output.
type KnowledgePort struct {
	Name    string `json:"name"`
	Concept string `json:"concept,omitzero"`
}

// KnowledgeContract is a procedure's formal contract.
type KnowledgeContract struct {
	Key                        string          `json:"key"`
	Inputs                     []KnowledgePort `json:"inputs,omitzero"`
	Outputs                    []KnowledgePort `json:"outputs,omitzero"`
	OutputsComplete            bool            `json:"outputs_complete"`
	Preconditions              []string        `json:"preconditions,omitzero"`
	Postconditions             []string        `json:"postconditions,omitzero"`
	Invariants                 []string        `json:"invariants,omitzero"`
	InformalConditions         []string        `json:"informal_conditions,omitzero"`
	PreconditionsComplete      bool            `json:"preconditions_complete,omitzero"`
	PostconditionsComplete     bool            `json:"postconditions_complete,omitzero"`
	InvariantsComplete         bool            `json:"invariants_complete,omitzero"`
	InformalConditionsComplete bool            `json:"informal_conditions_complete,omitzero"`
}

// KnowledgeFinding is one reason for a composition verdict.
type KnowledgeFinding struct {
	Kind   string `json:"kind"`
	Item   string `json:"item"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// KnowledgeComposition is the verdict on running the goal, then Then.
type KnowledgeComposition struct {
	Version  string             `json:"version"`
	Snapshot string             `json:"snapshot"`
	Producer KnowledgeContract  `json:"producer"`
	Consumer KnowledgeContract  `json:"consumer"`
	Verdict  string             `json:"verdict"`
	Findings []KnowledgeFinding `json:"findings"`
}

// KnowledgeClaimStatus summarises the evidence present for a claim.
type KnowledgeClaimStatus struct {
	Claim         string   `json:"claim"`
	Status        string   `json:"status"`
	Supporting    []string `json:"supporting,omitzero"`
	Contradicting []string `json:"contradicting,omitzero"`
}

// KnowledgePlanEntry is one entry of a resolve result's plan (ADR-0052):
// an immediate composed child of the goal, in precedes order. Status is
// "done" (named in the request's completed), "next" (the first not-done
// entry), or absent (the rest). Path, "escalate"/"blocked" statuses and
// Children are specific to a walkthrough's plan (start/advance,
// ADR-0053/ADR-0054/ADR-0056): resolve's own plan never sets any of
// them, since it has no notion of a frozen leaf, of blocked, or of
// expand. When set, Path is the full chain of keys from this entry down
// to the actual next or frozen leaf — a single-element array, equal to
// [Key], when this entry is itself a leaf. Children, present only with
// expand, is this entry's own nested entries, recursively, when it is
// itself composed; a nested entry's own status additionally includes
// "pending" (nothing reported, not gated by sibling order the way the
// top level's "next" is).
type KnowledgePlanEntry struct {
	Key      string               `json:"key"`
	Status   string               `json:"status,omitzero"`
	Path     []string             `json:"path,omitzero"`
	Children []KnowledgePlanEntry `json:"children,omitzero"`
}

// KnowledgeResolution is what applies to a goal.
type KnowledgeResolution struct {
	Version         string                     `json:"version"`
	Snapshot        string                     `json:"snapshot"`
	Goal            string                     `json:"goal"`
	At              string                     `json:"at"`
	Trusted         map[string]string          `json:"trusted,omitzero"`
	Complete        bool                       `json:"complete"`
	Incomplete      []string                   `json:"incomplete,omitzero"`
	Applicable      []string                   `json:"applicable"`
	Specializations []KnowledgeSpecialization  `json:"specializations,omitzero"`
	Obligations     []KnowledgeObligation      `json:"obligations,omitzero"`
	Derived         []KnowledgeDerivedFact     `json:"derived,omitzero"`
	Exceptions      []KnowledgeExceptionResult `json:"exceptions,omitzero"`
	Conflicts       []KnowledgeConflict        `json:"conflicts,omitzero"`
	Unknowns        []KnowledgeUnknown         `json:"unknowns,omitzero"`
	Evidence        []KnowledgeClaimStatus     `json:"evidence,omitzero"`
	Superseded      []string                   `json:"superseded,omitzero"`
	Composition     *KnowledgeComposition      `json:"composition,omitzero"`
	Plan            []KnowledgePlanEntry       `json:"plan,omitzero"`
}
