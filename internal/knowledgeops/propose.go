package knowledgeops

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/reasoning"
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// ProposeTimeout bounds validation and composition checks for one proposal.
const ProposeTimeout = 30 * time.Second

type Proposer struct {
	kernel *knowledge.Kernel
	ev     reasoning.Evaluator
	trust  policy.Trust
}

func NewProposer(kernel *knowledge.Kernel, ev reasoning.Evaluator, trust policy.Trust) *Proposer {
	return &Proposer{kernel: kernel, ev: ev, trust: trust}
}

// Propose retains the frozen v4 request shape, but that shape cannot carry
// a complete field-review receipt and is no longer allowed to publish.
func (p *Proposer) Propose(_ context.Context, _ wire.ProposeRequest) (wire.ProposeResult, error) {
	return wire.ProposeResult{}, retiredProposalError()
}

// ProposeReviewed validates a field-reviewed proposal, applies the
// non-vacuous composition gate to each touched Procedure key, and publishes
// through the shared kernel gate. Meaningful guidance changes need not alter
// Corpus Logic resolution; exact no-ops are refused by the kernel.
func (p *Proposer) ProposeReviewed(ctx context.Context, prop knowledge.Proposal, key string, compositionKeys []string) (wire.ProposeResult, error) {
	if err := p.ValidateReviewed(ctx, prop, compositionKeys); err != nil {
		return wire.ProposeResult{}, err
	}
	snap, err := p.kernel.Publish(ctx, prop)
	if err != nil {
		return wire.ProposeResult{}, classify(err)
	}
	return wire.ProposeResult{Snapshot: string(snap), Key: key}, nil
}

func (p *Proposer) ValidateReviewed(ctx context.Context, prop knowledge.Proposal, compositionKeys []string) error {
	ctx, cancel := context.WithTimeout(ctx, ProposeTimeout)
	defer cancel()
	candidate, err := p.kernel.Validate(ctx, prop)
	if err != nil {
		return classify(err)
	}
	for _, touched := range slices.Compact(slices.Clone(compositionKeys)) {
		if problems, err := p.checkOwnComposition(ctx, candidate, touched); err != nil {
			return err
		} else if len(problems) > 0 {
			return classify(&knowledge.ValidationError{Problems: problems})
		}
	}
	return nil
}

func (p *Proposer) checkOwnComposition(ctx context.Context, candidate *knowledge.Catalog, key string) ([]knowledge.Problem, error) {
	var problems []knowledge.Problem
	for _, pair := range reasoning.PrecedesAdjacentPairs(candidate) {
		if pair.Producer != key && pair.Consumer != key {
			continue
		}
		res, err := reasoning.Compose(ctx, candidate, p.ev, pair.Producer, pair.Consumer, reasoning.Limits{})
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(res.Findings, func(f reasoning.Finding) bool { return f.Status == "established" || f.Status == "contradicted" }) {
			continue
		}
		problems = append(problems, knowledge.Problem{
			Field: fmt.Sprintf("procedures[%s].composes[%s->%s]", pair.Parent, pair.Producer, pair.Consumer),
			Message: fmt.Sprintf(
				"is vacuous: %s's own postconditions, outputs and invariants establish or contradict none of %s's preconditions", pair.Producer, pair.Consumer),
		})
	}
	return problems, nil
}

func retiredProposalError() error {
	return invalidField("proposal", "the v4/v5 proposal shape cannot review every authorable field; use /mcp/agent/v6 propose_start and propose_advance")
}
