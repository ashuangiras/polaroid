package knowledgeops

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/ashuangiras/polaroid/internal/gitcore"
	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledgesync"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// historyScanLimit bounds how far Reconcile searches the knowledge history
// for the publication that addresses a finding, when it is not told the
// publication directly (Publish's own auto-reconciliation passes it).
const historyScanLimit = 2000

// Operator is the operator-facing interface to Git-native knowledge
// (ADR-0048): status, publish, fetch, validate, activate, push,
// activations and reconcile. Every operation here is explicit: there is no
// scheduler, and nothing here is reachable from agent mode.
type Operator struct {
	kernel *knowledge.Kernel
	syncer *knowledgesync.Syncer
	policy knowledgesync.Policy
	fb     *memory.Service

	mu         sync.Mutex
	candidates map[string]*knowledgesync.Candidate // by source name; the most recent fetch
}

// NewOperator builds an Operator on syncer, which always has a kernel on
// the same repository even when its policy configures no sources; fetch,
// validate, activate and push then report not_found for any source name.
func NewOperator(syncer *knowledgesync.Syncer, policy knowledgesync.Policy, fb *memory.Service) *Operator {
	return &Operator{kernel: syncer.Kernel(), syncer: syncer, policy: policy, fb: fb, candidates: map[string]*knowledgesync.Candidate{}}
}

// Status is the active knowledge's summary and the configured sources.
func (o *Operator) Status(ctx context.Context) (wire.KnowledgeStatus, error) {
	cat, err := o.kernel.Active(ctx)
	if err != nil {
		return wire.KnowledgeStatus{}, classify(err)
	}
	inst, err := o.kernel.Instance(ctx)
	if err != nil {
		return wire.KnowledgeStatus{}, classify(err)
	}
	st := wire.KnowledgeStatus{Snapshot: string(cat.Snapshot()), Empty: cat.Snapshot() == "", Instance: inst, Keys: cat.Len()}
	if cat.Format() > 1 {
		st.Format, st.Records = cat.Format(), len(cat.RecordKeys())
	}
	for _, src := range o.policy.Sources {
		st.Sources = append(st.Sources, wire.KnowledgeSource{Name: src.Name, URL: src.URL, Branch: src.Branch, Publish: src.Publish})
	}
	return st, nil
}

// Publish authors a proposal through the kernel. When Finding names a
// feedback report, it then attempts to reconcile that report: a failed
// reconciliation never undoes the publication, which already succeeded; it
// is reported in the result for a later, independent retry.
func (o *Operator) Publish(ctx context.Context, in wire.KnowledgePublishRequest) (wire.KnowledgePublishResult, error) {
	prop := knowledge.Proposal{
		Base: knowledge.Snapshot(in.Base),
		Provenance: knowledge.Provenance{
			Author: in.Author, Summary: in.Summary, Rationale: in.Rationale, Finding: in.Finding, Validation: in.Validation,
		},
	}
	for _, c := range in.Changes {
		var proc *knowledge.Procedure
		if !c.Remove {
			p := &knowledge.Procedure{
				Key: c.Key, Goal: c.Goal, Philosophy: c.Philosophy, Method: c.Method,
				Applicability: knowledge.Applicability{Scope: knowledge.Scope(c.Scope), Repository: c.Repository},
				Contract:      c.Contract, Instructions: c.Instructions,
			}
			for _, r := range c.References {
				ref := knowledge.Reference{Name: r.Name, Key: r.Key, Inputs: r.Inputs}
				if r.Condition != "" {
					cond := r.Condition
					ref.Condition = &cond
				}
				p.References = append(p.References, ref)
			}
			proc = p
		}
		prop.Changes = append(prop.Changes, knowledge.Change{Key: c.Key, Procedure: proc})
	}
	for i, c := range in.Records {
		var r *knowledge.Record
		switch {
		case c.Remove && len(c.Document) > 0:
			return wire.KnowledgePublishResult{}, invalidField(fmt.Sprintf("records[%d]", i), "names either a document or remove, not both")
		case !c.Remove:
			var err error
			if r, err = knowledge.ParseRecord(c.Document); err != nil {
				return wire.KnowledgePublishResult{}, invalidField(fmt.Sprintf("records[%d].document", i), err.Error())
			}
		}
		prop.Records = append(prop.Records, knowledge.RecordChange{Key: c.Key, Record: r})
	}
	snap, err := o.kernel.Publish(ctx, prop)
	if err != nil {
		return wire.KnowledgePublishResult{}, classify(err)
	}
	result := wire.KnowledgePublishResult{Snapshot: string(snap)}
	if in.Finding != "" {
		rec, err := o.Reconcile(ctx, in.Finding)
		if err != nil {
			rec = wire.KnowledgeReconcileResult{FeedbackID: in.Finding, ReconcileError: err.Error()}
		}
		result.Reconciliation = &rec
	}
	return result, nil
}

// Rollback restores an earlier published snapshot's knowledge as a new
// publication: restored rules take new revisions, and rule governance
// applies as to any publication (ADR-0050).
func (o *Operator) Rollback(ctx context.Context, in wire.KnowledgeRollbackRequest) (wire.KnowledgeRollbackResult, error) {
	snap, err := o.kernel.Rollback(ctx, knowledge.Snapshot(in.Base), knowledge.Snapshot(in.To), knowledge.Provenance{
		Author: in.Author, Summary: in.Summary, Rationale: in.Rationale, Validation: in.Validation,
	})
	if err != nil {
		return wire.KnowledgeRollbackResult{}, classify(err)
	}
	return wire.KnowledgeRollbackResult{Snapshot: string(snap)}, nil
}

// Fetch transfers a source's knowledge into quarantine, replacing any
// earlier fetched candidate for that source.
func (o *Operator) Fetch(ctx context.Context, source string, cred wire.KnowledgeCredentials) (wire.KnowledgeCandidateStatus, error) {
	cand, err := o.syncer.Fetch(ctx, source, toCredentials(cred))
	if err != nil {
		return wire.KnowledgeCandidateStatus{}, classify(err)
	}
	o.mu.Lock()
	if old := o.candidates[source]; old != nil {
		_ = old.Close()
	}
	o.candidates[source] = cand
	o.mu.Unlock()
	return candidateStatus(source, cand), nil
}

// Validate checks a fetched candidate's Git integrity, trust policy and
// complete catalog. A rejected candidate's problems are returned in the
// result, not as a request failure.
func (o *Operator) Validate(ctx context.Context, source string) (wire.KnowledgeCandidateStatus, error) {
	cand, err := o.candidate(source)
	if err != nil {
		return wire.KnowledgeCandidateStatus{}, err
	}
	verr := o.syncer.Validate(ctx, cand)
	if verr != nil && !errors.Is(verr, knowledgesync.ErrRejected) {
		return wire.KnowledgeCandidateStatus{}, classify(verr)
	}
	return candidateStatus(source, cand), nil
}

// Activate makes a validated fast-forward candidate the active knowledge.
func (o *Operator) Activate(ctx context.Context, source string, decision wire.KnowledgeDecision) (wire.KnowledgeActivation, error) {
	cand, err := o.candidate(source)
	if err != nil {
		return wire.KnowledgeActivation{}, err
	}
	if err := o.syncer.Activate(ctx, cand, knowledge.Provenance{
		Author: decision.Author, Summary: decision.Summary, Rationale: decision.Rationale,
		Finding: decision.Finding, Validation: decision.Validation,
	}); err != nil {
		return wire.KnowledgeActivation{}, classify(err)
	}
	o.mu.Lock()
	delete(o.candidates, source)
	o.mu.Unlock()
	return wire.KnowledgeActivation{Source: source, Snapshot: string(cand.Remote)}, nil
}

// Push sends the active knowledge to a source the policy permits to
// receive it, as a fast-forward only.
func (o *Operator) Push(ctx context.Context, source string, cred wire.KnowledgeCredentials) (wire.KnowledgePushResult, error) {
	res, err := o.syncer.Publish(ctx, source, toCredentials(cred))
	if err != nil {
		return wire.KnowledgePushResult{}, classify(err)
	}
	return wire.KnowledgePushResult{Before: string(res.Before), After: string(res.After), UpToDate: res.UpToDate, Bytes: res.Bytes, Objects: res.Objects}, nil
}

// DefaultActivationsLimit is Activations' limit when none is given (a
// limit of exactly 0 would otherwise list none at all).
const DefaultActivationsLimit = 50

// Activations lists accepted activations, newest first.
func (o *Operator) Activations(ctx context.Context, limit int) (wire.KnowledgeActivations, error) {
	if limit <= 0 {
		limit = DefaultActivationsLimit
	}
	recs, err := o.kernel.Activations(ctx, limit)
	if err != nil {
		return wire.KnowledgeActivations{}, classify(err)
	}
	out := wire.KnowledgeActivations{Activations: make([]wire.KnowledgeActivationRecord, len(recs))}
	for i, r := range recs {
		out.Activations[i] = wire.KnowledgeActivationRecord{
			Target: string(r.Target), Previous: string(r.Previous), When: r.When,
			Author: r.Provenance.Author, Summary: r.Provenance.Summary, Rationale: r.Provenance.Rationale,
			Finding: r.Provenance.Finding, Accepted: r.Accepted,
		}
	}
	return out, nil
}

// Reconcile resolves a feedback report as addressed once a local
// publication of this instance addresses it: its Finding trailer names the
// report, and it changed the report's procedure. It is idempotent and safe
// to call independently of Publish, including after a restart or an
// earlier interrupted attempt: an already-resolved report, or one with no
// addressing publication yet, is reported, not an error.
func (o *Operator) Reconcile(ctx context.Context, findingID string) (wire.KnowledgeReconcileResult, error) {
	if findingID == "" {
		return wire.KnowledgeReconcileResult{}, requiredField("finding_id")
	}
	f, err := o.fb.Feedback(ctx, findingID)
	if err != nil {
		return wire.KnowledgeReconcileResult{}, err
	}
	if f.Resolution != nil {
		return wire.KnowledgeReconcileResult{FeedbackID: findingID, Status: "already_resolved"}, nil
	}
	ref, err := refFromContext(f.Context)
	if err != nil {
		return wire.KnowledgeReconcileResult{}, classify(fmt.Errorf("%w: %w", knowledge.ErrInvalid, err))
	}
	pub, found, err := o.findLocalPublication(ctx, findingID, ref.Key)
	if err != nil {
		return wire.KnowledgeReconcileResult{}, err
	}
	if !found {
		return wire.KnowledgeReconcileResult{FeedbackID: findingID, Status: "no_publication_yet"}, nil
	}
	_, err = o.fb.ResolveFeedback(ctx, findingID, memory.FeedbackResolutionRequest{
		Outcome: memory.OutcomeAddressed,
		Reason:  "published improved knowledge",
		Actor:   "polaroid-knowledge-reconciler",
		References: []memory.ExternalReference{
			{URI: pub.String(), Note: "the publication that addressed this finding"},
		},
	})
	if err != nil && !errors.Is(err, memory.ErrFeedbackResolved) {
		return wire.KnowledgeReconcileResult{}, err
	}
	return wire.KnowledgeReconcileResult{FeedbackID: findingID, Status: "resolved", Publication: pub.String()}, nil
}

// findLocalPublication searches the knowledge history for a publication of
// this instance (never an imported one: an activation never counts as
// locally verified feedback resolution) whose Finding trailer is findingID
// and which changed key.
func (o *Operator) findLocalPublication(ctx context.Context, findingID, key string) (knowledge.PublicationRef, bool, error) {
	active, err := o.kernel.Active(ctx)
	if err != nil {
		return knowledge.PublicationRef{}, false, classify(err)
	}
	if active.Snapshot() == "" {
		return knowledge.PublicationRef{}, false, nil
	}
	pubs, err := o.kernel.History(ctx, active.Snapshot(), historyScanLimit)
	if err != nil {
		return knowledge.PublicationRef{}, false, classify(err)
	}
	inst, err := o.kernel.Instance(ctx)
	if err != nil {
		return knowledge.PublicationRef{}, false, classify(err)
	}
	for _, p := range pubs {
		if p.Provenance.Finding != findingID {
			continue
		}
		if !slices.ContainsFunc(p.Changed, func(c knowledge.KeyChange) bool { return c.Key == key && !c.Record }) {
			continue
		}
		origin, err := o.kernel.Origin(ctx, p.Snapshot)
		if err != nil {
			return knowledge.PublicationRef{}, false, classify(err)
		}
		if origin != knowledge.OriginLocal {
			continue
		}
		return knowledge.PublicationRef{Instance: inst, Snapshot: p.Snapshot, Key: key}, true, nil
	}
	return knowledge.PublicationRef{}, false, nil
}

// candidate returns the source's fetched candidate, or a not_found error
// naming the step the operator must do first.
func (o *Operator) candidate(source string) (*knowledgesync.Candidate, error) {
	o.mu.Lock()
	cand := o.candidates[source]
	o.mu.Unlock()
	if cand == nil {
		return nil, classify(fmt.Errorf("source %q has no fetched candidate: fetch it first: %w", source, knowledge.ErrNotFound))
	}
	return cand, nil
}

func toCredentials(c wire.KnowledgeCredentials) gitcore.Credentials {
	return gitcore.Credentials{
		Username: c.Username, Password: c.Password,
		SSHKey: []byte(c.SSHKey), SSHPassphrase: c.SSHPassphrase, KnownHosts: c.KnownHosts,
	}
}

func candidateStatus(source string, c *knowledgesync.Candidate) wire.KnowledgeCandidateStatus {
	st := wire.KnowledgeCandidateStatus{
		Source: source, Remote: string(c.Remote), Local: string(c.Local), Status: string(c.Status),
		Problems: c.Problems, Bytes: c.Bytes, Objects: c.Objects,
	}
	for _, in := range c.Incoming {
		st.Incoming = append(st.Incoming, wire.KnowledgeIncoming{
			Snapshot: string(in.Snapshot), Publisher: in.Publisher, When: in.When, Summary: in.Summary, Procedures: in.Procedures,
		})
	}
	return st
}
