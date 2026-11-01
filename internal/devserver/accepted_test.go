package devserver

import (
	"errors"
	"net"
	"os"
	"testing"
)

func TestParseAccepted(t *testing.T) {
	out := []byte("p100\nf20\nPTCP\nn127.0.0.1:5173->127.0.0.1:50001\n" +
		"f21\nPTCP\nn[::1]:5173->[::1]:50002\n" +
		"p200\nf7\nPTCP\nn127.0.0.1:50001->127.0.0.1:5173\n" + // the browser's side
		"f8\nPUDP\nn127.0.0.1:9999->127.0.0.1:1\n" + // not TCP
		"f9\nPTCP\nn*:6006\n") // a listener line: not a connection
	got := parseAccepted(out)
	if len(got[5173]) != 2 || got[5173][0] != (Conn{PID: 100, Remote: "127.0.0.1:50001"}) ||
		got[5173][1] != (Conn{PID: 100, Remote: "[::1]:50002"}) {
		t.Fatalf("port 5173 = %+v", got[5173])
	}
	if c := got[50001]; len(c) != 1 || c[0].PID != 200 {
		t.Fatalf("the client side must be keyed by its own local port: %+v", got[50001])
	}
	if _, ok := got[9999]; ok {
		t.Fatal("UDP line parsed as a TCP connection")
	}
	if _, ok := got[6006]; ok {
		t.Fatal("listener line parsed as a connection")
	}
}

func TestAcceptedResultExitCodes(t *testing.T) {
	if m, err := acceptedResult(nil, exitErr(t, 1)); err != nil || len(m) != 0 {
		t.Fatalf("exit 1, no output = (%v, %v), want empty and nil", m, err)
	}
	if _, err := acceptedResult([]byte("garbage"), exitErr(t, 1)); err == nil {
		t.Fatal("exit 1 with output must be an error")
	}
	if _, err := acceptedResult(nil, errors.New("exec: lsof: not found")); err == nil {
		t.Fatal("a missing lsof must be an error, never 'no connections'")
	}
}

func TestListAcceptedSeesALiveConnection(t *testing.T) {
	requireLsof(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	srv, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	conns, err := ListAccepted()
	if err != nil {
		t.Fatal(err)
	}
	want := Conn{PID: os.Getpid(), Remote: client.LocalAddr().String()}
	for _, c := range conns[port] {
		if c == want {
			return
		}
	}
	t.Fatalf("accepted connection %+v absent from port %d: %+v", want, port, conns[port])
}
