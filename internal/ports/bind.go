package ports

import (
	"errors"
	"net"
	"strconv"
	"syscall"
)

// probeAddrs are the loopback addresses the allocator binds to test a port:
// on 2026-10-05 a node server on [::1]:3013 alone was invisible to a
// 127.0.0.1-only probe, and 3013 was handed to a second worktree.
//
// Loopback only, never 0.0.0.0 or :: (D7): a wildcard bind raises the macOS
// firewall dialog and pigtaild listens on loopback by rule. Wildcard and LAN
// listeners are found by the daemon's one-lsof inventory (internal/daemon.ListenerSnapshot).
//
// ::1 is optional: on a host without IPv6 the bind fails with
// EADDRNOTAVAIL/EAFNOSUPPORT, and since nothing else can listen there either,
// that failure says nothing about the port. Only EADDRINUSE does.
var probeAddrs = []struct {
	network, host string
	optional      bool
}{
	{"tcp4", "127.0.0.1", false},
	{"tcp6", "::1", true},
}

// CanBind reports whether a new server could listen on port right now on
// both loopbacks. It cannot see a wildcard or LAN listener on every OS: the
// daemon's probe pairs it with lsof.
func CanBind(port int) bool { return canBindWith(net.Listen, port) }

func canBindWith(listen func(network, addr string) (net.Listener, error), port int) bool {
	for _, a := range probeAddrs {
		ln, err := listen(a.network, net.JoinHostPort(a.host, strconv.Itoa(port)))
		if err != nil {
			if a.optional && !errors.Is(err, syscall.EADDRINUSE) {
				continue
			}
			return false
		}
		_ = ln.Close()
	}
	return true
}
