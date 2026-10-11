package memory

import (
	"fmt"
	"strings"
)

// WriteLimitsVersion identifies the write-limit policy (ADR-0037); a change
// of any limit increments it.
const WriteLimitsVersion = 1

// The tiers of ADR-0037, in bytes.
const (
	MaxDocumentBytes = 64 << 10
	MaxObjectBytes   = 16 << 10
	MaxTextBytes     = 8 << 10
	MaxLineBytes     = 2 << 10
	// MaxWriteBytes bounds the sum of every bounded field of one write.
	MaxWriteBytes = 256 << 10
)

// The record families a write limit applies to.
const (
	recVersion      = "procedure_version"
	recOrigin       = "procedure_origin"
	recBinding      = "binding_revision"
	recExecution    = "execution"
	recInvalidation = "execution_invalidation"
	recLifecycle    = "procedure_lifecycle_event"
	recFeedback     = "feedback"
	recRepository   = "repository"
	recAlias        = "repository_alias"
	recToil         = "toil_revision"
)

// LimitKind says how a field is measured: JSON after compaction, or text as
// persisted, both in UTF-8 bytes.
type LimitKind string

const (
	LimitJSON LimitKind = "json"
	LimitText LimitKind = "text"
)

// FieldLimit bounds one field of a record family. Field is the request
// path, with [] standing for any index.
type FieldLimit struct {
	Record   string
	Field    string
	Kind     LimitKind
	MaxBytes int
}

var writeLimits = []FieldLimit{
	{recVersion, "version.philosophy", LimitText, MaxTextBytes},
	{recVersion, "version.method", LimitText, MaxTextBytes},
	{recVersion, "version.goal", LimitText, MaxLineBytes},
	{recVersion, "version.contract", LimitJSON, MaxDocumentBytes},
	{recVersion, "version.instructions", LimitJSON, MaxDocumentBytes},
	{recVersion, "version.references[].inputs", LimitJSON, MaxObjectBytes},
	{recVersion, "version.references[].condition", LimitText, MaxTextBytes},
	{recVersion, "version.revision_reason", LimitText, MaxTextBytes},
	{recOrigin, "origin.reason", LimitText, MaxTextBytes},
	{recBinding, "revision.inputs", LimitJSON, MaxObjectBytes},
	{recBinding, "revision.revision_reason", LimitText, MaxTextBytes},
	{recExecution, "environment.attributes", LimitJSON, MaxObjectBytes},
	{recExecution, "inputs", LimitJSON, MaxObjectBytes},
	{recExecution, "evidence", LimitJSON, MaxDocumentBytes},
	{recExecution, "decisions[].rationale", LimitText, MaxTextBytes},
	{recExecution, "decisions[].evidence", LimitJSON, MaxObjectBytes},
	{recInvalidation, "reason", LimitText, MaxTextBytes},
	{recLifecycle, "reason", LimitText, MaxTextBytes},
	{recFeedback, "summary", LimitText, MaxLineBytes},
	{recFeedback, "details", LimitText, MaxTextBytes},
	{recFeedback, "context", LimitJSON, MaxObjectBytes},
	{recRepository, "name", LimitText, MaxLineBytes},
	{recAlias, "reason", LimitText, MaxTextBytes},
	{recToil, "revision.activity", LimitText, MaxLineBytes},
	{recToil, "revision.sources[].steps[]", LimitText, MaxLineBytes},
	{recToil, "revision.observations[].note", LimitText, MaxTextBytes},
	{recToil, "revision.assessment", LimitJSON, MaxDocumentBytes},
	{recToil, "revision.proposal", LimitJSON, MaxDocumentBytes},
	{recToil, "revision.status_rationale", LimitText, MaxTextBytes},
	{recToil, "revision.replacement.summary", LimitText, MaxTextBytes},
	{recToil, "revision.replacement.references[].uri", LimitText, MaxLineBytes},
	{recToil, "revision.replacement.references[].note", LimitText, MaxTextBytes},
	{recToil, "revision.validation.rationale", LimitText, MaxTextBytes},
	{recToil, "revision.validation.executions[].note", LimitText, MaxTextBytes},
	{recToil, "revision.validation.references[].uri", LimitText, MaxLineBytes},
	{recToil, "revision.validation.references[].note", LimitText, MaxTextBytes},
	{recToil, "revision.revision_reason", LimitText, MaxTextBytes},
}

// WriteLimits returns the write-limit policy that validation enforces.
func WriteLimits() []FieldLimit {
	return append([]FieldLimit(nil), writeLimits...)
}

// LimitViolation is one value over its limit; Field "*" is the whole write.
type LimitViolation struct {
	Field    string
	Bytes    int
	MaxBytes int
}

// LimitError reports values over their write limits (ADR-0037). It names
// fields and sizes, never content.
type LimitError struct {
	Violations []LimitViolation
}

func (e *LimitError) Error() string {
	parts := make([]string, len(e.Violations))
	for i, v := range e.Violations {
		name := v.Field
		if name == "*" {
			name = "the whole write"
		}
		parts[i] = fmt.Sprintf("%s is %d bytes, at most %d", name, v.Bytes, v.MaxBytes)
	}
	return "value too large: " + strings.Join(parts, "; ")
}

// sizes measures the bounded fields of one write.
type sizes struct {
	total int
	over  []LimitViolation
}

// add measures n bytes of field (pattern, with each [] replaced by the
// next index) against the policy's limit for record.
func (s *sizes) add(record, pattern string, n int, index ...int) {
	limit := limitOf(record, pattern)
	s.total += n
	if n <= limit {
		return
	}
	field := pattern
	for _, i := range index {
		field = strings.Replace(field, "[]", fmt.Sprintf("[%d]", i), 1)
	}
	s.over = append(s.over, LimitViolation{Field: field, Bytes: n, MaxBytes: limit})
}

func (s *sizes) text(record, pattern, value string, index ...int) {
	s.add(record, pattern, len(value), index...)
}

// json measures a stored (compacted) JSON value; nil is absent.
func (s *sizes) json(record, pattern string, value []byte, index ...int) {
	if value != nil {
		s.add(record, pattern, len(value), index...)
	}
}

func (s *sizes) err() error {
	over := s.over
	if s.total > MaxWriteBytes {
		over = append(over, LimitViolation{Field: "*", Bytes: s.total, MaxBytes: MaxWriteBytes})
	}
	if len(over) == 0 {
		return nil
	}
	return &LimitError{Violations: over}
}

func limitOf(record, pattern string) int {
	for _, l := range writeLimits {
		if l.Record == record && l.Field == pattern {
			return l.MaxBytes
		}
	}
	panic(fmt.Sprintf("memory: no write limit for %s %s", record, pattern))
}

func (d Definition) measure(s *sizes) {
	s.text(recVersion, "version.philosophy", d.Philosophy)
	s.text(recVersion, "version.method", d.Method)
	if d.Goal != "" {
		s.text(recVersion, "version.goal", d.Goal)
	}
	s.json(recVersion, "version.contract", d.Contract)
	s.json(recVersion, "version.instructions", d.Instructions)
	for i, r := range d.References {
		s.json(recVersion, "version.references[].inputs", r.Inputs, i)
		if r.Condition != nil {
			s.text(recVersion, "version.references[].condition", *r.Condition, i)
		}
	}
	s.text(recVersion, "version.revision_reason", d.RevisionReason)
}

func (c BindingConfig) measure(s *sizes) {
	s.json(recBinding, "revision.inputs", c.Inputs)
	s.text(recBinding, "revision.revision_reason", c.RevisionReason)
}

func (r ExecutionRecord) measure(s *sizes) {
	s.json(recExecution, "environment.attributes", r.Environment.Attributes)
	s.json(recExecution, "inputs", r.Inputs)
	s.json(recExecution, "evidence", r.Evidence)
	for i, d := range r.Decisions {
		s.text(recExecution, "decisions[].rationale", d.Rationale, i)
		s.json(recExecution, "decisions[].evidence", d.Evidence, i)
	}
}

func (r FeedbackRecord) measure(s *sizes) {
	s.text(recFeedback, "summary", r.Summary)
	s.text(recFeedback, "details", r.Details)
	s.json(recFeedback, "context", r.Context)
}

func (c ToilContent) measure(s *sizes) {
	s.text(recToil, "revision.activity", c.Activity)
	for i, src := range c.Sources {
		for j, step := range src.Steps {
			s.text(recToil, "revision.sources[].steps[]", step, i, j)
		}
	}
	for i, o := range c.Observations {
		s.text(recToil, "revision.observations[].note", o.Note, i)
	}
	s.json(recToil, "revision.assessment", c.Assessment)
	s.json(recToil, "revision.proposal", c.Proposal)
	s.text(recToil, "revision.status_rationale", c.StatusRationale)
	if r := c.Replacement; r != nil {
		s.text(recToil, "revision.replacement.summary", r.Summary)
		measureReferences(s, "revision.replacement.references[]", r.References)
	}
	if v := c.Validation; v != nil {
		s.text(recToil, "revision.validation.rationale", v.Rationale)
		for i, e := range v.Executions {
			s.text(recToil, "revision.validation.executions[].note", e.Note, i)
		}
		measureReferences(s, "revision.validation.references[]", v.References)
	}
	s.text(recToil, "revision.revision_reason", c.RevisionReason)
}

func measureReferences(s *sizes, prefix string, refs []ExternalReference) {
	for i, r := range refs {
		s.text(recToil, prefix+".uri", r.URI, i)
		if r.Note != "" {
			s.text(recToil, prefix+".note", r.Note, i)
		}
	}
}

// measureReason checks the single bounded field of an invalidation, a
// lifecycle event or an alias.
func measureReason(record, reason string) error {
	var s sizes
	s.text(record, "reason", reason)
	return s.err()
}
