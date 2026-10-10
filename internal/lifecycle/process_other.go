//go:build !unix

package lifecycle

func processAlive(int) bool { return false }
