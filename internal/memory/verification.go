package memory

import (
	"bytes"
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
	// ReferenceMappings returns a version's references, in order, with their
	// input mappings.
	ReferenceMappings(ctx context.Context, procedureID string, version int) ([]ReferenceMapping, error)
}

// ReferenceMapping is a reference's name, its input mapping (ADR-0008) and
// whether it is conditional (ADR-0029).
type ReferenceMapping struct {
	Name        string
	Inputs      jsontext.Value
	Conditional bool
}

// VerificationFilter selects the executions of one procedure version,
// optionally only those in one repository (by identity, ADR-0022), commit or
// environment.
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
	ProblemOutcomeFailed       ProblemCode = "outcome_failed"
	ProblemMissingChild        ProblemCode = "missing_child"
	ProblemChildInputsMismatch ProblemCode = "child_inputs_mismatch"
	ProblemChildNotVerified    ProblemCode = "child_not_verified"
	ProblemMissingDecision     ProblemCode = "missing_decision"
)

// VerificationProblem is one direct reason an execution is not verified.
// Reference and ExecutionID are set for child problems.
type VerificationProblem struct {
	Code        ProblemCode
	Reference   string
	ExecutionID string
}

// Combination is the context an execution verifies (ADR-0012). Repository
// is the identity's canonical identifier when RepositoryID is set, else the
// unregistered identifier itself (ADR-0022). Inputs are canonical, and
// Children follow the version's reference order.
type Combination struct {
	Repository   string
	RepositoryID string
	Commit       string
	Environment  string
	Inputs       jsontext.Value
	Children     []ChildVersion
}

// combinationRepository returns the repository components of a combination
// for identifier, whose identity is id.
func combinationRepository(identifier string, id RepositoryIdentity) (string, string) {
	if id.ID == "" {
		return identifier, ""
	}
	return id.Identifier, id.ID
}

// ChildVersion is the version a linked child ran, with its own children,
// or, with Skipped, a conditional reference decided not applicable
// (ADR-0029).
type ChildVersion struct {
	Reference string
	Version   int
	Skipped   bool
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
// oldest first. Its status is the verification of the latest one. For a
// target, Undecided lists the reference paths of conditional references
// without a target decision; then no execution is looked up (ADR-0029).
type CombinationStatus struct {
	Combination       Combination
	Verified          bool
	LatestExecutionID string
	ExecutionIDs      []string
	Undecided         []string
}

// VerifyExecution judges one stored execution (ADR-0012, ADR-0029): it is
// verified if it succeeded and every reference of its version is fulfilled
// by a linked child that is itself verified, except a conditional reference
// recorded as not applicable; every conditional reference needs a decision.
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
	refs map[string][]ReferenceMapping
}

func newVerifier(r VerificationReader) *verifier {
	return &verifier{r: r, done: map[string]Verification{}, refs: map[string][]ReferenceMapping{}}
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
	refs, err := v.referenceMappings(ctx, e.ProcedureID, e.Version)
	if err != nil {
		return Verification{}, err
	}
	inputs, err := canonicalInputs(e.Inputs)
	if err != nil {
		return Verification{}, fmt.Errorf("canonicalize inputs of execution %q: %w", id, err)
	}
	repository, repositoryID := combinationRepository(e.Repository, e.Identity)
	out := Verification{
		ExecutionID: id,
		ProcedureID: e.ProcedureID,
		Version:     e.Version,
		Combination: Combination{Repository: repository, RepositoryID: repositoryID, Commit: e.Commit, Environment: e.Environment.Name, Inputs: inputs},
	}
	if e.Outcome != OutcomeSucceeded {
		out.Problems = append(out.Problems, VerificationProblem{Code: ProblemOutcomeFailed})
	}
	children := make(map[string]string, len(e.Children))
	for _, c := range e.Children {
		children[c.Reference] = c.ExecutionID
	}
	decisions := make(map[string]bool, len(e.Decisions))
	for _, d := range e.Decisions {
		decisions[d.Reference] = d.Applicable != nil && *d.Applicable
	}
	for _, ref := range refs {
		name := ref.Name
		childID, ok := children[name]
		if ref.Conditional {
			applicable, decided := decisions[name]
			switch {
			case !decided:
				out.Problems = append(out.Problems, VerificationProblem{Code: ProblemMissingDecision, Reference: name})
				if !ok {
					continue
				}
			case !applicable:
				out.Combination.Children = append(out.Combination.Children, ChildVersion{Reference: name, Skipped: true})
				continue
			}
		}
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
		want, err := mappedInputs(ref.Inputs, e.Inputs)
		if err != nil {
			return Verification{}, fmt.Errorf("map inputs of reference %q of execution %q: %w", name, id, err)
		}
		if !bytes.Equal(want, child.Combination.Inputs) {
			out.Problems = append(out.Problems, VerificationProblem{Code: ProblemChildInputsMismatch, Reference: name, ExecutionID: childID})
		}
		if !child.Verified {
			out.Problems = append(out.Problems, VerificationProblem{Code: ProblemChildNotVerified, Reference: name, ExecutionID: childID})
		}
	}
	out.Verified = len(out.Problems) == 0
	v.done[id] = out
	return out, nil
}

func (v *verifier) referenceMappings(ctx context.Context, procedureID string, version int) ([]ReferenceMapping, error) {
	key := procedureID + "@" + strconv.Itoa(version)
	if refs, ok := v.refs[key]; ok {
		return refs, nil
	}
	refs, err := v.r.ReferenceMappings(ctx, procedureID, version)
	if err != nil {
		return nil, err
	}
	v.refs[key] = refs
	return refs, nil
}

// canonicalInputs returns inputs in the canonical form combinations compare
// (ADR-0012): members sorted, RFC 8785 strings and non-integer numbers,
// integers exact.
func canonicalInputs(inputs jsontext.Value) (jsontext.Value, error) {
	c := inputs.Clone()
	if err := c.Canonicalize(jsontext.CanonicalizeRawInts(false)); err != nil {
		return nil, err
	}
	return c, nil
}

// mappedInputs returns, in canonical form, the inputs a child fulfilling a
// reference with mapping must have run with when its parent's effective
// inputs are parent (ADR-0024).
func mappedInputs(mapping, parent jsontext.Value) (jsontext.Value, error) {
	m, err := mapInputs(mapping, parent)
	if err != nil {
		return nil, err
	}
	return canonicalInputs(m)
}

// key identifies c. A registered identity is keyed by its ID, behind a byte
// no identifier contains; repositories, commits, environment names and
// reference names cannot contain NUL, '@', '!', '(', ')' or ',', and inputs
// are canonical.
func (c Combination) key() string {
	var b strings.Builder
	repository := c.Repository
	if c.RepositoryID != "" {
		repository = "\x01" + c.RepositoryID
	}
	for _, s := range []string{repository, c.Commit, c.Environment, string(c.Inputs)} {
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
		if c.Skipped {
			fmt.Fprintf(b, "%s!skipped", c.Reference)
			continue
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
