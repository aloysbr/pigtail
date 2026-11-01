package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/cmuxcheck"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// runCheckCmd runs the command outside any cell, never on the cell table
// of the machine running the tests (cells_helper_test.go).
func runCheckCmd(t *testing.T, args ...string) (cmuxcheck.Options, string, error) {
	t.Helper()
	withCellScopes(t, map[string]string{"m": "perso"}, "")
	return runCheckCmdHere(t, args...)
}

// runCheckCmdHere runs it on the cell table the test set up.
func runCheckCmdHere(t *testing.T, args ...string) (cmuxcheck.Options, string, error) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CMUX_SURFACE_ID", "SELF")
	var got cmuxcheck.Options
	stubCheckSeams(t)
	ccRun = func(o cmuxcheck.Options) cmuxcheck.Report { got = o; return cmuxcheck.Report{Fix: o.Fix} }
	cmd := cmuxCheckCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return got, out.String(), err
}

// stubCheckSeams saves the command's seams, restores them at cleanup, and
// stubs the ones that would read this machine's pigtaild (machine info, the
// maintenance owner, D4); the test sets ccRun.
func stubCheckSeams(t *testing.T) {
	t.Helper()
	testutil.SerialOnly(t) // swaps ccRun
	oldRun, oldInfo, oldOwner := ccRun, ccInfo, ccMaintenanceOwner
	t.Cleanup(func() { ccRun, ccInfo, ccMaintenanceOwner = oldRun, oldInfo, oldOwner })
	ccInfo = func(*pigtail.Client, string) ([]string, []string) { return nil, nil }
	ccMaintenanceOwner = func(*pigtail.Client) string { return "" }
}

// The peers read is scoped (repo rule): the command declares scopedRead()
// and calls newScopedClient — TestScopedCommandsDeclareAccess counts it.
func TestCheckDeclaresScopedRead(t *testing.T) {
	t.Parallel()
	if got := cmuxCheckCmd().Annotations[scopeAccessKey]; got != "read" {
		t.Fatalf("annotation = %q, want read", got)
	}
}

// D7: every scope from the master cell or outside any cell with --scope all
// (pigtaild's run); a non-master cell or the bare default is partial, and
// Options.PeersScope names its scope so the report says so.
func TestCheckPeersScope(t *testing.T) {
	cells := map[string]string{"s": "globex", "m": "perso"}
	withCellScopes(t, cells, "m")
	if o, _, err := runCheckCmdHere(t); err != nil || o.PeersScope != "" {
		t.Fatalf("master cell: PeersScope %q, %v; want every scope", o.PeersScope, err)
	}
	withCellScopes(t, cells, "s")
	if o, _, _ := runCheckCmdHere(t); o.PeersScope == "" || scope.SeesAll(scope.Scope(o.PeersScope)) {
		t.Fatalf("non-master cell: PeersScope %q, want its own scope", o.PeersScope)
	}
	withCellScopes(t, cells, "")
	if o, _, _ := runCheckCmdHere(t); o.PeersScope != string(scope.Default()) {
		t.Fatalf("outside any cell, no --scope: PeersScope %q, want the default %q", o.PeersScope, scope.Default())
	}
	t.Setenv(envScope, "all") // = pigtaild's `--scope all`, outside any cell
	if o, _, err := runCheckCmdHere(t); err != nil || o.PeersScope != "" {
		t.Fatalf("outside any cell with all: PeersScope %q, %v; want every scope", o.PeersScope, err)
	}
}

func TestCheckDefaultsToInventory(t *testing.T) {
	o, _, _ := runCheckCmd(t)
	if o.Fix || o.Self != "SELF" {
		t.Fatalf("options = %+v", o)
	}
}

func TestCheckFixFalseWins(t *testing.T) {
	o, _, _ := runCheckCmd(t, "--fix", "--fix=false")
	if o.Fix {
		t.Fatal("--fix=false after --fix must stay an inventory (journal correction on repeated flags)")
	}
}

func TestCheckSurfacesAndLast(t *testing.T) {
	o, _, _ := runCheckCmd(t, "--surface", "S1", "--surface", "S2")
	if strings.Join(o.Surfaces, ",") != "S1,S2" {
		t.Fatalf("surfaces = %v", o.Surfaces)
	}
	_, out, _ := runCheckCmd(t, "--last")
	if !strings.Contains(out, "no previous check") {
		t.Fatalf("--last with nothing saved: %q", out)
	}
}

// D13 (É17): pigtail posts no cmux notification — the defect shows as the
// pigtail-chk pill (Task 9), read from last.json. The flag is gone: an old
// `--notify` in a script fails loudly instead of being silently ignored.
func TestCheckHasNoNotifyFlag(t *testing.T) {
	if f := cmuxCheckCmd().Flags().Lookup("notify"); f != nil {
		t.Fatalf("--notify still registered (%q): D13, no notification", f.Usage)
	}
	if _, _, err := runCheckCmd(t, "--notify"); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("--notify accepted (%v); want cobra's unknown flag", err)
	}
}

// É25 (B2): --surface makes the pass partial and last.json says so — SaveLast
// then keeps the other surfaces' rows, and their pills (Task 9).
func TestCheckPartialPassMarked(t *testing.T) {
	runCheckCmd(t, "--surface", "S1")
	if r, err := cmuxcheck.LoadLast(cmuxcheck.StateDir()); err != nil || !r.Partial {
		t.Fatalf("--surface S1: last.json %+v, %v; want partial", r, err)
	}
	runCheckCmd(t)
	if r, err := cmuxcheck.LoadLast(cmuxcheck.StateDir()); err != nil || r.Partial {
		t.Fatalf("full pass: last.json %+v, %v; want not partial", r, err)
	}
}

// runCheckCmdWith runs the command on the cell table and state dir the test
// set up, ccRun returning rep.
func runCheckCmdWith(t *testing.T, rep cmuxcheck.Report, args ...string) string {
	t.Helper()
	testutil.SerialOnly(t) // swaps ccRun
	stubCheckSeams(t)
	ccRun = func(cmuxcheck.Options) cmuxcheck.Report { return rep }
	cmd := cmuxCheckCmd()
	var out bytes.Buffer
	var errOut bytes.Buffer // cobra writes "Error: cmux check: …" here (exit 1): not in the decoded stdout
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	_ = cmd.Execute()
	return out.String() // stdout only
}

// D23 (É28): the "exists on N accounts" line (machineInfo, written to
// last.json by a --scope all run) carries the session id and no title. Red if
// p.CmuxTitle is put back in the line.
func TestMultiAccountInfoNamesNoTitle(t *testing.T) {
	t.Parallel()
	alive := true
	ps := []pigtail.PeerSession{
		{SessionID: "sess-1", CmuxTitle: "other-info-title", Alive: &alive},
		{SessionID: "sess-2", CmuxTitle: "single-title", Alive: &alive},
	}
	find := func(id string, _ []string) int {
		if id == "sess-1" {
			return 2
		}
		return 1
	}
	info := multiAccountInfo(ps, []string{"/cfg/a", "/cfg/b"}, find)
	if len(info) != 1 || info[0] != "session sess-1 exists on 2 accounts" {
		t.Fatalf("info %q; want the id-only line for sess-1", info)
	}
	for _, line := range info {
		if strings.Contains(line, "other-info-title") || strings.Contains(line, "single-title") {
			t.Errorf("line %q names a session title (D23)", line)
		}
	}
	if got := multiAccountInfo(ps, []string{"/cfg/a"}, find); len(got) != 0 {
		t.Errorf("one account only: %q; want none", got)
	}
}

// leakRow returns a row whose every exported field carries a sentinel
// (reflect), so a field added to Row later is covered without editing this
// test; a field of a kind it does not know fails the test until it gets one.
func leakRow(t *testing.T, tag string) cmuxcheck.Row {
	t.Helper()
	var r cmuxcheck.Row
	v := reflect.ValueOf(&r).Elem()
	for i := 0; i < v.NumField(); i++ {
		f, name := v.Field(i), v.Type().Field(i).Name
		if !v.Type().Field(i).IsExported() {
			continue
		}
		val := "leak-" + tag + "-" + name
		switch {
		case f.Kind() == reflect.String:
			f.SetString(val)
		case f.Kind() == reflect.Int:
			f.SetInt(424242)
		case f.Kind() == reflect.Bool:
			f.SetBool(true)
		case f.Kind() == reflect.Slice && f.Type().Elem().Kind() == reflect.String:
			sl := reflect.MakeSlice(f.Type(), 1, 1)
			sl.Index(0).SetString(val)
			f.Set(sl)
		default:
			t.Fatalf("Row.%s (%s): give it a sentinel in leakRow (D18)", name, f.Type())
		}
	}
	return r
}

// D18 (decided 2026-10-08, allow-list; É29, É30; D23 of the repo): run from
// a non-master cell, the command prints, of a row whose scope that cell
// cannot read (another scope's account, or an account no cell owns), its
// short surface id and its class only — no other field, whatever Row holds —
// in text, --json and --last alike; and the info/notes of a pass of another
// scope (the master's included) never, only its machine lines. last.json
// keeps everything (its reader, pigtaild, is master); the master cell sees it.
func TestCheckReportRedactsOtherScopes(t *testing.T) {
	scopetest.Three(t) // perso (master), globex, acme
	cells := map[string]string{"s": "globex", "a": "acme", "m": "perso"}
	dirs := withCellScopes(t, cells, "s")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CMUX_SURFACE_ID", "SELF")
	const ownID, ownTitle = "AAAAAAAA-0000-4000-8000-000000000001", "own-title"
	nocell := leakRow(t, "nocell") // no cell owns its account: scope unresolved
	nocell.SurfaceID = "BBBBBBBB-0000-4000-8000-000000000002"
	acme := leakRow(t, "acme") // another non-master scope's account
	acme.SurfaceID, acme.ConfigDir, acme.LiveConfigDir = "CCCCCCCC-0000-4000-8000-000000000003", dirs["a"], dirs["a"]
	// The pilot (D3–D5) is printed to the master only: Redacted never copies it.
	rep := cmuxcheck.Report{Launch: cmuxcheck.Launch{PID: 7}, Rows: []cmuxcheck.Row{
		{SurfaceID: ownID, Title: ownTitle, ConfigDir: dirs["s"]}, nocell, acme,
	}, Pilot: cmuxcheck.PilotInfo{SurfaceID: "DDDDDDDD-0000-4000-8000-000000000004", Source: "leak-pilot-source", Message: "leak-pilot-message"}}
	hidden := []string{"leak-", "424242", "-0000-4000-8000-000000000002", "-0000-4000-8000-000000000003", "DDDDDDDD", dirs["a"]}
	for _, args := range [][]string{nil, {"--json"}, {"--last"}} {
		out := runCheckCmdWith(t, rep, args...)
		for _, h := range hidden {
			if strings.Contains(out, h) {
				t.Errorf("%v: output shows %q, from a row this cell cannot read (D18):\n%s", args, h, out)
			}
		}
		// B1 (É30): Write never prints a config dir — required in --json only.
		shown := []string{ownTitle, "BBBBBBBB", "CCCCCCCC"}
		asJSON := len(args) == 1 && args[0] == "--json"
		if asJSON {
			shown = append(shown, dirs["s"])
		}
		for _, w := range shown {
			if !strings.Contains(out, w) {
				t.Errorf("%v: output lacks %q:\n%s", args, w, out)
			}
		}
		// The sentinels make both rows blocked: their class is "left".
		for _, line := range strings.Split(out, "\n") {
			if !asJSON && (strings.Contains(line, "BBBBBBBB") || strings.Contains(line, "CCCCCCCC")) && !strings.Contains(line, string(cmuxcheck.CatLeft)) {
				t.Errorf("%v: masked row line %q lacks its class", args, line)
			}
		}
		if asJSON {
			var got cmuxcheck.Report
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("--json: %v\n%s", err, out)
			}
			for _, row := range got.Rows[1:] {
				v := reflect.ValueOf(row)
				for i := 0; i < v.NumField(); i++ {
					name := v.Type().Field(i).Name
					if name != "SurfaceID" && name != "Masked" && !v.Field(i).IsZero() {
						t.Errorf("--json: masked row %s keeps %s = %v (D18: id and class only)", row.SurfaceID, name, v.Field(i))
					}
				}
				if row.Masked != cmuxcheck.CatLeft || len(row.SurfaceID) != 8 {
					t.Errorf("--json: masked row %+v; want the short id and class left", row)
				}
			}
		}
	}
	saved, err := cmuxcheck.LoadLast(cmuxcheck.StateDir())
	if err != nil || len(saved.Rows) != 3 || !reflect.DeepEqual(saved.Rows[1], nocell) || !reflect.DeepEqual(saved.Rows[2], acme) {
		t.Fatalf("last.json %+v, %v; want every field kept", saved.Rows, err)
	}

	// Info and notes follow the pass that wrote them (D18): read with --last.
	passes := []struct {
		pass string
		keep bool
	}{{"acme", false}, {"", false}, {"globex", true}} // "" = a master pass, every scope
	for _, c := range passes {
		r := cmuxcheck.Report{Launch: cmuxcheck.Launch{PID: 7}, PassScope: c.pass,
			Notes: []string{"leak-note-of-" + c.pass}, Info: []string{"leak-info-of-" + c.pass},
			Machine: []string{"7 dev servers running (pigtail servers)"}}
		if err := cmuxcheck.SaveLast(cmuxcheck.StateDir(), r); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"--last"}, {"--last", "--json"}} {
			out := runCheckCmdWith(t, rep, args...)
			for _, w := range []string{"leak-note-of-" + c.pass, "leak-info-of-" + c.pass} {
				if strings.Contains(out, w) != c.keep {
					t.Errorf("pass %q, %v: %q shown = %v, want %v:\n%s", c.pass, args, w, !c.keep, c.keep, out)
				}
			}
			if !strings.Contains(out, "7 dev servers running") {
				t.Errorf("pass %q, %v: the machine line is gone:\n%s", c.pass, args, out)
			}
			if !c.keep && strings.Contains(out, "acme") {
				t.Errorf("pass %q, %v: names a scope this cell cannot read (D19):\n%s", c.pass, args, out)
			}
		}
	}

	withCellScopes(t, cells, "m") // the master cell reads everything
	out := runCheckCmdWith(t, rep, "--json")
	for _, w := range []string{"leak-nocell-Problems", "leak-acme-Result", dirs["a"], "-0000-4000-8000-000000000003"} {
		if !strings.Contains(out, w) {
			t.Fatalf("master cell: %q masked:\n%s", w, out)
		}
	}
}

func TestCheckCtrlCFlag(t *testing.T) {
	o, _, err := runCheckCmd(t, "--ctrl-c", "unread")
	if err != nil {
		t.Fatal(err)
	}
	if o.Quit != cmuxcheck.QuitUnread {
		t.Fatalf("Quit = %q, want unread", o.Quit)
	}
	if _, _, err := runCheckCmd(t, "--ctrl-c", "always"); err == nil || exitCode(err) != 2 {
		t.Fatalf("--ctrl-c always: err %v, want exit 2", err)
	}
}

// T-f (revue 1 #11): the running file exists while a --fix pass runs, never
// during an inventory; Options.Heartbeat is set only for --fix.
func TestCheckRunningOnlyWhenFixing(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{{nil, false}, {[]string{"--fix"}, true}} {
		withCellScopes(t, map[string]string{"m": "perso"}, "")
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		t.Setenv("CMUX_SURFACE_ID", "SELF")
		oldRun, oldInfo, oldOwner := ccRun, ccInfo, ccMaintenanceOwner
		ccMaintenanceOwner = func(*pigtail.Client) string { return "" }
		var during bool
		var hb bool
		ccRun = func(o cmuxcheck.Options) cmuxcheck.Report {
			during = cmuxcheck.Running(cmuxcheck.StateDir(), func(int) bool { return true }, time.Now())
			hb = o.Heartbeat != nil
			return cmuxcheck.Report{Fix: o.Fix}
		}
		ccInfo = func(*pigtail.Client, string) ([]string, []string) { return nil, nil }
		cmd := cmuxCheckCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(c.args)
		_ = cmd.Execute()
		ccRun, ccInfo, ccMaintenanceOwner = oldRun, oldInfo, oldOwner
		if during != c.want || hb != c.want {
			t.Errorf("%v: running during the pass = %v, heartbeat %v; want %v", c.args, during, hb, c.want)
		}
		if cmuxcheck.Running(cmuxcheck.StateDir(), func(int) bool { return true }, time.Now()) {
			t.Errorf("%v: running file left after the command", c.args)
		}
	}
}

// D3: --fix-pilot = fix the pilot only; with --fix it is refused (exit 2).
// D8: --unattended reaches Options.
func TestCheckFixPilotFlag(t *testing.T) {
	got, _, err := runCheckCmd(t, "--fix-pilot", "--unattended")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Fix || !got.PilotOnly || !got.Unattended {
		t.Fatalf("options %+v, want Fix, PilotOnly, Unattended", got)
	}
	if _, _, err := runCheckCmd(t, "--fix", "--fix-pilot"); err == nil || exitCode(err) != 2 {
		t.Fatalf("--fix --fix-pilot: %v, want exit 2", err)
	}
}

// D4 (second source): the maintenance owner reaches Options.
func TestCheckPassesMaintenanceOwner(t *testing.T) {
	withCellScopes(t, map[string]string{"m": "perso"}, "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stubCheckSeams(t)
	ccMaintenanceOwner = func(*pigtail.Client) string { return "sid-owner" }
	var got cmuxcheck.Options
	ccRun = func(o cmuxcheck.Options) cmuxcheck.Report { got = o; return cmuxcheck.Report{} }
	cmd := cmuxCheckCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--fix-pilot"})
	_ = cmd.Execute()
	if got.MaintenanceOwner != "sid-owner" {
		t.Fatalf("MaintenanceOwner = %q", got.MaintenanceOwner)
	}
}

// D4: the command loads the marker into Options.Marker and, when the pass
// took its pilot from it, records the launch that consumed it.
func TestCheckMarksPilotUsed(t *testing.T) {
	withCellScopes(t, map[string]string{"m": "perso"}, "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CMUX_SURFACE_ID", "SELF")
	if err := cmuxcheck.SavePilot(cmuxcheck.StateDir(), cmuxcheck.PilotMarker{SurfaceID: "S-A", SessionID: "sid-a", CmuxPID: 10, At: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	stubCheckSeams(t)
	var got cmuxcheck.Options
	ccRun = func(o cmuxcheck.Options) cmuxcheck.Report {
		got = o
		return cmuxcheck.Report{Launch: cmuxcheck.Launch{PID: 20}, Pilot: cmuxcheck.PilotInfo{SurfaceID: "S-A", Source: cmuxcheck.PilotFromMarker}}
	}
	cmd := cmuxCheckCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--fix-pilot"})
	_ = cmd.Execute()
	if got.Marker == nil || got.Marker.SurfaceID != "S-A" {
		t.Fatalf("Options.Marker = %+v, want the marker", got.Marker)
	}
	if m, err := cmuxcheck.LoadPilot(cmuxcheck.StateDir()); err != nil || m.UsedBy != 20 {
		t.Fatalf("marker after the pass: %+v, %v; want used by launch 20", m, err)
	}
}

// Revue PR B I2: the "message typed" state lives in pilot-msg.json, written
// by the pass that typed it and read by every later pass — a fatal pass, a
// pass that resolved no pilot (a non-master photo, maintenance off) rewrite
// last.json but never lose it.
func TestCheckPilotMessageStateSurvivesOtherPasses(t *testing.T) {
	withCellScopes(t, map[string]string{"m": "perso"}, "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CMUX_SURFACE_ID", "SELF")
	stubCheckSeams(t)
	start := time.Now().Add(-time.Minute)
	launch := cmuxcheck.Launch{PID: 20, Start: start}
	var got cmuxcheck.Options
	for _, rep := range []cmuxcheck.Report{
		{At: time.Now(), Fix: true, Launch: launch, Pilot: cmuxcheck.PilotInfo{SurfaceID: "S-A", Message: "typed", MessageTyped: true}},
		{At: time.Now(), Fatal: "cmux unreachable (socket): x"},
		{At: time.Now(), Launch: launch}, // no pilot resolved
		{At: time.Now(), Launch: launch},
	} {
		ccRun = func(o cmuxcheck.Options) cmuxcheck.Report { got = o; return rep }
		cmd := cmuxCheckCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"--fix-pilot"})
		_ = cmd.Execute()
	}
	if got.PilotMsg == nil || got.PilotMsg.LaunchPID != 20 || got.PilotMsg.SurfaceID != "S-A" {
		t.Fatalf("Options.PilotMsg = %+v, want the state the first pass wrote", got.PilotMsg)
	}
}

// D16: the check reads the hooks' Standby marker (same directory, a file
// read, no route): <dir>/s1 → in Standby; s2 → not; a directory that cannot
// be read → in Standby, with the error.
func TestCmuxCheckWiresStandbyMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s1"), []byte("standby\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PIGTAIL_STANDBY_DIR", dir)
	o, _, err := runCheckCmd(t, "--fix")
	if err != nil || o.InStandby == nil {
		t.Fatalf("InStandby not wired: %v", err)
	}
	if in, err := o.InStandby("s1"); !in || err != nil {
		t.Fatalf(`InStandby("s1") = %v, %v`, in, err)
	}
	if in, err := o.InStandby("s2"); in || err != nil {
		t.Fatalf(`InStandby("s2") = %v, %v`, in, err)
	}
	file := filepath.Join(dir, "s1")
	t.Setenv("PIGTAIL_STANDBY_DIR", filepath.Join(file, "standby"))
	o, _, _ = runCheckCmd(t, "--fix")
	if in, err := o.InStandby("s3"); !in || err == nil {
		t.Fatalf("unreadable marker dir: InStandby = %v, %v; want true with the error", in, err)
	}
}

// D1 (sub_x000000000000037): the check reads cmux's resume setting; off sets
// CmuxResumeOff, a missing key keeps today's behaviour, an unreadable file
// keeps it too and says so (never "off").
func TestCmuxCheckResumeOffWiring(t *testing.T) {
	oldDefaults := cmux.ReadDefaults
	cmux.ReadDefaults = func(string) (string, bool, error) { return "", false, nil }
	t.Cleanup(func() { cmux.ReadDefaults = oldDefaults })
	for _, c := range []struct {
		name, body string
		off, note  bool
	}{
		{"off", `{"terminal":{"autoResumeAgentSessions":false}}`, true, false},
		{"absent key", `{}`, false, false},
		{"unreadable", `{ not json`, false, true},
	} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		dir := filepath.Join(home, ".config", "cmux")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		var o cmuxcheck.Options
		note := ccResumeSetting(&o)
		if o.CmuxResumeOff != c.off || (note != "") != c.note {
			t.Errorf("%s: CmuxResumeOff=%v note=%q, want off=%v note=%v", c.name, o.CmuxResumeOff, note, c.off, c.note)
		}
	}
}

// Review round 1: the command itself (RunE) must call ccResumeSetting —
// dropping that call leaves CmuxResumeOff false and the note unprinted.
func TestCmuxCheckRunEAppliesResumeSetting(t *testing.T) {
	oldDefaults := cmux.ReadDefaults
	cmux.ReadDefaults = func(string) (string, bool, error) { return "", false, nil }
	t.Cleanup(func() { cmux.ReadDefaults = oldDefaults })
	for _, c := range []struct {
		name, body string
		off, note  bool
	}{
		{"off", `{"terminal":{"autoResumeAgentSessions":false}}`, true, false},
		{"unreadable", `{ not json`, false, true},
	} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		dir := filepath.Join(home, ".config", "cmux")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		got, out, err := runCheckCmd(t)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.CmuxResumeOff != c.off {
			t.Errorf("%s: RunE gave CmuxResumeOff=%v, want %v", c.name, got.CmuxResumeOff, c.off)
		}
		if has := strings.Contains(out, "resume setting unreadable"); has != c.note {
			t.Errorf("%s: note printed=%v, want %v; output %q", c.name, has, c.note, out)
		}
	}
}
