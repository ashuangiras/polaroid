//go:build linux

package lifecycle

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Listener finds the TCP listener on port in /proc: the socket inode from
// /proc/net/tcp{,6}, then the process holding it among this user's.
func Listener(port int) (Process, error) {
	inodes := map[string]bool{}
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(table)
		if err != nil {
			continue
		}
		s := bufio.NewScanner(f)
		for s.Scan() {
			// sl local_address rem_address st tx:rx tr:when retrnsmt uid timeout inode
			fields := strings.Fields(s.Text())
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			_, hexPort, ok := strings.Cut(fields[1], ":")
			if p, err := strconv.ParseInt(hexPort, 16, 32); ok && err == nil && int(p) == port {
				inodes["socket:["+fields[9]+"]"] = true
			}
		}
		_ = f.Close()
	}
	if len(inodes) == 0 {
		return Process{}, nil
	}
	procs, err := filepath.Glob("/proc/[0-9]*")
	if err != nil {
		return Process{}, err
	}
	for _, dir := range procs {
		fds, err := os.ReadDir(filepath.Join(dir, "fd"))
		if err != nil {
			continue // another user's process, or gone
		}
		for _, fd := range fds {
			if target, err := os.Readlink(filepath.Join(dir, "fd", fd.Name())); err == nil && inodes[target] {
				pid, _ := strconv.Atoi(filepath.Base(dir))
				comm, _ := os.ReadFile(filepath.Join(dir, "comm"))
				return Process{PID: pid, Command: strings.TrimSpace(string(comm))}, nil
			}
		}
	}
	return Process{}, fmt.Errorf("port %d is listening, but not in a process of this user", port)
}
