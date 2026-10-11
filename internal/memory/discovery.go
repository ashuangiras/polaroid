package memory

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Lexical discovery and duplicate suggestions (ADR-0032). Both read the
// latest version of each candidate procedure and compare words, never
// meaning; neither reads executions or bindings, nor writes anything.

const (
	// MaxDiscoveryLimit bounds the results of discovery and duplicate
	// suggestions; DefaultDiscoveryLimit applies when no limit is given.
	MaxDiscoveryLimit     = 50
	DefaultDiscoveryLimit = 10
	// MaxTaskBytes bounds a discovery task.
	MaxTaskBytes = 4096
)

// LatestVersion is a procedure's latest version as discovery reads it: its
// identity, applicability and descriptive content, without references,
// origin, revision reason or timestamps.
type LatestVersion struct {
	ProcedureID   string
	CanonicalKey  string
	Version       int
	Applicability Applicability
	Goal          string
	Method        string
	Philosophy    string
	Contract      jsontext.Value
	Instructions  jsontext.Value
}

// The searched fields, in explanation order, and their weights in a
// discovery score. Duplicate suggestions compare the first four.
var searchedFields = []struct {
	name   string
	weight int
}{
	{"canonical_key", 3}, {"goal", 3}, {"method", 2}, {"philosophy", 1}, {"contract", 1}, {"instructions", 1},
}

const summaryFields = 4

// stopWords are common English function words, never searched.
var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be but by can do does for from has have if in into is it its
		may must no not of on or our should so than that the their then there these this those to was we were when
		where which while who will with without you your`) {
		stopWords[w] = true
	}
}

// suffixes are removed, the first that applies and at most one, when at
// least three characters remain.
var suffixes = []struct{ suffix, replacement string }{
	{"ies", "y"}, {"ied", "y"}, {"ing", ""}, {"ed", ""}, {"es", ""}, {"s", ""}, {"e", ""},
}

func stem(word string) string {
	for _, s := range suffixes {
		rest := len(word) - len(s.suffix)
		if !strings.HasSuffix(word, s.suffix) || utf8.RuneCountInString(word[:rest])+len(s.replacement) < 3 {
			continue
		}
		if s.suffix == "s" && strings.HasSuffix(word, "ss") {
			continue
		}
		return word[:rest] + s.replacement
	}
	return word
}

// words is text's searchable words in order: lowercase runs of letters and
// digits, at least two characters long, other than stop words.
func words(text string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if utf8.RuneCountInString(w) >= 2 && !stopWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// query is a request's distinct terms, in the order their first word
// appears, with that word for explanations.
type query struct {
	terms []string
	words map[string]string
}

func (q *query) add(text string) {
	if q.words == nil {
		q.words = map[string]string{}
	}
	for _, w := range words(text) {
		t := stem(w)
		if _, ok := q.words[t]; !ok {
			q.words[t] = w
			q.terms = append(q.terms, t)
		}
	}
}

func (q query) wordList() []string {
	out := make([]string, len(q.terms))
	for i, t := range q.terms {
		out[i] = q.words[t]
	}
	return out
}

// document is the term set of each searched field of one version.
type document [6]map[string]bool

func newDocument(v LatestVersion) document {
	var d document
	for i, text := range []string{v.CanonicalKey, v.Goal, v.Method, v.Philosophy} {
		d[i] = termSet(text)
	}
	d[4], d[5] = map[string]bool{}, map[string]bool{}
	jsonStrings(v.Contract, func(s string) { addTerms(d[4], s) })
	jsonStrings(v.Instructions, func(s string) { addTerms(d[5], s) })
	return d
}

func termSet(text string) map[string]bool {
	set := map[string]bool{}
	addTerms(set, text)
	return set
}

func addTerms(set map[string]bool, text string) {
	for _, w := range words(text) {
		set[stem(w)] = true
	}
}

// jsonStrings calls add with every string value in v, at any depth, but
// not with member names.
func jsonStrings(v jsontext.Value, add func(string)) {
	dec := jsontext.NewDecoder(bytes.NewReader(v))
	for {
		tok, err := dec.ReadToken()
		if err != nil {
			return
		}
		if tok.Kind() != '"' {
			continue
		}
		if kind, n := dec.StackIndex(dec.StackDepth()); kind == '{' && n%2 == 1 {
			continue
		}
		add(tok.String())
	}
}

// FieldMatch names a searched field of a candidate and the request's words
// found in it.
type FieldMatch struct {
	Field string
	Terms []string
}

// matches lists, per field among the first n, the words of q's terms in it.
func (d document) matches(q query, n int) []FieldMatch {
	var out []FieldMatch
	for i := range n {
		var found []string
		for _, t := range q.terms {
			if d[i][t] {
				found = append(found, q.words[t])
			}
		}
		if found != nil {
			out = append(out, FieldMatch{Field: searchedFields[i].name, Terms: found})
		}
	}
	return out
}

// CandidateVersion identifies and describes the version a candidate was
// found in: always its procedure's latest version at the time of the read.
type CandidateVersion struct {
	ProcedureID   string
	CanonicalKey  string
	Version       int
	Applicability Applicability
	Goal          string
	Method        string
}

func candidateVersion(v LatestVersion) CandidateVersion {
	return CandidateVersion{ProcedureID: v.ProcedureID, CanonicalKey: v.CanonicalKey, Version: v.Version,
		Applicability: v.Applicability, Goal: v.Goal, Method: v.Method}
}

// DiscoveryRequest asks for procedures relevant to a task, optionally only
// those applicable in a repository, at most Limit (0 for the default).
type DiscoveryRequest struct {
	Task       string
	Repository string
	Limit      int
}

// Candidate is a procedure that matched a task. MatchedTerms counts the
// task's distinct terms found anywhere in it; Score adds, for each, the
// highest weight among the fields containing it.
type Candidate struct {
	CandidateVersion
	MatchedTerms int
	Score        int
	Matches      []FieldMatch
}

// Discovery is the ranked result of a task: its words, how many procedures
// matched at least one, and the best of them.
type Discovery struct {
	Terms      []string
	Repository string
	Matched    int
	Candidates []Candidate
}

const noTermsRule = "has no searchable words: use words of two or more letters or digits, other than common words such as 'the' or 'and'"

// DiscoverProcedures returns the procedures whose latest version matches
// r.Task, ranked by score, then matched terms, then canonical key.
func (s *Service) DiscoverProcedures(ctx context.Context, r DiscoveryRequest) (Discovery, error) {
	var p problems
	var q query
	switch {
	case strings.TrimSpace(r.Task) == "":
		p.add("task", "is required")
	case len(r.Task) > MaxTaskBytes:
		p.add("task", fmt.Sprintf("must be at most %d bytes", MaxTaskBytes))
	case !utf8.ValidString(r.Task):
		p.add("task", "must be valid UTF-8")
	default:
		if q.add(r.Task); len(q.terms) == 0 {
			p.add("task", noTermsRule)
		}
	}
	limit := discoveryScope(&p, r.Repository, r.Limit)
	if err := p.err(); err != nil {
		return Discovery{}, err
	}
	latest, err := s.store.LatestVersions(ctx, r.Repository)
	if err != nil {
		return Discovery{}, fmt.Errorf("read latest versions: %w", err)
	}
	found := []Candidate{}
	for _, v := range latest {
		d := newDocument(v)
		c := Candidate{CandidateVersion: candidateVersion(v)}
		for _, t := range q.terms {
			best := 0
			for i, f := range searchedFields {
				if d[i][t] {
					best = max(best, f.weight)
				}
			}
			if best > 0 {
				c.MatchedTerms++
				c.Score += best
			}
		}
		if c.MatchedTerms > 0 {
			c.Matches = d.matches(q, len(searchedFields))
			found = append(found, c)
		}
	}
	slices.SortFunc(found, func(a, b Candidate) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(b.MatchedTerms, a.MatchedTerms),
			strings.Compare(a.CanonicalKey, b.CanonicalKey))
	})
	return Discovery{Terms: q.wordList(), Repository: r.Repository, Matched: len(found), Candidates: found[:min(limit, len(found))]}, nil
}

// discoveryScope checks the optional repository and limit, and returns the
// limit to apply.
func discoveryScope(p *problems, repository string, limit int) int {
	if repository != "" {
		checkRepository(p, "repository", repository)
	}
	switch {
	case limit == 0:
		return DefaultDiscoveryLimit
	case limit < 1 || limit > MaxDiscoveryLimit:
		p.add("limit", fmt.Sprintf("must be a number from 1 to %d", MaxDiscoveryLimit))
	}
	return limit
}

// DuplicateRequest is a proposed procedure's descriptive content, checked
// against existing procedures before it is created, or before a revision of
// ExcludeProcedureID.
type DuplicateRequest struct {
	CanonicalKey       string
	Goal               string
	Method             string
	Philosophy         string
	Repository         string
	ExcludeProcedureID string
	Limit              int
}

// KeyCollision is an existing procedure with the proposed canonical key.
type KeyCollision struct {
	ProcedureID   string
	CanonicalKey  string
	LatestVersion int
}

// Suggestion is an existing procedure whose descriptive summary overlaps a
// proposal's: Similarity is 2 × SharedTerms ÷ (proposal terms + its terms),
// rounded to two decimals.
type Suggestion struct {
	CandidateVersion
	Similarity  float64
	SharedTerms int
	Matches     []FieldMatch
}

// Duplicates is the advisory result of a duplicate check.
type Duplicates struct {
	ProposalTerms int
	Repository    string
	KeyCollision  *KeyCollision
	Matched       int
	Suggestions   []Suggestion
}

// The suggestion threshold, a similarity of at least
// suggestNumerator/suggestDenominator, compared exactly.
const suggestNumerator, suggestDenominator = 7, 20

// SuggestDuplicates returns the existing procedure that already uses the
// proposed canonical key, if any, and the procedures whose summary overlaps
// the proposal's. It never stores anything.
func (s *Service) SuggestDuplicates(ctx context.Context, r DuplicateRequest) (Duplicates, error) {
	var p problems
	if r.CanonicalKey != "" {
		checkKey(&p, "canonical_key", r.CanonicalKey)
	}
	if r.Goal != "" {
		checkLine(&p, "goal", r.Goal)
	}
	checkText(&p, "method", r.Method)
	checkText(&p, "philosophy", r.Philosophy)
	var q query
	for _, text := range []string{r.CanonicalKey, r.Goal, r.Method, r.Philosophy} {
		q.add(text)
	}
	if len(p) == 0 && len(q.terms) == 0 {
		p.add("method", noTermsRule)
	}
	limit := discoveryScope(&p, r.Repository, r.Limit)
	if err := p.err(); err != nil {
		return Duplicates{}, err
	}
	if r.ExcludeProcedureID != "" {
		if _, err := s.store.History(ctx, r.ExcludeProcedureID); errors.Is(err, ErrNotFound) {
			return Duplicates{}, &ValidationError{Problems: []FieldProblem{{Field: "exclude_procedure_id",
				Message: fmt.Sprintf("procedure %q does not exist", r.ExcludeProcedureID)}}}
		} else if err != nil {
			return Duplicates{}, fmt.Errorf("read excluded procedure: %w", err)
		}
	}
	out := Duplicates{ProposalTerms: len(q.terms), Repository: r.Repository, Suggestions: []Suggestion{}}
	if r.CanonicalKey != "" {
		h, err := s.store.HistoryByKey(ctx, r.CanonicalKey)
		switch {
		case err == nil && h.Procedure.ID != r.ExcludeProcedureID:
			out.KeyCollision = &KeyCollision{ProcedureID: h.Procedure.ID, CanonicalKey: h.Procedure.CanonicalKey, LatestVersion: h.Procedure.LatestVersion}
		case err != nil && !errors.Is(err, ErrNotFound):
			return Duplicates{}, fmt.Errorf("read procedure by key: %w", err)
		}
	}
	latest, err := s.store.LatestVersions(ctx, r.Repository)
	if err != nil {
		return Duplicates{}, fmt.Errorf("read latest versions: %w", err)
	}
	type scored struct {
		Suggestion
		total int // proposal terms + candidate terms
	}
	var found []scored
	for _, v := range latest {
		if v.ProcedureID == r.ExcludeProcedureID {
			continue
		}
		d := newDocument(v)
		summary := map[string]bool{}
		for i := range summaryFields {
			for t := range d[i] {
				summary[t] = true
			}
		}
		shared := 0
		for _, t := range q.terms {
			if summary[t] {
				shared++
			}
		}
		total := len(q.terms) + len(summary)
		if suggestDenominator*2*shared < suggestNumerator*total {
			continue
		}
		found = append(found, scored{Suggestion{CandidateVersion: candidateVersion(v), SharedTerms: shared,
			Similarity: math.Round(200*float64(shared)/float64(total)) / 100, Matches: d.matches(q, summaryFields)}, total})
	}
	slices.SortFunc(found, func(a, b scored) int {
		return cmp.Or(cmp.Compare(b.SharedTerms*a.total, a.SharedTerms*b.total), cmp.Compare(b.SharedTerms, a.SharedTerms),
			strings.Compare(a.CanonicalKey, b.CanonicalKey))
	})
	out.Matched = len(found)
	for _, f := range found[:min(limit, len(found))] {
		out.Suggestions = append(out.Suggestions, f.Suggestion)
	}
	return out, nil
}
