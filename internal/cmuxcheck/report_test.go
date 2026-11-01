package cmuxcheck

import (
	"bytes"
	"strings"
	"testing"
)

// D15, D16 (É20): a remaining row is fixable (planned actions, nothing
// blocks, no repair tried), stuck (blocked, or its repair refused or failed),
// or reported only (no action: background work lost, D9).
func TestRowFixable(t *testing.T) {
	cases := []struct {
		name         string
		r            Row
		fixable, stk bool
	}{
		{"fixable", Row{Remaining: true, Problems: []string{"resumed by cmux"}, Actions: []Action{ActRelaunch}}, true, false},
		{"blocked", Row{Remaining: true, Problems: []string{"x"}, Actions: []Action{ActRelaunch}, Blocked: "draft in the input"}, false, true},
		{"failed", Row{Remaining: true, Problems: []string{"x"}, Actions: []Action{ActRelaunch}, Result: "relaunch failed: did not exit"}, false, true},
		{"lost background only (D16)", Row{Remaining: true, Problems: []string{"background work lost by the relaunch (agent x)"}}, false, false},
		{"not remaining", Row{Actions: []Action{ActRelaunch}}, false, false},
		{"repaired", Row{Problems: []string{"x"}, Done: []Action{ActRelaunch}, Result: "repaired"}, false, false},
	}
	for _, c := range cases {
		if got := c.r.Fixable(); got != c.fixable {
			t.Errorf("%s: Fixable = %v, want %v", c.name, got, c.fixable)
		}
		if got := c.r.Stuck(); got != c.stk {
			t.Errorf("%s: Stuck = %v, want %v", c.name, got, c.stk)
		}
	}
}

// D18 (É29): a row's class is what a masked row keeps; Split counts by it, so
// the report line of a redacted report shares its rows as the full one does.
func TestRowCategory(t *testing.T) {
	cases := []struct {
		r    Row
		want Category
	}{
		{Row{Remaining: true, Actions: []Action{ActRelaunch}}, CatFix},
		{Row{Remaining: true, Blocked: "draft in the input"}, CatLeft},
		{Row{Remaining: true, Problems: []string{"background work lost by the relaunch (agent x)"}}, CatReported},
		{Row{Done: []Action{ActRelaunch}}, CatRepaired},
		{Row{}, CatOK},
		{Row{Masked: CatLeft}, CatLeft}, // a masked row has nothing else: its class is Masked
	}
	for _, c := range cases {
		if got := c.r.Category(); got != c.want {
			t.Errorf("%+v: Category = %q, want %q", c.r, got, c.want)
		}
	}
	rep := Report{Rows: []Row{{Masked: CatFix}, {Masked: CatLeft}, {Masked: CatReported}, {Masked: CatRepaired}}}
	if fix, left, reported := rep.Split(); fix != 1 || left != 1 || reported != 1 || rep.repaired() != 1 {
		t.Fatalf("Split of masked rows = %d, %d, %d, repaired %d; want 1, 1, 1, 1", fix, left, reported, rep.repaired())
	}
}

func TestReportLineAndExit(t *testing.T) {
	l := cleanLaunch()
	ok := Row{Title: "a"}
	fixable := Row{Title: "f", Problems: []string{"resumed by cmux: its width may stay frozen"}, Actions: []Action{ActRelaunch}, Remaining: true}
	left := Row{Title: "b", Problems: []string{"resumed by cmux: its width may stay frozen"}, Actions: []Action{ActRelaunch}, Blocked: "draft in the input", Remaining: true}
	lost := Row{Title: "l", Problems: []string{"background work lost by the relaunch (agent x)"}, Remaining: true} // D16
	fixed := Row{Title: "c", Problems: []string{"x"}, Done: []Action{ActRelaunch}, Result: "repaired"}
	cases := []struct {
		r       Report
		code    int
		in, out []string
	}{
		{Report{Fatal: "cmux unreachable (socket): refused"}, 2, []string{"cmux unreachable", "nothing checked"}, nil},
		{Report{Launch: l, Rows: []Row{ok, fixed}, Fix: true}, 0, []string{"2 sessions", "1 repaired", "0 left"}, []string{"to fix"}},
		{Report{Launch: l, Rows: []Row{ok, fixable}}, 1, []string{"1 to fix — run `pigtail cmux check --fix`"}, []string{"left", "reported only"}},
		{Report{Launch: l, Rows: []Row{ok, left}}, 1, []string{"1 left"}, []string{"to fix", "--fix"}},
		{Report{Launch: l, Rows: []Row{ok, fixable, left}}, 1, []string{"1 to fix — run `pigtail cmux check --fix` · 1 left"}, nil},
		// D16: reported only, never "to fix"; D17: not a defect either — exit 0.
		{Report{Launch: l, Rows: []Row{ok, lost}}, 0, []string{"1 reported only"}, []string{"to fix", "--fix", "left", "all fine"}},
		// D17: a fixable row next to it still exits 1.
		{Report{Launch: l, Rows: []Row{ok, lost, fixable}}, 1, []string{"1 to fix — run `pigtail cmux check --fix` · 1 reported only"}, []string{"left"}},
		{Report{Launch: l, Rows: []Row{ok, left, fixed}, Fix: true}, 1, []string{"1 repaired", "1 left"}, []string{"to fix"}},
	}
	for _, c := range cases {
		if got := c.r.ExitCode(); got != c.code {
			t.Errorf("exit = %d, want %d (%+v)", got, c.code, c.r)
		}
		line := c.r.Line()
		for _, s := range c.in {
			if !strings.Contains(line, s) {
				t.Errorf("line %q lacks %q", line, s)
			}
		}
		for _, s := range c.out {
			if strings.Contains(line, s) {
				t.Errorf("line %q has %q", line, s)
			}
		}
	}
	var buf bytes.Buffer
	Report{Launch: l, Rows: []Row{left}}.Write(&buf)
	if !strings.Contains(buf.String(), "LEFT") || !strings.Contains(buf.String(), "draft in the input") {
		t.Fatalf("detail = %q", buf.String())
	}
	// D15: "left" is a stuck row only; a fixable one reads "to fix", as the
	// report line and a masked row say it.
	buf.Reset()
	Report{Launch: l, Rows: []Row{fixable}}.Write(&buf)
	if strings.Contains(buf.String(), "LEFT") || !strings.Contains(buf.String(), "TO FIX: resumed by cmux") {
		t.Fatalf("fixable detail = %q, want \"TO FIX:\", never LEFT (D15)", buf.String())
	}
	buf.Reset()
	Report{Launch: l, Rows: []Row{lost}}.Write(&buf)
	if strings.Contains(buf.String(), "LEFT") || !strings.Contains(buf.String(), "reported only: background work lost") {
		t.Fatalf("D16 detail = %q, want \"reported only\", never LEFT", buf.String())
	}
	// D17: Split is what checkExit names; Defects counts fixable + stuck only.
	rep := Report{Launch: l, Rows: []Row{ok, fixable, left, lost, lost}}
	if f, lf, rp := rep.Split(); f != 1 || lf != 1 || rp != 2 || rep.Defects() != 2 {
		t.Fatalf("split %d/%d/%d, defects %d; want 1/1/2, 2", f, lf, rp, rep.Defects())
	}
}

// D10 (decided 2026-10-08, A): closed bound surfaces stay info; one header
// line counts those whose conversation runs nowhere; the exit code is
// unchanged.
func TestReportClosedOrphans(t *testing.T) {
	gone := Row{Title: "g", ClosedOrphan: true, Info: []string{"surface closed since the relaunch, conversation alive nowhere"}}
	moved := Row{Title: "m", Info: []string{"surface closed since the relaunch (conversation alive in S-A)"}}
	rep := Report{Launch: cleanLaunch(), Rows: []Row{gone, gone, moved, {Title: "a"}}}
	var buf bytes.Buffer
	rep.Write(&buf)
	lines := strings.Split(buf.String(), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[1]) != "2 bound surfaces closed, conversation alive nowhere" {
		t.Fatalf("report = %q, want the header line second", buf.String())
	}
	if rep.ClosedOrphans() != 2 || rep.ExitCode() != 0 || rep.Defects() != 0 {
		t.Fatalf("orphans %d, exit %d, defects %d; want 2, 0, 0", rep.ClosedOrphans(), rep.ExitCode(), rep.Defects())
	}
	buf.Reset()
	Report{Launch: cleanLaunch(), Rows: []Row{moved}}.Write(&buf)
	if strings.Contains(buf.String(), "bound surfaces closed") {
		t.Fatalf("report = %q, want no header line when every closed conversation runs elsewhere", buf.String())
	}
}

func TestSaveLoadLast(t *testing.T) {
	dir := t.TempDir()
	r := Report{Fatal: "x"}
	if err := SaveLast(dir, r); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLast(dir)
	if err != nil || got.Fatal != "x" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// É25 (B2): a pass is partial when --surface narrowed it or its photo was
// rebuilt from one scope's peers (the peersScopeNote case).
func TestPartialPass(t *testing.T) {
	cases := []struct {
		name string
		o    Options
		src  string
		want bool
	}{
		{"full", Options{}, "previous", false},
		{"--surface", Options{Surfaces: []string{"S1"}}, "previous", true},
		{"one scope's peers", Options{PeersScope: "x"}, "previous+peers", true},
		{"one scope, file photo", Options{PeersScope: "x"}, "previous", false},
		{"every scope's peers", Options{}, "peers", false},
		// A non-master cell's pass that could not look: its photo is unknown,
		// it may have been partial — never let it erase the other scopes' rows.
		{"one scope, fatal", Options{PeersScope: "x"}, "", true},
	}
	for _, c := range cases {
		r := Report{PhotoSource: c.src}
		if c.src == "" {
			r.Fatal = "cmux unreachable"
		}
		if got := PartialPass(c.o, r); got != c.want {
			t.Errorf("%s: PartialPass = %v, want %v", c.name, got, c.want)
		}
	}
}

// É25 (B2): a partial pass keeps, from the last report of the same cmux
// launch, the rows of the surfaces it did not look at; the surfaces it looked
// at take their new row (S1 repaired: its pill goes). A full pass, another
// launch or a fatal report replaces the file.
func TestSaveLastMergesPartialPass(t *testing.T) {
	dir := t.TempDir()
	fixable := func(s string) Row {
		return Row{SurfaceID: s, Remaining: true, Problems: []string{"x"}, Actions: []Action{ActRelaunch}}
	}
	surfaces := func(r Report) string {
		var s []string
		for _, row := range r.Rows {
			s = append(s, row.SurfaceID)
		}
		return strings.Join(s, ",")
	}
	if err := SaveLast(dir, Report{Launch: Launch{PID: 500}, Rows: []Row{fixable("S1"), fixable("S2")}}); err != nil {
		t.Fatal(err)
	}
	repaired := Row{SurfaceID: "s1", Problems: []string{"x"}, Done: []Action{ActRelaunch}, Result: "repaired"}
	if err := SaveLast(dir, Report{Launch: Launch{PID: 500}, Partial: true, Fix: true, Rows: []Row{repaired}}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLast(dir)
	if err != nil || surfaces(got) != "s1,S2" || got.Rows[0].Remaining || !got.Rows[1].Fixable() {
		t.Fatalf("after --surface S1 --fix: %+v, %v; want S1 repaired (case-insensitive match), S2 kept in defect", got.Rows, err)
	}
	// Another launch: nothing carried over.
	if err := SaveLast(dir, Report{Launch: Launch{PID: 501}, Partial: true, Rows: []Row{fixable("S3")}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadLast(dir); surfaces(got) != "S3" {
		t.Fatalf("partial pass of a new launch: rows %q, want S3 only", surfaces(got))
	}
	// A full pass replaces.
	if err := SaveLast(dir, Report{Launch: Launch{PID: 501}, Rows: []Row{fixable("S4")}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadLast(dir); surfaces(got) != "S4" {
		t.Fatalf("full pass: rows %q, want S4 only", surfaces(got))
	}
}

// A fatal partial pass (--surface, or a non-master cell) looked at nothing:
// it never replaces a good report of the same cmux launch — the pills stay.
// Its failure shows on stderr and in the exit code, and --last names it next
// to the kept report (LastFailure). A fatal full pass replaces the file as
// before (TestSaveLoadLast).
func TestSaveLastFatalPartialKeepsReport(t *testing.T) {
	dir := t.TempDir()
	good := Report{Launch: Launch{PID: 500}, Rows: []Row{{SurfaceID: "S1", Remaining: true, Problems: []string{"x"}, Actions: []Action{ActRelaunch}}}}
	for _, fatal := range []Report{
		{Partial: true, Fatal: "cmux unreachable (socket): refused"},                   // before the launch was known
		{Partial: true, Launch: Launch{PID: 500}, Fatal: "process listing: ps failed"}, // same launch
	} {
		if err := SaveLast(dir, good); err != nil {
			t.Fatal(err)
		}
		if err := SaveLast(dir, fatal); err != nil {
			t.Fatal(err)
		}
		got, err := LoadLast(dir)
		if err != nil || got.Fatal != "" || len(got.Rows) != 1 || !got.Rows[0].Fixable() {
			t.Fatalf("after a fatal partial pass (%q): %+v, %v; want the good report kept", fatal.Fatal, got, err)
		}
		if got.LastFailure == nil || got.LastFailure.Fatal != fatal.Fatal {
			t.Fatalf("kept report: LastFailure %+v; want the fatal pass named", got.LastFailure)
		}
		var buf bytes.Buffer
		got.Write(&buf)
		if !strings.Contains(buf.String(), fatal.Fatal) {
			t.Fatalf("--last text %q: the later failure is not said", buf.String())
		}
	}
	// Another launch, known: the fatal pass replaces it.
	if err := SaveLast(dir, Report{Partial: true, Launch: Launch{PID: 501}, Fatal: "process listing: ps failed"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadLast(dir); got.Fatal == "" {
		t.Fatalf("fatal partial pass of another launch: %+v; want it saved", got)
	}
}
