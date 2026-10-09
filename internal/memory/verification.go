package memory

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"strconv"
	"strings"
)

// VerificationReader gives verification a consistent view of executions.
type VerificationReader interface {
	// Run returns an execution with its inputs and children but without its
	// evidence, or an error wrapping ErrNotFound.
	Run(ctx context.Context, id string) (Execution, error)
	// RunIDs returns the IDs of the executions f selects, oldest first.
	RunIDs(ctx context.Context, f VerificationFilter) ([]string, error)
	// ReferenceNames returns the names of a version's references in order.
	ReferenceNames(ctx context.Context, procedureID string, version int) ([]string, error)
}

// VerificationFilter selects the executions of one procedure version,
// optionally only those in one repository, commit or environment.
type VerificationFilter struct {
	ProcedureID string
	Version     int
	Repository  string
	Commit      string
	Environment string
}

// ProblemCode names why an execution is not verified.
type ProblemCode string

const (
	ProblemOutcomeFailed    ProblemCode = "outcome_failed"
	ProblemMissingChild     ProblemCode = "missing_child"
	ProblemChildNotVerified ProblemCode = "child_not_verified"
)

// VerificationProblem is one direct reason an execution is not verified.
// Reference and ExecutionID are set for child problems.
type VerificationProblem struct {
	Code        ProblemCode
	Reference   string
	ExecutionID string
}

// Combination is the context an execution verifies (ADR-0012). Inputs are
// canonical, and Children follow the version's reference order.
type Combination struct {
	Repository  string
	Commit      string
	Environment string
	Inputs      jsontext.Value
	Children    []ChildVersion
}

// ChildVersion is the version a linked child ran, with its own children.
type ChildVersion struct {
	Reference string
	Version   int
	Children  []ChildVersion
}

// Verification judges one execution.
type Verification struct {
	ExecutionID string
	ProcedureID string
	Version     int
	Verified    bool
	Problems    []VerificationProblem
	Combination Combination
}

// CombinationStatus is one combination of a version with its executions,
// oldest first. Its status is the verification of the latest one.
type CombinationStatus struct {
	Combination       Combination
	Verified          bool
	LatestExecutionID string
	ExecutionIDs      []string
}

// VerifyExecution judges one stored execution (ADR-0012): it is verified if
// it succeeded and every reference of its version is fulfilled by a linked
// child that is itself verified.
func VerifyExecution(ctx context.Context, r VerificationReader, id string) (Verification, error) {
	return newVerifier(r).verify(ctx, id)
}

// ListCombinations groups the executions f selects by combination, ordered
// by each combination's first execution.
func ListCombinations(ctx context.Context, r VerificationReader, f VerificationFilter) ([]CombinationStatus, error) {
	ids, err := r.RunIDs(ctx, f)
	if err != nil {
		return nil, err
	}
	v := newVerifier(r)
	statuses := []CombinationStatus{}
	index := map[string]int{}
	for _, id := range ids {
		ver, err := v.verify(ctx, id)
		if err != nil {
			return nil, err
		}
		key := ver.Combination.key()
		i, seen := index[key]
		if !seen {
			i = len(statuses)
			index[key] = i
			statuses = append(statuses, CombinationStatus{Combination: ver.Combination})
		}
		s := &statuses[i]
		s.ExecutionIDs = append(s.ExecutionIDs, id)
		s.LatestExecutionID, s.Verified = id, ver.Verified
	}
	return statuses, nil
}

type verifier struct {
	r    VerificationReader
	done map[string]Verification
	refs map[string][]string
}

func newVerifier(r VerificationReader) *verifier {
	return &verifier{r: r, done: map[string]Verification{}, refs: map[string][]string{}}
}

// verify needs no depth or cycle guard: each execution has at most one
// parent and is recorded before it, so execution trees are finite and
// disjoint.
func (v *verifier) verify(ctx context.Context, id string) (Verification, error) {
	if done, ok := v.done[id]; ok {
		return done, nil
	}
	e, err := v.r.Run(ctx, id)
	if err != nil {
		return Verification{}, err
	}
	names, err := v.referenceNames(ctx, e.ProcedureID, e.Version)
	if err != nil {
		return Verification{}, err
	}
	inputs := e.Inputs.Clone()
	if err := inputs.Canonicalize(jsontext.CanonicalizeRawInts(false)); err != nil {
		return Verification{}, fmt.Errorf("canonicalize inputs of execution %q: %w", id, err)
	}
	out := Verification{
		ExecutionID: id,
		ProcedureID: e.ProcedureID,
		Version:     e.Version,
		Combination: Combination{Repository: e.Repository, Commit: e.Commit, Environment: e.Environment.Name, Inputs: inputs},
	}
	if e.Outcome != OutcomeSucceeded {
		out.Problems = append(out.Problems, VerificationProblem{Code: ProblemOutcomeFailed})
	}
	children := make(map[string]string, len(e.Children))
	for _, c := range e.Children {
		children[c.Reference] = c.ExecutionID
	}
	for _, name := range names {
		childID, ok := children[name]
		if !ok {
			out.Problems = append(out.Problems, VerificationProblem{Code: ProblemMissingChild, Reference: name})
			continue
		}
		child, err := v.verify(ctx, childID)
		if err != nil {
			return Verification{}, fmt.Errorf("verify child %q: %w", name, err)
		}
		out.Combination.Children = append(out.Combination.Children,
			ChildVersion{Reference: name, Version: child.Version, Children: child.Combination.Children})
		if !child.Verified {
			out.Problems = append(out.Problems, VerificationProblem{Code: ProblemChildNotVerified, Reference: name, ExecutionID: childID})
		}
	}
	out.Verified = len(out.Problems) == 0
	v.done[id] = out
	return out, nil
}

func (v *verifier) referenceNames(ctx context.Context, procedureID string, version int) ([]string, error) {
	key := procedureID + "@" + strconv.Itoa(version)
	if names, ok := v.refs[key]; ok {
		return names, nil
	}
	names, err := v.r.ReferenceNames(ctx, procedureID, version)
	if err != nil {
		return nil, err
	}
	v.refs[key] = names
	return names, nil
}

// key identifies c. Repositories, commits, environment names and reference
// names cannot contain NUL, '@', '(', ')' or ',', and inputs are canonical.
func (c Combination) key() string {
	var b strings.Builder
	for _, s := range []string{c.Repository, c.Commit, c.Environment, string(c.Inputs)} {
		b.WriteString(s)
		b.WriteByte(0)
	}
	writeChildKey(&b, c.Children)
	return b.String()
}

func writeChildKey(b *strings.Builder, children []ChildVersion) {
	for i, c := range children {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(b, "%s@%d(", c.Reference, c.Version)
		writeChildKey(b, c.Children)
		b.WriteByte(')')
	}
}

// Verification judges one execution.
func (s *Service) Verification(ctx context.Context, executionID string) (Verification, error) {
	v, err := s.store.ExecutionVerification(ctx, executionID)
	if err != nil {
		return Verification{}, describeLookup(err, fmt.Sprintf("execution %q", executionID))
	}
	return v, nil
}

// Verifications lists the combinations in which a version was executed.
func (s *Service) Verifications(ctx context.Context, f VerificationFilter) ([]CombinationStatus, error) {
	var p problems
	if f.Version < 1 {
		p.add("version", "must be a version number of at least 1")
	}
	if f.Repository != "" {
		checkRepository(&p, "repository", f.Repository)
	}
	if f.Commit != "" && !commitPattern.MatchString(f.Commit) {
		p.add("commit", "must be a full commit hash: 40 or 64 lowercase hex characters")
	}
	if f.Environment != "" {
		checkKey(&p, "environment", f.Environment)
	}
	if err := p.err(); err != nil {
		return nil, err
	}
	if _, err := s.store.Version(ctx, f.ProcedureID, f.Version); err != nil {
		return nil, describeLookup(err, fmt.Sprintf("procedure %q version %d", f.ProcedureID, f.Version))
	}
	statuses, err := s.store.Verifications(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("list verifications: %w", err)
	}
	return statuses, nil
}
