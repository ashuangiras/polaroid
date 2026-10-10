//go:build darwin

package lifecycle

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Listener asks lsof, which macOS ships, for the TCP listener on port.
func Listener(port int) (Process, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-a", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-Fpc").Output() //nolint:gosec // a fixed program; the port is an integer
	var exit *exec.ExitError
	nothing := errors.As(err, &exit) && exit.ExitCode() == 1 && len(out) == 0
	if err != nil && !nothing {
		return Process{}, err
	}
	var p Process
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "p") && p.PID == 0:
			p.PID, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "c") && p.Command == "":
			p.Command = line[1:]
		}
	}
	return p, nil
}
