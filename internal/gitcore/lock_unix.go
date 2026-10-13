//go:build unix

package gitcore

import (
	"os"
	"syscall"
)

// flock takes an exclusive advisory lock on f. The kernel releases it when
// the process exits, however it exits, so a crash leaves no stale lock.
func flock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }

// tryFlock takes the lock only if no process holds it.
func tryFlock(f *os.File) bool {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}
