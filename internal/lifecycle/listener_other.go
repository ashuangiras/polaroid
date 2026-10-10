//go:build !darwin && !linux

package lifecycle

import "fmt"

// Listener is not available where service management is not supported.
func Listener(int) (Process, error) {
	return Process{}, fmt.Errorf("%w: cannot identify listening processes here", ErrUnsupported)
}
