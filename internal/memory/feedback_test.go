package memory

import (
	"encoding/json/jsontext"
	"slices"
	"testing"
)

func validFeedback() FeedbackRecord {
	return FeedbackRecord{
		Kind:     FeedbackProblem,
		Summary:  "record_execution rejected a short commit without saying how long it must be",
		Details:  "I passed a 7-character hash.\nThe error said only that the commit was invalid.",
		Reporter: "copilot.vscode",
		Context:  jsontext.Value(`{"tool": "record_execution", "ids": ["e1"]}`),
	}
}

func TestValidFeedbackIsCompacted(t *testing.T) {
	r, err := validFeedback().validate()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(r.Context); got != `{"tool":"record_execution","ids":["e1"]}` {
		t.Fatalf("stored context = %s", got)
	}
	if r.Details != validFeedback().Details {
		t.Fatalf("details changed: %q", r.Details)
	}
}

func TestFeedbackContextIsOptional(t *testing.T) {
	r := validFeedback()
	r.Context = nil
	got, err := r.validate()
	if err != nil || string(got.Context) != "{}" {
		t.Fatalf("absent context = %s, %v; want {}", got.Context, err)
	}
}

func TestFeedbackRequiresEveryField(t *testing.T) {
	_, err := FeedbackRecord{}.validate()
	got := problemFields(t, err)
	want := []string{"kind", "summary", "details", "reporter"}
	if !slices.Equal(got, want) {
		t.Fatalf("problem fields = %v, want %v", got, want)
	}
}

func TestFeedbackFieldRules(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*FeedbackRecord)
		field string // "" means valid
	}{
		{"suggestion", func(r *FeedbackRecord) { r.Kind = FeedbackSuggestion }, ""},
		{"unknown kind", func(r *FeedbackRecord) { r.Kind = "bug" }, "kind"},
		{"blank summary", func(r *FeedbackRecord) { r.Summary = " \t" }, "summary"},
		{"multi-line summary", func(r *FeedbackRecord) { r.Summary = "one\ntwo" }, "summary"},
		{"carriage return in summary", func(r *FeedbackRecord) { r.Summary = "one\rtwo" }, "summary"},
		{"line separator in summary", func(r *FeedbackRecord) { r.Summary = "one\u2028two" }, "summary"},
		{"only a newline", func(r *FeedbackRecord) { r.Summary = "\n" }, "summary"},
		{"invalid UTF-8 summary", func(r *FeedbackRecord) { r.Summary = "bad \xff" }, "summary"},
		{"blank details", func(r *FeedbackRecord) { r.Details = "\n\n" }, "details"},
		{"invalid reporter", func(r *FeedbackRecord) { r.Reporter = "Copilot Chat" }, "reporter"},
		{"context not an object", func(r *FeedbackRecord) { r.Context = jsontext.Value(`["tool"]`) }, "context"},
		{"null context", func(r *FeedbackRecord) { r.Context = jsontext.Value(`null`) }, "context"},
		{"empty context object", func(r *FeedbackRecord) { r.Context = jsontext.Value(`{}`) }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validFeedback()
			tc.edit(&r)
			_, err := r.validate()
			var got []string
			if err != nil {
				got = problemFields(t, err)
			}
			if tc.field == "" && got != nil || tc.field != "" && !slices.Equal(got, []string{tc.field}) {
				t.Fatalf("problem fields = %v, want [%s]", got, tc.field)
			}
		})
	}
}
