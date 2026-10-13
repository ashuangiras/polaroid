package gitstore

import (
	"errors"
	"io"
	"sync"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// lockedStorage serialises every object access. go-git's filesystem object
// storage is not safe for concurrent use once packs are involved: its pack
// index map and pack handle cache are built lazily and replaced by Reindex
// and by PackfileWriter without synchronisation (found with -race). It also
// loads the pack list once per handle, so a pack written by another process
// is invisible until Reindex; a miss is therefore retried once after one.
type lockedStorage struct {
	*filesystem.Storage
	mu sync.Mutex
}

func (l *lockedStorage) EncodedObject(t plumbing.ObjectType, h plumbing.Hash) (plumbing.EncodedObject, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	o, err := l.Storage.EncodedObject(t, h)
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		l.Storage.Reindex()
		o, err = l.Storage.EncodedObject(t, h)
	}
	return o, err
}

func (l *lockedStorage) HasEncodedObject(h plumbing.Hash) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Storage.HasEncodedObject(h)
}

func (l *lockedStorage) EncodedObjectSize(h plumbing.Hash) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Storage.EncodedObjectSize(h)
}

func (l *lockedStorage) SetEncodedObject(o plumbing.EncodedObject) (plumbing.Hash, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Storage.SetEncodedObject(o)
}

func (l *lockedStorage) PackfileWriter() (io.WriteCloser, error) {
	l.mu.Lock()
	w, err := l.Storage.PackfileWriter()
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &lockedCloser{WriteCloser: w, mu: &l.mu}, nil
}

// lockedCloser: closing a pack writer registers the pack in the shared index.
type lockedCloser struct {
	io.WriteCloser
	mu *sync.Mutex
}

func (c *lockedCloser) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.WriteCloser.Close()
}
