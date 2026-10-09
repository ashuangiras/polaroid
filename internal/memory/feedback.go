package memory

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"strings"
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

// FeedbackRecord is the client-supplied content of a feedback report.
// Context is a JSON object that is stored but never interpreted; an absent
// context is stored as {}.
type FeedbackRecord struct {
	Kind     FeedbackKind
	Summary  string
	Details  string
	Reporter string
	Context  jsontext.Value
}

// Feedback is an immutable report about Polaroid itself (ADR-0015).
type Feedback struct {
	ID        string
	CreatedAt time.Time
	FeedbackRecord
}

// lineBreaks are the Unicode line terminators, which a summary must not
// contain.
const lineBreaks = "\n\v\f\r\u0085\u2028\u2029"

// validate checks r and returns it in stored form.
func (r FeedbackRecord) validate() (FeedbackRecord, error) {
	var p problems
	checkFeedbackKind(&p, r.Kind)
	checkText(&p, "summary", r.Summary)
	if strings.TrimSpace(r.Summary) != "" && strings.ContainsAny(r.Summary, lineBreaks) {
		p.add("summary", "must be a single line")
	}
	checkText(&p, "details", r.Details)
	checkKey(&p, "reporter", r.Reporter)
	if len(r.Context) == 0 {
		r.Context = jsontext.Value("{}")
	} else {
		r.Context = checkObject(&p, "context", r.Context)
	}
	return r, p.err()
}

func checkFeedbackKind(p *problems, k FeedbackKind) {
	if k != FeedbackProblem && k != FeedbackSuggestion {
		p.add("kind", `must be "problem" or "suggestion"`)
	}
}

// ReportFeedback stores in as a new feedback report. It reads and changes no
// other record: IDs in Context are not checked.
func (s *Service) ReportFeedback(ctx context.Context, in FeedbackRecord) (Feedback, error) {
	rec, err := in.validate()
	if err != nil {
		return Feedback{}, err
	}
	f := Feedback{ID: uuid.NewV7().String(), CreatedAt: timestamp(), FeedbackRecord: rec}
	if err := s.store.CreateFeedback(ctx, f); err != nil {
		return Feedback{}, fmt.Errorf("report feedback: %w", err)
	}
	return f, nil
}

// Feedback returns one feedback report.
func (s *Service) Feedback(ctx context.Context, id string) (Feedback, error) {
	f, err := s.store.Feedback(ctx, id)
	if err != nil {
		return Feedback{}, describeLookup(err, fmt.Sprintf("feedback %q", id))
	}
	return f, nil
}

// ListFeedback returns every feedback report, or only those of kind if it
// is not empty, oldest first.
func (s *Service) ListFeedback(ctx context.Context, kind FeedbackKind) ([]Feedback, error) {
	if kind != "" {
		var p problems
		checkFeedbackKind(&p, kind)
		if err := p.err(); err != nil {
			return nil, err
		}
	}
	reports, err := s.store.ListFeedback(ctx, kind)
	if err != nil {
		return nil, fmt.Errorf("list feedback: %w", err)
	}
	return reports, nil
}
