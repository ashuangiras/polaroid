package gitcore

import (
	"errors"
	"sync"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// lockedStorage serialises every object access. go-git's filesystem object
// storage is not safe for concurrent use once packs exist: its pack index
// and pack handle cache are built lazily and replaced without
// synchronisation. It also loads the pack list once per handle, so a pack
// another process added (by fetch or by a Git tool) is missed until Reindex;
// a miss is retried once after it.
type lockedStorage struct {
	*filesystem.Storage
	mu sync.Mutex
}

func (l *lockedStorage) EncodedObject(t plumbing.ObjectType, h plumbing.Hash) (plumbing.EncodedObject, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	o, err := l.Storage.EncodedObject(t, h)
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		l.Reindex()
		o, err = l.Storage.EncodedObject(t, h)
	}
	return o, err
}

func (l *lockedStorage) HasEncodedObject(h plumbing.Hash) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	err := l.Storage.HasEncodedObject(h)
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		l.Reindex()
		err = l.Storage.HasEncodedObject(h)
	}
	return err
}

func (l *lockedStorage) EncodedObjectSize(h plumbing.Hash) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Storage.EncodedObjectSize(h)
}

// SetEncodedObject is refused: objects are written by writeObject, durably.
func (l *lockedStorage) SetEncodedObject(plumbing.EncodedObject) (plumbing.Hash, error) {
	return plumbing.ZeroHash, errors.New("gitcore: objects are written only through Publish")
}

// reindex makes packs that were just moved into the store visible.
func (l *lockedStorage) reindex() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Reindex()
}
