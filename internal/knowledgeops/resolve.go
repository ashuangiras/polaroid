package knowledgeops

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/reasoning"
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// ResolveTimeout bounds one resolution.
const ResolveTimeout = 10 * time.Second

// Resolver answers agent resolution requests (ADR-0050) on the same kernel
// the agent reads, with an evaluator and the operator's trust material.
type Resolver struct {
	kernel *knowledge.Kernel
	ev     reasoning.Evaluator
	trust  policy.Trust
	now    func() time.Time
}

// NewResolver builds a Resolver. trust is operator-held authorisation
// material; an empty Trust lets no exception apply.
func NewResolver(kernel *knowledge.Kernel, ev reasoning.Evaluator, trust policy.Trust) *Resolver {
	return &Resolver{kernel: kernel, ev: ev, trust: trust, now: time.Now}
}

// Resolve resolves what applies to in.Goal at a snapshot (the active one by
// default). The evaluation time defaults to now and is echoed in the result,
// so the same request with that time reproduces it exactly.
func (r *Resolver) Resolve(ctx context.Context, in wire.KnowledgeResolveRequest) (wire.KnowledgeResolution, error) {
	if in.Goal == "" {
		return wire.KnowledgeResolution{}, requiredField("goal")
	}
	at := r.now().UTC().Truncate(time.Second)
	if in.At != "" {
		t, err := time.Parse(time.RFC3339, in.At)
		if err != nil {
			return wire.KnowledgeResolution{}, invalidField("at", "must be an RFC 3339 time")
		}
		at = t
	}
	var cat *knowledge.Catalog
	var err error
	if in.Snapshot != "" {
		cat, err = r.kernel.At(ctx, knowledge.Snapshot(in.Snapshot))
	} else {
		cat, err = r.kernel.Active(ctx)
	}
	if err != nil {
		return wire.KnowledgeResolution{}, classify(err)
	}
	ctx, cancel := context.WithTimeout(ctx, ResolveTimeout)
	defer cancel()
	// Only verified assertions establish trusted context; without them,
	// project-scoped and conditional exceptions fail closed.
	assertions, err := decodeAssertions(in.Assertions)
	if err != nil {
		return wire.KnowledgeResolution{}, err
	}
	trusted, err := policy.Establish(r.trust, assertions, at)
	if err != nil {
		return wire.KnowledgeResolution{}, invalidField("assertions", err.Error())
	}
	res, err := reasoning.Resolve(ctx, cat, r.ev, r.trust, reasoning.Request{Goal: in.Goal, Context: in.Context, Trusted: trusted, Then: in.Then, At: at})
	switch {
	case errors.Is(err, reasoning.ErrUnknownGoal):
		return wire.KnowledgeResolution{}, wire.NewKnowledgeError(http.StatusNotFound, wire.ErrorDetail{Code: "not_found", Message: err.Error()})
	case errors.Is(err, reasoning.ErrContextConflict):
		return wire.KnowledgeResolution{}, invalidField("context", err.Error())
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, reasoning.ErrBudget):
		return wire.KnowledgeResolution{}, wire.NewKnowledgeError(http.StatusUnprocessableEntity, wire.ErrorDetail{Code: "budget_exceeded",
			Message: fmt.Sprintf("resolution exceeded its budget (%s, or a fact limit): %v", ResolveTimeout, err)})
	case err != nil:
		return wire.KnowledgeResolution{}, err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return wire.KnowledgeResolution{}, err
	}
	var out wire.KnowledgeResolution
	if err := json.Unmarshal(b, &out, json.RejectUnknownMembers(true)); err != nil {
		return wire.KnowledgeResolution{}, fmt.Errorf("resolution shape: %w", err)
	}
	return out, nil
}

func decodeAssertions(in []wire.KnowledgeAssertion) ([]policy.Assertion, error) {
	var out []policy.Assertion
	for i, a := range in {
		field := fmt.Sprintf("assertions[%d]", i)
		from, err1 := time.Parse(time.RFC3339, a.ValidFrom)
		until, err2 := time.Parse(time.RFC3339, a.ValidUntil)
		if err1 != nil || err2 != nil {
			return nil, invalidField(field, "valid_from and valid_until must be RFC 3339 times")
		}
		sig, err := base64.StdEncoding.DecodeString(a.Signature)
		if err != nil {
			return nil, invalidField(field+".signature", "must be base64")
		}
		out = append(out, policy.Assertion{Reference: a.Reference, Key: a.Key, Repository: a.Repository, Facts: a.Facts,
			ValidFrom: from, ValidUntil: until, Signature: sig})
	}
	return out, nil
}
