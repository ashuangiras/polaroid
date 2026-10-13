//go:build !unix

package gitcore

import (
	"errors"
	"os"
)

func flock(*os.File) error { return errors.New("gitcore: publication needs a Unix file lock") }
