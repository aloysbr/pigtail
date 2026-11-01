package cmuxcheck

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"
)

// D4 (second source): the active maintenance's owner session names the pilot
// by the binding of its conversation.
func TestResolvePilotMaintenanceOwner(t *testing.T) {
	before, now := photos(t)
	s, src, notes := resolvePilot(Options{MaintenanceOwner: "sid-a"}, before, now, Launch{PID: 7})
	if s != "S-A" || src != PilotFromMaintenance || len(notes) != 0 {
		t.Fatalf("got %q %q %v", s, src, notes)
	}
	s, _, notes = resolvePilot(Options{MaintenanceOwner: "sid-nowhere"}, before, now, Launch{PID: 7})
	if s != "" || len(notes) != 1 || !strings.Contains(notes[0], "no pilot") {
		t.Fatalf("unknown owner: got %q %v, want no pilot and a note", s, notes)
	}
}

// D3: --fix-pilot repairs the pilot's row only; the others keep their plan,
// nothing typed into them.
func TestRunFixPilotOnly(t *testing.T) {
	w := newWorld()
	w.add(t, 40, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", ts("2026-10-08T10:31:20Z"), "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{40: true}, exitOK: true,
		spawnPID: 41, spawnEnv: "CLAUDE_CONFIG_DIR=/cfg/a PIGTAIL_CMUX_RELAUNCH=7", spawnArg: "claude --resume sid-a --permission-mode auto"}
	o := runOpts(t)
	o.Fix, o.PilotOnly, o.MaintenanceOwner = true, true, "sid-a"
	rep := Run(w.deps(), repairer(w, p), o)
	if rep.Pilot.SurfaceID != "S-A" || rep.Pilot.Source != PilotFromMaintenance {
		t.Fatalf("pilot %+v", rep.Pilot)
	}
	if r := row(t, rep.Rows, "S-A"); r.Category() != CatRepaired {
		t.Fatalf("pilot row %+v (%s), want repaired", r, r.Category())
	}
	for _, r := range rep.Rows {
		if r.SurfaceID != "S-A" && len(r.Done) > 0 {
			t.Fatalf("row %s repaired outside the pilot: %+v", r.SurfaceID, r)
		}
	}
}

// T-h: no pilot found — report only, the note says so, nothing typed.
func TestRunFixPilotNoPilot(t *testing.T) {
	w := newWorld()
	w.add(t, 40, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", ts("2026-10-08T10:31:20Z"), "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{40: true}, exitOK: true}
	o := runOpts(t)
	o.Fix, o.PilotOnly = true, true
	rep := Run(w.deps(), repairer(w, p), o)
	if len(p.sent) != 0 || !strings.Contains(strings.Join(rep.Notes, "|"), "no pilot: nothing repaired, report only") {
		t.Fatalf("sent %q, notes %v", p.sent, rep.Notes)
	}
}

// D8: under the rescue policy only a human names a surface — never the
// pilot, never a daemon pass's --surface (its second pass, D6).
func TestOptionsNamedByAHumanOnly(t *testing.T) {
	if (Options{Pilot: "S-A"}).named("S-A") {
		t.Fatal("the pilot is not named (D8)")
	}
	if !(Options{Surfaces: []string{"s-a"}}).named("S-A") {
		t.Fatal("--surface by hand names it (any case)")
	}
	if (Options{Surfaces: []string{"S-A"}, Unattended: true}).named("S-A") {
		t.Fatal("a daemon pass's --surface names nothing (D8)")
	}
}

// D9: the pilot's repair blocked by the keyboard focus — the report says to
// click another pane (daemon.log gets the check's output).
func TestRunPilotHoldsFocusNote(t *testing.T) {
	w := newWorld()
	w.add(t, 40, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", ts("2026-10-08T10:31:20Z"), "")
	w.focused = "S-A"
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{40: true}, exitOK: true}
	o := runOpts(t)
	o.Fix, o.PilotOnly, o.MaintenanceOwner = true, true, "sid-a"
	rep := Run(w.deps(), repairer(w, p), o)
	if len(p.sent) != 0 || !strings.Contains(strings.Join(rep.Notes, "|"), "pilot holds keyboard focus: click another pane") {
		t.Fatalf("sent %q, notes %v", p.sent, rep.Notes)
	}
}

// T-g: BusyLeft — a remaining row blocked busy (alone or after another
// block), never a row left for another reason.
func TestRowBusyLeft(t *testing.T) {
	cases := map[string]struct {
		r    Row
		want bool
	}{
		"busy":               {Row{Remaining: true, Blocked: "busy: status busy"}, true},
		"busy after another": {Row{Remaining: true, Blocked: "draft in the input; busy: esc to interrupt"}, true},
		"focus":              {Row{Remaining: true, Blocked: "busy: pane has keyboard focus"}, true},
		"draft only":         {Row{Remaining: true, Blocked: "draft in the input"}, false},
		"not remaining":      {Row{Blocked: "busy: status busy"}, false},
	}
	for name, c := range cases {
		if got := c.r.BusyLeft(); got != c.want {
			t.Errorf("%s: BusyLeft = %v, want %v", name, got, c.want)
		}
	}
	rep := Report{Launch: Launch{PID: 7}, Rows: []Row{{SurfaceID: "S-A", Remaining: true, Blocked: "busy: x"}, {SurfaceID: "S-B"}},
		Pilot: PilotInfo{SurfaceID: "S-B", MessagePending: true}}
	if got := strings.Join(rep.BusyLeft(7, false), ","); got != "S-A,S-B" {
		t.Fatalf("fix mode: Report.BusyLeft(7) = %q", got)
	}
	// revue 1 #5: pilot mode repairs the pilot alone — S-A, left busy, is
	// not this mode's to retry.
	if got := strings.Join(rep.BusyLeft(7, true), ","); got != "S-B" {
		t.Fatalf("pilot mode: Report.BusyLeft(7) = %q, want the pilot only", got)
	}
	if got := rep.BusyLeft(8, false); len(got) != 0 {
		t.Fatalf("another launch: %v", got)
	}
}

// Review Focus 5, D4: valid for the next cmux launch only.
func TestPilotMarkerValidFor(t *testing.T) {
	start := ts("2026-10-09T10:00:00Z")
	l := Launch{PID: 20, Start: start}
	ok := PilotMarker{SurfaceID: "S-A", CmuxPID: 10, At: start.Add(-time.Minute)}
	cases := map[string]struct {
		m    PilotMarker
		want bool
		why  string
	}{
		"set before this launch":     {ok, true, ""},
		"used by this launch":        {func() PilotMarker { m := ok; m.UsedBy = 20; return m }(), true, ""},
		"used by another launch":     {func() PilotMarker { m := ok; m.UsedBy = 15; return m }(), false, "consumed by an earlier cmux launch"},
		"set under the current cmux": {func() PilotMarker { m := ok; m.CmuxPID = 20; return m }(), false, "set under the running cmux"},
		"set after the launch":       {func() PilotMarker { m := ok; m.At = start.Add(time.Minute); return m }(), false, "set after this cmux launch"},
		"too old":                    {func() PilotMarker { m := ok; m.At = start.Add(-3 * time.Hour); return m }(), false, "older than"},
	}
	for name, c := range cases {
		got, why := c.m.ValidFor(l)
		if got != c.want || !strings.Contains(why, c.why) {
			t.Errorf("%s: ValidFor = %v %q, want %v %q", name, got, why, c.want, c.why)
		}
	}
}

func TestPilotMarkerFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadPilot(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no marker: %v", err)
	}
	m := PilotMarker{SurfaceID: "S-A", SessionID: "sid-a", CmuxPID: 10, At: ts("2026-10-09T09:59:00Z")}
	if err := SavePilot(dir, m); err != nil {
		t.Fatal(err)
	}
	if err := MarkPilotUsed(dir, 20); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPilot(dir)
	if err != nil || got.UsedBy != 20 || got.SurfaceID != "S-A" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if err := ClearPilot(dir); err != nil {
		t.Fatal(err)
	}
	if err := ClearPilot(dir); err != nil {
		t.Fatalf("clearing nothing: %v", err)
	}
}

// D4: the marker first; owner second; a marker that does not hold falls back
// to the owner with a note naming why.
func TestResolvePilotMarkerFirst(t *testing.T) {
	before, now := photos(t)
	l := Launch{PID: 20, Start: ts("2026-10-09T10:00:00Z")}
	m := &PilotMarker{SurfaceID: "s-a", SessionID: "sid-a", CmuxPID: 10, At: l.Start.Add(-time.Minute)}
	s, src, notes := resolvePilot(Options{Marker: m, MaintenanceOwner: "sid-b"}, before, now, l)
	if !sameSurface(s, "S-A") || src != PilotFromMarker || len(notes) != 0 {
		t.Fatalf("got %q %q %v", s, src, notes)
	}
	stale := *m
	stale.UsedBy = 15
	s, src, notes = resolvePilot(Options{Marker: &stale}, before, now, l)
	if s != "" || src != "" || !strings.Contains(strings.Join(notes, "|"), "pilot marker ignored: consumed by an earlier cmux launch") {
		t.Fatalf("stale: %q %q %v", s, src, notes)
	}
	// The owner is the fallback when the marker does not hold.
	s, src, notes = resolvePilot(Options{Marker: &stale, MaintenanceOwner: "sid-b"}, before, now, l)
	if s != "S-B" || src != PilotFromMaintenance || !strings.Contains(strings.Join(notes, "|"), "pilot marker ignored") {
		t.Fatalf("stale then owner: %q %q %v", s, src, notes)
	}
	other := *m
	other.SessionID = "sid-other"
	if s, _, notes = resolvePilot(Options{Marker: &other}, before, now, l); s != "" || !strings.Contains(strings.Join(notes, "|"), "not the conversation bound to that surface") {
		t.Fatalf("session mismatch: %q %v", s, notes)
	}
	gone := *m
	gone.SurfaceID = "S-NOWHERE"
	if s, _, notes = resolvePilot(Options{Marker: &gone}, before, now, l); s != "" || !strings.Contains(strings.Join(notes, "|"), "in neither photo") {
		t.Fatalf("unknown surface: %q %v", s, notes)
	}
}
