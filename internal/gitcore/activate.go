package gitcore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// journalRef is a local history of activations: one commit, with the empty
// tree, per Advance, written before the active ref moves (an interrupted
// activation leaves an entry whose move did not happen; Verify warns). It
// is never sent to a remote.
const journalRef = "refs/polaroid/activations"

// Advance makes an imported snapshot active, as a fast-forward of the
// active one.
type Advance struct {
	Expected Snapshot // the active snapshot, or ErrStale
	Target   Snapshot // an imported snapshot that descends from Expected
	// Validate sees the target's complete tree under the writer lock; an
	// error refuses the activation and is returned as is.
	Validate func(*Tree) error
	Author   string // who authorised the activation
	Subject  string
	Body     string
	Trailers []Trailer
	When     time.Time // zero: now
}

// Advance moves the active ref to a.Target. The target must be reachable
// from a tracking ref (that is, imported after verification) and descend
// from the active snapshot, so the move loses nothing. The move is recorded
// in the reflog and in the activation journal; the target's commits keep
// their own identity, authors and messages.
func (r *Repository) Advance(ctx context.Context, a Advance) error {
	for _, t := range a.Trailers {
		if t.Key == "Activated" || t.Key == "Previous" {
			return invalid("trailer %s is recorded by Advance itself", t.Key)
		}
	}
	trailers := append(append([]Trailer(nil), a.Trailers...), Trailer{"Activated", string(a.Target)}, Trailer{"Previous", previous(a.Expected)})
	msg, err := (&Publication{Author: a.Author, Subject: a.Subject, Body: a.Body, Trailers: trailers}).message(r.lim)
	if err != nil {
		return err
	}
	if !a.Target.valid() {
		return invalid("target %q is not a snapshot", a.Target)
	}
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := r.cleanTmp(); err != nil {
		return err
	}
	active, err := r.Active(ctx)
	if err != nil {
		return err
	}
	if active != a.Expected {
		return ErrStale
	}
	if a.Target == active {
		return invalid("%s is already active", a.Target)
	}
	if ok, err := r.tracked(a.Target); err != nil || !ok {
		return fmt.Errorf("snapshot %s is not imported: %w", a.Target, errorOr(err, ErrNotFound))
	}
	if active != "" {
		ok, err := r.descends(r.st, a.Target, active)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: %s does not descend from the active %s", ErrDiverged, a.Target, active)
		}
	}
	ix, err := r.indexOf(a.Target)
	if err != nil {
		return err
	}
	if a.Validate != nil {
		if err := a.Validate(&Tree{r: r, snap: a.Target, ix: ix}); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	when := a.When
	if when.IsZero() {
		when = time.Now()
	}
	sig := object.Signature{Name: a.Author, Email: a.Author + "@polaroid.invalid", When: when.UTC().Truncate(time.Second)}
	// The journal comes first: whatever is active was recorded, so nothing
	// imported is ever mistaken for a local publication.
	if err := r.appendJournal(sig, msg); err != nil {
		return err
	}
	hook("journal-durable")
	if err := r.setActive(a.Target); err != nil {
		return err
	}
	hook("ref-moved")
	if err := r.markReachable(a.Target); err != nil {
		return err
	}
	if err := r.appendReflog(active, a.Target, sig, "activate: "+strings.TrimSpace(a.Subject)); err != nil {
		return fmt.Errorf("activated %s, but the reflog was not updated: %w", a.Target, err)
	}
	return nil
}

func previous(s Snapshot) string {
	if s == "" {
		return "none"
	}
	return string(s)
}

func errorOr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

// tracked reports whether s is a tracking ref's snapshot or an ancestor of one.
func (r *Repository) tracked(s Snapshot) (bool, error) {
	refs, err := r.trackingRefs()
	if err != nil {
		return false, err
	}
	for _, t := range refs {
		if ok, err := r.descends(r.st, t, s); err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

func (r *Repository) appendJournal(sig object.Signature, msg string) error {
	prev, err := r.readRef(journalRef)
	if err != nil {
		return err
	}
	empty := &plumbing.MemoryObject{}
	empty.SetType(plumbing.TreeObject)
	commit := &object.Commit{Author: sig, Committer: sig, Message: msg, TreeHash: empty.Hash()}
	if prev != "" {
		commit.ParentHashes = []plumbing.Hash{plumbing.NewHash(string(prev))}
	}
	co := &plumbing.MemoryObject{}
	if err := commit.Encode(co); err != nil {
		return err
	}
	if err := r.writeObjects([]*plumbing.MemoryObject{empty, co}); err != nil {
		return err
	}
	return r.writeRef(journalRef, Snapshot(co.Hash().String()))
}

// Activations lists the activation journal, newest first, at most limit
// entries. Each entry's trailers end with Activated and Previous.
func (r *Repository) Activations(ctx context.Context, limit int) ([]Commit, error) {
	head, err := r.readRef(journalRef)
	if err != nil {
		return nil, err
	}
	var out []Commit
	for h := plumbing.NewHash(string(head)); head != "" && len(out) < limit && !h.IsZero(); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c, err := object.GetCommit(r.st, h)
		if err != nil {
			return nil, fmt.Errorf("%w: activation %s: %w", ErrCorrupt, h, err)
		}
		subject, body, trailers := parseMessage(c.Message)
		out = append(out, Commit{Snapshot: Snapshot(c.Hash.String()), Author: c.Author.Name, When: c.Author.When, Subject: subject, Body: body, Trailers: trailers})
		h = plumbing.ZeroHash
		if len(c.ParentHashes) > 0 {
			h = c.ParentHashes[0]
		}
	}
	return out, nil
}

// Tracking returns what a remote's branch was last known to hold.
func (r *Repository) Tracking(_ context.Context, rm Remote) (Snapshot, error) {
	if !safeName(rm.Name) || !safeName(rm.branch()) {
		return "", invalid("remote name %q and branch %q must be safe path segments", rm.Name, rm.Branch)
	}
	return r.readRef(rm.trackingRef())
}
