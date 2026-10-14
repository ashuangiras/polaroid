// Package knowledgesync exchanges living knowledge between independent
// Polaroid instances through ordinary Git remotes (ADR-0046).
//
// Fetch, validation and activation are separate steps. Fetch transfers a
// remote's snapshot into a quarantine and changes nothing. Validate checks
// Git integrity (Git-core), the trust policy, and every new snapshot as a
// complete catalog (the knowledge kernel's own rules); an accepted candidate
// is stored and tracked, still inactive. Only Activate changes the active
// knowledge, through the kernel, as a validated fast-forward, and records
// who accepted it and from where. Diverged histories are both kept; nothing
// is merged and nothing is forced.
package knowledgesync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ashuangiras/polaroid/internal/gitcore"
	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/gitstore"
)

// Source is a remote this instance trusts, and how far.
type Source struct {
	Name     string // local name, e.g. "team"
	URL      string // https, ssh or an absolute local path; never credentials
	Branch   string // "" is "main"
	CABundle []byte // extra PEM roots for an HTTPS source
	// Publishers are the publication authors accepted from this source.
	// Authors are claims: they narrow what is accepted, and never grant
	// trust that the source's own access control does not.
	Publishers []string
	// Publish permits sending this instance's active knowledge to the source.
	Publish bool
}

// Policy is the complete, explicit trust configuration: a source not listed
// is not contacted.
type Policy struct {
	Sources []Source
	Limits  gitcore.TransferLimits // zero: gitcore.DefaultTransferLimits
}

var (
	ErrUnknownSource = errors.New("the source is not in the trust policy")
	ErrRejected      = errors.New("candidate rejected")
	ErrNotPermitted  = errors.New("the policy does not permit publishing to this source")
	ErrNotEligible   = errors.New("the candidate cannot be activated")
)

// Status of a candidate.
type Status string

const (
	Fetched     Status = "fetched"      // transferred, not validated
	Identical   Status = "identical"    // the remote holds the active knowledge
	FastForward Status = "fast-forward" // valid and descends from the active knowledge
	LocalAhead  Status = "local-ahead"  // the active knowledge already contains it
	Diverged    Status = "diverged"     // valid, but both histories have publications the other lacks
	Rejected    Status = "rejected"
)

// Incoming is one new publication a candidate carries, oldest first.
type Incoming struct {
	Snapshot   knowledge.Snapshot
	Publisher  string
	When       time.Time
	Summary    string
	Rationale  string
	Trailers   []knowledge.Trailer
	Procedures int
}

// Candidate is a remote's knowledge on its way in.
type Candidate struct {
	Source   string
	Remote   knowledge.Snapshot // the remote's snapshot; "" when it holds none
	Local    knowledge.Snapshot // the active snapshot it was validated against
	Status   Status
	Problems []string
	Incoming []Incoming
	Bytes    int64 // pack bytes received
	Objects  int

	g *gitcore.Candidate
}

// Close discards a candidate that will not be validated.
func (c *Candidate) Close() error { return c.g.Close() }

// Syncer exchanges one instance's knowledge under a policy.
type Syncer struct {
	repo    *gitcore.Repository
	kernel  *knowledge.Kernel
	sources map[string]Source
	limits  gitcore.TransferLimits
}

// New checks the policy. The kernel reads the same repository, configured
// with opts.
func New(repo *gitcore.Repository, p Policy, opts ...knowledge.Option) (*Syncer, error) {
	s := &Syncer{repo: repo, kernel: knowledge.New(gitstore.New(repo), opts...), sources: map[string]Source{}, limits: p.Limits}
	var probs []string
	for _, src := range p.Sources {
		switch {
		case src.Name == "" || s.sources[src.Name].Name != "":
			probs = append(probs, fmt.Sprintf("source %q: names must be unique and non-empty", src.Name))
		case src.URL == "":
			probs = append(probs, fmt.Sprintf("source %q: a URL is required", src.Name))
		case len(src.Publishers) == 0 || slices.ContainsFunc(src.Publishers, func(p string) bool { return strings.TrimSpace(p) != p || p == "" }):
			probs = append(probs, fmt.Sprintf("source %q: at least one publisher is required, each a trimmed, non-empty name", src.Name))
		}
		s.sources[src.Name] = src
	}
	if len(probs) > 0 {
		return nil, fmt.Errorf("knowledgesync: invalid policy: %s", strings.Join(probs, "; "))
	}
	return s, nil
}

// Kernel is the knowledge kernel on the same repository.
func (s *Syncer) Kernel() *knowledge.Kernel { return s.kernel }

func (src Source) remote() gitcore.Remote {
	return gitcore.Remote{Name: src.Name, URL: src.URL, Branch: src.Branch, CABundle: src.CABundle}
}

func (s *Syncer) source(name string) (Source, error) {
	src, ok := s.sources[name]
	if !ok {
		return Source{}, fmt.Errorf("%w: %q", ErrUnknownSource, name)
	}
	return src, nil
}

// Fetch transfers the source's knowledge into quarantine. Nothing local
// changes. Credentials are used for this call only.
func (s *Syncer) Fetch(ctx context.Context, source string, cred gitcore.Credentials) (*Candidate, error) {
	src, err := s.source(source)
	if err != nil {
		return nil, err
	}
	g, err := s.repo.Fetch(ctx, src.remote(), cred, s.limits)
	if err != nil {
		return nil, err
	}
	return &Candidate{Source: source, Remote: knowledge.Snapshot(g.Snapshot), Status: Fetched, Bytes: g.Bytes, Objects: g.Objects, g: g}, nil
}

// Validate decides whether a fetched candidate is acceptable: Git
// integrity, then for every new publication an authorised publisher, a
// stated rationale and a valid complete catalog. A rejected candidate is
// discarded (ErrRejected, with its problems). An accepted one is stored
// and tracked, not activated, and its Status says whether it can be.
// Nothing in the knowledge is executed or interpreted.
func (s *Syncer) Validate(ctx context.Context, c *Candidate) error {
	if c.Status != Fetched {
		return fmt.Errorf("%w: the candidate is %s", ErrNotEligible, c.Status)
	}
	commits, err := c.g.Verify(ctx)
	if err != nil {
		return s.reject(c, err.Error())
	}
	src := s.sources[c.Source]
	var probs []string
	cats := map[knowledge.Snapshot]*knowledge.Catalog{}
	c.Incoming = nil
	for _, cm := range commits {
		in := Incoming{Snapshot: knowledge.Snapshot(cm.Snapshot), Publisher: cm.Author, When: cm.When, Summary: cm.Subject, Rationale: cm.Body}
		for _, t := range cm.Trailers {
			in.Trailers = append(in.Trailers, knowledge.Trailer{Key: t.Key, Value: t.Value})
		}
		if !slices.Contains(src.Publishers, cm.Author) {
			probs = append(probs, fmt.Sprintf("snapshot %s: publisher %q is not authorised for source %q", cm.Snapshot, cm.Author, src.Name))
		}
		if strings.TrimSpace(cm.Body) == "" {
			probs = append(probs, fmt.Sprintf("snapshot %s: no rationale", cm.Snapshot))
		}
		tree, err := c.g.Tree(ctx, cm.Snapshot)
		if err != nil {
			return err
		}
		cat, err := s.kernel.Check(ctx, in.Snapshot, gitstore.Files(tree))
		if err != nil {
			probs = append(probs, fmt.Sprintf("snapshot %s: %v", cm.Snapshot, err))
		} else {
			in.Procedures = cat.Len()
			cats[in.Snapshot] = cat
			problem, err := s.checkTransition(ctx, cm, cat, cats)
			if err != nil {
				return err
			}
			if problem != nil {
				probs = append(probs, fmt.Sprintf("snapshot %s: %v", cm.Snapshot, problem))
			}
		}
		c.Incoming = append(c.Incoming, in)
	}
	if len(probs) > 0 {
		return s.reject(c, probs...)
	}
	local, err := s.repo.Active(ctx)
	if err != nil {
		return err
	}
	rel, err := c.g.Relation(ctx, local)
	if err != nil {
		return err
	}
	if err := s.repo.Import(ctx, c.g); err != nil {
		return err
	}
	c.Local = knowledge.Snapshot(local)
	c.Status = map[gitcore.Relation]Status{gitcore.Same: Identical, gitcore.Behind: FastForward, gitcore.Ahead: LocalAhead, gitcore.Diverged: Diverged}[rel]
	return nil
}

// checkTransition checks an incoming publication against its parent: an
// earlier incoming publication, published history, or a snapshot imported
// earlier and not yet activated. Every edge of every history this
// instance can activate is therefore checked, so a rule is never removed
// and its revision never falls anywhere along it, and an approval
// cancelled by a revision never matches again. A parent held nowhere here
// is refused rather than skipped. Trailers grant nothing: rule governance
// has no exemptions.
func (s *Syncer) checkTransition(ctx context.Context, cm gitcore.Commit, cat *knowledge.Catalog, cats map[knowledge.Snapshot]*knowledge.Catalog) (problem, err error) {
	if len(cm.Parents) == 0 {
		return nil, nil
	}
	parent := knowledge.Snapshot(cm.Parents[0])
	prev := cats[parent]
	if prev == nil {
		tree, err := s.repo.StoredTree(ctx, gitcore.Snapshot(parent))
		if errors.Is(err, gitcore.ErrNotFound) {
			return fmt.Errorf("its parent %s is not knowledge held here, so the change from it cannot be checked", parent), nil
		}
		if err != nil {
			return nil, err
		}
		c, err := s.kernel.Check(ctx, parent, gitstore.Files(tree))
		if errors.Is(err, knowledge.ErrInvalid) {
			return fmt.Errorf("its parent %s: %w", parent, err), nil
		}
		if err != nil {
			return nil, err
		}
		prev = c
	}
	return knowledge.CheckTransition(prev, cat), nil
}

func (s *Syncer) reject(c *Candidate, probs ...string) error {
	c.Status, c.Problems = Rejected, probs
	_ = c.g.Close()
	return fmt.Errorf("%w: %s", ErrRejected, strings.Join(probs, "; "))
}

// Trailer keys of an activation's origin.
const (
	TrailerSource     = "Polaroid-Source"
	TrailerRemote     = "Polaroid-Remote-Snapshot"
	TrailerPublishers = "Polaroid-Publishers"
)

// Activate makes a validated fast-forward candidate the active knowledge,
// through the kernel, which validates the catalog again under the writer
// lock. A local publication since validation is ErrStale; the decision
// records who accepted the knowledge, why, and where it came from.
func (s *Syncer) Activate(ctx context.Context, c *Candidate, decision knowledge.Provenance) error {
	if c.Status != FastForward {
		err := fmt.Errorf("%w: the candidate is %s", ErrNotEligible, c.Status)
		if c.Status == Diverged {
			err = errors.Join(err, knowledge.ErrDiverged)
		}
		return err
	}
	src := s.sources[c.Source]
	var pubs []string
	for _, in := range c.Incoming {
		if !slices.Contains(pubs, in.Publisher) {
			pubs = append(pubs, in.Publisher)
		}
	}
	origin := []knowledge.Trailer{{Key: TrailerSource, Value: src.Name + " " + src.URL}, {Key: TrailerRemote, Value: string(c.Remote)}}
	if len(pubs) > 0 {
		origin = append(origin, knowledge.Trailer{Key: TrailerPublishers, Value: strings.Join(pubs, ", ")})
	}
	return s.kernel.Activate(ctx, knowledge.Activation{Base: c.Local, Target: c.Remote, Provenance: decision, Origin: origin})
}

// Publish sends the active knowledge to a source the policy permits, as a
// fast-forward only: gitcore.ErrBehind when the source holds knowledge not
// fetched here, gitcore.ErrDiverged when histories diverge, gitcore.ErrStale
// when the source changed during the push.
func (s *Syncer) Publish(ctx context.Context, source string, cred gitcore.Credentials) (gitcore.PushResult, error) {
	src, err := s.source(source)
	if err != nil {
		return gitcore.PushResult{}, err
	}
	if !src.Publish {
		return gitcore.PushResult{}, fmt.Errorf("%w: %q", ErrNotPermitted, source)
	}
	return s.repo.Push(ctx, src.remote(), cred)
}
