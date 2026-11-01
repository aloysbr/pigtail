package devserver

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// ErrTCPTableHidden is returned when the kernel hides the TCP table from this
// process. It carries no "time-wait inventory:" prefix: the caller's log says it.
var ErrTCPTableHidden = errors.New("the kernel hides the TCP table from this process (macOS 27 masks net.inet.tcp.pcblist under an ad-hoc-signed ancestor)")

// ListTimeWait returns the TCP connections in TIME_WAIT, by port.
//
// A short connection (a module re-fetched after an HMR edit, a page load on
// an expired keep-alive socket) may open and close between two sweeps. The
// side that closes it keeps the socket in TIME_WAIT for 2×MSL — 30 s on macOS
// (net.inet.tcp.msl = 15000), 60 s on Linux — longer than a sweep. Such a
// socket has no owning process, so lsof cannot show it; netstat and ss can.
//
// A key is listed under its local port (the server closed: keep-alive
// timeout) and, when the remote address is loopback, under its remote port
// too (a local client closed first: the browser on reload).
//
// On macOS 27 the kernel hides net.inet.tcp.pcblist from any process whose
// exec ancestry holds an ad-hoc-signed binary (go build output, pigtaild):
// netstat then prints nothing and exits 0, while net.inet.tcp.pcbcount stays
// right. When netstat shows no tcp row at all though PCBs exist, ListTimeWait
// returns ErrTCPTableHidden rather than an empty map that reads as "no
// TIME_WAIT".
func ListTimeWait() (map[int][]string, error) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("netstat", "-an", "-p", "tcp").Output()
		if err != nil {
			return nil, fmt.Errorf("netstat: %w", err)
		}
		return timeWaitFromNetstat(out, tcpPCBCount)
	case "linux":
		out, err := exec.Command("ss", "-Htan", "state", "time-wait").Output()
		if err != nil {
			return nil, fmt.Errorf("ss: %w", err)
		}
		return parseSSTimeWait(out), nil
	}
	return nil, fmt.Errorf("time-wait inventory: unsupported OS %s", runtime.GOOS)
}

// timeWaitFromNetstat decides between a TIME_WAIT map and ErrTCPTableHidden.
// The key is "a tcp row exists", never "the parsed map is empty": a visible
// table without TIME_WAIT is the common case. count (the kernel's PCB count)
// is only consulted when netstat showed no tcp row.
func timeWaitFromNetstat(out []byte, count func() (int, error)) (map[int][]string, error) {
	if hasTCPRow(out) {
		return parseNetstatTimeWait(out), nil
	}
	n, err := count()
	if err != nil {
		return nil, fmt.Errorf("sysctl pcbcount: %w", err)
	}
	if !netstatVisible(out, n) {
		return nil, ErrTCPTableHidden
	}
	return parseNetstatTimeWait(out), nil
}

// hasTCPRow reports whether any line of netstat's output starts with a tcp
// protocol field, whatever its state.
func hasTCPRow(out []byte) bool {
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 && strings.HasPrefix(f[0], "tcp") {
			return true
		}
	}
	return false
}

// netstatVisible: the table is readable when it shows a tcp row, or when the
// kernel counts no PCB at all (a truly empty table).
func netstatVisible(out []byte, pcbcount int) bool {
	return hasTCPRow(out) || pcbcount == 0
}

// parsePCBCount reads `sysctl -n net.inet.tcp.pcbcount`.
func parsePCBCount(out []byte) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("pcbcount %q: %w", strings.TrimSpace(string(out)), err)
	}
	if n < 0 {
		return 0, fmt.Errorf("pcbcount %d is negative", n)
	}
	return n, nil
}

// tcpPCBCount asks the kernel how many TCP PCBs exist; unlike pcblist, this
// counter stays readable under an ad-hoc ancestor.
func tcpPCBCount() (int, error) {
	out, err := exec.Command("/usr/sbin/sysctl", "-n", "net.inet.tcp.pcbcount").Output()
	if err != nil {
		return 0, err
	}
	return parsePCBCount(out)
}

// parseNetstatTimeWait reads `netstat -an -p tcp` (macOS): addresses end in
// ".port" ("127.0.0.1.5173", "::1.5173").
func parseNetstatTimeWait(out []byte) map[int][]string {
	res := map[int][]string{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 6 || !strings.HasPrefix(f[0], "tcp") || f[5] != "TIME_WAIT" {
			continue
		}
		la, lp, ok1 := splitHostPort(f[3], ".")
		ra, rp, ok2 := splitHostPort(f[4], ".")
		if ok1 && ok2 {
			addTimeWait(res, la, lp, ra, rp)
		}
	}
	return res
}

// parseSSTimeWait reads `ss -Htan state time-wait` (Linux): with one state
// selected, ss prints no state column; the last two fields are the local and
// peer endpoints ("127.0.0.1:5173", "[::1]:5173").
func parseSSTimeWait(out []byte) map[int][]string {
	res := map[int][]string{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		la, lp, ok1 := splitHostPort(f[len(f)-2], ":")
		ra, rp, ok2 := splitHostPort(f[len(f)-1], ":")
		if ok1 && ok2 {
			addTimeWait(res, la, lp, ra, rp)
		}
	}
	return res
}

func splitHostPort(s, sep string) (string, int, bool) {
	i := strings.LastIndex(s, sep)
	if i <= 0 {
		return "", 0, false
	}
	port, err := strconv.Atoi(s[i+1:])
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, false
	}
	return strings.Trim(s[:i], "[]"), port, true
}

func addTimeWait(res map[int][]string, la string, lp int, ra string, rp int) {
	key := la + ":" + strconv.Itoa(lp) + ">" + ra + ":" + strconv.Itoa(rp)
	res[lp] = append(res[lp], key)
	if ip := net.ParseIP(ra); ip != nil && ip.IsLoopback() && rp != lp {
		res[rp] = append(res[rp], key)
	}
}
