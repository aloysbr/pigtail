package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// gateDaemon is a fake daemon answering POST /session-budget/leases with a,
// counting the calls and recording the requests.
type gateDaemon struct {
	mu    sync.Mutex
	calls int
	reqs  []pigtail.SessionLeaseRequest
}

func newGateDaemon(t *testing.T, a pigtail.SessionLeaseAnswer) (*stubDaemon, *gateDaemon) {
	t.Helper()
	g := &gateDaemon{}
	mux := http.NewServeMux()
	mux.HandleFunc("/session-budget/leases", func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.SessionLeaseRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		g.mu.Lock()
		g.calls++
		g.reqs = append(g.reqs, req)
		g.mu.Unlock()
		writeData(w, http.StatusOK, a)
	})
	s := &stubDaemon{srv: httptest.NewServer(mux)}
	t.Cleanup(s.srv.Close)
	return s, g
}

// runGate runs `pigtail session gate --kind human` against s and returns its
// stderr, stdout, error and duration.
func runGate(t *testing.T, s *stubDaemon) (stderr, stdout string, took time.Duration, err error) {
	t.Helper()
	var eb, ob bytes.Buffer
	withStubDaemon(t, s, func() {
		cmd := sessionCmd()
		cmd.SetArgs([]string{"gate", "--kind", "human"})
		cmd.SetErr(&eb)
		cmd.SetOut(&ob)
		start := time.Now()
		err = cmd.Execute()
		took = time.Since(start)
	})
	return eb.String(), ob.String(), took, err
}

func clearGateEnv(t *testing.T) {
	t.Setenv("PIGTAIL_SESSION_GATE", "")
	t.Setenv("PIGTAIL_GATE_PASSED", "")
}

func over(v bool) *bool { return &v }

func TestGateHumanOverBudgetPrintsLineExitsZero(t *testing.T) {
	clearGateEnv(t)
	s, g := newGateDaemon(t, pigtail.SessionLeaseAnswer{Admitted: true, Busy: 12, Budget: 6, Reason: "compresseur 18 Mo/s", OverBudget: over(true)})
	stderr, stdout, _, err := runGate(t, s)
	if err != nil {
		t.Fatalf("gate over budget must exit 0: %v", err)
	}
	if want := "au-delà du budget : 12/6 au travail · compresseur 18 Mo/s\n"; stderr != want || stdout != "" {
		t.Fatalf("stderr %q stdout %q, want stderr %q", stderr, stdout, want)
	}
	if len(g.reqs) != 1 || g.reqs[0].Kind != "human" {
		t.Fatalf("asked %+v, want one kind human ask", g.reqs)
	}
}

func TestGateOpenPrintsNothingAndReturnsUnderHalfSecond(t *testing.T) {
	clearGateEnv(t)
	s, g := newGateDaemon(t, pigtail.SessionLeaseAnswer{Admitted: true, Busy: 3, Budget: 20, OverBudget: over(false)})
	stderr, stdout, took, err := runGate(t, s)
	if err != nil || stderr != "" || stdout != "" {
		t.Fatalf("open gate: err %v stderr %q stdout %q", err, stderr, stdout)
	}
	if took >= 500*time.Millisecond {
		t.Fatalf("open gate took %v", took)
	}
	if g.calls != 1 {
		t.Fatalf("calls %d", g.calls)
	}
}

func TestGateDisabledPrintsNothing(t *testing.T) {
	clearGateEnv(t)
	// A disabled controller never sets over_budget.
	s, _ := newGateDaemon(t, pigtail.SessionLeaseAnswer{Admitted: true, Busy: 40, OverBudget: over(false)})
	if stderr, _, _, err := runGate(t, s); err != nil || stderr != "" {
		t.Fatalf("disabled gate: err %v stderr %q", err, stderr)
	}
}

func TestGateDaemonHangsTimesOutThenPasses(t *testing.T) {
	clearGateEnv(t)
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/session-budget/leases", func(http.ResponseWriter, *http.Request) { <-release })
	s := &stubDaemon{srv: httptest.NewServer(mux)}
	t.Cleanup(s.srv.Close)
	t.Cleanup(func() { close(release) }) // runs first: lets the handler return
	stderr, stdout, took, err := runGate(t, s)
	if err != nil || stderr != "" || stdout != "" {
		t.Fatalf("hanging daemon: err %v stderr %q stdout %q", err, stderr, stdout)
	}
	if took < gateTimeout || took > gateTimeout+time.Second {
		t.Fatalf("returned after %v, want about the %v client timeout", took, gateTimeout)
	}
}

func TestGateDaemonDownPassesSilently(t *testing.T) {
	clearGateEnv(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close() // nothing listens there now
	s := &stubDaemon{srv: &httptest.Server{URL: "http://" + addr}}
	stderr, stdout, _, gerr := runGate(t, s)
	if gerr != nil || stderr != "" || stdout != "" {
		t.Fatalf("daemon down: err %v stderr %q stdout %q", gerr, stderr, stdout)
	}
}

func TestGateEnvBypass(t *testing.T) {
	s, g := newGateDaemon(t, pigtail.SessionLeaseAnswer{Admitted: true, Busy: 12, Budget: 6, Reason: "budget plein", OverBudget: over(true)})
	cases := []struct{ key, val string }{
		{"PIGTAIL_SESSION_GATE", "0"},
		{"PIGTAIL_GATE_PASSED", "spawn"},
		{"PIGTAIL_GATE_PASSED", "respawn"},
		{"PIGTAIL_GATE_PASSED", "cmux-fix"},
		{"PIGTAIL_GATE_PASSED", "relay"},
	}
	for _, c := range cases {
		clearGateEnv(t)
		t.Setenv(c.key, c.val)
		if stderr, _, _, err := runGate(t, s); err != nil || stderr != "" {
			t.Fatalf("%s=%s: err %v stderr %q", c.key, c.val, err, stderr)
		}
	}
	if g.calls != 0 {
		t.Fatalf("a bypassed gate called the daemon %d times", g.calls)
	}
}

func TestSessionGateHiddenFromSessionHelp(t *testing.T) {
	var out bytes.Buffer
	cmd := sessionCmd()
	cmd.SetArgs([]string{"--help"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "gate") {
		t.Fatalf("`pigtail session --help` lists the gate:\n%s", out.String())
	}
	gate, _, err := sessionCmd().Find([]string{"gate"})
	if err != nil || gate == nil || gate.Name() != "gate" || !gate.Hidden {
		t.Fatalf("gate not found as a hidden subcommand: %v %v", gate, err)
	}
	if gate.Annotations[scopeAccessKey] != "read" {
		t.Fatalf("gate must be scopedRead: %v", gate.Annotations)
	}
}
