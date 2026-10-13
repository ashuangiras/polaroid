package knowledge

import (
	"context"
	"time"
)

// Store is the storage the kernel needs: snapshots of a file tree, an
// atomic compare-and-publish, and a log. internal/knowledge/gitstore
// implements it on Git-core.
type Store interface {
	// Active returns the active snapshot, "" when nothing is published.
	Active(ctx context.Context) (Snapshot, error)
	// Files returns the files of a published snapshot, ErrNotFound for any
	// other token.
	Files(ctx context.Context, s Snapshot) (Files, error)
	// Commit publishes atomically, or returns ErrStale when Expected is no
	// longer active. Check sees the complete proposed tree first; its error
	// refuses the commit.
	Commit(ctx context.Context, c Commit) (Snapshot, error)
	// Log lists publications from a published snapshot along first
	// parents, newest first.
	Log(ctx context.Context, from Snapshot, limit int) ([]LogEntry, error)
	// Advance makes a stored snapshot that arrived from elsewhere active, as
	// a fast-forward of Expected: ErrStale when Expected is no longer active,
	// ErrDiverged when Target does not descend from it, ErrNotFound when
	// Target was never accepted into storage. Check sees Target's complete
	// tree first; its error refuses the move.
	Advance(ctx context.Context, a Advance) error
	// Activations lists the recorded activations, newest first.
	Activations(ctx context.Context, limit int) ([]ActivationEntry, error)
	// Instance identifies this store among the instances exchanging
	// knowledge; it authenticates nothing.
	Instance(ctx context.Context) (string, error)
	// Imported reports whether a published snapshot arrived through an
	// activation, judged from the activation record alone; ErrNotFound for
	// a snapshot that is not published.
	Imported(ctx context.Context, s Snapshot) (bool, error)
}

// Files is a read view of one snapshot's tree.
type Files interface {
	Paths() []string
	Read(path string) ([]byte, error)
}

type FileChange struct {
	Path    string
	Content []byte
	Delete  bool
}

// Trailer is one structured provenance line of a publication.
type Trailer struct{ Key, Value string }

type Commit struct {
	Expected Snapshot
	Changes  []FileChange
	Author   string
	Subject  string
	Body     string
	Trailers []Trailer
	Check    func(Files) error
}

type LogEntry struct {
	Snapshot Snapshot
	Parents  []Snapshot
	Author   string
	When     time.Time
	Subject  string
	Body     string
	Trailers []Trailer
	Changed  []PathChange
}

// PathChange: Action is "added", "modified" or "deleted".
type PathChange struct {
	Path   string
	Action string
}

type Advance struct {
	Expected Snapshot
	Target   Snapshot
	Author   string
	Subject  string
	Body     string
	Trailers []Trailer
	Check    func(Files) error
}

// ActivationEntry is one recorded Advance.
type ActivationEntry struct {
	Target   Snapshot
	Previous Snapshot
	Author   string
	When     time.Time
	Subject  string
	Body     string
	Trailers []Trailer
}
