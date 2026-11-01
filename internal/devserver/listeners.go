package devserver

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ListListeners returns every TCP listener on the machine as port → holding
// PIDs, in one lsof invocation.
//
// Why a second probe next to IsPortBusy rather than a faster IsPortBusy:
// the two are asked different questions. IsPortBusy answers "is *this* port
// taken", on interactive or decision paths — `pigtail dev`'s already-taken
// guard, the stop path, the watchdog's escalation, the rename guard — where
// one port is all that is wanted and a machine-wide inventory would be both
// wider and slower. ListListeners answers "what is listening, everywhere",
// which is the question the /servers route and rammon's sweep actually ask:
// they walk every workspace's every configured port, and used to fork one
// lsof per port, in series, inside the request.
//
// Measured on 2026-09-21, 51 live workspaces / 102 port entries:
// GET /servers took 26.6 / 8.9 / 7.4 s; this single command takes 0.10 s.
//
// The flags, each load-bearing:
//
//	-iTCP        TCP internet files. Unlike the bare `-i` trap documented on
//	             IsPortBusy, this form *takes* its argument, so it selects
//	             rather than OR-ing in every internet file on the machine.
//	-sTCP:LISTEN listeners only — a process merely connected to a port is not
//	             the server (see TestIsPortBusyReportsOnlyTheListener).
//	-P           mandatory: without it lsof renders well-known ports as
//	             service names ("n*:http"), and the port parse breaks.
//	-n           no reverse DNS, so no network round-trip per address.
//	-FpPn        field output: p=pid, P=protocol, n=address. lsof always adds
//	             the f (descriptor) field when file fields are requested.
//
// One deliberate semantic divergence from IsPortBusy, verified on
// 2026-09-21: `-sTCP:LISTEN` constrains TCP *state*, it does not exclude
// UDP. IsPortBusy's `-i :P` selects both protocols on that port, so a
// process holding only a UDP socket on :P comes back as holding the port —
// measured: a python UDP bind on 127.0.0.1:59321 made
// `lsof -t -sTCP:LISTEN -i :59321` print its pid. `-iTCP` excludes UDP by
// selection, so that same process is absent here. The divergence goes the
// right way: a port with no TCP listener but a UDP socket used to be
// reported "busy" and fabricated a phantom dev server.
func ListListeners() (map[int][]int, error) {
	// Output(), not CombinedOutput(): lsof writes warnings to stderr while
	// exiting 0 — "WARNING: can't stat() smbfs file system …" on this machine
	// — and folding those lines into the field stream would corrupt the
	// parse. IsPortBusy can afford CombinedOutput because its `-t` parser
	// drops any line that is not a bare integer; a field stream has no such
	// immunity.
	out, err := exec.Command("lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-FpPn").Output()
	return listenersResult(out, err)
}

// listenersResult interprets one bulk lsof invocation. Mirror of
// portProbeResult, and it carries the same non-negotiable property: a probe
// that did not happen must never read as "nothing is listening".
//
//   - exit 0            → parse the field stream.
//   - exit 1, no stdout → lsof matched nothing. On this command that means
//     the machine has no TCP listener at all. It is a
//     valid answer, so: empty map, nil error.
//   - anything else     → error. lsof absent from PATH, EMFILE under load,
//     EPERM, a signal — the caller must treat every port
//     as undecided, never as free.
//
// Exit 1 *with* stdout is the odd case: lsof produced output and still
// reported no match. Rather than guess which half to believe, it is an error
// — undecided is always the safe reading.
func listenersResult(out []byte, err error) (map[int][]int, error) {
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			return nil, fmt.Errorf("lsof listeners: %w", err)
		}
		if strings.TrimSpace(string(out)) != "" {
			return nil, fmt.Errorf("lsof listeners: exit 1 with output: %s",
				strings.TrimSpace(string(out)))
		}
		// Exit 1, nothing on stdout: no listener anywhere. An answer.
		return map[int][]int{}, nil
	}
	return parseListeners(out), nil
}

// parseListeners turns lsof's -F field stream into port → PIDs.
//
// The stream is stateful: a `p<pid>` line opens a process set, and every
// following file set (`f<fd>`, then its `P`/`n` fields) belongs to it until
// the next `p`. A process with several descriptors on one port emits that
// port once per descriptor, and two processes can share a port (SO_REUSEPORT,
// or a parent and its forked child), so both axes are de-duplicated.
//
// Unparseable lines are skipped rather than failed on: an unknown field
// letter from a future lsof must not empty the whole inventory. A line that
// cannot be read is one listener possibly missed, which the callers already
// tolerate; refusing the whole stream would report the machine as idle.
func parseListeners(out []byte) map[int][]int {
	ports := map[int]map[int]bool{}
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
			if err != nil {
				havePID = false
				continue
			}
			pid, havePID = n, true
		case 'f':
			// New file set: the protocol of the previous one does not carry.
			proto = ""
		case 'P':
			proto = val
		case 'n':
			if !havePID || (proto != "" && proto != "TCP") {
				continue
			}
			port, ok := portFromAddress(val)
			if !ok {
				continue
			}
			if ports[port] == nil {
				ports[port] = map[int]bool{}
			}
			ports[port][pid] = true
		}
	}
	// Sorted output would be nicer to read but costs a sort per port for no
	// caller: sumTree and the equality checks are both order-insensitive.
	res := make(map[int][]int, len(ports))
	for port, pids := range ports {
		list := make([]int, 0, len(pids))
		for p := range pids {
			list = append(list, p)
		}
		res[port] = list
	}
	return res
}

// portFromAddress extracts the port from an lsof address field.
//
// Three shapes, all seen in one `lsof -nP -iTCP -sTCP:LISTEN` run on this
// machine: `*:7000` (wildcard), `127.0.0.1:62491` (IPv4), `[::1]:3000`
// (IPv6). The brackets are what make the last colon unambiguous in the IPv6
// case, so splitting on the *last* colon is correct for all three.
//
// -P is what guarantees the tail is numeric; without it a well-known port
// arrives as "*:http" and lands in the !ok branch.
func portFromAddress(addr string) (int, bool) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return 0, false
	}
	// A "->" means a connection, not a listen. -sTCP:LISTEN already excludes
	// those; dropping them here too keeps the parser honest on its own.
	if strings.Contains(addr, "->") {
		return 0, false
	}
	port, err := strconv.Atoi(addr[i+1:])
	if err != nil || port <= 0 || port > 65535 {
		return 0, false
	}
	return port, true
}
