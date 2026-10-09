package memory

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"
	"uuid"
)

// FeedbackKind says whether a report describes a problem with Polaroid or
// suggests an improvement to it.
type FeedbackKind string

const (
	FeedbackProblem    FeedbackKind = "problem"
	FeedbackSuggestion FeedbackKind = "suggestion"
)

// SubjectType names what a report is about (ADR-0021).
type SubjectType string

const (
	SubjectService    SubjectType = "service"
	SubjectRepository SubjectType = "repository"
	SubjectProcedure  SubjectType = "procedure"
	SubjectBinding    SubjectType = "binding"
	SubjectExecution  SubjectType = "execution"
)

// Subject is what a report is about. Only the members of its Type may be
// set: RepositoryID for a repository; ProcedureID and optionally Version for
// a procedure; BindingID and optionally Revision for a binding; ExecutionID
// for an execution; none for the service.
type Subject struct {
	Type         SubjectType
	RepositoryID string
	ProcedureID  string
	Version      int
	BindingID    string
	Revision     int
	ExecutionID  string
}

// ID is the subject record's ID, "" for the service.
func (s Subject) ID() string {
	return s.RepositoryID + s.ProcedureID + s.BindingID + s.ExecutionID
}

// Number is the subject's procedure version or binding revision, 0 for the
// record as a whole.
func (s Subject) Number() int {
	return s.Version + s.Revision
}

// NewSubject returns the subject of type t with record ID id and version or
// revision number, as a store reads it back.
func NewSubject(t SubjectType, id string, number int) Subject {
	s := Subject{Type: t}
	switch t {
	case SubjectRepository:
		s.RepositoryID = id
	case SubjectProcedure:
		s.ProcedureID, s.Version = id, number
	case SubjectBinding:
		s.BindingID, s.Revision = id, number
	case SubjectExecution:
		s.ExecutionID = id
	}
	return s
}

// FeedbackRecord is the client-supplied content of a feedback report.
// Context is a JSON object that is stored but never interpreted; an absent
// context is stored as {}. Subject is nil for a report without one, which
// means the Polaroid service (ADR-0015). Repository and ExecutionID say
// where the report was made, and are empty when not given.
type FeedbackRecord struct {
	Kind        FeedbackKind
	Summary     string
	Details     string
	Reporter    string
	Context     jsontext.Value
	Subject     *Subject
	Repository  string
	ExecutionID string
}

// Feedback is an immutable report about Polaroid or a record it holds
// (ADR-0015, ADR-0021).
type Feedback struct {
	ID        string
	CreatedAt time.Time
	FeedbackRecord
}

// FeedbackFilter selects reports (ADR-0021). SubjectType service includes
// reports without a subject. Repository matches the report's repository,
// or a repository subject, by identity. Repositories, RepositoryID and After
// are set by the service for the store.
type FeedbackFilter struct {
	Kind           FeedbackKind
	SubjectType    SubjectType
	SubjectID      string
	SubjectVersion int
	Repository     string
	Page           Page
	Repositories   []string
	RepositoryID   string
	After          *Position
}

// lineBreaks are the Unicode line terminators, which a summary must not
// contain.
const lineBreaks = "\n\v\f\r\u0085\u2028\u2029"

// validate checks r's shape and returns it in stored form.
func (r FeedbackRecord) validate() (FeedbackRecord, error) {
	var p problems
	checkFeedbackKind(&p, r.Kind)
	checkLine(&p, "summary", r.Summary)
	checkText(&p, "details", r.Details)
	checkKey(&p, "reporter", r.Reporter)
	if len(r.Context) == 0 {
		r.Context = jsontext.Value("{}")
	} else {
		r.Context = checkObject(&p, "context", r.Context)
	}
	if r.Subject != nil {
		checkSubject(&p, *r.Subject)
	}
	if r.Repository != "" {
		checkRepository(&p, "repository", r.Repository)
	}
	return r, p.err()
}

func checkSubject(p *problems, s Subject) {
	members := []struct {
		name  string
		set   bool
		owner SubjectType
	}{
		{"repository_id", s.RepositoryID != "", SubjectRepository},
		{"procedure_id", s.ProcedureID != "", SubjectProcedure},
		{"version", s.Version != 0, SubjectProcedure},
		{"binding_id", s.BindingID != "", SubjectBinding},
		{"revision", s.Revision != 0, SubjectBinding},
		{"execution_id", s.ExecutionID != "", SubjectExecution},
	}
	switch s.Type {
	case SubjectService, SubjectRepository, SubjectProcedure, SubjectBinding, SubjectExecution:
	default:
		p.add("subject.type", `must be "service", "repository", "procedure", "binding" or "execution"`)
		return
	}
	for _, m := range members {
		if m.set && m.owner != s.Type {
			p.add("subject."+m.name, fmt.Sprintf("does not belong to a %s subject", s.Type))
		}
	}
	if s.Type != SubjectService && s.ID() == "" {
		p.add("subject."+string(s.Type)+"_id", "is required")
	}
	if s.Version < 0 {
		p.add("subject.version", "must be a version number of at least 1")
	}
	if s.Revision < 0 {
		p.add("subject.revision", "must be a revision number of at least 1")
	}
}

func checkFeedbackKind(p *problems, k FeedbackKind) {
	if k != FeedbackProblem && k != FeedbackSuggestion {
		p.add("kind", `must be "problem" or "suggestion"`)
	}
}

// ReportFeedback stores in as a new feedback report. It changes no other
// record. Its subject must exist, and its repository and execution must
// agree with the subject and with each other; IDs in Context are not
// checked.
func (s *Service) ReportFeedback(ctx context.Context, in FeedbackRecord) (Feedback, error) {
	rec, err := in.validate()
	if err != nil {
		return Feedback{}, err
	}
	// Every record a report can name is immutable and never deleted, so
	// checking it before the write cannot race with it.
	if err := s.checkFeedbackTargets(ctx, rec); err != nil {
		return Feedback{}, err
	}
	f := Feedback{ID: uuid.NewV7().String(), CreatedAt: timestamp(), FeedbackRecord: rec}
	if err := s.store.CreateFeedback(ctx, &f); err != nil {
		return Feedback{}, fmt.Errorf("report feedback: %w", err)
	}
	return f, nil
}

// checkFeedbackTargets checks that the subject and the execution exist, and
// that the execution, the repository and the subject agree.
func (s *Service) checkFeedbackTargets(ctx context.Context, r FeedbackRecord) error {
	var p problems
	var run *Execution
	if r.ExecutionID != "" {
		e, err := s.store.Execution(ctx, r.ExecutionID)
		switch {
		case errors.Is(err, ErrNotFound):
			p.add("execution_id", fmt.Sprintf("execution %q does not exist", r.ExecutionID))
		case err != nil:
			return fmt.Errorf("read execution: %w", err)
		default:
			run = &e
		}
	}
	// within is the repository identifier the subject belongs to, if any.
	within, err := s.checkSubjectTarget(ctx, r.Subject, run, &p)
	if err != nil {
		return err
	}
	if run != nil {
		if within != "" {
			if same, err := s.sameIdentity(ctx, within, run.Repository); err != nil {
				return err
			} else if !same {
				p.add("execution_id", fmt.Sprintf("ran in %q, not in the subject's repository %q", run.Repository, within))
			}
		} else {
			within = run.Repository
		}
	}
	if within != "" && r.Repository != "" {
		if same, err := s.sameIdentity(ctx, within, r.Repository); err != nil {
			return err
		} else if !same {
			p.add("repository", fmt.Sprintf("is not the repository of the subject or execution (%q)", within))
		}
	}
	return p.err()
}

// checkSubjectTarget checks that sub exists, and that run, if any, belongs to
// it. It returns the repository identifier sub belongs to, if it has one.
func (s *Service) checkSubjectTarget(ctx context.Context, sub *Subject, run *Execution, p *problems) (string, error) {
	if sub == nil || sub.Type == SubjectService {
		return "", nil
	}
	field := "subject." + string(sub.Type) + "_id"
	id := sub.ID()
	missing := func(err error, what string) (bool, error) {
		if errors.Is(err, ErrNotFound) {
			p.add(field, fmt.Sprintf("%s %q does not exist", what, id))
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("read feedback subject: %w", err)
		}
		return true, nil
	}
	switch sub.Type {
	case SubjectRepository:
		repo, err := s.store.Repository(ctx, id)
		if ok, err := missing(err, "repository"); !ok {
			return "", err
		}
		return repo.Identifier, nil
	case SubjectProcedure:
		h, err := s.store.History(ctx, id)
		if ok, err := missing(err, "procedure"); !ok {
			return "", err
		}
		if sub.Version > h.Procedure.LatestVersion {
			p.add("subject.version", fmt.Sprintf("procedure %q has no version %d", id, sub.Version))
		}
		if run != nil && (run.ProcedureID != id || (sub.Version != 0 && run.Version != sub.Version)) {
			p.add("execution_id", fmt.Sprintf("ran procedure %q version %d, not the subject", run.ProcedureID, run.Version))
		}
		return "", nil
	case SubjectBinding:
		h, err := s.store.BindingHistory(ctx, id)
		if ok, err := missing(err, "binding"); !ok {
			return "", err
		}
		if sub.Revision > h.Binding.LatestRevision {
			p.add("subject.revision", fmt.Sprintf("binding %q has no revision %d", id, sub.Revision))
		}
		if run != nil && (run.BindingID != id || (sub.Revision != 0 && run.BindingRevision != sub.Revision)) {
			p.add("execution_id", "did not run under the subject binding or revision")
		}
		return h.Binding.Repository, nil
	default: // SubjectExecution
		e, err := s.store.Execution(ctx, id)
		if ok, err := missing(err, "execution"); !ok {
			return "", err
		}
		if run != nil && run.ID != id {
			p.add("execution_id", "must be the subject execution, or be left out")
		}
		return e.Repository, nil
	}
}

// sameIdentity reports whether two identifiers are equal or registered to
// the same repository.
func (s *Service) sameIdentity(ctx context.Context, a, b string) (bool, error) {
	if a == b {
		return true, nil
	}
	ids, _, err := s.sameRepository(ctx, a)
	if err != nil {
		return false, err
	}
	return slices.Contains(ids, b), nil
}

// Feedback returns one feedback report.
func (s *Service) Feedback(ctx context.Context, id string) (Feedback, error) {
	f, err := s.store.Feedback(ctx, id)
	if err != nil {
		return Feedback{}, describeLookup(err, fmt.Sprintf("feedback %q", id))
	}
	return f, nil
}

// ListFeedback returns the reports f selects, oldest first, and the cursor
// of the next page if there is one.
func (s *Service) ListFeedback(ctx context.Context, f FeedbackFilter) ([]Feedback, string, error) {
	var p problems
	if f.Kind != "" {
		checkFeedbackKind(&p, f.Kind)
	}
	switch f.SubjectType {
	case "":
		if f.SubjectID != "" {
			p.add("subject_id", "requires subject_type")
		}
	case SubjectService:
		if f.SubjectID != "" {
			p.add("subject_id", "a service subject has no ID")
		}
	case SubjectRepository, SubjectProcedure, SubjectBinding, SubjectExecution:
	default:
		p.add("subject_type", `must be "service", "repository", "procedure", "binding" or "execution"`)
	}
	switch {
	case f.SubjectVersion < 0:
		p.add("subject_version", "must be at least 1")
	case f.SubjectVersion > 0 && (f.SubjectID == "" || (f.SubjectType != SubjectProcedure && f.SubjectType != SubjectBinding)):
		p.add("subject_version", "requires subject_id of a procedure or binding")
	}
	if f.Repository != "" {
		checkRepository(&p, "repository", f.Repository)
	}
	g := newPager("feedback", true, f.Page, string(f.Kind), string(f.SubjectType), f.SubjectID, strconv.Itoa(f.SubjectVersion), f.Repository)
	f.After = g.start(&p)
	if err := p.err(); err != nil {
		return nil, "", err
	}
	if f.Repository != "" {
		var err error
		if f.Repositories, f.RepositoryID, err = s.sameRepository(ctx, f.Repository); err != nil {
			return nil, "", err
		}
	}
	reports, err := s.store.ListFeedback(ctx, f)
	if err != nil {
		return nil, "", fmt.Errorf("list feedback: %w", err)
	}
	reports, next := trim(f.Page, reports, func(r Feedback) string { return g.next("", r.CreatedAt, r.ID) })
	return reports, next, nil
}
