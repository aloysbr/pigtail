package tmpsweep

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/fsutil"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestMain(m *testing.M) {
	scopetest.MainTwo()
	os.Exit(m.Run())
}

var t0 = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type rig struct {
	t       *testing.T
	root    string
	s       *Sweeper
	mu      sync.Mutex
	entries []pigtail.JournalEntry
	logs    []string
}

func newRig(t *testing.T, mode Mode) *rig {
	t.Helper()
	r := &rig{t: t, root: t.TempDir()}
	s := New(mode)
	s.Dirs = []string{r.root}
	var tick time.Duration
	s.Now = func() time.Time { tick++; return t0.Add(tick) } // distinct ids per write
	s.Journal = func(e pigtail.JournalEntry) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.entries = append(r.entries, e)
		return nil
	}
	s.Logf = func(f string, a ...any) { r.logs = append(r.logs, f) }
	r.s = s
	return r
}

// mk makes a dir name under the root, aged age before t0, optionally read-only.
func (r *rig) mk(name string, age time.Duration, readonly bool) string {
	r.t.Helper()
	p := filepath.Join(r.root, name)
	sub := filepath.Join(p, "mod", "x")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		r.t.Fatal(err)
	}
	f := filepath.Join(sub, "f")
	if err := os.WriteFile(f, []byte("data"), 0o444); err != nil {
		r.t.Fatal(err)
	}
	old := t0.Add(-age)
	for _, q := range []string{f, sub, filepath.Join(p, "mod"), p} {
		if err := os.Chtimes(q, old, old); err != nil {
			r.t.Fatal(err)
		}
	}
	if readonly {
		for _, q := range []string{sub, filepath.Join(p, "mod")} {
			if err := os.Chmod(q, 0o555); err != nil {
				r.t.Fatal(err)
			}
		}
	}
	r.t.Cleanup(func() { _ = fsutil.RemoveTree(p) })
	return p
}

func (r *rig) exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func (r *rig) kinds() []string {
	var k []string
	for _, e := range r.entries {
		k = append(k, e.Kind)
	}
	return k
}

func TestSweepsOldReadOnlyLeftover(t *testing.T) {
	r := newRig(t, ModeOn)
	old := r.mk("pt-admit-123", 3*time.Hour, true)
	r.s.Tick(t0)
	if r.exists(old) {
		t.Fatal("old read-only leftover still there")
	}
	if len(r.entries) != 1 || r.entries[0].Kind != "tmp-swept" {
		t.Fatalf("journal = %v", r.kinds())
	}
	e := r.entries[0]
	if e.Scope != string(scope.Master()) {
		t.Errorf("scope = %q, want master", e.Scope)
	}
	if !strings.HasPrefix(e.ID, "tmp-swept:") || !strings.Contains(e.Summary, "n=1") ||
		!strings.Contains(e.Summary, "bytes=4") || !strings.Contains(e.Summary, "oldest=") {
		t.Errorf("entry = %+v", e)
	}
	if err := e.Validate(); err != nil {
		t.Errorf("invalid journal entry: %v", err)
	}
}

func TestLeavesWhatItShould(t *testing.T) {
	r := newRig(t, ModeOn)
	young := r.mk("pt-young-1", 10*time.Minute, false)
	mixed := r.mk("pt-mixed-2", 5*time.Hour, false)
	// a recent file deep in an old dir
	nf := filepath.Join(mixed, "mod", "x", "new")
	if err := os.WriteFile(nf, []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(nf, t0.Add(-time.Minute), t0.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	unknown := r.mk("something-9", 5*time.Hour, false)
	nodigits := r.mk("pt-nodigits", 5*time.Hour, false)
	generic := r.mk("h2-77", 5*time.Hour, false)
	afile := filepath.Join(r.root, "pt-file-3")
	if err := os.WriteFile(afile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(afile, t0.Add(-5*time.Hour), t0.Add(-5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	target := r.mk("target-dir", 5*time.Hour, false)
	link := filepath.Join(r.root, "pt-link-4")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	r.s.Tick(t0)
	for _, p := range []string{young, mixed, unknown, nodigits, generic, afile, target, link} {
		if !r.exists(p) {
			t.Errorf("%s was removed", filepath.Base(p))
		}
	}
	if len(r.entries) != 0 {
		t.Errorf("journal = %v, want none", r.kinds())
	}
}

func TestObserveNeverDeletesAndDoesNotRepeat(t *testing.T) {
	r := newRig(t, ModeObserve)
	old := r.mk("pt-obs-5", 3*time.Hour, false)
	r.s.Tick(t0)
	r.s.Tick(t0)
	if !r.exists(old) {
		t.Fatal("observe deleted")
	}
	if got := r.kinds(); len(got) != 1 || got[0] != "tmp-would-sweep" {
		t.Fatalf("journal = %v, want one tmp-would-sweep", got)
	}
	r.mk("pt-obs-6", 3*time.Hour, false)
	r.s.Tick(t0)
	if len(r.entries) != 2 {
		t.Fatalf("a changed candidate set must write again: %v", r.kinds())
	}
	if r.entries[0].ID == r.entries[1].ID {
		t.Error("journal ids must be unique per write")
	}
}

func TestLegacyPrefixes(t *testing.T) {
	for name, want := range map[string]bool{
		"pt-a-b-12":             true,
		"pt-12":                 true,
		"pt-x":                  false,
		"pt-Upper-1":            false,
		"pigtail-cli-test-123":  true,
		"pigtail-cli-test-":     false,
		"pigtail-cli-test-abc1": false,
		"pgt-home5":             true,
		"pgt-md1":               true,
		"pgt-xs9":               true,
		"pgt-xc9":               true,
		"pgt-state42":           true,
		"pigtail-state-test-7":  true,
		"pgt123":                true,
		"pgtx1":                 false,
		"pa123":                 false,
		"lp123":                 false,
		"h2-123":                false,
	} {
		if got := Matches(name); got != want {
			t.Errorf("Matches(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestNoSweepBeforeInterval(t *testing.T) {
	r := newRig(t, ModeOn)
	old := r.mk("pt-run-8", 3*time.Hour, false)
	r.s.Interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.s.Run(ctx); close(done) }()
	cancel()
	<-done
	if !r.exists(old) {
		t.Fatal("Run swept before its first Interval (or at start)")
	}
}

func TestRunSweepsAfterInterval(t *testing.T) {
	r := newRig(t, ModeOn)
	old := r.mk("pt-run-9", 3*time.Hour, false)
	r.s.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.s.Run(ctx); close(done) }()
	testutil.Eventually(t, 30*time.Second, "Run sweeps after its Interval", func() bool { return !r.exists(old) })
	cancel()
	<-done
}

func TestOffDoesNothing(t *testing.T) {
	r := newRig(t, ModeOff)
	old := r.mk("pt-off-1", 3*time.Hour, false)
	r.s.Tick(t0)
	if !r.exists(old) {
		t.Fatal("off deleted")
	}
}

func TestDedupesDirs(t *testing.T) {
	r := newRig(t, ModeOn)
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(r.root, link); err != nil {
		t.Fatal(err)
	}
	r.s.Dirs = []string{r.root, link, r.root}
	r.mk("pt-dd-1", 3*time.Hour, false)
	r.s.Tick(t0)
	if len(r.entries) != 1 || !strings.Contains(r.entries[0].Summary, "n=1") {
		t.Fatalf("entries = %+v", r.entries)
	}
}

func TestParseMode(t *testing.T) {
	for _, s := range []string{"on", "observe", "off"} {
		if m, err := ParseMode(s); err != nil || string(m) != s {
			t.Errorf("ParseMode(%q) = %v, %v", s, m, err)
		}
	}
	if _, err := ParseMode("yes"); err == nil {
		t.Error("yes accepted")
	}
}
