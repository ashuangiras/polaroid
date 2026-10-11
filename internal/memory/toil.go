package memory

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"uuid"
)

// ToilStatus is an agent's assessment state of a toil item (ADR-0034). It is
// not execution verification: Polaroid records it and checks its structure,
// and derives nothing from it.
type ToilStatus string

const (
	ToilObserved    ToilStatus = "observed"
	ToilCandidate   ToilStatus = "candidate"
	ToilImplemented ToilStatus = "implemented"
	ToilValidated   ToilStatus = "validated"
	ToilRetired     ToilStatus = "retired"
)

// ToilStatuses lists the statuses in lifecycle order.
var ToilStatuses = []ToilStatus{ToilObserved, ToilCandidate, ToilImplemented, ToilValidated, ToilRetired}

// toilTransitions lists, for each status, the statuses its next revision may
// have. Every status may follow itself.
var toilTransitions = map[ToilStatus][]ToilStatus{
	ToilObserved:    {ToilObserved, ToilCandidate, ToilRetired},
	ToilCandidate:   {ToilCandidate, ToilObserved, ToilImplemented, ToilRetired},
	ToilImplemented: {ToilImplemented, ToilCandidate, ToilValidated, ToilRetired},
	ToilValidated:   {ToilValidated, ToilCandidate, ToilRetired},
	ToilRetired:     {ToilRetired, ToilObserved},
}

// ToilTransitionAllowed reports whether a revision with status to may follow
// one with status from.
func ToilTransitionAllowed(from, to ToilStatus) bool {
	return slices.Contains(toilTransitions[from], to)
}

// ToilItem is the stable identity of a toil item, with a summary of its
// latest revision. Its ID and canonical key never change.
type ToilItem struct {
	ID             string
	CanonicalKey   string
	CreatedAt      time.Time
	LatestRevision int
	Activity       string
	Status         ToilStatus
	Applicability  Applicability
	UpdatedAt      time.Time
}

// ToilSource is an exact procedure version where the activity happens. Steps
// are declared identifiers, never checked against the version's
// instructions (ADR-0034).
type ToilSource struct {
	ProcedureID string
	Version     int
	Steps       []string
}

// ToilEvidence cites an execution and says what it shows. Polaroid derives
// nothing from the execution's outcome or verification.
type ToilEvidence struct {
	ExecutionID string
	Note        string
}

// ExternalReference is a declared reference to something outside the
// catalog. It is stored and returned, never fetched or executed.
type ExternalReference struct {
	URI  string
	Note string
}

// ToilReplacement identifies what replaces the activity.
type ToilReplacement struct {
	Summary    string
	References []ExternalReference
}

// ToilValidation is the evidence an agent offers that a replacement meets
// the proposal's acceptance criteria, with its rationale.
type ToilValidation struct {
	Rationale  string
	Executions []ToilEvidence
	References []ExternalReference
}

// ToilContent is the author-supplied content of one toil revision.
// Assessment and Proposal are JSON objects of task knowledge; Polaroid
// checks only the members ADR-0034 names. Proposal, Replacement and
// Validation are nil when absent.
type ToilContent struct {
	Activity        string
	Applicability   Applicability
	Sources         []ToilSource
	Observations    []ToilEvidence
	Assessment      jsontext.Value
	Proposal        jsontext.Value
	Status          ToilStatus
	StatusRationale string
	Replacement     *ToilReplacement
	Validation      *ToilValidation
	RevisionReason  string
	Author          string
}

// ToilRevision is one immutable revision of a toil item, numbered
// contiguously from 1.
type ToilRevision struct {
	ToilItemID string
	Number     int
	CreatedAt  time.Time
	ToilContent
}

// ToilHistory is a toil item with all of its revisions, oldest first.
type ToilHistory struct {
	Item      ToilItem
	Revisions []ToilRevision
}

// NewToilItem asks to create a toil item and its revision 1.
type NewToilItem struct {
	CanonicalKey string
	Content      ToilContent
}

// ToilRevise asks to append a revision derived from BaseRevision, which must
// be the item's latest revision when it is stored.
type ToilRevise struct {
	BaseRevision int
	Content      ToilContent
}

// ToilFilter selects toil items by their latest revision (ADR-0034).
// Repository keeps shared items and those local to its repository; After
// and Snapshot are set by the service for the store.
type ToilFilter struct {
	Status      ToilStatus
	Repository  string
	ProcedureID string
	Page        Page
	After       *Position
	Snapshot    Snapshot
}

// assessmentClaims are the assessment members whose status Polaroid checks.
var assessmentClaims = []string{"repetition", "burden"}

// claimStatuses are the values a claim's status may take.
var claimStatuses = []string{"observed", "hypothesis", "unknown"}

// proposalMembers are the members every proposal must have.
var proposalMembers = []string{"inputs", "outputs", "remaining_judgment", "failure_and_recovery", "acceptance_criteria"}

// validate checks n and returns its content in stored form.
func (n NewToilItem) validate() (ToilContent, error) {
	var p problems
	checkKey(&p, "canonical_key", n.CanonicalKey)
	c := checkToilContent(&p, n.Content)
	if c.Status != "" && c.Status != ToilObserved && c.Status != ToilCandidate {
		p.add("revision.status", `must be "observed" or "candidate" for revision 1`)
	}
	return c, toilSizes(p, c)
}

// validate checks r and returns its content in stored form.
func (r ToilRevise) validate() (ToilContent, error) {
	var p problems
	if r.BaseRevision < 1 {
		p.add("base_revision", "must be a revision number of at least 1")
	}
	c := checkToilContent(&p, r.Content)
	return c, toilSizes(p, c)
}

// toilSizes returns p's error, else c's limit error, if any.
func toilSizes(p problems, c ToilContent) error {
	if err := p.err(); err != nil {
		return err
	}
	var s sizes
	c.measure(&s)
	return s.err()
}

func checkToilContent(p *problems, c ToilContent) ToilContent {
	const f = "revision."
	checkLine(p, f+"activity", c.Activity)
	if c.Applicability.Kind == ScopeUnspecified {
		p.add(f+"applicability", `is required: {"shared": {}} or {"repository": "<repository id>"}`)
	} else {
		checkApplicability(p, f+"applicability", c.Applicability)
	}
	c.Sources = checkSources(p, c.Sources)
	c.Observations = checkEvidence(p, f+"observations", c.Observations)
	c.Assessment = checkAssessment(p, c.Assessment)
	if len(c.Proposal) > 0 {
		c.Proposal = checkProposal(p, c.Proposal)
	} else {
		c.Proposal = nil
	}
	if !slices.Contains(ToilStatuses, c.Status) {
		p.add(f+"status", `must be "observed", "candidate", "implemented", "validated" or "retired"`)
	}
	checkText(p, f+"status_rationale", c.StatusRationale)
	if c.Replacement != nil {
		r := *c.Replacement
		checkText(p, f+"replacement.summary", r.Summary)
		r.References = checkReferencesList(p, f+"replacement.references", r.References)
		if len(r.References) == 0 {
			p.add(f+"replacement.references", "must name at least one reference that identifies the replacement")
		}
		c.Replacement = &r
	}
	if c.Validation != nil {
		v := *c.Validation
		checkText(p, f+"validation.rationale", v.Rationale)
		v.Executions = checkEvidence(p, f+"validation.executions", v.Executions)
		v.References = checkReferencesList(p, f+"validation.references", v.References)
		if len(v.Executions) == 0 && len(v.References) == 0 {
			p.add(f+"validation", "must cite at least one execution or reference as evidence")
		}
		c.Validation = &v
	}
	checkToilStructure(p, c)
	checkText(p, f+"revision_reason", c.RevisionReason)
	checkKey(p, f+"author", c.Author)
	return c
}

// checkToilStructure checks what c's status requires and allows (ADR-0034).
func checkToilStructure(p *problems, c ToilContent) {
	const f = "revision."
	requires := func(field string, present bool) {
		if !present {
			p.add(f+field, fmt.Sprintf("is required for status %q", c.Status))
		}
	}
	refuses := func(field string, present bool, allowed string) {
		if present {
			p.add(f+field, fmt.Sprintf("is allowed only with status %s, not %q", allowed, c.Status))
		}
	}
	proposal, replacement, validation := c.Proposal != nil, c.Replacement != nil, c.Validation != nil
	switch c.Status {
	case ToilObserved:
		refuses("replacement", replacement, `"implemented", "validated" or "retired"`)
		refuses("validation", validation, `"validated" or "retired"`)
	case ToilCandidate:
		requires("proposal", proposal)
		refuses("replacement", replacement, `"implemented", "validated" or "retired"`)
		refuses("validation", validation, `"validated" or "retired"`)
	case ToilImplemented:
		requires("proposal", proposal)
		requires("replacement", replacement)
		refuses("validation", validation, `"validated" or "retired"`)
	case ToilValidated:
		requires("proposal", proposal)
		requires("replacement", replacement)
		requires("validation", validation)
	}
}

// checkSources returns the sources in stored form: at least one, each an
// exact version, unique per revision, with steps normalised to nil when
// empty.
func checkSources(p *problems, sources []ToilSource) []ToilSource {
	if len(sources) == 0 {
		p.add("revision.sources", "must name at least one procedure version")
		return nil
	}
	out := make([]ToilSource, len(sources))
	seen := make(map[string]bool, len(sources))
	for i, s := range sources {
		field := fmt.Sprintf("revision.sources[%d]", i)
		if s.ProcedureID == "" {
			p.add(field+".procedure_id", "is required")
		}
		if s.Version < 1 {
			p.add(field+".version", "must be a version number of at least 1")
		}
		key := fmt.Sprintf("%s@%d", s.ProcedureID, s.Version)
		if seen[key] {
			p.add(field, "duplicates an earlier source")
		}
		seen[key] = true
		steps := make(map[string]bool, len(s.Steps))
		for j, step := range s.Steps {
			sf := fmt.Sprintf("%s.steps[%d]", field, j)
			checkLine(p, sf, step)
			if steps[step] {
				p.add(sf, "duplicates an earlier step")
			}
			steps[step] = true
		}
		if len(s.Steps) == 0 {
			s.Steps = nil
		}
		out[i] = s
	}
	return out
}

// checkEvidence returns execution citations in stored form, nil when there
// are none: each names an execution once and says what it shows.
func checkEvidence(p *problems, field string, cites []ToilEvidence) []ToilEvidence {
	if len(cites) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(cites))
	for i, e := range cites {
		f := fmt.Sprintf("%s[%d]", field, i)
		if e.ExecutionID == "" {
			p.add(f+".execution_id", "is required")
		} else if seen[e.ExecutionID] {
			p.add(f+".execution_id", "duplicates an earlier execution")
		}
		seen[e.ExecutionID] = true
		checkText(p, f+".note", e.Note)
	}
	return slices.Clone(cites)
}

// checkReferencesList returns declared references in stored form, nil when
// there are none.
func checkReferencesList(p *problems, field string, refs []ExternalReference) []ExternalReference {
	if len(refs) == 0 {
		return nil
	}
	for i, r := range refs {
		f := fmt.Sprintf("%s[%d]", field, i)
		checkLine(p, f+".uri", r.URI)
		if r.Note != "" {
			checkText(p, f+".note", r.Note)
		}
	}
	return slices.Clone(refs)
}

// checkAssessment accepts a JSON object whose repetition and burden members
// are objects with a status of observed, hypothesis or unknown. Every other
// member is task knowledge and is not interpreted.
func checkAssessment(p *problems, v jsontext.Value) jsontext.Value {
	c := checkObject(p, "revision.assessment", v)
	if c == nil {
		return nil
	}
	members := objectMembers(c)
	for _, name := range assessmentClaims {
		field := "revision.assessment." + name
		raw, ok := members[name]
		if !ok {
			p.add(field, `is required: {"status": "observed" | "hypothesis" | "unknown", ...}`)
			continue
		}
		var claim map[string]jsontext.Value
		if json.Unmarshal(raw, &claim) != nil {
			p.add(field, "must be a JSON object")
			continue
		}
		var status string
		if json.Unmarshal(claim["status"], &status) != nil || !slices.Contains(claimStatuses, status) {
			p.add(field+".status", `must be "observed", "hypothesis" or "unknown"`)
		}
	}
	return c
}

// checkProposal accepts a JSON object with the members every proposal has:
// non-null inputs, outputs, remaining_judgment and failure_and_recovery,
// and acceptance_criteria, a non-empty array of non-blank strings.
func checkProposal(p *problems, v jsontext.Value) jsontext.Value {
	c := checkObject(p, "revision.proposal", v)
	if c == nil {
		return nil
	}
	members := objectMembers(c)
	for _, name := range proposalMembers {
		raw, ok := members[name]
		if !ok || raw.Kind() == jsontext.KindNull {
			p.add("revision.proposal."+name, "is required")
		}
	}
	if raw, ok := members["acceptance_criteria"]; ok && raw.Kind() != jsontext.KindNull {
		var criteria []string
		switch {
		case json.Unmarshal(raw, &criteria) != nil:
			p.add("revision.proposal.acceptance_criteria", "must be an array of strings")
		case len(criteria) == 0:
			p.add("revision.proposal.acceptance_criteria", "must list at least one criterion")
		case slices.ContainsFunc(criteria, func(s string) bool { return strings.TrimSpace(s) == "" }):
			p.add("revision.proposal.acceptance_criteria", "must not contain a blank criterion")
		}
	}
	return c
}

// objectMembers returns the members of a valid, compact JSON object.
func objectMembers(v jsontext.Value) map[string]jsontext.Value {
	var m map[string]jsontext.Value
	_ = json.Unmarshal(v, &m)
	return m
}

// CreateToilItem creates a toil item with a new stable ID and stores
// in.Content as its revision 1.
func (s *Service) CreateToilItem(ctx context.Context, in NewToilItem) (ToilHistory, error) {
	c, err := in.validate()
	if err != nil {
		return ToilHistory{}, err
	}
	if err := s.checkToilLinks(ctx, c); err != nil {
		return ToilHistory{}, err
	}
	now := timestamp()
	item := ToilItem{ID: uuid.NewV7().String(), CanonicalKey: in.CanonicalKey, CreatedAt: now, LatestRevision: 1,
		Activity: c.Activity, Status: c.Status, Applicability: c.Applicability, UpdatedAt: now}
	r := ToilRevision{ToilItemID: item.ID, Number: 1, CreatedAt: now, ToilContent: c}
	if err := s.store.CreateToilItem(ctx, item, r); err != nil {
		if errors.Is(err, ErrCanonicalKeyExists) {
			return ToilHistory{}, fmt.Errorf("toil item %q: %w", in.CanonicalKey, ErrCanonicalKeyExists)
		}
		return ToilHistory{}, fmt.Errorf("create toil item: %w", err)
	}
	return ToilHistory{Item: item, Revisions: []ToilRevision{r}}, nil
}

// ReviseToilItem appends in.Content as the next revision of the item. It
// fails with a *RevisionConflictError, leaving history untouched, unless
// in.BaseRevision is the latest revision, and with a *ValidationError when
// the status may not follow the base revision's.
func (s *Service) ReviseToilItem(ctx context.Context, id string, in ToilRevise) (ToilRevision, error) {
	c, err := in.validate()
	if err != nil {
		return ToilRevision{}, err
	}
	h, err := s.store.ToilHistory(ctx, id)
	if err != nil {
		return ToilRevision{}, describeLookup(err, fmt.Sprintf("toil item %q", id))
	}
	// The base must be the latest; the store checks it again atomically, and
	// a stored revision never changes, so its status is the one to follow.
	if latest := h.Item.LatestRevision; in.BaseRevision != latest {
		return ToilRevision{}, &RevisionConflictError{BaseRevision: in.BaseRevision, LatestRevision: latest}
	}
	if from := h.Revisions[in.BaseRevision-1].Status; !ToilTransitionAllowed(from, c.Status) {
		return ToilRevision{}, &ValidationError{Problems: []FieldProblem{{Field: "revision.status",
			Message: fmt.Sprintf("%q cannot follow %q; allowed: %s", c.Status, from, joinStatuses(toilTransitions[from]))}}}
	}
	if err := s.checkToilLinks(ctx, c); err != nil {
		return ToilRevision{}, err
	}
	r := ToilRevision{ToilItemID: id, Number: in.BaseRevision + 1, CreatedAt: timestamp(), ToilContent: c}
	if err := s.store.AppendToilRevision(ctx, in.BaseRevision, r); err != nil {
		var conflict *RevisionConflictError
		switch {
		case errors.Is(err, ErrNotFound):
			return ToilRevision{}, fmt.Errorf("toil item %q: %w", id, ErrNotFound)
		case errors.As(err, &conflict):
			return ToilRevision{}, conflict
		}
		return ToilRevision{}, fmt.Errorf("append toil revision: %w", err)
	}
	return r, nil
}

func joinStatuses(ss []ToilStatus) string {
	names := make([]string, len(ss))
	for i, s := range ss {
		names[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(names, ", ")
}

// checkToilLinks checks that every linked procedure version and execution
// exists and, for a local item, belongs to its repository (ADR-0034).
// Procedures, versions, executions and repositories are never deleted, so
// checking before the write cannot race with it.
func (s *Service) checkToilLinks(ctx context.Context, c ToilContent) error {
	var p problems
	var local *Repository
	if c.Applicability.Kind == ScopeLocal {
		r, err := s.store.Repository(ctx, c.Applicability.RepositoryID)
		switch {
		case errors.Is(err, ErrNotFound):
			p.add("revision.applicability.repository", fmt.Sprintf("repository %q is not registered", c.Applicability.RepositoryID))
		case err != nil:
			return fmt.Errorf("read repository: %w", err)
		default:
			local = &r
		}
	}
	for i, src := range c.Sources {
		field := fmt.Sprintf("revision.sources[%d]", i)
		h, err := s.store.History(ctx, src.ProcedureID)
		switch {
		case errors.Is(err, ErrNotFound):
			p.add(field+".procedure_id", fmt.Sprintf("procedure %q does not exist", src.ProcedureID))
			continue
		case err != nil:
			return fmt.Errorf("read source procedure: %w", err)
		case src.Version > h.Procedure.LatestVersion:
			p.add(field+".version", fmt.Sprintf("procedure %q has no version %d", src.ProcedureID, src.Version))
			continue
		}
		if a := h.Versions[src.Version-1].Applicability; local != nil && !a.ApplicableIn(local.ID) {
			p.add(field, fmt.Sprintf("version %d of procedure %q is %s, so a toil item local to repository %q cannot cite it",
				src.Version, src.ProcedureID, a.describe(), local.ID))
		}
	}
	cited := []struct {
		field string
		cites []ToilEvidence
	}{{"revision.observations", c.Observations}}
	if c.Validation != nil {
		cited = append(cited, struct {
			field string
			cites []ToilEvidence
		}{"revision.validation.executions", c.Validation.Executions})
	}
	for _, list := range cited {
		for i, e := range list.cites {
			field := fmt.Sprintf("%s[%d].execution_id", list.field, i)
			run, err := s.store.Execution(ctx, e.ExecutionID)
			switch {
			case errors.Is(err, ErrNotFound):
				p.add(field, fmt.Sprintf("execution %q does not exist", e.ExecutionID))
			case err != nil:
				return fmt.Errorf("read cited execution: %w", err)
			case local != nil && !slices.Contains(local.Identifiers(), run.Repository):
				p.add(field, fmt.Sprintf("ran in %q, which is not an identifier of repository %q", run.Repository, local.ID))
			}
		}
	}
	return p.err()
}

// ToilItem returns a toil item and all of its revisions.
func (s *Service) ToilItem(ctx context.Context, id string) (ToilHistory, error) {
	h, err := s.store.ToilHistory(ctx, id)
	if err != nil {
		return ToilHistory{}, describeLookup(err, fmt.Sprintf("toil item %q", id))
	}
	return h, nil
}

// ToilItemByKey returns the toil item with the given canonical key and all
// of its revisions.
func (s *Service) ToilItemByKey(ctx context.Context, canonicalKey string) (ToilHistory, error) {
	h, err := s.store.ToilHistoryByKey(ctx, canonicalKey)
	if err != nil {
		return ToilHistory{}, describeLookup(err, fmt.Sprintf("toil item with canonical key %q", canonicalKey))
	}
	return h, nil
}

// ToilRevision returns one revision of a toil item.
func (s *Service) ToilRevision(ctx context.Context, id string, number int) (ToilRevision, error) {
	if number < 1 {
		return ToilRevision{}, &ValidationError{Problems: []FieldProblem{{Field: "revision", Message: "must be a revision number of at least 1"}}}
	}
	r, err := s.store.ToilRevision(ctx, id, number)
	if err != nil {
		return ToilRevision{}, describeLookup(err, fmt.Sprintf("toil item %q revision %d", id, number))
	}
	return r, nil
}

// ListToilItems returns summaries of the toil items f selects, ordered by
// canonical key, and the cursor of the next page if there is one.
func (s *Service) ListToilItems(ctx context.Context, f ToilFilter) ([]ToilItem, string, error) {
	var p problems
	if f.Status != "" && !slices.Contains(ToilStatuses, f.Status) {
		p.add("status", `must be "observed", "candidate", "implemented", "validated" or "retired"`)
	}
	if f.Repository != "" {
		checkRepository(&p, "repository", f.Repository)
	}
	g := newPager("toil-items", false, f.Page, string(f.Status), f.Repository, f.ProcedureID)
	f.After = g.start(&p)
	if err := p.err(); err != nil {
		return nil, "", err
	}
	if f.Page.Snapshot && f.After == nil {
		var err error
		if g.Snapshot, err = s.store.ToilSnapshot(ctx); err != nil {
			return nil, "", fmt.Errorf("read snapshot boundary: %w", err)
		}
	}
	f.Snapshot = g.Snapshot
	items, err := s.store.ListToilItems(ctx, f)
	if err != nil {
		return nil, "", listError("toil items", err)
	}
	items, next := trim(f.Page, items, func(t ToilItem) string { return g.next(t.CanonicalKey, time.Time{}, t.ID) })
	return items, next, nil
}
