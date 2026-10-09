package memory

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
	"uuid"
)

// Outcome is how a run ended.
type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
)

// Environment identifies where a run happened. Name is the identity;
// Attributes is a JSON object that is stored but never interpreted.
type Environment struct {
	Name       string
	Attributes jsontext.Value
}

// ExecutionRecord is the client-supplied content of an execution. BindingID
// is empty, and BindingRevision 0, when the run did not use a binding.
// Inputs and Evidence are JSON objects whose members are task knowledge.
type ExecutionRecord struct {
	ProcedureID     string
	Version         int
	BindingID       string
	BindingRevision int
	Repository      string
	Commit          string
	Environment     Environment
	Inputs          jsontext.Value
	Outcome         Outcome
	Evidence        jsontext.Value
	Children        []ChildExecution
}

// ChildExecution links an already-recorded execution to the parent's
// reference it fulfilled (ADR-0011).
type ChildExecution struct {
	Reference   string
	ExecutionID string
}

// LinkedChildError reports that the child at Index is already linked to
// another parent execution.
type LinkedChildError struct {
	Index    int
	ParentID string
}

func (e *LinkedChildError) Error() string {
	return fmt.Sprintf("child %d is already linked to execution %q", e.Index, e.ParentID)
}

// Execution is an immutable record of one finished run (ADR-0010). Identity
// is derived when it is read, never stored (ADR-0022).
type Execution struct {
	ID        string
	CreatedAt time.Time
	Identity  RepositoryIdentity
	ExecutionRecord
}

// ExecutionFilter selects executions (ADR-0021): optionally of one
// procedure, of one version of it (Version > 0), in one repository (by
// identity: every identifier of the repository Repository is registered
// to), and at one commit. Repositories and After are set by the service for
// the store; nil Repositories selects every repository.
type ExecutionFilter struct {
	ProcedureID  string
	Version      int
	Repository   string
	Commit       string
	Page         Page
	Repositories []string
	After        *Position
}

// A full SHA-1 or SHA-256 commit hash.
var commitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// validate checks r and returns it in stored form.
func (r ExecutionRecord) validate() (ExecutionRecord, error) {
	var p problems
	if r.ProcedureID == "" {
		p.add("procedure_id", "is required")
	}
	if r.Version < 1 {
		p.add("version", "must be a version number of at least 1")
	}
	switch {
	case r.BindingID == "" && r.BindingRevision != 0:
		p.add("binding_id", "is required when binding_revision is set")
	case r.BindingID != "" && r.BindingRevision < 1:
		p.add("binding_revision", "must be a revision number of at least 1 when binding_id is set")
	}
	checkRepository(&p, "repository", r.Repository)
	if !commitPattern.MatchString(r.Commit) {
		p.add("commit", "must be a full commit hash: 40 or 64 lowercase hex characters")
	}
	checkKey(&p, "environment.name", r.Environment.Name)
	r.Environment.Attributes = checkObject(&p, "environment.attributes", r.Environment.Attributes)
	r.Inputs = checkObject(&p, "inputs", r.Inputs)
	if r.Outcome != OutcomeSucceeded && r.Outcome != OutcomeFailed {
		p.add("outcome", `must be "succeeded" or "failed"`)
	}
	r.Evidence = checkObject(&p, "evidence", r.Evidence)
	if string(r.Evidence) == "{}" {
		p.add("evidence", "must not be empty")
	}
	r.Children = checkChildren(&p, r.Children)
	return r, p.err()
}

// checkChildren returns a copy of children, or nil if there are none.
func checkChildren(p *problems, children []ChildExecution) []ChildExecution {
	if len(children) == 0 {
		return nil
	}
	references := make(map[string]bool, len(children))
	executions := make(map[string]bool, len(children))
	for i, c := range children {
		field := fmt.Sprintf("children[%d]", i)
		checkKey(p, field+".reference", c.Reference)
		if c.Reference != "" && references[c.Reference] {
			p.add(field+".reference", "is already fulfilled by an earlier child")
		}
		references[c.Reference] = true
		switch {
		case c.ExecutionID == "":
			p.add(field+".execution_id", "is required")
		case executions[c.ExecutionID]:
			p.add(field+".execution_id", "is already listed as an earlier child")
		}
		executions[c.ExecutionID] = true
	}
	return append([]ChildExecution(nil), children...)
}

// RecordExecution stores in as a new execution. The procedure version must
// exist, and so must the binding revision if one is named; the binding must
// be for the same procedure and repository, and a pinned revision must pin
// the version that ran.
func (s *Service) RecordExecution(ctx context.Context, in ExecutionRecord) (Execution, error) {
	rec, err := in.validate()
	if err != nil {
		return Execution{}, err
	}
	// Versions and binding revisions are immutable and never deleted, so
	// checking them before the write cannot race with it.
	if err := s.checkExecutionTargets(ctx, rec); err != nil {
		return Execution{}, err
	}
	e := Execution{ID: uuid.NewV7().String(), CreatedAt: timestamp(), ExecutionRecord: rec}
	if err := s.store.CreateExecution(ctx, &e); err != nil {
		var linked *LinkedChildError
		if errors.As(err, &linked) {
			return Execution{}, &ValidationError{Problems: []FieldProblem{{
				Field:   fmt.Sprintf("children[%d].execution_id", linked.Index),
				Message: fmt.Sprintf("is already the child of execution %q", linked.ParentID),
			}}}
		}
		return Execution{}, fmt.Errorf("record execution: %w", err)
	}
	r, err := s.identity(ctx, e.Repository)
	if err != nil {
		return Execution{}, err
	}
	e.Identity = r.identity()
	return e, nil
}

func (s *Service) checkExecutionTargets(ctx context.Context, r ExecutionRecord) error {
	v, err := s.store.Version(ctx, r.ProcedureID, r.Version)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("read version: %w", err)
		}
		// Every procedure has version 1.
		if _, err := s.store.Version(ctx, r.ProcedureID, 1); err != nil {
			return describeLookup(err, fmt.Sprintf("procedure %q", r.ProcedureID))
		}
		return &ValidationError{Problems: []FieldProblem{{Field: "version", Message: fmt.Sprintf("procedure %q has no version %d", r.ProcedureID, r.Version)}}}
	}
	var p problems
	repositoryID, err := s.identityID(ctx, r.Repository)
	if err != nil {
		return err
	}
	if !v.Applicability.ApplicableIn(repositoryID) {
		p.add("version", fmt.Sprintf("version %d is %s, so it does not apply in repository %q", v.Number, v.Applicability.describe(), r.Repository))
	}
	if r.BindingID != "" {
		if err := s.checkBinding(ctx, r, &p); err != nil {
			return err
		}
	}
	if err := s.checkChildLinks(ctx, r, v, &p); err != nil {
		return err
	}
	return p.err()
}

// checkChildLinks checks each child against the parent version's references
// and run (ADR-0011). Whether a child already has another parent is left to
// the store, which decides it atomically.
func (s *Service) checkChildLinks(ctx context.Context, r ExecutionRecord, v Version, p *problems) error {
	for i, c := range r.Children {
		field := fmt.Sprintf("children[%d]", i)
		var ref *Reference
		for j := range v.References {
			if v.References[j].Name == c.Reference {
				ref = &v.References[j]
			}
		}
		if ref == nil {
			p.add(field+".reference", fmt.Sprintf("version %d has no reference named %q", v.Number, c.Reference))
		}
		child, err := s.store.Execution(ctx, c.ExecutionID)
		if errors.Is(err, ErrNotFound) {
			p.add(field+".execution_id", fmt.Sprintf("execution %q does not exist", c.ExecutionID))
			continue
		}
		if err != nil {
			return fmt.Errorf("read child execution: %w", err)
		}
		if child.Repository != r.Repository || child.Commit != r.Commit {
			p.add(field+".execution_id", fmt.Sprintf("ran in %s at %s, not the parent's %s at %s", child.Repository, child.Commit, r.Repository, r.Commit))
		}
		switch {
		case ref == nil:
		case child.ProcedureID != ref.ProcedureID:
			p.add(field+".execution_id", fmt.Sprintf("ran procedure %q, but reference %q targets %q", child.ProcedureID, ref.Name, ref.ProcedureID))
		case ref.VersionPolicy.Kind == PolicyPin && child.Version != ref.VersionPolicy.Pin:
			p.add(field+".execution_id", fmt.Sprintf("ran version %d, but reference %q pins version %d", child.Version, ref.Name, ref.VersionPolicy.Pin))
		default:
			cv, err := s.store.Version(ctx, child.ProcedureID, child.Version)
			if err != nil {
				return fmt.Errorf("read child version: %w", err)
			}
			if !v.Applicability.Admits(cv.Applicability) {
				p.add(field+".execution_id", fmt.Sprintf("ran version %d, which is %s; a %s version cannot compose it", child.Version, cv.Applicability.describe(), v.Applicability.describe()))
			}
		}
	}
	return nil
}

func (s *Service) checkBinding(ctx context.Context, r ExecutionRecord, p *problems) error {
	h, err := s.store.BindingHistory(ctx, r.BindingID)
	if err != nil {
		return describeLookup(err, fmt.Sprintf("binding %q", r.BindingID))
	}
	if h.Binding.ProcedureID != r.ProcedureID {
		p.add("binding_id", fmt.Sprintf("binds procedure %q, not %q", h.Binding.ProcedureID, r.ProcedureID))
	}
	if h.Binding.Repository != r.Repository {
		p.add("binding_id", fmt.Sprintf("belongs to repository %q, not %q", h.Binding.Repository, r.Repository))
	}
	switch {
	case r.BindingRevision > h.Binding.LatestRevision:
		p.add("binding_revision", fmt.Sprintf("binding %q has no revision %d", r.BindingID, r.BindingRevision))
	case h.Binding.ProcedureID != r.ProcedureID:
		// A pin of another procedure says nothing about this version.
	default:
		if policy := h.Revisions[r.BindingRevision-1].VersionPolicy; policy.Kind == PolicyPin && policy.Pin != r.Version {
			p.add("version", fmt.Sprintf("binding revision %d pins version %d", r.BindingRevision, policy.Pin))
		}
	}
	return nil
}

// Execution returns one execution.
func (s *Service) Execution(ctx context.Context, id string) (Execution, error) {
	e, err := s.store.Execution(ctx, id)
	if err != nil {
		return Execution{}, describeLookup(err, fmt.Sprintf("execution %q", id))
	}
	return e, nil
}

// ListExecutions returns the executions selected by f, oldest first, and the
// cursor of the next page if there is one.
func (s *Service) ListExecutions(ctx context.Context, f ExecutionFilter) ([]Execution, string, error) {
	var p problems
	switch {
	case f.Version < 0:
		p.add("version", "must be a version number of at least 1")
	case f.Version > 0 && f.ProcedureID == "":
		p.add("version", "requires procedure_id")
	}
	if f.Repository != "" {
		checkRepository(&p, "repository", f.Repository)
	}
	if f.Commit != "" && !commitPattern.MatchString(f.Commit) {
		p.add("commit", "must be a full commit hash: 40 or 64 lowercase hex characters")
	}
	g := newPager("executions", true, f.Page, f.ProcedureID, strconv.Itoa(f.Version), f.Repository, f.Commit)
	f.After = g.start(&p)
	if err := p.err(); err != nil {
		return nil, "", err
	}
	if f.Repository != "" {
		var err error
		if f.Repositories, _, err = s.sameRepository(ctx, f.Repository); err != nil {
			return nil, "", err
		}
	}
	executions, err := s.store.ListExecutions(ctx, f)
	if err != nil {
		return nil, "", fmt.Errorf("list executions: %w", err)
	}
	executions, next := trim(f.Page, executions, func(e Execution) string { return g.next("", e.CreatedAt, e.ID) })
	return executions, next, nil
}
