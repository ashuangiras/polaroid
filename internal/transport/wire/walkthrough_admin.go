package wire

import (
	"sort"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// Operator visibility into guided-procedure walkthroughs (ADR-0056): the
// legacy /mcp and HTTP surfaces' list_walkthroughs and get_walkthrough.
// Unlike WalkthroughResult (/mcp/agent/v3's own start/advance), these
// never carry a resolved plan: this surface has no access to
// internal/knowledge or internal/knowledgeops (internal/archtest keeps
// it that way), so it cannot derive the composition-tree-based
// in_progress/complete status start and advance report. Status here is
// the coarser "active"/"escalate"/"blocked" memory.WalkthroughState.Status
// already derives from the per-leaf fold alone; see its doc comment for
// exactly what "active" does and does not mean.

// WalkthroughLeaf is one key an agent has reported an outcome for, as
// stored (ADR-0053, ADR-0056): Key is the raw string memory.Walkthroughs
// folds on, opaque to this legacy surface — for a multi-level outcome
// (ADR-0054) it is the full path joined with "/", an encoding this
// package does not parse back into separate elements. Reason is present
// only when last reported alongside a blocked status.
type WalkthroughLeaf struct {
	Key    string `json:"key"`
	Status string `json:"status"`
	Reason string `json:"reason,omitzero"`
	Note   string `json:"note,omitzero"`
}

// Walkthrough is one walkthrough's current state, for an operator
// (ADR-0056): context is always present, {} when none was given (the
// same convention as Feedback's); leaves is sorted by key for a stable,
// reproducible document — the fold itself keeps no reporting order.
type Walkthrough struct {
	Token      string            `json:"token"`
	Goal       string            `json:"goal"`
	Snapshot   string            `json:"snapshot"`
	Repository string            `json:"repository,omitzero"`
	Context    map[string]string `json:"context,omitzero"`
	Status     string            `json:"status"`
	CreatedAt  time.Time         `json:"created_at"`
	ExpiresAt  time.Time         `json:"expires_at"`
	Leaves     []WalkthroughLeaf `json:"leaves,omitzero"`
}

// NewWalkthrough converts st.
func NewWalkthrough(st memory.WalkthroughState) Walkthrough {
	keys := make([]string, 0, len(st.Statuses))
	for k := range st.Statuses {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	leaves := make([]WalkthroughLeaf, len(keys))
	for i, k := range keys {
		leaves[i] = WalkthroughLeaf{Key: k, Status: string(st.Statuses[k]), Reason: st.Reasons[k], Note: st.Notes[k]}
	}
	return Walkthrough{
		Token:      st.Token,
		Goal:       st.Goal,
		Snapshot:   st.Snapshot,
		Repository: st.Repository,
		Context:    st.Context,
		Status:     st.Status(),
		CreatedAt:  st.CreatedAt,
		ExpiresAt:  st.ExpiresAt,
		Leaves:     leaves,
	}
}

// WalkthroughList is list_walkthroughs' result (ADR-0056).
type WalkthroughList struct {
	Walkthroughs []Walkthrough `json:"walkthroughs"`
	Next         string        `json:"next,omitzero"`
}

// NewWalkthroughList converts states.
func NewWalkthroughList(states []memory.WalkthroughState, next string) WalkthroughList {
	body := WalkthroughList{Walkthroughs: make([]Walkthrough, len(states)), Next: next}
	for i, st := range states {
		body.Walkthroughs[i] = NewWalkthrough(st)
	}
	return body
}
