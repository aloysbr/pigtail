package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// `pigtail staffing` (sub_x000000000000004, Task 8): the scheduler's state,
// its decision log, mode and ceiling — launch refused from a Claude session.

// cliStaffing is a handlers.StaffingService answering from memory.
type cliStaffing struct {
	mu      sync.Mutex
	state   pigtail.StaffingState
	ds      []pigtail.StaffingDecision
	updates []pigtail.StaffingSettings
}

func (f *cliStaffing) State() (pigtail.StaffingState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, nil
}

func (f *cliStaffing) Update(in pigtail.StaffingSettings) (pigtail.StaffingState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, in)
	if in.Mode != "" {
		f.state.Mode = in.Mode
	}
	if in.Ceiling != nil {
		f.state.Ceiling = *in.Ceiling
	}
	return f.state, nil
}

func (f *cliStaffing) Decisions(time.Time, int) ([]pigtail.StaffingDecision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ds, nil
}

func (f *cliStaffing) updated() []pigtail.StaffingSettings {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pigtail.StaffingSettings(nil), f.updates...)
}

// staffingDaemon serves the real /staffing handlers behind the real scopeMW.
func staffingDaemon(t *testing.T, f *cliStaffing) *stubDaemon {
	t.Helper()
	s, _ := realSubjectDaemonSvc(t, func(svc *handlers.Services) { svc.Staffing = f }, nil)
	return s
}

// noClaude: no Claude session above this process, no session header.
func noClaude(t *testing.T) {
	t.Helper()
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	saved := spawnCallerClaude
	t.Cleanup(func() { spawnCallerClaude = saved })
	spawnCallerClaude = func() (claudesession.Proc, bool, error) { return claudesession.Proc{}, false, nil }
}

func underClaude(t *testing.T) {
	t.Helper()
	saved := spawnCallerClaude
	t.Cleanup(func() { spawnCallerClaude = saved })
	spawnCallerClaude = func() (claudesession.Proc, bool, error) { return claudesession.Proc{PID: 4242}, true, nil }
}

// runStaffing runs the real root command `pigtail <args>` against s,
// returning stdout, stderr and the error.
func runStaffing(t *testing.T, s *stubDaemon, args ...string) (string, string, error) {
	t.Helper()
	testutil.SerialOnly(t) // the root's flags are package globals
	prevHTTP, prevSocket, prevScope := flagHTTP, flagSocket, flagScope
	t.Cleanup(func() { flagHTTP, flagSocket, flagScope = prevHTTP, prevSocket, prevScope })
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(append([]string{"--http", strings.TrimPrefix(s.srv.URL, "http://")}, args...))
	err := root.Execute()
	return out.String(), errb.String(), err
}

func TestStaffingDeclaresScopedAccess(t *testing.T) {
	root := newRootCmd()
	for _, tc := range []struct {
		path  []string
		want  string
		cross bool
	}{
		{[]string{"staffing"}, "read", false},
		{[]string{"staffing", "log"}, "read", false},
		{[]string{"staffing", "mode"}, "write", true},
		{[]string{"staffing", "ceiling"}, "write", true},
	} {
		c, _, err := root.Find(tc.path)
		if err != nil || c.Name() != tc.path[len(tc.path)-1] {
			t.Fatalf("%v: not found (%v)", tc.path, err)
		}
		if got := c.Annotations[scopeAccessKey]; got != tc.want {
			t.Errorf("%v: access %q, want %q", tc.path, got, tc.want)
		}
		if has := c.Flags().Lookup("cross-scope") != nil; has != tc.cross {
			t.Errorf("%v: --cross-scope %v, want %v", tc.path, has, tc.cross)
		}
		if c.Flags().Lookup("json") == nil {
			t.Errorf("%v: no --json", tc.path)
		}
	}
}

// bareTerminal: a cell table that does not hold the caller's config dir.
func bareTerminal(t *testing.T) {
	t.Helper()
	withCells(t, func(_, _ string) string { return t.TempDir() })
	noClaude(t)
}

func TestStaffingLaunchPathFromBareTerminal(t *testing.T) {
	bareTerminal(t)
	f := &cliStaffing{state: pigtail.StaffingState{Mode: pigtail.StaffingAdvise, Ceiling: 3}}
	s := staffingDaemon(t, f)
	master := string(scope.Master())

	if _, _, err := runStaffing(t, s, "staffing", "mode", "launch", "--cross-scope"); err == nil ||
		!strings.Contains(err.Error(), "only the master cell") {
		t.Fatalf("bare terminal, no --scope: %v, want a 403", err)
	}
	if _, _, err := runStaffing(t, s, "--scope", master, "staffing", "mode", "launch"); err == nil {
		t.Fatal("--scope master without --cross-scope: accepted")
	}
	if len(f.updated()) != 0 {
		t.Fatalf("refused calls changed the settings: %+v", f.updated())
	}
	out, _, err := runStaffing(t, s, "--scope", master, "staffing", "mode", "launch", "--cross-scope")
	if err != nil || f.state.Mode != pigtail.StaffingLaunch || !strings.HasPrefix(out, "staffing: launch") {
		t.Fatalf("--scope master --cross-scope: %v, mode %s, out %q", err, f.state.Mode, out)
	}
}

// The refusal from a Claude session cites a command; run from a bare
// terminal, that exact command turns launch on.
func TestStaffingRefusalCommandWorks(t *testing.T) {
	bareTerminal(t)
	f := &cliStaffing{state: pigtail.StaffingState{Mode: pigtail.StaffingAdvise, Ceiling: 3}}
	s := staffingDaemon(t, f)
	underClaude(t)
	_, _, err := runStaffing(t, s, "staffing", "mode", "launch")
	if err == nil {
		t.Fatal("mode launch from a Claude session: accepted")
	}
	_, cited, ok := strings.Cut(err.Error(), "outside any Claude session: ")
	if !ok || !strings.HasPrefix(cited, "pigtail ") {
		t.Fatalf("refusal cites no command: %v", err)
	}
	noClaude(t)
	if _, _, err := runStaffing(t, s, strings.Fields(strings.TrimPrefix(cited, "pigtail "))...); err != nil {
		t.Fatalf("the cited command %q: %v", cited, err)
	}
	if f.state.Mode != pigtail.StaffingLaunch {
		t.Fatalf("the cited command left mode %s", f.state.Mode)
	}
}

func TestStaffingModeLaunchRefusedFromClaude(t *testing.T) {
	bareTerminal(t)
	f := &cliStaffing{}
	s := staffingDaemon(t, f)
	underClaude(t)
	_, _, err := runStaffing(t, s, "--scope", string(scope.Master()), "staffing", "mode", "launch", "--cross-scope")
	want := "staffing mode launch: only the user turns launch on — run it from a terminal outside any Claude session: pigtail --scope " +
		string(scope.Master()) + " staffing mode launch --cross-scope"
	if err == nil || err.Error() != want || exitCode(err) != 1 {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if n := len(subjectCallsAll(s)); n != 0 {
		t.Fatalf("the daemon was called: %v", subjectCallsAll(s))
	}
	// off and advise pass from anywhere.
	for _, m := range []string{"off", "advise"} {
		if _, _, err := runStaffing(t, s, "--scope", string(scope.Master()), "staffing", "mode", m, "--cross-scope"); err != nil {
			t.Fatalf("mode %s from a Claude session: %v", m, err)
		}
	}
}

// subjectCallsAll is every request s served.
func subjectCallsAll(s *stubDaemon) []string {
	s.pathsMu.Lock()
	defer s.pathsMu.Unlock()
	return append([]string(nil), s.paths...)
}

// recordingDaemon answers /staffing from a fixed state and records bodies.
type recordingDaemon struct {
	*stubDaemon
	mu     sync.Mutex
	bodies []string
	state  pigtail.StaffingState
	ds     []pigtail.StaffingDecision
}

func newRecordingDaemon(t *testing.T, st pigtail.StaffingState, ds []pigtail.StaffingDecision) *recordingDaemon {
	t.Helper()
	d := &recordingDaemon{state: st, ds: ds}
	mux := http.NewServeMux()
	mux.HandleFunc("/staffing", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			d.mu.Lock()
			d.bodies = append(d.bodies, strings.TrimSpace(string(b)))
			d.mu.Unlock()
		}
		writeData(w, http.StatusOK, d.state)
	})
	mux.HandleFunc("/staffing/decisions", func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, d.ds)
	})
	d.stubDaemon = &stubDaemon{srv: httptest.NewServer(mux)}
	t.Cleanup(d.srv.Close)
	return d
}

func (d *recordingDaemon) posted() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.bodies...)
}

func TestStaffingModeLaunchFromTerminal(t *testing.T) {
	bareTerminal(t)
	d := newRecordingDaemon(t, pigtail.StaffingState{Mode: pigtail.StaffingLaunch, Ceiling: 3}, nil)
	if _, _, err := runStaffing(t, d.stubDaemon, "--scope", string(scope.Master()), "staffing", "mode", "launch", "--cross-scope"); err != nil {
		t.Fatal(err)
	}
	if got := d.posted(); len(got) != 1 || got[0] != `{"mode":"launch"}` {
		t.Fatalf("posted %v", got)
	}
}

func TestStaffingCeilingSendsInt(t *testing.T) {
	bareTerminal(t)
	d := newRecordingDaemon(t, pigtail.StaffingState{Mode: pigtail.StaffingAdvise, Ceiling: 2}, nil)
	if _, _, err := runStaffing(t, d.stubDaemon, "--scope", string(scope.Master()), "staffing", "ceiling", "2", "--cross-scope"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runStaffing(t, d.stubDaemon, "--scope", string(scope.Master()), "staffing", "ceiling", "x", "--cross-scope"); err == nil {
		t.Fatal("ceiling x: accepted")
	}
	if got := d.posted(); len(got) != 1 || got[0] != `{"ceiling":2}` {
		t.Fatalf("posted %v", got)
	}
}

func staffingAt(hh, mm int) time.Time { return time.Date(2026, 10, 31, hh, mm, 0, 0, time.Local) }

func TestPrintStaffingStates(t *testing.T) {
	at := staffingAt(10, 2)
	long := strings.Repeat("x", 70)
	for _, tc := range []struct {
		name string
		st   pigtail.StaffingState
		want []string
		json []string
	}{
		{name: "off", st: pigtail.StaffingState{Mode: pigtail.StaffingOff, Ceiling: 3, Launcher: pigtail.StaffingLauncherPilot},
			want: []string{"staffing: off — nothing is staffed (pigtail staffing mode advise|launch)"},
			json: []string{`"mode": "off"`, `"launcher": "pilot"`}},
		{name: "empty queue", st: pigtail.StaffingState{Mode: pigtail.StaffingAdvise, Ceiling: 3, Launcher: pigtail.StaffingLauncherPilot,
			Queues: map[string][]pigtail.StaffingCandidate{}},
			want: []string{"staffing: advise · 0/3 places · launcher: pilot", "places: (none)", "(no ready subject)"},
			json: []string{`"queues": {}`}},
		{name: "places full", st: pigtail.StaffingState{Mode: pigtail.StaffingLaunch, Ceiling: 3, PlacesUsed: 3, Launcher: pigtail.StaffingLauncherScheduler,
			Places: []pigtail.StaffingPlace{
				{SessionID: "s1", SubjectID: "sub_1", ShortTitle: "One", Label: "one", Wait: "review"},
				{SessionID: "s2", SubjectID: "sub_2", ShortTitle: "Two", Label: "two"},
				{SubjectID: "sub_3", ShortTitle: long, LaunchID: "ln_3", Pending: true},
			}},
			want: []string{"staffing: launch · 3/3 places · launcher: scheduler", "no free place (3/3)",
				"one: sub_1 One · waits for the user (review)", "two: sub_2 Two · working", "sub_3 " + long[:59] + "… · launch pending"},
			json: []string{`"places_used": 3`, `"short_title": "` + long + `"`, `"pending": true`}},
		{name: "over margin", st: pigtail.StaffingState{Mode: pigtail.StaffingAdvise, Ceiling: 3, Launcher: pigtail.StaffingLauncherPilot,
			Queues: map[string][]pigtail.StaffingCandidate{"perso": {{SubjectID: "sub_m", ShortTitle: "Margin", Scope: "perso", Rank: 1, Skip: pigtail.StaffingSkipOverMargin}}},
			Last:   &pigtail.StaffingDecision{TS: at, LastAt: at, Outcome: pigtail.StaffingOutcomeIdle, Gate: pigtail.StaffingGateOverMargin}},
			want: []string{"ready queue — perso:", "1. sub_m Margin — accounts over margin", "last decision (10:02): idle - over_margin"},
			json: []string{`"skip": "over_margin"`, `"gate": "over_margin"`}},
		{name: "advise", st: pigtail.StaffingState{Mode: pigtail.StaffingAdvise, Ceiling: 3, Launcher: pigtail.StaffingLauncherPilot,
			Queues: map[string][]pigtail.StaffingCandidate{"perso": {
				{SubjectID: "sub_a", ShortTitle: "Ready", Scope: "perso", Rank: 1},
				{SubjectID: "sub_b", ShortTitle: "Blocked", Scope: "perso", Rank: 2, Skip: pigtail.StaffingSkipDeps, Detail: "sub_a"},
				{SubjectID: "sub_c", ShortTitle: "Later", Scope: "perso", Rank: 3, Skip: pigtail.StaffingSkipCooldown, Detail: "11:15"},
				{SubjectID: "sub_d", ShortTitle: "Asks", Scope: "perso", Rank: 4, Skip: pigtail.StaffingSkipWaitsHuman, Detail: "review"},
			}},
			Last: &pigtail.StaffingDecision{TS: at, LastAt: at, Outcome: pigtail.StaffingOutcomeWouldLaunch, SubjectID: "sub_a",
				ShortTitle: "Ready", Reason: "would launch sub_a (Ready)"}},
			want: []string{"1. sub_a Ready — ready", "2. sub_b Blocked — blocked by sub_a", "3. sub_c Later — retry after 11:15",
				"4. sub_d Asks — waits for the user (review)", "last decision (10:02): would launch sub_a"},
			json: []string{`"outcome": "would_launch"`}},
		{name: "settings error", st: pigtail.StaffingState{Mode: pigtail.StaffingAdvise, Ceiling: 3, Launcher: pigtail.StaffingLauncherPilot,
			SettingsError: "staffing.json: unexpected end of JSON input"},
			want: []string{"settings error: staffing.json: unexpected end of JSON input"},
			json: []string{`"settings_error": "staffing.json: unexpected end of JSON input"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			printStaffingState(&b, tc.st)
			text := b.String()
			for _, w := range tc.want {
				if !strings.Contains(text, w) {
					t.Errorf("text lacks %q:\n%s", w, text)
				}
			}
			if strings.Contains(text, "\x1b") {
				t.Errorf("colour in text:\n%s", text)
			}
			b.Reset()
			if err := printJSON(&b, tc.st); err != nil {
				t.Fatal(err)
			}
			var obj map[string]any
			if err := json.Unmarshal(b.Bytes(), &obj); err != nil {
				t.Fatalf("--json is not one object: %v\n%s", err, b.String())
			}
			for _, w := range tc.json {
				if !strings.Contains(b.String(), w) {
					t.Errorf("--json lacks %s:\n%s", w, b.String())
				}
			}
		})
	}
}

func TestStaffingJSONHasLauncher(t *testing.T) {
	bareTerminal(t)
	for mode, launcher := range map[pigtail.StaffingMode]string{
		pigtail.StaffingLaunch: pigtail.StaffingLauncherScheduler, pigtail.StaffingAdvise: pigtail.StaffingLauncherPilot} {
		d := newRecordingDaemon(t, pigtail.StaffingState{Mode: mode, Ceiling: 3, Launcher: launcher}, nil)
		out, _, err := runStaffing(t, d.stubDaemon, "staffing", "--json")
		if err != nil || !strings.Contains(out, `"launcher": "`+launcher+`"`) {
			t.Fatalf("%s: %v %s", mode, err, out)
		}
	}
}

func TestStaffingLogPrintsWouldLaunch(t *testing.T) {
	bareTerminal(t)
	at := staffingAt(10, 2)
	d := newRecordingDaemon(t, pigtail.StaffingState{}, []pigtail.StaffingDecision{
		{TS: at, LastAt: at, Outcome: pigtail.StaffingOutcomeWouldLaunch, SubjectID: "sub_a", Reason: "would launch sub_a (Ready)"},
		{TS: at, LastAt: at, Outcome: pigtail.StaffingOutcomeIdle, Gate: pigtail.StaffingGateNoFreePlace},
	})
	out, _, err := runStaffing(t, d.stubDaemon, "staffing", "log")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"10:02:00 would launch sub_a - would launch sub_a (Ready)", "10:02:00 idle - no_free_place"} {
		if !strings.Contains(out, w) {
			t.Errorf("log lacks %q:\n%s", w, out)
		}
	}
	out, _, err = runStaffing(t, d.stubDaemon, "staffing", "log", "--json")
	var ds []pigtail.StaffingDecision
	if err != nil || json.Unmarshal([]byte(out), &ds) != nil || len(ds) != 2 {
		t.Fatalf("log --json: %v %s", err, out)
	}
}

func TestStaffingLogPrintsRepeatsAndObserved(t *testing.T) {
	bareTerminal(t)
	at := staffingAt(10, 2)
	d := newRecordingDaemon(t, pigtail.StaffingState{}, []pigtail.StaffingDecision{
		{TS: at, LastAt: staffingAt(11, 30), Repeats: 4, Outcome: pigtail.StaffingOutcomeIdle, Gate: pigtail.StaffingGateSessionBudgetOff},
		{TS: at, LastAt: at, Outcome: pigtail.StaffingOutcomeObservedLaunch, SubjectID: "sub_o", Origin: "api", Reason: "pending"},
	})
	out, _, err := runStaffing(t, d.stubDaemon, "staffing", "log")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"idle - session_budget_off (×5 until 11:30)", "observed launch (api) sub_o - pending"} {
		if !strings.Contains(out, w) {
			t.Errorf("log lacks %q:\n%s", w, out)
		}
	}
}

func TestStaffingDaemonUnreachableExitsNonZero(t *testing.T) {
	bareTerminal(t)
	d := newRecordingDaemon(t, pigtail.StaffingState{}, nil)
	d.srv.Close()
	out, _, err := runStaffing(t, d.stubDaemon, "staffing")
	if err == nil || exitCode(err) == 0 || out != "" || strings.Contains(err.Error(), "\n") {
		t.Fatalf("unreachable: err %v, exit %d, stdout %q", err, exitCode(err), out)
	}
}

func TestStaffingSummaryShowsOverBudget(t *testing.T) {
	bareTerminal(t)
	const why = "every account of scope perso is out of its weekly token budget; first release at Mon 15:04"
	at := staffingAt(10, 2)
	d := newRecordingDaemon(t, pigtail.StaffingState{}, []pigtail.StaffingDecision{
		{TS: at, LastAt: at, Outcome: pigtail.StaffingOutcomeIdle, Gate: pigtail.StaffingGateOverBudget, Reason: why},
	})
	out, _, err := runStaffing(t, d.stubDaemon, "staffing", "log")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"idle - over_budget", why} {
		if !strings.Contains(out, w) {
			t.Errorf("log lacks %q:\n%s", w, out)
		}
	}
}

func TestStaffingSkipTextOverBudget(t *testing.T) {
	for detail, want := range map[string]string{
		"every account of scope perso…":      "accounts over budget",
		"unmeasured: reading older than 30m": "accounts not measured",
		"":                                   "accounts over budget or not measured", // masked for a non-master caller
	} {
		c := pigtail.StaffingCandidate{Skip: pigtail.StaffingSkipOverBudget, Detail: detail}
		if got := staffingSkipText(c); got != want {
			t.Errorf("detail %q: got %q, want %q", detail, got, want)
		}
	}
}
