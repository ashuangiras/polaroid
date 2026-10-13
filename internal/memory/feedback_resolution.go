package memory

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// FeedbackOutcome says how a resolved report was handled (ADR-0040).
type FeedbackOutcome string

const (
	OutcomeAddressed     FeedbackOutcome = "addressed"
	OutcomeDeclined      FeedbackOutcome = "declined"
	OutcomeNotReproduced FeedbackOutcome = "not_reproduced"
	OutcomeDuplicate     FeedbackOutcome = "duplicate"
)

// FeedbackStatus says whether a report has been resolved (ADR-0040).
type FeedbackStatus string

const (
	FeedbackOpen     FeedbackStatus = "open"
	FeedbackResolved FeedbackStatus = "resolved"
)

// FeedbackResolution records how a feedback report was handled. It is
// stored apart from the report, which never changes, and it is
// irreversible.
type FeedbackResolution struct {
	FeedbackID string
	Outcome    FeedbackOutcome
	Reason     string
	Actor      string
	References []ExternalReference
	CreatedAt  time.Time
}

// FeedbackResolutionRequest asks to resolve a feedback report: how, why and
// who.
type FeedbackResolutionRequest struct {
	Outcome    FeedbackOutcome
	Reason     string
	Actor      string
	References []ExternalReference
}

// Status reports whether f has been resolved.
func (f Feedback) Status() FeedbackStatus {
	return feedbackStatus(f.Resolution)
}

func feedbackStatus(r *FeedbackResolution) FeedbackStatus {
	if r != nil {
		return FeedbackResolved
	}
	return FeedbackOpen
}

// ErrFeedbackResolved reports that a feedback report is already resolved.
var ErrFeedbackResolved = errors.New("feedback report is already resolved")

func checkFeedbackOutcome(p *problems, o FeedbackOutcome) {
	switch o {
	case OutcomeAddressed, OutcomeDeclined, OutcomeNotReproduced, OutcomeDuplicate:
	default:
		p.add("outcome", `must be "addressed", "declined", "not_reproduced" or "duplicate"`)
	}
}

// ResolveFeedback records that a feedback report has been handled. It never
// changes the report, and it is irreversible: a second resolution fails
// with ErrFeedbackResolved.
func (s *Service) ResolveFeedback(ctx context.Context, feedbackID string, in FeedbackResolutionRequest) (FeedbackResolution, error) {
	var p problems
	checkFeedbackOutcome(&p, in.Outcome)
	checkText(&p, "reason", in.Reason)
	checkKey(&p, "actor", in.Actor)
	refs := checkReferencesList(&p, "references", in.References)
	if err := p.err(); err != nil {
		return FeedbackResolution{}, err
	}
	if err := measureFeedbackResolution(in.Reason, refs); err != nil {
		return FeedbackResolution{}, err
	}
	r := FeedbackResolution{FeedbackID: feedbackID, Outcome: in.Outcome, Reason: in.Reason, Actor: in.Actor,
		References: refs, CreatedAt: timestamp()}
	if err := s.store.ResolveFeedback(ctx, r); err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			return FeedbackResolution{}, fmt.Errorf("feedback %q: %w", feedbackID, ErrNotFound)
		case errors.Is(err, ErrFeedbackResolved):
			return FeedbackResolution{}, fmt.Errorf("feedback %q: %w; report a new problem if it recurs", feedbackID, ErrFeedbackResolved)
		}
		return FeedbackResolution{}, fmt.Errorf("resolve feedback: %w", err)
	}
	return r, nil
}
