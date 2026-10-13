package wire

import (
	"encoding/json/jsontext"
	"time"
)

// Knowledge-first wire shapes (ADR-0048): the Git-backed agent interface's
// request and result documents, shared by MCP agent mode and the operator
// HTTP API. They never name a procedure version, a binding or an
// evidence-based selection — only a canonical key and an opaque snapshot
// token.

// KnowledgeDiscoverRequest is a discover call.
type KnowledgeDiscoverRequest struct {
	Task       string `json:"task" jsonschema:"the task, in your own words"`
	Repository string `json:"repository,omitzero" jsonschema:"your repository identifier, for example github.com/owner/repo: only knowledge that applies there"`
	Limit      int    `json:"limit,omitzero" jsonschema:"at most this many candidates, 1 to 50 (default 5)"`
}

// KnowledgeCandidate is one ranked discovery result.
type KnowledgeCandidate struct {
	Key   string `json:"key"`
	Goal  string `json:"goal,omitzero"`
	Score int    `json:"score"`
}

// KnowledgeDiscovery is a discover call's result. Empty is true, with no
// candidates, when no knowledge has been published yet; it is a well-formed
// result, not an error.
type KnowledgeDiscovery struct {
	Snapshot   string               `json:"snapshot,omitzero"`
	Empty      bool                 `json:"empty,omitzero"`
	Message    string               `json:"message,omitzero"`
	Candidates []KnowledgeCandidate `json:"candidates,omitzero"`
}

// KnowledgeReadRequest is a read call.
type KnowledgeReadRequest struct {
	Key        string         `json:"key" jsonschema:"the procedure's canonical key, from discover"`
	Snapshot   string         `json:"snapshot,omitzero" jsonschema:"a snapshot token from discover or an earlier read; omit for the active knowledge"`
	Repository string         `json:"repository,omitzero" jsonschema:"your repository identifier; checked against the procedure's applicability"`
	Inputs     jsontext.Value `json:"inputs,omitzero" jsonschema:"your effective inputs, to compute each reference's mapped child inputs"`
	Full       bool           `json:"full,omitzero" jsonschema:"also include philosophy"`
}

// KnowledgeReference is one reference to another procedure, resolved only
// one level: its own references are read with a further read call.
type KnowledgeReference struct {
	Name         string         `json:"name"`
	Key          string         `json:"key"`
	Goal         string         `json:"goal,omitzero"`
	Condition    string         `json:"condition,omitzero"`
	Inputs       jsontext.Value `json:"inputs,omitzero"`
	InputMapping jsontext.Value `json:"input_mapping,omitzero"`
}

// KnowledgeGuidance is a read call's result.
type KnowledgeGuidance struct {
	Snapshot     string               `json:"snapshot"`
	Key          string               `json:"key"`
	Scope        string               `json:"scope,omitzero"`
	Goal         string               `json:"goal,omitzero"`
	Method       string               `json:"method,omitzero"`
	Contract     jsontext.Value       `json:"contract,omitzero"`
	Instructions jsontext.Value       `json:"instructions,omitzero"`
	References   []KnowledgeReference `json:"references,omitzero"`
	Philosophy   string               `json:"philosophy,omitzero"`
}

// KnowledgeFeedbackRequest is a feedback call: the procedure read and the
// snapshot it was read from, so the daemon can confirm the pair is real
// before recording it.
type KnowledgeFeedbackRequest struct {
	Key            string         `json:"key" jsonschema:"the procedure's canonical key"`
	Snapshot       string         `json:"snapshot" jsonschema:"the snapshot you read it from"`
	Classification string         `json:"classification" jsonschema:"incorrect_applicability, missing_precondition, incorrect_instruction, inadequate_verification, improved_method, product_defect, environment_problem or transient_failure"`
	Summary        string         `json:"summary" jsonschema:"one line"`
	Details        string         `json:"details,omitzero"`
	Reporter       string         `json:"reporter" jsonschema:"who reports, canonical-key form, for example copilot.agent"`
	Repository     string         `json:"repository,omitzero"`
	Evidence       jsontext.Value `json:"evidence,omitzero"`
}

// KnowledgeFeedbackResult is a feedback call's result.
type KnowledgeFeedbackResult struct {
	ID             string    `json:"id"`
	Classification string    `json:"classification"`
	CreatedAt      time.Time `json:"created_at"`
}

// KnowledgeError is a knowledge-domain error already mapped to its response
// shape. internal/knowledgeops builds these so that wire.Classify can map
// Git-core, knowledge-kernel and synchronization errors without this
// package depending on any of those packages.
type KnowledgeError struct {
	Status int
	Detail ErrorDetail
}

func (e *KnowledgeError) Error() string { return e.Detail.Message }

// NewKnowledgeError builds a KnowledgeError.
func NewKnowledgeError(status int, detail ErrorDetail) error {
	return &KnowledgeError{Status: status, Detail: detail}
}

// --- Operator surface (ADR-0048): explicit, operator-controlled knowledge
// operations. None of this appears in agent mode.

// KnowledgeStatus is the operator status call's result. Empty is always
// present (never omitted), since an operator's readiness check must not
// rely on a field's absence meaning false.
type KnowledgeStatus struct {
	Snapshot string            `json:"snapshot,omitzero"`
	Empty    bool              `json:"empty"`
	Instance string            `json:"instance,omitzero"`
	Keys     int               `json:"keys"`
	Sources  []KnowledgeSource `json:"sources,omitzero"`
}

// KnowledgeSource is one entry of the trust policy, never its credentials.
type KnowledgeSource struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Branch  string `json:"branch,omitzero"`
	Publish bool   `json:"publish,omitzero"`
}

// KnowledgeCredentials are supplied for one call and never stored or
// logged.
type KnowledgeCredentials struct {
	Username      string `json:"username,omitzero"`
	Password      string `json:"password,omitzero"`
	SSHKey        string `json:"ssh_key,omitzero"`
	SSHPassphrase string `json:"ssh_passphrase,omitzero"`
	KnownHosts    string `json:"known_hosts,omitzero"`
}

func (KnowledgeCredentials) String() string     { return "wire.KnowledgeCredentials(redacted)" }
func (c KnowledgeCredentials) GoString() string { return c.String() }

// KnowledgeFetchRequest fetches a source's knowledge into quarantine.
type KnowledgeFetchRequest struct {
	Credentials KnowledgeCredentials `json:"credentials,omitzero"`
}

// KnowledgeCandidateStatus is a fetched or validated candidate.
type KnowledgeCandidateStatus struct {
	Source   string              `json:"source"`
	Remote   string              `json:"remote,omitzero"`
	Local    string              `json:"local,omitzero"`
	Status   string              `json:"status"`
	Problems []string            `json:"problems,omitzero"`
	Incoming []KnowledgeIncoming `json:"incoming,omitzero"`
	Bytes    int64               `json:"bytes,omitzero"`
	Objects  int                 `json:"objects,omitzero"`
}

// KnowledgeIncoming is one new publication a candidate carries.
type KnowledgeIncoming struct {
	Snapshot   string    `json:"snapshot"`
	Publisher  string    `json:"publisher"`
	When       time.Time `json:"when"`
	Summary    string    `json:"summary"`
	Procedures int       `json:"procedures"`
}

// KnowledgeDecision is an operator's acceptance of a candidate: who, and
// why.
type KnowledgeDecision struct {
	Author     string `json:"author"`
	Summary    string `json:"summary"`
	Rationale  string `json:"rationale"`
	Finding    string `json:"finding,omitzero"`
	Validation string `json:"validation,omitzero"`
}

// KnowledgeActivation is an activate call's result.
type KnowledgeActivation struct {
	Source   string `json:"source"`
	Snapshot string `json:"snapshot"`
}

// KnowledgePushRequest pushes the active knowledge to a source.
type KnowledgePushRequest struct {
	Credentials KnowledgeCredentials `json:"credentials,omitzero"`
}

// KnowledgePushResult is a push call's result.
type KnowledgePushResult struct {
	Before   string `json:"before,omitzero"`
	After    string `json:"after"`
	UpToDate bool   `json:"up_to_date,omitzero"`
	Bytes    int64  `json:"bytes,omitzero"`
	Objects  int    `json:"objects,omitzero"`
}

// KnowledgeActivationRecord is one accepted activation.
type KnowledgeActivationRecord struct {
	Target    string    `json:"target"`
	Previous  string    `json:"previous,omitzero"`
	When      time.Time `json:"when"`
	Author    string    `json:"author"`
	Summary   string    `json:"summary"`
	Rationale string    `json:"rationale,omitzero"`
	Finding   string    `json:"finding,omitzero"`
	Accepted  string    `json:"accepted,omitzero"`
}

// KnowledgeActivations lists accepted activations, newest first.
type KnowledgeActivations struct {
	Activations []KnowledgeActivationRecord `json:"activations"`
}

// KnowledgeChange is one change of a publish call: a complete replacement
// of the procedure at Key, or its removal.
type KnowledgeChange struct {
	Key          string               `json:"key"`
	Remove       bool                 `json:"remove,omitzero"`
	Scope        string               `json:"scope,omitzero"`
	Repository   string               `json:"repository,omitzero"`
	Goal         string               `json:"goal,omitzero"`
	Philosophy   string               `json:"philosophy,omitzero"`
	Method       string               `json:"method,omitzero"`
	Contract     jsontext.Value       `json:"contract,omitzero"`
	Instructions jsontext.Value       `json:"instructions,omitzero"`
	References   []KnowledgeReference `json:"references,omitzero"`
}

// KnowledgePublishRequest proposes changes to the knowledge active at Base.
type KnowledgePublishRequest struct {
	Base       string            `json:"base" jsonschema:"the active snapshot this proposal is based on"`
	Changes    []KnowledgeChange `json:"changes" jsonschema:"at least one change"`
	Author     string            `json:"author"`
	Summary    string            `json:"summary"`
	Rationale  string            `json:"rationale"`
	Finding    string            `json:"finding,omitzero" jsonschema:"a feedback report id this change addresses; attempts reconciliation after publishing"`
	Validation string            `json:"validation,omitzero"`
}

// KnowledgeReconcileResult is a reconcile call's result. Status is
// "resolved", "already_resolved" or "no_publication_yet" (the finding has
// no local publication addressing it yet; reconcile again later).
type KnowledgeReconcileResult struct {
	FeedbackID     string `json:"feedback_id"`
	Status         string `json:"status"`
	Publication    string `json:"publication,omitzero"`
	ReconcileError string `json:"reconcile_error,omitzero"`
}

// KnowledgePublishResult is a publish call's result.
type KnowledgePublishResult struct {
	Snapshot       string                    `json:"snapshot"`
	Reconciliation *KnowledgeReconcileResult `json:"reconciliation,omitzero"`
}
