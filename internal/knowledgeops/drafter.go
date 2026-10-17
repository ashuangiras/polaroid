package knowledgeops

import (
	"context"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// Drafter preserves the v5 tool contract while refusing its incomplete
// four-field review. Full field-complete proposals are handled by
// FieldDrafter on /mcp/agent/v6.
type Drafter struct{}

// NewDrafter builds the retired v5 compatibility stub.
func NewDrafter(_ *knowledge.Kernel, _ *Proposer, _ *memory.Drafts) *Drafter {
	return &Drafter{}
}

func (d *Drafter) Start(_ context.Context, _ wire.DraftStartRequest) (wire.DraftResult, error) {
	return wire.DraftResult{}, retiredProposalError()
}

func (d *Drafter) Advance(_ context.Context, _ wire.DraftAdvanceRequest) (wire.DraftResult, error) {
	return wire.DraftResult{}, retiredProposalError()
}
