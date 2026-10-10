package lifecycle

// Process is a process holding a listening socket.
type Process struct {
	PID     int    `json:"pid"`
	Command string `json:"command,omitempty"`
}

// ListenerFunc returns the process listening on TCP port, or a zero Process
// if none of this user's processes does.
type ListenerFunc func(port int) (Process, error)
