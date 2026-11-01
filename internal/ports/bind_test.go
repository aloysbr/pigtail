package ports

import (
	"errors"
	"net"
	"os"
	"strconv"
	"syscall"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

type nopListener struct{}

func (nopListener) Accept() (net.Conn, error) { return nil, errors.New("nop") }
func (nopListener) Close() error              { return nil }
func (nopListener) Addr() net.Addr            { return &net.TCPAddr{} }

func inUse(network string) error {
	return &net.OpError{Op: "listen", Net: network, Err: os.NewSyscallError("bind", syscall.EADDRINUSE)}
}

// The incident of 2026-10-05: a node server listened on [::1]:3013 only.
// Binding 127.0.0.1 succeeded, so 3013 looked free.
func TestCanBindRefusesIPv6OnlyListener(t *testing.T) {
	listen := func(network, addr string) (net.Listener, error) {
		if addr == "[::1]:3013" {
			return nil, inUse(network)
		}
		return nopListener{}, nil
	}
	if canBindWith(listen, 3013) {
		t.Fatal("a port held on [::1] must not read as free")
	}
}

func TestCanBindRefusesIPv4LoopbackListener(t *testing.T) {
	listen := func(network, addr string) (net.Listener, error) {
		if addr == "127.0.0.1:3013" {
			return nil, inUse(network)
		}
		return nopListener{}, nil
	}
	if canBindWith(listen, 3013) {
		t.Fatal("held on 127.0.0.1: read as free")
	}
}

// D7: the probe binds loopback only. A wildcard bind, even a probe closed at
// once, raises the macOS firewall dialog; wildcard listeners are lsof's job.
func TestCanBindNeverBindsWildcard(t *testing.T) {
	var tried []string
	listen := func(network, addr string) (net.Listener, error) {
		tried = append(tried, addr)
		return nopListener{}, nil
	}
	canBindWith(listen, 3013)
	for _, a := range tried {
		host, _, _ := net.SplitHostPort(a)
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			t.Errorf("probe bound %s: only loopback addresses may be bound", a)
		}
	}
	if len(tried) != 2 {
		t.Fatalf("tried %v, want 127.0.0.1 and ::1", tried)
	}
}

// A host without IPv6 cannot bind ::1 at all — and nothing else can listen
// there either. That must not turn every port into "taken".
func TestCanBindIgnoresMissingIPv6(t *testing.T) {
	listen := func(network, addr string) (net.Listener, error) {
		if network == "tcp6" {
			return nil, &net.OpError{Op: "listen", Net: network, Err: os.NewSyscallError("bind", syscall.EADDRNOTAVAIL)}
		}
		return nopListener{}, nil
	}
	if !canBindWith(listen, 3013) {
		t.Fatal("no IPv6 on the host: the port must stay allocatable")
	}
}

// Real sockets: listen on [::1]:P only, then ask the allocator for [P,P].
func TestAllocateSkipsRealIPv6OnlyListener(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer ln.Close()
	p := ln.Addr().(*net.TCPAddr).Port
	a := New(fakeUsed{m: map[int]bool{}})
	got, err := a.Allocate(map[string]pigtail.PortRange{"dev": {p, p}}, "dev")
	if err == nil {
		t.Fatalf("allocated %d while [::1]:%s listens", got, strconv.Itoa(p))
	}
}
