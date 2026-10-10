//go:build darwin

package main

import (
	"os"
	"syscall"
)

// redirectOutput points standard output and error at f, so that output the
// logger does not see, such as a panic, lands in the bounded log file.
func redirectOutput(f *os.File) error {
	for _, fd := range []int{1, 2} {
		if err := syscall.Dup2(int(f.Fd()), fd); err != nil {
			return err
		}
	}
	return nil
}
