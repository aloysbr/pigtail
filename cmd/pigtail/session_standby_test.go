package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// standbyServer is a relay test daemon that notes every request it serves
// ("METHOD path"), for the calls no route reads back.
type standbyServer struct {
	st  *store.Store
	dir string
	mu  sync.Mutex
	got []string
}

func (s *standbyServer) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

func (s *standbyServer) count(call string) int {
	n := 0
	for _, c := range s.calls() {
		if c == call {
			n++
		}
	}
	return n
}

func startStandbyServer(t *testing.T) *standbyServer {
	t.Helper()
	return startStandbyServerWith(t, nil)
}

// startStandbyServerWith is startStandbyServer with a sidebar loop stand-in
// (nil = none wired).
func startStandbyServerWith(t *testing.T, sidebar handlers.CmuxSidebarControl) *standbyServer {
	t.Helper()
	testutil.SerialOnly(t)                // swaps flagSocket
	dir := testutil.MkdirTemp(t, "ptsb-") // short: unix socket paths are capped near 104 bytes
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &standbyServer{st: st, dir: dir}
	svc := &handlers.Services{Store: st, Logf: t.Logf}
	if sidebar != nil {
		svc.CmuxSidebar = sidebar
	}
	rl := handlers.NewRelay(svc, handlers.RelayConfig{
		MarkerDir:        filepath.Join(dir, "inbox"),
		WaitMarkerDir:    filepath.Join(dir, "wait"),
		StandbyMarkerDir: filepath.Join(dir, "standby"),
		Alive:            func(p pigtail.PeerSession) bool { return p.EndedAt == nil },
	})
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			s.mu.Lock()
			s.got = append(s.got, req.Method+" "+req.URL.Path)
			s.mu.Unlock()
			next.ServeHTTP(w, req)
		})
	})
	handlers.RegisterRelayRoutes(r, rl, envResponder{})
	sock := filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	old := flagSocket
	flagSocket = sock
	t.Cleanup(func() { flagSocket = old })
	return s
}

// `pigtail session standby` sets the session's Standby (PUT, registering the
// session first if the daemon did not know it); --clear drops it (DELETE).
func TestSessionStandbyCommand(t *testing.T) {
	s := startStandbyServer(t)
	freeBgDeps(t)
	env := envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-sb"})
	var out bytes.Buffer
	if err := runSessionStandby(context.Background(), &out, &bytes.Buffer{}, env, false, false); err != nil {
		t.Fatal(err)
	}
	if s.count("PUT /peers/s-sb/standby") == 0 {
		t.Fatalf("no PUT: %v", s.calls())
	}
	if sb, err := s.st.GetStandby("s-sb"); err != nil || sb == nil {
		t.Fatalf("row = %+v, %v (calls %v)", sb, err, s.calls())
	}
	if _, err := os.Stat(filepath.Join(s.dir, "standby", "s-sb")); err != nil {
		t.Fatalf("no marker: %v", err)
	}
	if err := runSessionStandby(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, env, true, false); err != nil {
		t.Fatal(err)
	}
	if s.count("DELETE /peers/s-sb/standby") != 1 {
		t.Fatalf("no DELETE: %v", s.calls())
	}
	if sb, _ := s.st.GetStandby("s-sb"); sb != nil {
		t.Fatalf("row after --clear: %+v", sb)
	}
	// --clear for a session the daemon does not know: a quiet success.
	if err := runSessionStandby(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-none"}), true, false); err != nil {
		t.Fatalf("--clear unknown: %v", err)
	}
	// Outside a Claude session: an error, no call.
	before := len(s.calls())
	if err := runSessionStandby(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, noEnv, false, false); err == nil {
		t.Fatal("no session, no error")
	}
	if len(s.calls()) != before {
		t.Fatalf("called the daemon without a session: %v", s.calls()[before:])
	}
}

// standbyRun runs `pigtail session standby` against the test daemon with the
// given deps, and returns its stderr, its exit code and the PUTs it sent.
func standbyRun(t *testing.T, s *standbyServer, env map[string]string, clear, check bool) (string, int, int) {
	t.Helper()
	if env == nil {
		env = map[string]string{}
	}
	if _, ok := env["CLAUDE_CODE_SESSION_ID"]; !ok {
		env["CLAUDE_CODE_SESSION_ID"] = "s-bg"
	}
	before := s.count("PUT /peers/s-bg/standby")
	var out, errOut bytes.Buffer
	err := runSessionStandby(context.Background(), &out, &errOut, envOf(env), clear, check)
	return errOut.String(), exitCode(err), s.count("PUT /peers/s-bg/standby") - before
}

const (
	d29 = "Standby refusé — tâche de fond en cours : %s. Relance /standby à sa fin.\n"
	d33 = "Standby refusé — je n'arrive pas à lire les tâches de fond (%s). Réessaie /standby dans un instant.\n"
)

// D25, D29, D39, D42: a cron or wakeup listed at the last Stop, a task pending
// in the transcript, a Bash-tool shell or a background row on screen refuses
// /standby before anything is written; the screen never proves "free".
func TestStandbyRefusedWithBackgroundWork(t *testing.T) {
	s := startStandbyServer(t)
	bg, err := os.ReadFile(filepath.Join("..", "..", "internal", "claudesession", "testdata", "screen-bg-agent.txt"))
	if err != nil {
		t.Fatal(err)
	}
	screenWithBackgroundRow := string(bg)
	cases := []struct {
		name string
		set  func(d *standbyBgDeps)
		what string
	}{
		{"wakeup", func(d *standbyBgDeps) {
			d.Record = func(string, int, string) (bgRecord, error) {
				return bgRecord{PID: 4242, Turn: "p0", Crons: &[]bgCron{{ID: "50253cb1", Schedule: "30 15 * * *"}}}, nil
			}
		}, "cron 50253cb1 (30 15 * * *)"},
		{"loop cron", func(d *standbyBgDeps) {
			d.Record = func(string, int, string) (bgRecord, error) {
				return bgRecord{PID: 4242, Turn: "p0", Crons: &[]bgCron{{ID: "bcef88fa", Schedule: "*/1 * * * *", Recurring: true}}}, nil
			}
		}, "cron bcef88fa (*/1 * * * *)"},
		{"pending", func(d *standbyBgDeps) {
			d.Pending = func(string, time.Time) ([]string, error) { return []string{"b4sorpvo8"}, nil }
		}, "tâche b4sorpvo8"},
		{"shell", func(d *standbyBgDeps) {
			d.Shells = func(int) ([]int, error) { return []int{77, 901}, nil }
		}, "shell Bash 901"},
		{"screen", func(d *standbyBgDeps) {
			d.Screen = func() (string, error) { return screenWithBackgroundRow, nil }
		}, "tâche de fond affichée à l'écran"},
		{"quiet screen, cron listed", func(d *standbyBgDeps) {
			d.Record = func(string, int, string) (bgRecord, error) {
				return bgRecord{PID: 4242, Turn: "p0", Crons: &[]bgCron{{ID: "c1", Schedule: "* * * * *"}}}, nil
			}
			d.Screen = func() (string, error) { return "", nil }
		}, "cron c1 (* * * * *)"},
	}
	for _, c := range cases {
		c.set(freeBgDeps(t))
		stderr, code, puts := standbyRun(t, s, nil, false, false)
		if want := fmt.Sprintf(d29, c.what); stderr != want || code != 3 || puts != 0 {
			t.Fatalf("%s: stderr %q code %d puts %d, want %q", c.name, stderr, code, puts, want)
		}
	}
	// Positive controls: nothing anywhere, or only the launching shells → one PUT.
	freeBgDeps(t)
	if stderr, code, puts := standbyRun(t, s, nil, false, false); code != 0 || puts == 0 || stderr != "" {
		t.Fatalf("free: stderr %q code %d puts %d", stderr, code, puts)
	}
}

// The command's own launching shells (its ancestors) are not background work.
func TestStandbyBackgroundAncestorExcluded(t *testing.T) {
	s := startStandbyServer(t)
	d := freeBgDeps(t)
	d.Shells = func(int) ([]int, error) { return []int{77}, nil }
	if stderr, code, puts := standbyRun(t, s, nil, false, false); code != 0 || puts == 0 {
		t.Fatalf("ancestor shell only: stderr %q code %d puts %d", stderr, code, puts)
	}
}

// D33: an unreadable reading with no work found refuses with its own named
// reason and code 4, never the D29 text; a reading whose dependency failed
// is skipped, so only the cause is named. D43: hooks off name the cause.
func TestStandbyUnreadableDistinctFromBackground(t *testing.T) {
	s := startStandbyServer(t)
	type tc struct {
		name   string
		set    func(t *testing.T, d *standbyBgDeps)
		env    map[string]string
		reason string
	}
	rec := func(err error) func(string, int, string) (bgRecord, error) {
		return func(string, int, string) (bgRecord, error) { return bgRecord{}, err }
	}
	cases := []tc{
		{"no record", func(_ *testing.T, d *standbyBgDeps) { d.Record = rec(errNoBgRecord) }, nil,
			"aucune fin de tour enregistrée (premier tour de la session, ou hook Stop de pigtail absent)"},
		{"unreadable record", func(_ *testing.T, d *standbyBgDeps) { d.Record = rec(errBgUnreadable) }, nil, "liste des crons illisible"},
		{"stale record", func(_ *testing.T, d *standbyBgDeps) { d.Record = rec(errStaleBgRecord) }, nil,
			"dernière fin de tour enregistrée pour un autre tour ou un autre processus"},
		{"no list", func(_ *testing.T, d *standbyBgDeps) { d.Record = rec(errNoBgLists) }, nil, "le Stop ne porte pas la liste des crons"},
		{"no previous turn", func(t *testing.T, d *standbyBgDeps) {
			d.PrevTurn = func(string) (string, bool) { return "", false }
			d.Record = func(string, int, string) (bgRecord, error) { t.Error("Record called"); return bgRecord{}, nil }
		}, nil, "tour précédent introuvable au transcript"},
		{"no transcript", func(t *testing.T, d *standbyBgDeps) {
			d.Transcript = func(string) (string, bool, error) { return "", false, nil }
			d.PrevTurn = func(string) (string, bool) { t.Error("PrevTurn called"); return "", false }
			d.Record = func(string, int, string) (bgRecord, error) { t.Error("Record called"); return bgRecord{}, nil }
			d.Pending = func(string, time.Time) ([]string, error) { t.Error("Pending called"); return nil, nil }
		}, nil, "transcript de la session introuvable"},
		{"transcript ambiguous", func(_ *testing.T, d *standbyBgDeps) {
			d.Transcript = func(string) (string, bool, error) { return "", false, errors.New("two accounts") }
		}, nil, "transcript illisible"},
		{"transcript unreadable", func(_ *testing.T, d *standbyBgDeps) {
			d.Pending = func(string, time.Time) ([]string, error) { return nil, errors.New("eio") }
		}, nil, "transcript illisible"},
		{"screen unreadable", func(_ *testing.T, d *standbyBgDeps) {
			d.Screen = func() (string, error) { return "", errors.New("cmux down") }
		}, nil, "écran de la session illisible"},
		{"no caller", func(t *testing.T, d *standbyBgDeps) {
			d.Caller = func() (int, bool, error) { return 0, false, nil }
			d.Native = func(int) (claudesession.Native, bool) { t.Error("Native called"); return claudesession.Native{}, false }
			d.Record = func(string, int, string) (bgRecord, error) { t.Error("Record called"); return bgRecord{}, nil }
			d.Shells = func(int) ([]int, error) { t.Error("Shells called"); return nil, nil }
		}, nil, "processus Claude appelant introuvable"},
		{"ancestors unreadable", func(t *testing.T, d *standbyBgDeps) {
			d.Ancestors = func() ([]int, error) { return nil, errors.New("ps") }
			d.Shells = func(int) ([]int, error) { t.Error("Shells called"); return nil, nil }
		}, nil, "processus parents illisibles"},
		{"no native", func(t *testing.T, d *standbyBgDeps) {
			d.Native = func(int) (claudesession.Native, bool) { return claudesession.Native{}, false }
			d.Pending = func(string, time.Time) ([]string, error) { t.Error("Pending called"); return nil, nil }
		}, nil, "état natif de la session introuvable"},
	}
	for _, c := range cases {
		c.set(t, freeBgDeps(t))
		stderr, code, puts := standbyRun(t, s, c.env, false, false)
		if want := fmt.Sprintf(d33, c.reason); stderr != want || code != 4 || puts != 0 {
			t.Fatalf("%s: stderr %q code %d puts %d, want %q", c.name, stderr, code, puts, want)
		}
	}
	// The real screen reader refuses an empty or invalid surface id.
	for _, sid := range []string{"", "not-a-uuid"} {
		d := freeBgDeps(t)
		d.Screen = standbyScreenReader(envOf(map[string]string{"CMUX_SURFACE_ID": sid}))
		if stderr, code, _ := standbyRun(t, s, nil, false, false); code != 4 || stderr != fmt.Sprintf(d33, "écran de la session illisible") {
			t.Fatalf("surface %q: stderr %q code %d", sid, stderr, code)
		}
	}
	// Hooks off (D43): the cause is named, nothing is read.
	for _, k := range []string{"PIGTAIL_WAIT", "PIGTAIL_RELAY"} {
		d := freeBgDeps(t)
		d.Caller = func() (int, bool, error) { t.Error("read with hooks off"); return 0, false, nil }
		stderr, code, puts := standbyRun(t, s, map[string]string{k: "off"}, false, false)
		want := "Standby refusé — je n'arrive pas à lire les tâches de fond (hooks pigtail coupés : " + k + "=off). Réactive-les pour utiliser /standby.\n"
		if stderr != want || code != 4 || puts != 0 {
			t.Fatalf("%s off: stderr %q code %d puts %d", k, stderr, code, puts)
		}
	}
	// A task found beats an unreadable reading: D29, never D33.
	d := freeBgDeps(t)
	d.Pending = func(string, time.Time) ([]string, error) { return []string{"a1"}, nil }
	d.Screen = func() (string, error) { return "", errors.New("cmux down") }
	if stderr, code, _ := standbyRun(t, s, nil, false, false); code != 3 || stderr != fmt.Sprintf(d29, "tâche a1") {
		t.Fatalf("task + unreadable: stderr %q code %d", stderr, code)
	}
}

// --check reads and refuses like the command but never PUTs; --clear reads
// nothing and DELETEs.
func TestStandbyCheckAndClear(t *testing.T) {
	s := startStandbyServer(t)
	freeBgDeps(t)
	if stderr, code, puts := standbyRun(t, s, nil, false, true); code != 0 || puts != 0 || stderr != "" {
		t.Fatalf("--check free: %q %d %d", stderr, code, puts)
	}
	d := freeBgDeps(t)
	d.Pending = func(string, time.Time) ([]string, error) { return []string{"a1"}, nil }
	if _, code, puts := standbyRun(t, s, nil, false, true); code != 3 || puts != 0 {
		t.Fatalf("--check busy: %d %d", code, puts)
	}
	d = freeBgDeps(t)
	d.Screen = func() (string, error) { return "", errors.New("x") }
	if _, code, puts := standbyRun(t, s, nil, false, true); code != 4 || puts != 0 {
		t.Fatalf("--check unreadable: %d %d", code, puts)
	}
	d = freeBgDeps(t)
	d.Caller = func() (int, bool, error) { t.Error("--clear read the background"); return 0, false, nil }
	before := s.count("DELETE /peers/s-bg/standby")
	if _, code, _ := standbyRun(t, s, nil, true, false); code != 0 || s.count("DELETE /peers/s-bg/standby") != before+1 {
		t.Fatalf("--clear: code %d calls %v", code, s.calls())
	}
}

// Through cobra: the refusal is the only stderr line (no "Error:", no usage)
// and its code reaches exitCode; every other error is printed once. The test
// fixes its own environment; the package's scope table is the built-in one.
func TestStandbyRefusalThroughCobra(t *testing.T) {
	s := startStandbyServer(t)
	sock := flagSocket
	t.Setenv("CLAUDE_CODE_SESSION_ID", "s-x")
	t.Setenv("PIGTAIL_WAIT", "")
	t.Setenv("PIGTAIL_RELAY", "")
	// The Standby reads the tab's pin (D31): never the machine's real cmux.
	t.Setenv("CMUX_WORKSPACE_ID", "")
	run := func(args ...string) (string, string, int) {
		t.Helper()
		root := newRootCmd()
		var out, errOut bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errOut)
		root.SetArgs(args)
		err := root.Execute()
		return out.String(), errOut.String(), exitCode(err)
	}
	t.Cleanup(func() { flagSocket = sock })
	d := freeBgDeps(t)
	d.Pending = func(string, time.Time) ([]string, error) { return []string{"a1"}, nil }
	if out, stderr, code := run("--socket", sock, "session", "standby"); stderr != fmt.Sprintf(d29, "tâche a1") || out != "" || code != 3 {
		t.Fatalf("D29: out %q stderr %q code %d", out, stderr, code)
	}
	d = freeBgDeps(t)
	d.Screen = func() (string, error) { return "", errors.New("x") }
	if out, stderr, code := run("--socket", sock, "session", "standby"); stderr != fmt.Sprintf(d33, "écran de la session illisible") || out != "" || code != 4 {
		t.Fatalf("D33: out %q stderr %q code %d", out, stderr, code)
	}
	if s.count("PUT /peers/s-x/standby") != 0 {
		t.Fatalf("a refusal PUT: %v", s.calls())
	}
	// Any other error: printed once, no usage, code 1.
	freeBgDeps(t)
	dead := filepath.Join(t.TempDir(), "dead.sock")
	_, stderr, code := run("--socket", dead, "session", "standby")
	if code != 1 || strings.Count(stderr, "Error:") != 1 || strings.Contains(stderr, "Usage:") {
		t.Fatalf("dead socket: stderr %q code %d", stderr, code)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	_, stderr, code = run("--socket", sock, "session", "standby")
	if code != 1 || strings.Count(stderr, "Error:") != 1 || strings.Contains(stderr, "Usage:") {
		t.Fatalf("no session: stderr %q code %d", stderr, code)
	}
	// A flag error is still cobra's to print.
	if _, stderr, code := run("--socket", sock, "session", "standby", "--bogus"); code == 0 || !strings.Contains(stderr, "bogus") {
		t.Fatalf("--bogus: stderr %q code %d", stderr, code)
	}
}

// sidebarState is a sidebar loop stand-in that only answers State.
type sidebarState struct{ st pigtail.CmuxSidebarState }

func (f sidebarState) State() pigtail.CmuxSidebarState              { return f.st }
func (sidebarState) SetEnabled(bool)                                {}
func (sidebarState) Nudge()                                         {}
func (sidebarState) Released(pigtail.SubjectCmuxBinding)            {}
func (sidebarState) TrustPending(string, int)                       {}
func (sidebarState) RespawnMark(string, pigtail.CmuxRespawnRequest) {}
func (sidebarState) RespawnClear(string)                            {}

// Folder arrangement off: the success line says the tab stays where it is
// (exit 0); folders on, or no loop wired: the line is unchanged.
func TestSessionStandbyFoldersOffLine(t *testing.T) {
	const plain = "standby: set (the next prompt you type ends it)\n"
	for _, c := range []struct {
		name    string
		sidebar handlers.CmuxSidebarControl
		want    string
	}{
		{"folders off", sidebarState{pigtail.CmuxSidebarState{Enabled: true}},
			"standby: set (the next prompt you type ends it) — folder arrangement is off (pigtaild --cmux-folders=false): the tab stays where it is\n"},
		{"sidebar off", sidebarState{pigtail.CmuxSidebarState{}},
			"standby: set (the next prompt you type ends it) — folder arrangement is off (cmux sidebar off): the tab stays where it is\n"},
		{"folders on", sidebarState{pigtail.CmuxSidebarState{Enabled: true, Folders: true}}, plain},
		{"no loop", nil, plain},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := startStandbyServerWith(t, c.sidebar)
			freeBgDeps(t)
			env := envOf(map[string]string{"CLAUDE_CODE_SESSION_ID": "s-fo"})
			var out, errOut bytes.Buffer
			if err := runSessionStandby(context.Background(), &out, &errOut, env, false, false); err != nil {
				t.Fatalf("err %v (stderr %q)", err, errOut.String())
			}
			if out.String() != c.want {
				t.Fatalf("stdout = %q, want %q", out.String(), c.want)
			}
			if errOut.Len() != 0 {
				t.Fatalf("stderr = %q", errOut.String())
			}
			if sb, err := s.st.GetStandby("s-fo"); err != nil || sb == nil {
				t.Fatalf("row = %+v, %v", sb, err)
			}
		})
	}
}
