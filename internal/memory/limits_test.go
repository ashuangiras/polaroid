package memory

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// jsonOfSize returns a compact JSON object of exactly n bytes (n >= 8).
func jsonOfSize(n int) jsontext.Value {
	return jsontext.Value(`{"a":"` + strings.Repeat("x", n-8) + `"}`)
}

func textOfSize(n int) string { return strings.Repeat("t", n) }

// padded returns prefix (an unterminated object ending in an open string)
// completed to exactly n bytes.
func padded(prefix string, n int) jsontext.Value {
	return jsontext.Value(prefix + strings.Repeat("x", n-len(prefix)-2) + `"}`)
}

func limitDefinition() Definition {
	cond := "Only when asked."
	return Definition{Philosophy: "p", Method: "m", Goal: "g", Contract: jsontext.Value(`{}`), Instructions: jsontext.Value(`{}`),
		References: []Reference{
			{Name: "a", ProcedureID: "x", VersionPolicy: VersionPolicy{Kind: PolicyPin, Pin: 1}, Inputs: jsontext.Value(`{}`)},
			{Name: "b", ProcedureID: "x", VersionPolicy: VersionPolicy{Kind: PolicyPin, Pin: 1}, Inputs: jsontext.Value(`{}`), Condition: &cond},
		},
		RevisionReason: "r"}
}

func limitExecution() ExecutionRecord {
	yes := true
	return ExecutionRecord{ProcedureID: "p", Version: 1, Repository: "github.com/o/a", Commit: strings.Repeat("a", 40),
		Environment: Environment{Name: "ci", Attributes: jsontext.Value(`{}`)}, Inputs: jsontext.Value(`{}`), Outcome: OutcomeSucceeded,
		Evidence: jsontext.Value(`{"exit":0}`), Decisions: []ApplicabilityDecision{
			{Reference: "a", Applicable: &yes, Rationale: "r"},
			{Reference: "b", Applicable: &yes, Rationale: "r", Evidence: jsontext.Value(`{}`)},
		}}
}

func limitToil() ToilContent {
	return ToilContent{Activity: "a", Applicability: Applicability{Kind: ScopeShared},
		Sources:      []ToilSource{{ProcedureID: "p", Version: 1, Steps: []string{"s0", "s1"}}},
		Observations: []ToilEvidence{{ExecutionID: "e", Note: "n"}},
		Assessment:   jsontext.Value(`{"repetition":{"status":"observed"},"burden":{"status":"unknown"}}`),
		Proposal:     jsontext.Value(`{"inputs":1,"outputs":1,"remaining_judgment":1,"failure_and_recovery":1,"acceptance_criteria":["c"]}`),
		Status:       ToilRetired, StatusRationale: "r",
		Replacement:    &ToilReplacement{Summary: "s", References: []ExternalReference{{URI: "u", Note: "n"}}},
		Validation:     &ToilValidation{Rationale: "r", Executions: []ToilEvidence{{ExecutionID: "e", Note: "n"}}, References: []ExternalReference{{URI: "u", Note: "n"}}},
		RevisionReason: "r", Author: "a"}
}

// writeOfField validates a valid write of l's record family whose field l
// holds a value of n bytes, at index 1 of the innermost collection where
// there is one.
func writeOfField(t *testing.T, l FieldLimit, n int) error {
	t.Helper()
	text, doc := textOfSize(n), jsonOfSize(n)
	switch l.Record {
	case recVersion, recOrigin:
		d := limitDefinition()
		var origin *Origin
		switch l.Field {
		case "version.philosophy":
			d.Philosophy = text
		case "version.method":
			d.Method = text
		case "version.goal":
			d.Goal = text
		case "version.contract":
			d.Contract = doc
		case "version.instructions":
			d.Instructions = doc
		case "version.references[].inputs":
			d.References[1].Inputs = jsontext.Value(`{"a":{"value":"` + strings.Repeat("x", n-18) + `"}}`)
		case "version.references[].condition":
			d.References[1].Condition = &text
		case "version.revision_reason":
			d.RevisionReason = text
		case "origin.reason":
			origin = &Origin{RepositoryID: "r", Reason: text}
		default:
			t.Fatalf("no write sets %s %s", l.Record, l.Field)
		}
		_, err := NewProcedure{CanonicalKey: "k", Origin: origin, Definition: d}.validate()
		return err
	case recBinding:
		c := BindingConfig{Inputs: jsontext.Value(`{}`), VersionPolicy: VersionPolicy{Kind: PolicyPin, Pin: 1}, RevisionReason: "r"}
		switch l.Field {
		case "revision.inputs":
			c.Inputs = doc
		case "revision.revision_reason":
			c.RevisionReason = text
		default:
			t.Fatalf("no write sets %s %s", l.Record, l.Field)
		}
		_, err := BindingRevise{BaseRevision: 1, Config: c}.validate()
		return err
	case recExecution:
		e := limitExecution()
		switch l.Field {
		case "environment.attributes":
			e.Environment.Attributes = doc
		case "inputs":
			e.Inputs = doc
		case "evidence":
			e.Evidence = doc
		case "decisions[].rationale":
			e.Decisions[1].Rationale = text
		case "decisions[].evidence":
			e.Decisions[1].Evidence = doc
		default:
			t.Fatalf("no write sets %s %s", l.Record, l.Field)
		}
		_, err := e.validate()
		return err
	case recFeedback:
		f := FeedbackRecord{Kind: FeedbackProblem, Summary: "s", Details: "d", Reporter: "r"}
		switch l.Field {
		case "summary":
			f.Summary = text
		case "details":
			f.Details = text
		case "context":
			f.Context = doc
		default:
			t.Fatalf("no write sets %s %s", l.Record, l.Field)
		}
		_, err := f.validate()
		return err
	case recResolution:
		switch l.Field {
		case "reason":
			return measureFeedbackResolution(text, nil)
		case "references[].uri":
			return measureFeedbackResolution("r", []ExternalReference{{URI: "u"}, {URI: text}})
		case "references[].note":
			return measureFeedbackResolution("r", []ExternalReference{{URI: "u"}, {URI: "u2", Note: text}})
		default:
			t.Fatalf("no write sets %s %s", l.Record, l.Field)
		}
		return nil
	case recToil:
		c := limitToil()
		switch l.Field {
		case "revision.activity":
			c.Activity = text
		case "revision.sources[].steps[]":
			c.Sources = append(c.Sources, ToilSource{ProcedureID: "p", Version: 2, Steps: []string{"x", text}})
		case "revision.observations[].note":
			c.Observations = append(c.Observations, ToilEvidence{ExecutionID: "e2", Note: text})
		case "revision.assessment":
			c.Assessment = padded(`{"repetition":{"status":"observed"},"burden":{"status":"unknown"},"x":"`, n)
		case "revision.proposal":
			c.Proposal = padded(`{"inputs":1,"outputs":1,"remaining_judgment":1,"failure_and_recovery":1,"acceptance_criteria":["c"],"x":"`, n)
		case "revision.status_rationale":
			c.StatusRationale = text
		case "revision.replacement.summary":
			c.Replacement.Summary = text
		case "revision.replacement.references[].uri":
			c.Replacement.References = append(c.Replacement.References, ExternalReference{URI: text})
		case "revision.replacement.references[].note":
			c.Replacement.References = append(c.Replacement.References, ExternalReference{URI: "u2", Note: text})
		case "revision.validation.rationale":
			c.Validation.Rationale = text
		case "revision.validation.executions[].note":
			c.Validation.Executions = append(c.Validation.Executions, ToilEvidence{ExecutionID: "e2", Note: text})
		case "revision.validation.references[].uri":
			c.Validation.References = append(c.Validation.References, ExternalReference{URI: text})
		case "revision.validation.references[].note":
			c.Validation.References = append(c.Validation.References, ExternalReference{URI: "u2", Note: text})
		case "revision.revision_reason":
			c.RevisionReason = text
		default:
			t.Fatalf("no write sets %s %s", l.Record, l.Field)
		}
		_, err := ToilRevise{BaseRevision: 1, Content: c}.validate()
		return err
	case recRepository:
		var s sizes
		s.text(recRepository, l.Field, text)
		return s.err()
	case recInvalidation, recLifecycle, recAlias:
		return measureReason(l.Record, text)
	}
	t.Fatalf("no write for record %s", l.Record)
	return nil
}

// TestEveryWriteLimitIsEnforcedAtItsBoundary: every field of the policy
// accepts exactly its limit and refuses one byte more, naming its path with
// indices; nothing outside the policy is measured (ADR-0037).
func TestEveryWriteLimitIsEnforcedAtItsBoundary(t *testing.T) {
	for _, l := range WriteLimits() {
		t.Run(l.Record+"/"+l.Field, func(t *testing.T) {
			if err := writeOfField(t, l, l.MaxBytes); err != nil {
				t.Fatalf("at the limit (%d bytes): %v", l.MaxBytes, err)
			}
			err := writeOfField(t, l, l.MaxBytes+1)
			var tooLarge *LimitError
			if !errors.As(err, &tooLarge) {
				t.Fatalf("one byte over: %v", err)
			}
			want := strings.ReplaceAll(l.Field, "[]", "[1]")
			if tooLarge.Violations[0] != (LimitViolation{Field: want, Bytes: l.MaxBytes + 1, MaxBytes: l.MaxBytes}) || len(tooLarge.Violations) != 1 {
				t.Fatalf("violations = %+v, want %s", tooLarge.Violations, want)
			}
		})
	}
}

func TestWriteLimitPolicyShape(t *testing.T) {
	seen := map[string]bool{}
	for _, l := range WriteLimits() {
		key := l.Record + " " + l.Field
		if seen[key] {
			t.Errorf("duplicate limit %s", key)
		}
		seen[key] = true
		if !slices.Contains([]int{MaxDocumentBytes, MaxObjectBytes, MaxTextBytes, MaxLineBytes}, l.MaxBytes) || (l.Kind != LimitJSON && l.Kind != LimitText) {
			t.Errorf("limit %s: %d bytes, kind %q", key, l.MaxBytes, l.Kind)
		}
	}
	if MaxDocumentBytes != 65536 || MaxObjectBytes != 16384 || MaxTextBytes != 8192 || MaxLineBytes != 2048 || MaxWriteBytes != 262144 {
		t.Fatal("tiers differ from ADR-0037")
	}
}

// TestLimitsCountUTF8BytesOfStoredForms: text counts UTF-8 bytes, JSON
// counts the compacted text with escapes as received.
func TestLimitsCountUTF8BytesOfStoredForms(t *testing.T) {
	method := func(m string) error {
		d := limitDefinition()
		d.Method = m
		_, err := Revision{BaseVersion: 1, Definition: d}.validate()
		return err
	}
	if err := method(textOfSize(MaxTextBytes-2) + "é"); err != nil {
		t.Fatalf("8190 bytes and a 2-byte é: %v", err)
	}
	if err := method(textOfSize(MaxTextBytes-1) + "é"); err == nil {
		t.Fatal("8193 bytes in 8192 characters were accepted")
	}
	if err := method(strings.Repeat("😀", MaxTextBytes/4)); err != nil {
		t.Fatalf("2048 four-byte characters: %v", err)
	}

	evidence := func(raw string) error {
		e := limitExecution()
		e.Evidence = jsontext.Value(raw)
		_, err := e.validate()
		return err
	}
	// The escape \u00e9 is stored as written: 6 bytes, where é is 2.
	filler := strings.Repeat("x", MaxDocumentBytes-8-6)
	if err := evidence(`{"a":"` + filler + `\u00e9"}`); err != nil {
		t.Fatalf("an escape at the limit: %v", err)
	}
	if err := evidence(`{"a":"` + filler + `\u00e9x"}`); err == nil {
		t.Fatal("one byte over with an escape was accepted")
	}
	if err := evidence(`{"a":"` + filler + `é` + strings.Repeat("x", 4) + `"}`); err != nil {
		t.Fatalf("a literal é is 2 bytes: %v", err)
	}
	// Whitespace is removed before measuring.
	spaced := "{\n  \"a\" :   \"" + strings.Repeat("x", MaxDocumentBytes-8) + "\"   \n}"
	if err := evidence(spaced); err != nil {
		t.Fatalf("whitespace counted: %v", err)
	}
}

func TestAbsentValuesAreNotMeasured(t *testing.T) {
	d := limitDefinition()
	d.Goal, d.References = "", nil
	var s sizes
	d.measure(&s)
	if want := len("p") + len("m") + 2 + 2 + len("r"); s.total != want {
		t.Fatalf("measured %d bytes, want %d", s.total, want)
	}
	c := limitToil()
	c.Proposal, c.Replacement, c.Validation = nil, nil, nil
	s = sizes{}
	c.measure(&s)
	if want := len("a") + len("s0s1") + len("n") + len(c.Assessment) + len("r") + len("r"); s.total != want {
		t.Fatalf("measured %d toil bytes, want %d", s.total, want)
	}
	f, err := FeedbackRecord{Kind: FeedbackProblem, Summary: "s", Details: "d", Reporter: "r"}.validate()
	if err != nil || string(f.Context) != "{}" {
		t.Fatalf("feedback without context: %s, %v", f.Context, err)
	}
}

// TestTheWholeWriteIsBounded: values each within their limits are refused
// together when their sum exceeds MaxWriteBytes, reported as field "*".
func TestTheWholeWriteIsBounded(t *testing.T) {
	yes := true
	e := limitExecution()
	e.Evidence = jsonOfSize(MaxDocumentBytes)
	e.Inputs = jsonOfSize(MaxObjectBytes)
	e.Environment.Attributes = jsonOfSize(MaxObjectBytes)
	e.Decisions = nil
	for i := range 7 {
		e.Decisions = append(e.Decisions, ApplicabilityDecision{Reference: fmt.Sprintf("r%d", i), Applicable: &yes,
			Rationale: textOfSize(MaxTextBytes), Evidence: jsonOfSize(MaxObjectBytes)})
	}
	total := MaxDocumentBytes + 2*MaxObjectBytes + 7*(MaxTextBytes+MaxObjectBytes)
	_, err := e.validate()
	var tooLarge *LimitError
	if !errors.As(err, &tooLarge) || !slices.Equal(tooLarge.Violations, []LimitViolation{{Field: "*", Bytes: total, MaxBytes: MaxWriteBytes}}) {
		t.Fatalf("total %d: %v", total, err)
	}
	e.Decisions = e.Decisions[:6]
	if _, err := e.validate(); err != nil {
		t.Fatalf("total %d: %v", total-MaxTextBytes-MaxObjectBytes, err)
	}
}

// TestStructuralErrorsComeFirstAndContentIsNotEchoed: an invalid write is
// invalid_request without sizes; a limit error names fields and sizes only.
func TestStructuralErrorsComeFirstAndContentIsNotEchoed(t *testing.T) {
	e := limitExecution()
	e.Evidence = jsontext.Value(`{"secret":"` + strings.Repeat("S", MaxDocumentBytes) + `"}`)
	e.Commit = "abc"
	var invalid *ValidationError
	if _, err := e.validate(); !errors.As(err, &invalid) {
		t.Fatalf("an invalid commit with oversized evidence: %v", err)
	}
	e.Commit = strings.Repeat("a", 40)
	_, err := e.validate()
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "SSS") {
		t.Fatalf("message %v", err)
	}
	if err.Error() != fmt.Sprintf("value too large: evidence is %d bytes, at most 65536", MaxDocumentBytes+13) {
		t.Fatalf("message %q", err.Error())
	}
}
