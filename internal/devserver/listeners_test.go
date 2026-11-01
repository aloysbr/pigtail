package devserver

import (
	"net"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"testing"
)

// requireLsof is the answer to "did this test actually run?".
//
// The two lsof-backed tests in portbusy_test.go skipped when lsof was absent,
// and CI runs `go test` without -v, so a skip is invisible in the log: nobody
// could tell whether the lsof format was being validated on Linux or whether
// those tests had been silently sitting out every run since they were
// written. On a CI runner the absence of lsof is now a failure, not a skip —
// the workflow installs it explicitly (see .github/workflows/ci.yml), so a
// missing lsof there means the install step regressed.
//
// Off CI it still skips: a developer machine without lsof is not a reason to
// fail someone's unrelated test run.
func requireLsof(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("lsof"); err == nil {
		return
	}
	if os.Getenv("CI") != "" {
		t.Fatal("lsof not found on PATH, but CI is set: this test validates lsof's " +
			"output format and must not be skipped on the runner. " +
			"Check the 'Install lsof' step in .github/workflows/ci.yml")
	}
	t.Skip("lsof not available")
}

// macOSWitness is a verbatim excerpt of `lsof -nP -iTCP -sTCP:LISTEN -FpPn`
// captured on macOS 15 (Darwin 25.6.0) on 2026-09-21. It covers everything
// the parser has to survive: two descriptors of one process on one port
// (p701), one process holding two different ports (p839, :7000 and :5000),
// a wildcard address, a loopback IPv4 address, and an IPv6 address whose
// brackets are what make its last colon the port separator.
const macOSWitness = `p701
f8
PTCP
n*:50555
f11
PTCP
n*:50555
p781
f46
PTCP
n127.0.0.1:62491
p839
f9
PTCP
n*:7000
f10
PTCP
n*:7000
f11
PTCP
n*:5000
f12
PTCP
n*:5000
p919
f33
PTCP
n127.0.0.1:27124
p1078
f3
PTCP
n[::1]:3002
`

// linuxWitness is NOT a capture: no Linux host was available when this was
// written, so it is built from lsof's -F grammar, which is one codebase and
// platform-independent in its field encoding. It differs from the macOS
// sample in the two ways Linux output actually does: the unspecified IPv6
// address prints as `[::]` rather than `*`, and a socket shared by a parent
// and its forked child (nginx, and any SO_REUSEPORT server) puts two pids on
// one port.
//
// What turns this into a real witness is the CI job: the Linux runner now
// installs lsof and runs TestListListenersMatchesIsPortBusy against a live
// listener, so a format divergence fails there rather than being assumed away
// here.
const linuxWitness = `p1
f6
PTCP
n[::]:22
p1234
f7
PTCP
n0.0.0.0:80
p1235
f7
PTCP
n0.0.0.0:80
p1240
f11
PTCP
n127.0.0.1:631
f12
PTCP
n[::1]:631
`

func TestParseListeners(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want map[int][]int
	}{
		{
			name: "macOS capture",
			in:   macOSWitness,
			want: map[int][]int{
				50555: {701},
				62491: {781},
				7000:  {839},
				5000:  {839},
				27124: {919},
				3002:  {1078},
			},
		},
		{
			name: "Linux shapes",
			in:   linuxWitness,
			want: map[int][]int{
				22:  {1},
				80:  {1234, 1235},
				631: {1240},
			},
		},
		{
			name: "empty stream is an empty inventory, not a nil map",
			in:   "",
			want: map[int][]int{},
		},
		{
			name: "a name field with no preceding pid is dropped, not attributed",
			in:   "f3\nPTCP\nn*:8080\n",
			want: map[int][]int{},
		},
		{
			name: "a non-TCP file set is skipped even if it slipped through selection",
			in:   "p5\nf3\nPUDP\nn*:5353\nf4\nPTCP\nn*:8080\n",
			want: map[int][]int{8080: {5}},
		},
		{
			name: "a service name (what -P prevents) is dropped rather than misparsed",
			in:   "p5\nf3\nPTCP\nn*:http\n",
			want: map[int][]int{},
		},
		{
			name: "an unknown field letter does not empty the inventory",
			in:   "p5\nf3\nPTCP\nZ7\nn*:8080\n",
			want: map[int][]int{8080: {5}},
		},
		{
			name: "an unparseable pid detaches its following file sets",
			in:   "pXYZ\nf3\nPTCP\nn*:8080\np6\nf3\nPTCP\nn*:9090\n",
			want: map[int][]int{9090: {6}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseListeners([]byte(tc.in))
			for _, pids := range got {
				sort.Ints(pids)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseListeners() = %v, want %v", got, tc.want)
			}
		})
	}
}

// shellOutput runs a shell snippet and returns exactly what exec.Cmd.Output()
// returns for it — stdout plus a real *exec.ExitError. Real errors rather
// than fabricated ones: the branch under test reads ExitCode(), and a
// hand-built ProcessState would be testing the fake. (portprobe_test.go's
// exitErr is the same idea but discards stdout, which is the half this test
// needs.)
func shellOutput(t *testing.T, script string) ([]byte, error) {
	t.Helper()
	out, err := exec.Command("sh", "-c", script).Output() //nolint:gosec // fixed test scripts
	return out, err
}

// TestListenersResultExitCodes is the test that holds the safety property:
// a probe that did not happen must never be read as "nothing is listening".
// Only one failure mode is allowed to produce an empty inventory with a nil
// error — lsof exiting 1 with nothing on stdout, which is its way of saying
// it matched nothing.
func TestListenersResultExitCodes(t *testing.T) {
	t.Run("exit 0 parses", func(t *testing.T) {
		got, err := listenersResult([]byte("p42\nf3\nPTCP\nn*:8080\n"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, map[int][]int{8080: {42}}) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("exit 1 with empty stdout is a valid empty inventory", func(t *testing.T) {
		out, err := shellOutput(t, "exit 1")
		got, rerr := listenersResult(out, err)
		if rerr != nil {
			t.Fatalf("exit 1 with no output must be an answer, got error: %v", rerr)
		}
		if got == nil {
			t.Fatal("a valid empty inventory must be a non-nil empty map")
		}
		if len(got) != 0 {
			t.Fatalf("got %v, want empty", got)
		}
	})

	t.Run("exit 1 with stdout is undecided, not empty", func(t *testing.T) {
		out, err := shellOutput(t, "echo p123; exit 1")
		got, rerr := listenersResult(out, err)
		if rerr == nil {
			t.Fatalf("exit 1 with output must not read as an answer; got %v", got)
		}
		if got != nil {
			t.Fatalf("an errored probe must return no inventory, got %v", got)
		}
	})

	t.Run("any other exit code is undecided, not empty", func(t *testing.T) {
		for _, code := range []int{2, 9, 127} {
			out, err := shellOutput(t, "exit "+itoa(code))
			got, rerr := listenersResult(out, err)
			if rerr == nil {
				t.Fatalf("exit %d must be an error, not an empty inventory (got %v)", code, got)
			}
			if got != nil {
				t.Fatalf("exit %d returned an inventory %v; a failed probe must return none", code, got)
			}
		}
	})

	t.Run("a non-ExitError (lsof absent from PATH) is undecided", func(t *testing.T) {
		_, err := exec.Command("pigtail-no-such-binary-ever").Output()
		if err == nil {
			t.Skip("a binary by that name exists; nothing to assert")
		}
		got, rerr := listenersResult(nil, err)
		if rerr == nil {
			t.Fatalf("an unrunnable lsof must be an error, not an empty inventory (got %v)", got)
		}
		if got != nil {
			t.Fatalf("got inventory %v on an unrunnable probe", got)
		}
	})
}

// TestListListenersSeesALiveListener runs the real command against a real
// socket: the parse table above proves the grammar is read correctly, this
// proves the grammar is the one lsof actually emits on this machine.
func TestListListenersSeesALiveListener(t *testing.T) {
	requireLsof(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	listeners, err := ListListeners()
	if err != nil {
		t.Fatal(err)
	}
	pids, ok := listeners[port]
	if !ok {
		t.Fatalf("a live listener on :%d is absent from the inventory (%d ports found)",
			port, len(listeners))
	}
	self := os.Getpid()
	found := false
	for _, pid := range pids {
		if pid == self {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected this test process (pid %d) to hold :%d, got %v", self, port, pids)
	}
}

// TestListListenersMatchesIsPortBusy is the equivalence assertion, and the
// whole point of the bulk path: it may be faster, it may not answer
// differently. Same live port, same pid set.
//
// The listener is a *subprocess*, so its pid is distinguishable from the test
// process's own — a listener opened in-process would make the comparison
// trivially true for the wrong reason.
func TestListListenersMatchesIsPortBusy(t *testing.T) {
	requireLsof(t)

	port := freeTestPort(t)
	listener := spawnPortListener(t, port, "")
	waitPortBusyForTest(t, port)

	busy, unitPIDs, err := IsPortBusy(port)
	if err != nil {
		t.Fatal(err)
	}
	if !busy {
		t.Fatalf("IsPortBusy(:%d) says free while a listener is up", port)
	}

	listeners, err := ListListeners()
	if err != nil {
		t.Fatal(err)
	}
	bulkPIDs := listeners[port]

	sort.Ints(unitPIDs)
	sort.Ints(bulkPIDs)
	if !reflect.DeepEqual(unitPIDs, bulkPIDs) {
		t.Fatalf("bulk and unit probes disagree on :%d: ListListeners()=%v, IsPortBusy()=%v "+
			"(listener pid %d)", port, bulkPIDs, unitPIDs, listener.Process.Pid)
	}
	if len(bulkPIDs) == 0 {
		t.Fatalf("both probes returned nothing for :%d, so they agree on nothing", port)
	}
}

// TestListListenersReportsOnlyTheListener is the bulk-path copy of
// TestIsPortBusyReportsOnlyTheListener. The `-sTCP:LISTEN` flag is what earns
// it — without it lsof matches a socket by either endpoint, and every browser
// tab open on a dev server lands in the pid set that rammon attributes RSS to
// and that the stop path kills.
func TestListListenersReportsOnlyTheListener(t *testing.T) {
	requireLsof(t)

	port := freeTestPort(t)
	listener := spawnPortListener(t, port, "")
	waitPortBusyForTest(t, port)
	client, _ := spawnPortClient(t, port)

	listeners, err := ListListeners()
	if err != nil {
		t.Fatal(err)
	}
	pids := listeners[port]

	listenerPID := listener.Process.Pid
	clientPID := client.Process.Pid
	for _, pid := range pids {
		if pid == clientPID {
			t.Fatalf("a client of :%d (pid %d) must not be reported as holding the port; "+
				"got pids %v (listener is %d)", port, clientPID, pids, listenerPID)
		}
	}
	if len(pids) != 1 || pids[0] != listenerPID {
		t.Fatalf("expected exactly the listener pid %d on :%d, got %v", listenerPID, port, pids)
	}
}
