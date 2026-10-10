//go:build !darwin

package main

import "os"

// redirectOutput leaves standard output and error alone: -log-file is used by
// the macOS service; elsewhere the service manager collects them.
func redirectOutput(*os.File) error { return nil }
