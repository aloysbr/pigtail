package cmuxcheck

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runOpts(t *testing.T) Options {
	dir := t.TempDir()
	for src, dst := range map[string]string{"testdata/session-before.json": PreviousFile, "testdata/session-now.json": CurrentFile} {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, dst), b, 0o644); err != nil {
			t.Fatal(err)
		}
		at := ts("2026-10-08T10:31:00Z")
		_ = os.Chtimes(filepath.Join(dir, dst), at, at)
	}
	return Options{Dir: dir, Sentinel: writeSentinel(t, "pid=7"), Events: []string{"testdata/events-clean.jsonl"}, Ping: func() error { return nil }}
}

func TestRunInventoryNeverTypes(t *testing.T) {
	w := newWorld()
	w.add(t, 40, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", ts("2026-10-08T10:31:20Z"), "")
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{40: true}}
	rep := Run(w.deps(), repairer(w, p), runOpts(t))
	if rep.Fatal != "" || len(p.sent) != 0 {
		t.Fatalf("fatal %q, sent %q", rep.Fatal, p.sent)
	}
	if r := row(t, rep.Rows, "S-A"); !r.Remaining || !hasAction(r, ActRelaunch) {
		t.Fatalf("row = %+v", r)
	}
	if rep.PhotoSource != "previous" {
		t.Fatalf("photo source = %q", rep.PhotoSource)
	}
}

func TestRunCmuxUnreachableIsFatal(t *testing.T) {
	w := newWorld()
	o := runOpts(t)
	o.Ping = func() error { return errors.New("connection refused") }
	rep := Run(w.deps(), repairer(w, &pane{t: t, w: w}), o)
	if rep.Fatal == "" || len(rep.Rows) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

// D7: a photo rebuilt from one scope's peers says so; the full view, or a
// photo that did not use the peers, says nothing.
func TestRunPeersScopeNote(t *testing.T) {
	for _, src := range []string{"peers", "previous+peers"} {
		if n := peersScopeNote(src, "globex"); !strings.Contains(n, "globex") || !strings.Contains(n, "other scopes are missing") {
			t.Errorf("%s from one scope: note %q", src, n)
		}
		if n := peersScopeNote(src, ""); n != "" {
			t.Errorf("%s from every scope: note %q, want none", src, n)
		}
	}
	if n := peersScopeNote("previous", "globex"); n != "" {
		t.Errorf("previous photo: note %q, want none (peers unused)", n)
	}
}

func TestRunGivenPhotoWins(t *testing.T) {
	w := newWorld()
	o := runOpts(t)
	o.Before = &Photo{Source: "given", Panels: []Panel{{SurfaceID: "S-B", Binding: &Binding{AutoResume: false}}}}
	rep := Run(w.deps(), repairer(w, &pane{t: t, w: w}), o)
	if rep.PhotoSource != "given" || len(rep.Rows) != 1 {
		t.Fatalf("report = %+v", rep)
	}
}

// --surface matches without case, as last.json's merge does (mergePartial):
// "s-b" is S-B. A surface in neither photo is a usage error — exit 2 naming
// it — never an empty pass that reads "all fine".
func TestRunSurfaceCaseAndUnknown(t *testing.T) {
	w := newWorld()
	o := runOpts(t)
	o.Surfaces = []string{"s-b"}
	rep := Run(w.deps(), repairer(w, &pane{t: t, w: w}), o)
	if rep.Fatal != "" || len(rep.Rows) != 1 || rep.Rows[0].SurfaceID != "S-B" {
		t.Fatalf("--surface s-b: fatal %q, rows %+v; want S-B's row", rep.Fatal, rep.Rows)
	}
	o.Surfaces = []string{"S-B", "NOPE-1234"}
	rep = Run(w.deps(), repairer(w, &pane{t: t, w: w}), o)
	if rep.ExitCode() != 2 || !strings.Contains(rep.Fatal, "NOPE-1234") || strings.Contains(rep.Line(), "all fine") {
		t.Fatalf("--surface NOPE-1234: exit %d, line %q; want exit 2 naming it", rep.ExitCode(), rep.Line())
	}
}

func TestRunFixLeavesLateAccountSwitchAlone(t *testing.T) {
	w := newWorld()
	w.add(t, 93, "S-B", "/cfg/a", "sid-b", "claude", ts("2026-10-08T11:01:00Z"), "")
	p := &pane{t: t, w: w, surface: "S-B", alive: map[int]bool{93: true}}
	o := runOpts(t)
	o.Fix, o.Unattended = true, true
	d := w.deps()
	d.Now = func() time.Time { return ts("2026-10-08T11:31:00Z") }
	rep := Run(d, repairer(w, p), o)
	if len(p.sent) != 0 {
		t.Fatalf("sent %q, want nothing typed", p.sent)
	}
	r := row(t, rep.Rows, "S-B")
	if len(r.Problems) != 0 || r.Remaining || r.Fixable() || r.Stuck() {
		t.Fatalf("row = %+v, want no problem and no pill", r)
	}
}

func TestRunFixNeverRelaunchesWithoutTranscript(t *testing.T) {
	w := newWorld()
	w.add(t, 40, "S-A", "/cfg/a", "sid-a", "claude --permission-mode auto", ts("2026-10-08T10:31:20Z"), "")
	w.noTranscript["/cfg/a|sid-a"] = true
	p := &pane{t: t, w: w, surface: "S-A", alive: map[int]bool{40: true}}
	o := runOpts(t)
	o.Fix = true
	r := row(t, Run(w.deps(), repairer(w, p), o).Rows, "S-A")
	if len(p.sent) != 0 || !r.Stuck() || !strings.Contains(r.Blocked, noTranscriptWhy("sid-a", "/cfg/a")) {
		t.Fatalf("sent %q, row %+v", p.sent, r)
	}
}

func TestRunFixNeverQuitsWithoutTranscript(t *testing.T) {
	w, p := frozenRun(t)
	p.garbled = frozenScreen(t)
	w.screens["S-A"] = p.screenFor()
	w.noTranscript["/cfg/a|sid-a"] = true
	o := runOpts(t)
	o.Fix, o.Quit = true, QuitUnread
	r := row(t, Run(w.deps(), repairer(w, p), o).Rows, "S-A")
	if len(p.sent) != 0 || hasAction(r, ActQuit) || !strings.Contains(r.Blocked, noTranscriptWhy("sid-a", "/cfg/a")) || r.Category() != CatLeft {
		t.Fatalf("sent %q, row %+v (%s)", p.sent, r, r.Category())
	}
}

func TestRunFixNeverStartsWithoutTranscript(t *testing.T) {
	w := newWorld()
	w.idleSurface["S-B"] = true
	w.noTranscript["/cfg/b|sid-b"] = true
	p := &pane{t: t, w: w, surface: "S-B", alive: map[int]bool{}}
	o := runOpts(t)
	o.Fix, o.Unattended = true, true
	r := row(t, Run(w.deps(), repairer(w, p), o).Rows, "S-B")
	if len(p.sent) != 0 || !r.Stuck() || !strings.Contains(r.Blocked, noTranscriptWhy("sid-b", "/cfg/b")) {
		t.Fatalf("sent %q, row %+v", p.sent, r)
	}
}
