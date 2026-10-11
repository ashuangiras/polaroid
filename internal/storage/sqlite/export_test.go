package sqlite

// StorageFiles exposes storageFiles to the tests.
var StorageFiles = storageFiles

// SetReadStarted makes s call hook inside each multi-statement read once its
// snapshot is open; nil removes it.
func SetReadStarted(s *Store, hook func()) { s.readStarted = hook }

// ConnectionsInUse reports how many of s's connections are checked out.
func ConnectionsInUse(s *Store) int { return s.db.Stats().InUse }
