package devserver

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Conn is one established TCP connection as lsof shows it from the process
// holding it: Remote is the other end's endpoint, which identifies the client
// socket — a reload closes the old one and opens a new one.
type Conn struct {
	PID    int
	Remote string
}

// ListAccepted returns every established TCP connection on the machine, keyed
// by its *local* port, in one lsof invocation. A dev server's accepted
// connections land under its listening port; the client half of a loopback
// connection lands under its ephemeral port, which no caller looks up — and
// callers filter by the listener's PIDs anyway.
//
// Same tool and field format as ListListeners, kept separate so that inventory
// (served to GET /servers and decision paths) stays exactly what it is. Linux
// uses the same call: pigtail already needs lsof there for listeners.
func ListAccepted() (map[int][]Conn, error) {
	// Output(), not CombinedOutput(): see ListListeners — stderr warnings
	// would corrupt the field stream.
	out, err := exec.Command("lsof", "-nP", "-iTCP", "-sTCP:ESTABLISHED", "-FpPn").Output()
	return acceptedResult(out, err)
}

// acceptedResult applies listenersResult's exit-code rules: exit 1 with no
// output is "no connection anywhere", an answer; anything else that failed is
// an error, and the caller must read it as "unknown", never as "none".
func acceptedResult(out []byte, err error) (map[int][]Conn, error) {
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			return nil, fmt.Errorf("lsof established: %w", err)
		}
		if strings.TrimSpace(string(out)) != "" {
			return nil, fmt.Errorf("lsof established: exit 1 with output: %s", strings.TrimSpace(string(out)))
		}
		return map[int][]Conn{}, nil
	}
	return parseAccepted(out), nil
}

// parseAccepted reads lsof's -F stream (p=pid, f=fd, P=protocol,
// n="local->remote"). Lines it cannot read are skipped, as in parseListeners.
func parseAccepted(out []byte) map[int][]Conn {
	res := map[int][]Conn{}
	var pid int
	var havePID bool
	var proto string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		field, val := line[0], line[1:]
		switch field {
		case 'p':
			n, err := strconv.Atoi(strings.TrimSpace(val))
			havePID = err == nil
			pid = n
		case 'f':
			proto = ""
		case 'P':
			proto = val
		case 'n':
			if !havePID || (proto != "" && proto != "TCP") {
				continue
			}
			local, remote, ok := strings.Cut(val, "->")
			if !ok || remote == "" {
				continue // a listener, not a connection
			}
			port, ok := portFromAddress(local)
			if !ok {
				continue
			}
			res[port] = append(res[port], Conn{PID: pid, Remote: remote})
		}
	}
	return res
}
