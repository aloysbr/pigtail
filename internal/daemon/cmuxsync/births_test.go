package cmuxsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The birth clock (D31, D35): when each workspace was first seen, kept across
// a daemon restart in cmux-births.json.

var bt0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

type birthRig struct {
	r     *Runner
	path  string
	logs  []string
	saves int
	loads int
}

// newBirthRig builds a runner on a state file; Folders on unless off.
func newBirthRig(t *testing.T, path string, foldersOff bool) *birthRig {
	t.Helper()
	b := &birthRig{path: path}
	deps := Deps{
		Now:  func() time.Time { return bt0 },
		Logf: func(f string, a ...any) { b.logs = append(b.logs, strings.TrimSpace(f)) },
		LoadBirths: func() (map[string]time.Time, bool, error) {
			b.loads++
			return LoadBirthsFile(path)
		},
		SaveBirths: func(m map[string]time.Time) error {
			b.saves++
			return SaveBirthsFile(path, m)
		},
		BirthsModTime: func() (time.Time, error) { return BirthsFileModTime(path) },
	}
	b.r = New(Config{Enabled: true, Folders: !foldersOff}, deps)
	return b
}

func (b *birthRig) note(now time.Time, ids ...string) { b.r.noteBirths(ids, ids, now) }

func TestBirthsFileRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cmux-births.json")
	m, present, err := LoadBirthsFile(path)
	if err != nil || present || len(m) != 0 {
		t.Fatalf("absent file: %v %v %v", m, present, err)
	}
	in := map[string]time.Time{"AAA": bt0, "BBB": {}}
	if err := SaveBirthsFile(path, in); err != nil {
		t.Fatal(err)
	}
	out, present, err := LoadBirthsFile(path)
	if err != nil || !present || len(out) != 2 || !out["AAA"].Equal(bt0) || !out["BBB"].IsZero() {
		t.Fatalf("round trip: %v %v %v", out, present, err)
	}
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadBirthsFile(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("invalid json: want an error naming the path, got %v", err)
	}
}

func TestBirthsFirstRunAllOld(t *testing.T) {
	t.Parallel()
	b := newBirthRig(t, filepath.Join(t.TempDir(), "b.json"), false)
	b.note(bt0, "A", "B")
	if b.r.young("A", bt0) || b.r.young("B", bt0) {
		t.Fatal("first listing: everything already open is old (D35)")
	}
	b.note(bt0.Add(time.Second), "A", "B", "C")
	if !b.r.young("C", bt0.Add(time.Second)) {
		t.Fatal("C born after the first listing is young")
	}
}

func TestBirthsFirstRunWaitsForAllowedWindow(t *testing.T) {
	t.Parallel()
	b := newBirthRig(t, filepath.Join(t.TempDir(), "b.json"), false)
	b.r.noteBirths(nil, []string{"X"}, bt0) // a window listed, none allowed
	if b.saves != 0 {
		t.Fatal("nothing to mark yet")
	}
	b.r.noteBirths([]string{"A"}, []string{"X", "A"}, bt0.Add(time.Second))
	if b.r.young("A", bt0.Add(time.Second)) {
		t.Fatal("A is old: first-run mode was not consumed by the empty listing")
	}
	b.r.noteBirths([]string{"A", "N"}, []string{"X", "A", "N"}, bt0.Add(2*time.Second))
	if !b.r.young("N", bt0.Add(2*time.Second)) {
		t.Fatal("N born after is young")
	}
}

func TestBirthsNilLoaderIsMemoryOnly(t *testing.T) {
	t.Parallel()
	r := New(Config{Enabled: true, Folders: true}, Deps{Now: func() time.Time { return bt0 }, Logf: func(string, ...any) {}})
	r.noteBirths([]string{"A"}, []string{"A"}, bt0)
	if r.young("A", bt0) {
		t.Fatal("nil loader reads as an absent file: old")
	}
	r.noteBirths([]string{"A", "B"}, []string{"A", "B"}, bt0.Add(time.Second))
	if !r.young("B", bt0.Add(time.Second)) {
		t.Fatal("second listing: B young")
	}
}

func TestBirthsSurviveRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "b.json")
	b1 := newBirthRig(t, path, false)
	b1.note(bt0, "A")
	b1.note(bt0.Add(time.Second), "A", "C")
	b2 := newBirthRig(t, path, false)
	at := bt0.Add(10 * time.Minute)
	b2.note(at, "A", "C")
	if !b2.r.young("C", at) {
		t.Fatal("C keeps its birth date across a restart")
	}
	grace := b2.r.cfg.BirthGrace
	if b2.r.young("C", bt0.Add(time.Second).Add(grace)) {
		t.Fatal("C is old at birth + BirthGrace")
	}
}

func TestBirthsUnknownAfterRestartIsBornNow(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "b.json")
	b1 := newBirthRig(t, path, false)
	b1.note(bt0, "A")
	b2 := newBirthRig(t, path, false)
	t1 := bt0.Add(time.Hour)
	b2.note(t1, "A", "D")
	if !b2.r.young("D", t1) || b2.r.young("A", t1) {
		t.Fatal("D appeared during the stop: born at the first listing; A stays old")
	}
}

func TestBirthsSeenAtRoundIsBornNow(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "b.json")
	b := newBirthRig(t, path, false)
	b.note(bt0, "A")
	t1 := bt0.Add(5 * time.Second)
	b.r.seeBirths([]string{"A", "N"}, t1)
	if !b.r.young("N", t1) {
		t.Fatal("N seen at a round is born now")
	}
	m, _, _ := LoadBirthsFile(path)
	if !m["N"].Equal(t1) {
		t.Fatalf("file not rewritten with N: %v", m)
	}
	b.r.seeBirths([]string{"A"}, t1.Add(time.Second))
	if !b.r.young("N", t1.Add(time.Second)) {
		t.Fatal("a round listing never forgets")
	}
}

func TestBirthsForgetAfterTwoListings(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "b.json")
	b := newBirthRig(t, path, false)
	b.note(bt0, "A")
	b.note(bt0.Add(time.Second), "A", "C")
	b.note(bt0.Add(2*time.Second), "A") // C missed once
	if !b.r.young("C", bt0.Add(2*time.Second)) {
		t.Fatal("C kept after one miss")
	}
	b.note(bt0.Add(3*time.Second), "A", "C") // back: same date
	b.note(bt0.Add(4*time.Second), "A")
	if !b.r.young("C", bt0.Add(4*time.Second)) {
		t.Fatal("C back between the two misses: kept")
	}
	b.note(bt0.Add(5*time.Second), "A")
	m, _, _ := LoadBirthsFile(path)
	if _, ok := m["C"]; ok {
		t.Fatalf("C missed twice in a row must be forgotten: %v", m)
	}
}

func TestBirthsNotForgottenWithoutWindows(t *testing.T) {
	t.Parallel()
	fh, fw := newFolderHarness(t, func(c *Config, d *Deps) {
		path := filepath.Join(t.TempDir(), "b.json")
		d.LoadBirths = func() (map[string]time.Time, bool, error) { return LoadBirthsFile(path) }
		d.SaveBirths = func(m map[string]time.Time) error { return SaveBirthsFile(path, m) }
	})
	fw.add(win, "WS-A", "A")
	fh.step(time.Second)
	fw.add(win, "WS-C", "C")
	fh.step(31 * time.Second)
	born := fh.r.births["WS-C"]
	if !fh.r.young("WS-C", fh.now) {
		t.Fatal("setup: WS-C young")
	}
	fw.windowGone = true
	fh.step(31 * time.Second)
	fh.step(31 * time.Second)
	fw.windowGone = false
	fh.step(31 * time.Second)
	if got := fh.r.births["WS-C"]; !got.Equal(born) {
		t.Fatalf("WS-C birth changed across a window-less period: %v -> %v", born, got)
	}
}

func TestBirthsGeneratedAnchorIsOld(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "b.json")
	b := newBirthRig(t, path, false)
	b.note(bt0, "A")
	b.note(bt0.Add(time.Second), "A", "G")
	b.r.markOld("G")
	if b.r.young("G", bt0.Add(2*time.Second)) {
		t.Fatal("a generated anchor has no grace (D18)")
	}
	b2 := newBirthRig(t, path, false)
	b2.note(bt0.Add(time.Minute), "A", "G")
	if b2.r.young("G", bt0.Add(time.Minute)) {
		t.Fatal("still old after a restart")
	}
}

func TestBirthsUnreadableReadsOld(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "b.json")
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := newBirthRig(t, path, false)
	b.note(bt0, "A", "B")
	if b.r.young("A", bt0) || b.r.young("B", bt0) {
		t.Fatal("unreadable file reads as everything old, never young")
	}
	if len(b.logs) != 1 {
		t.Fatalf("one log line expected, got %q", b.logs)
	}
}

func TestMissedTwice(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		present bool
		ticks   int
		next    int
		forget  bool
	}{{true, 1, 0, false}, {false, 0, 1, false}, {false, 1, 2, true}} {
		n, f := missedTwice(c.present, c.ticks)
		if n != c.next || f != c.forget {
			t.Fatalf("missedTwice(%v,%d) = %d,%v", c.present, c.ticks, n, f)
		}
	}
}

func TestBirthsNotWrittenWithFoldersOff(t *testing.T) {
	t.Parallel()
	h := newHarness(t, Config{Enabled: true})
	loads, saves := 0, 0
	h.r.deps.LoadBirths = func() (map[string]time.Time, bool, error) { loads++; return nil, false, nil }
	h.r.deps.SaveBirths = func(map[string]time.Time) error { saves++; return nil }
	h.world.add(winA, "WS-A", "A")
	h.step(time.Second)
	h.world.add(winA, "WS-B", "B")
	h.step(31 * time.Second)
	h.step(31 * time.Second)
	if loads != 0 || saves != 0 {
		t.Fatalf("folders off: births touched (loads %d, saves %d)", loads, saves)
	}
}

// A file nobody wrote for longer than BirthGrace says nothing about the tabs
// opened since: the clock did not run (the daemon was off). They read old, as
// with an absent file; what the file knows keeps its date.
func TestBirthsStaleFileReadsUnknownOld(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "b.json")
	if err := SaveBirthsFile(path, map[string]time.Time{"A": bt0.Add(-4 * time.Hour), "C": bt0.Add(-5 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	stale := bt0.Add(-3 * time.Hour)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatal(err)
	}
	b := newBirthRig(t, path, false)
	b.note(bt0, "A", "C", "D")
	if b.r.young("D", bt0) {
		t.Fatal("D opened while the clock did not run: old, not young")
	}
	if got := b.r.births["C"]; !got.Equal(bt0.Add(-5 * time.Hour)) {
		t.Fatalf("a known id keeps its date: %v", got)
	}
	b.note(bt0.Add(time.Second), "A", "C", "D", "E")
	if !b.r.young("E", bt0.Add(time.Second)) {
		t.Fatal("first-run mode is consumed once: E born after is young")
	}
}

func TestBirthsFreshFileKeepsUnknownYoung(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "b.json")
	if err := SaveBirthsFile(path, map[string]time.Time{"A": bt0.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	recent := bt0.Add(-time.Minute)
	if err := os.Chtimes(path, recent, recent); err != nil {
		t.Fatal(err)
	}
	b := newBirthRig(t, path, false)
	b.note(bt0, "A", "D")
	if !b.r.young("D", bt0) {
		t.Fatal("a file written a minute ago: D appeared since, young")
	}
}
