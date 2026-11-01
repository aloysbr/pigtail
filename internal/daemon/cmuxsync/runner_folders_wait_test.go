package cmuxsync

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// D3 of sub_x000000000000019: `pigtail cmux sidebar status` shows the folder
// round owed and not yet applied, since when and why (State().FoldersWait).

// foldersWait is the wait of win, or the loop's own (no window); per window
// since D32 of sub_x000000000000007.
func foldersWait(h *folderHarness) *pigtail.CmuxFoldersWait { return foldersWaitIn(h, win) }

// foldersWaitIn is window w's wait in State, or the loop's own ("" window) when w
// has none.
func foldersWaitIn(h *folderHarness, w string) *pigtail.CmuxFoldersWait {
	var own *pigtail.CmuxFoldersWait
	for _, fw := range h.r.State().FoldersWait {
		switch fw.Window {
		case w:
			c := fw
			return &c
		case "":
			c := fw
			own = &c
		}
	}
	return own
}

// wantWait fails unless the wait has this reason and, when since is not zero,
// this start.
func wantWait(t *testing.T, h *folderHarness, reason string, since time.Time, what string) *pigtail.CmuxFoldersWait {
	t.Helper()
	w := foldersWait(h)
	switch {
	case w == nil:
		t.Fatalf("%s: no wait, want %q", what, reason)
	case w.Reason != reason:
		t.Fatalf("%s: reason %q, want %q (since %v)", what, w.Reason, reason, w.Since)
	case w.Since.IsZero():
		t.Fatalf("%s: zero since", what)
	case !since.IsZero() && !w.Since.Equal(since):
		t.Fatalf("%s: since %v, want %v", what, w.Since, since)
	}
	return w
}

// youngShed converges a window with one bound session, then lists a young
// unbound workspace « Shed » (it stays where it was born): declaring it owes
// a round.
func youngShed(t *testing.T, opts ...func(*Config, *Deps)) (*folderHarness, *folderWorld) {
	t.Helper()
	h, fw := newFolderHarness(t, opts...)
	h.sessionIn("WS-A", "Terrace")
	h.converge(6)
	fw.add(win, "SHED", "SHED")
	h.step(31 * time.Second) // listed young; the slow pass reads the focus, no op
	h.converge(2)
	if w := foldersWait(h); w != nil || fw.folderOf("SHED") != "" {
		t.Fatalf("setup: wait %+v, SHED in %q", w, fw.folderOf("SHED"))
	}
	return h, fw
}

func TestFoldersWaitKey(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	fw.key = true
	h.step(0)
	t0 := h.now
	wantWait(t, h, "key", t0, "key")
	for i := range 5 { // one pass in two stops at the focus-poll gate: it never replaces key
		h.step(time.Second)
		wantWait(t, h, "key", t0, "pass "+string(rune('1'+i)))
	}
	if n := len(fw.folderWrites()); n != 0 {
		t.Fatalf("key: %d folder writes", n)
	}
	fw.key = false
	h.converge(8)
	if w := foldersWait(h); w != nil || fw.folderOf("WS-A") != "Terrace" {
		t.Fatalf("converged: wait %+v, WS-A in %q", w, fw.folderOf("WS-A"))
	}
}

// The window's state unknown (list-windows fails at the focus read) reads as
// key: it is a visible wait, not a silent one.
func TestFoldersWaitKeyUnknown(t *testing.T) {
	t.Parallel()
	h, fw := youngShed(t)
	mark := len(fw.calls)
	fw.windowsErr = errors.New("cmux list-windows: injected")
	h.offTopic("SHED")
	h.step(3 * time.Second) // not a slow pass
	wantWait(t, h, "key", time.Time{}, "window state unknown")
	for _, c := range fw.calls[mark:] {
		if isFolderWrite(c) {
			t.Fatalf("a folder write: %q", c)
		}
	}
}

func TestFoldersWaitFocusPoll(t *testing.T) {
	t.Parallel()
	h, fw := youngShed(t)
	h.step(31 * time.Second) // a slow pass: a round owed, the focus read now, no op
	if w := foldersWait(h); w != nil {
		t.Fatalf("setup: wait %+v", w)
	}
	mark := len(fw.calls)
	h.offTopic("SHED")
	h.step(time.Second) // owed less than FocusPoll after the last focus read
	wantWait(t, h, "focus-poll", h.now, "focus-poll opens the wait")
	for _, c := range fw.calls[mark:] {
		if isFolderWrite(c) {
			t.Fatalf("a folder write: %q", c)
		}
	}
}

func TestFoldersWaitWriteError(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	fw.failOn = "group-create"
	h.step(0)
	t0 := h.now
	wantWait(t, h, "write-error", t0, "failed write")
	if w := fw.folderWrites(); len(w) == 0 || !strings.HasPrefix(w[len(w)-1], "group-create") {
		t.Fatalf("no write after the failed one: %q", w)
	}
	for i := range 3 { // through ArrangeSettle (the pass that falls on arrangeAfter)
		h.step(time.Second)
		wantWait(t, h, "write-error", t0, "pass "+string(rune('1'+i)))
	}
}

func TestFoldersWaitNoWindow(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t, func(c *Config, _ *Deps) { c.Windows = []string{"99999999-0000-4000-8000-000000000009"} })
	fw.add(win, "WS-A", "a")
	h.step(0)
	wantWait(t, h, "no-window", h.now, "no allowed window")
	if n := len(fw.folderWrites()); n != 0 {
		t.Fatalf("%d folder writes", n)
	}
}

// Between two write rounds the wait is settle, and it started with the first
// pause, not at the last round.
func TestFoldersWaitSettle(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.sessionIn("WS-B", "Garden")
	h.sessionIn("WS-C", "Hedge")
	h.step(0)
	if foldersWait(h) != nil || len(fw.folderWrites()) == 0 {
		t.Fatalf("setup: first round wrote nothing or waits: %+v", foldersWait(h))
	}
	var since time.Time
	rounds := 1
	for range 30 {
		before := len(fw.folderWrites())
		h.step(time.Second)
		if len(fw.folderWrites()) > before {
			rounds++
		}
		w := foldersWait(h)
		if w == nil {
			break
		}
		if since.IsZero() {
			since = w.Since
		}
		wantWait(t, h, "settle", since, "between rounds")
	}
	if rounds < 2 || since.IsZero() {
		t.Fatalf("want two write rounds and a settle between: rounds %d, since %v", rounds, since)
	}
	if w := foldersWait(h); w != nil {
		t.Fatalf("not converged: %+v", w)
	}
}

func TestFoldersWaitSelectedCollapsed(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	if err := SaveCollapseFile(h.statePath, map[string]bool{space.NoSubject: true}); err != nil {
		t.Fatal(err)
	}
	h.sessionIn("WS-A", "Terrace")
	fw.add(win, "SHED1", "SHED1") // old at the first listing: filed in « Inbox »
	fw.add(win, "SHED2", "SHED2")
	fw.selected = "SHED1"
	h.converge(10)
	if gr := fw.groupNamed(space.NoSubject); gr == nil || !gr.Collapsed || fw.folderOf("SHED2") != space.NoSubject || fw.folderOf("SHED1") != "" {
		t.Fatalf("setup: « Inbox » collapsed with SHED2, SHED1 left out: %+v", fw.groups)
	}
	wantWait(t, h, "selected-collapsed", time.Time{}, "selected left out")
	h.converge(3)
	wantWait(t, h, "selected-collapsed", time.Time{}, "while selected")
	fw.selected = ""
	h.step(31 * time.Second) // the next slow pass owes a round (the selection is not in the signature)
	h.converge(6)
	if w := foldersWait(h); w != nil || fw.folderOf("SHED1") != space.NoSubject {
		t.Fatalf("unselected: wait %+v, SHED1 in %q", w, fw.folderOf("SHED1"))
	}
}

// deferredSelected is R3b seen from outside Arrange: the round leaves the
// selected workspace out of the collapsed folder it targets.
func TestDeferredSelected(t *testing.T) {
	t.Parallel()
	other := cmux.Group{ID: "G1", Name: space.NoSubject, AnchorID: "GEN-1", AnchorGenerated: true, Members: []string{"GEN-1", "SHED2"}, Collapsed: true}
	base := func() ArrangeInput {
		return ArrangeInput{
			Folders: []string{"Terrace", space.NoSubject},
			Order:   []string{"GEN-1", "SHED2", "SHED1"},
			Groups:  []cmux.Group{other},
			Target:  map[string]string{"SHED1": space.NoSubject, "SHED2": space.NoSubject},
		}
	}
	for _, tc := range []struct {
		name string
		edit func(*ArrangeInput)
		want bool
	}{
		{"nothing selected", func(*ArrangeInput) {}, false},
		{"selected, target collapsed", func(in *ArrangeInput) { in.Selected = "SHED1" }, true},
		{"selected, already a member", func(in *ArrangeInput) { in.Selected = "SHED2" }, false},
		{"selected, target expanded", func(in *ArrangeInput) { in.Selected = "SHED1"; in.Groups[0].Collapsed = false }, false},
		{"selected, no target", func(in *ArrangeInput) { in.Selected = "SHED1"; delete(in.Target, "SHED1") }, false},
		{"selected, folder to be born collapsed", func(in *ArrangeInput) {
			in.Selected, in.Groups, in.Collapse = "SHED1", nil, map[string]bool{space.NoSubject: true}
		}, true},
		{"selected, folder to be born expanded", func(in *ArrangeInput) { in.Selected, in.Groups = "SHED1", nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			in.Groups = append([]cmux.Group(nil), in.Groups...)
			tc.edit(&in)
			if got := deferredSelected(in); got != tc.want {
				t.Fatalf("deferredSelected = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFoldersWaitListingDown(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	fw.key = true
	h.step(0)
	t0 := h.now
	wantWait(t, h, "key", t0, "a round owed")
	fw.listDown = errors.New("cmux list-windows: injected")
	h.step(31 * time.Second) // the slow listing fails: no arrangement
	wantWait(t, h, "listing-down", t0, "listing down")
	h.step(time.Second)
	wantWait(t, h, "listing-down", t0, "still down")
}

func TestFoldersWaitReadError(t *testing.T) {
	t.Parallel()
	h, _ := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	h.reads.offTopic = errors.New("store: injected")
	h.step(0)
	t0 := h.now
	wantWait(t, h, "read-error", t0, "store read")
	for i := range 3 { // through the settle pass arrangeFailed sets
		h.step(time.Second)
		wantWait(t, h, "read-error", t0, "pass "+string(rune('1'+i)))
	}
}

func TestFoldersWaitStopped(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	fw.renameOnCreate = func(n string) string { return n + "!" } // read back under another name
	h.sessionIn("WS-A", "Terrace")
	h.converge(4)
	if !h.r.foldersStopped {
		t.Fatal("folders not stopped")
	}
	wantWait(t, h, "stopped", time.Time{}, "stopped")
}

// Review Focus 5: a loop switched off, or no --cmux-folders, shows no wait and
// no window — on a real Runner, never CmuxSidebar=nil.
func TestFoldersWaitHiddenWhenOff(t *testing.T) {
	t.Parallel()
	t.Run("loop switched off", func(t *testing.T) {
		h, fw := newFolderHarness(t)
		h.sessionIn("WS-A", "Terrace")
		fw.key = true
		h.step(0)
		wantWait(t, h, "key", h.now, "positive control, loop on")
		if ws := h.r.State().FoldersWindows; !slices.Equal(ws, []string{win}) {
			t.Fatalf("positive control: FoldersWindows %q", ws)
		}
		h.r.SetEnabled(false)
		h.step(time.Second)
		if st := h.r.State(); st.FoldersWait != nil || st.FoldersWindows != nil {
			t.Fatalf("off: wait %+v, windows %q", st.FoldersWait, st.FoldersWindows)
		}
		h.step(10 * time.Minute) // no « since » that runs on while off
		if st := h.r.State(); st.FoldersWait != nil {
			t.Fatalf("off: wait %+v", st.FoldersWait)
		}
	})
	t.Run("no --cmux-folders", func(t *testing.T) {
		h, fw := newFolderHarness(t, func(c *Config, _ *Deps) { c.Folders = false })
		h.sessionIn("WS-A", "Terrace")
		fw.key = true
		h.converge(4)
		if st := h.r.State(); st.FoldersWait != nil || st.FoldersWindows != nil {
			t.Fatalf("folders off: wait %+v, windows %q", st.FoldersWait, st.FoldersWindows)
		}
	})
}

func isFolderWrite(call string) bool {
	f := strings.Fields(call)
	if len(f) == 0 {
		return false
	}
	for _, v := range folderVerbs {
		if f[0] == v {
			return true
		}
	}
	return false
}

// cmux unreachable: no round can be applied nor its window read, so the
// status shows neither a wait running on stale nor the last windows.
func TestFoldersWaitClearedWhenUnreachable(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	fw.key = true
	h.step(0)
	wantWait(t, h, "key", h.now, "positive control, cmux reachable")
	if ws := h.r.State().FoldersWindows; !slices.Equal(ws, []string{win}) {
		t.Fatalf("positive control: FoldersWindows %q", ws)
	}
	fw.down = errors.New("cmux: injected, unreachable")
	h.step(31 * time.Second) // the cached listing is still usable: degraded, not unreachable
	wantWait(t, h, "listing-down", time.Time{}, "positive control, listing down")
	h.step(10 * time.Minute) // past ListingMaxAge: cmux unreachable
	if h.r.isAvailable() {
		t.Fatal("setup: cmux still available")
	}
	for i := range 3 { // the failing pass, then the backed-off ones (early returns)
		if i > 0 {
			h.step(time.Second)
		}
		if st := h.r.State(); st.FoldersWait != nil || st.FoldersWindows != nil {
			t.Fatalf("pass %d, cmux unreachable: wait %+v, window %q", i+1, st.FoldersWait, st.FoldersWindows)
		}
	}
}

// Folders stopped stay « stopped » when the listing then fails: listing-down
// would hide that no round will ever be applied without a restart.
func TestFoldersWaitStoppedListingDown(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	fw.renameOnCreate = func(n string) string { return n + "!" }
	h.sessionIn("WS-A", "Terrace")
	h.converge(4)
	if !h.r.foldersStopped {
		t.Fatal("folders not stopped")
	}
	wantWait(t, h, "stopped", time.Time{}, "positive control, stopped")
	fw.listDown = errors.New("cmux list-windows: injected")
	h.sessionIn("WS-B", "Garden") // a round owed
	h.step(31 * time.Second)
	wantWait(t, h, "stopped", time.Time{}, "stopped, listing down")
}

// D32 of sub_x000000000000007: each allowed window has its own wait. A slow
// pass owes both windows a round; win's applies (nothing to do), winB's create
// fails: the wait is winB's alone, and the status lists both windows.
func TestFoldersWaitPerWindow(t *testing.T) {
	t.Parallel()
	h, fw := twoWindowWorld(t)
	if st := h.r.State(); !slices.Equal(st.FoldersWindows, []string{win, winB}) || st.FoldersWait != nil {
		t.Fatalf("converged: windows %q, wait %+v", st.FoldersWindows, st.FoldersWait)
	}
	fw.failOn = "group-create"
	h.sessionOn(winB, "WS-N", "Garden") // « Garden » is a new folder in winB only
	h.step(31 * time.Second)
	t0 := h.now
	st := h.r.State()
	if len(st.FoldersWait) != 1 || st.FoldersWait[0].Window != winB || st.FoldersWait[0].Reason != waitWriteError || !st.FoldersWait[0].Since.Equal(t0) {
		t.Fatalf("want one write-error wait on winB since %v, got %+v", t0, st.FoldersWait)
	}
	fw.failOn = ""
	h.converge(10)
	if st := h.r.State(); st.FoldersWait != nil || fw.folderIn(winB, "WS-N") != "Garden" {
		t.Fatalf("converged: wait %+v, WS-N in %q", st.FoldersWait, fw.folderIn(winB, "WS-N"))
	}
}

// CA4: a folder round owed past FoldersWaitLog (2 min) leaves a trace in the
// daemon log: one line per change of cause, one when it finally applies.

func waitLines(logs []string, sub string) []string {
	var out []string
	for _, l := range logs {
		if strings.Contains(l, "cmuxsync: folders: ") && strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return out
}

func TestFoldersWaitLoggedPastThreshold(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	fw.key = true
	h.step(0)
	t0 := h.now
	for range 6 { // 3 min under a key cmux
		h.step(30 * time.Second)
	}
	owed := waitLines(h.logs, "round owed")
	if len(owed) != 1 || !strings.Contains(owed[0], "waiting: key") || !strings.Contains(owed[0], "since "+t0.UTC().Format("15:04:05")) {
		t.Fatalf("want one `round owed … since %s, waiting: key` line, got %q", t0.UTC().Format("15:04:05"), h.logs)
	}

	fw.listDown = errors.New("cmux list-windows: injected")
	h.step(31 * time.Second)
	if n := len(waitLines(h.logs, "waiting: listing-down")); n != 1 {
		t.Fatalf("want one listing-down line, got %d: %q", n, h.logs)
	}
	if n := len(waitLines(h.logs, "round owed")); n != 2 {
		t.Fatalf("want two owed lines in all, got %d: %q", n, h.logs)
	}

	fw.listDown, fw.key = nil, false
	h.converge(10)
	applied := waitLines(h.logs, "applied after")
	if len(applied) != 1 || !strings.Contains(applied[0], "owed since "+t0.UTC().Format("15:04:05")) {
		t.Fatalf("want one applied line, got %q", h.logs)
	}
}

// The wait alternates key / focus-poll pass after pass: still one line.
func TestFoldersWaitKeyStepwiseLogsOnce(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	fw.key = true
	h.step(0)
	for range 180 {
		h.step(time.Second)
	}
	if n := len(waitLines(h.logs, "round owed")); n != 1 {
		t.Fatalf("want exactly one owed line, got %d: %q", n, h.logs)
	}
}

func TestFoldersWaitUnderThresholdSilent(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	fw.key = true
	h.step(0)
	for range 90 {
		h.step(time.Second)
	}
	fw.key = false
	h.converge(10)
	if l := waitLines(h.logs, ""); len(l) != 0 {
		t.Fatalf("under the threshold nothing is logged: %q", l)
	}
}

// A pause (unavailable) in the middle does not reset the start of the wait.
func TestFoldersWaitSurvivesUnavailable(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t)
	h.sessionIn("WS-A", "Terrace")
	fw.key = true
	h.step(0)
	t0 := h.now
	for range 2 { // 1 min
		h.step(30 * time.Second)
	}
	h.world.writeDown = errors.New("signal: killed")
	h.setStatus("s-WS-A", "WS-A", "waiting") // a pill to write: the killed write pauses the loop
	h.step(time.Second)
	if h.r.State().CmuxAvailable {
		t.Fatalf("setup: the killed write must pause; logs %q", h.logs)
	}
	h.world.writeDown = nil
	for range 8 { // 2 min and more, through the backoff
		h.step(30 * time.Second)
	}
	owed := waitLines(h.logs, "round owed")
	if len(owed) == 0 || !strings.Contains(owed[0], "since "+t0.UTC().Format("15:04:05")) {
		t.Fatalf("the start must be %s, got %q", t0.UTC().Format("15:04:05"), h.logs)
	}
}

// A window closed during a pause (unavailable() empties foldersWait, keeps
// waitLog) loses its trace at the next listing: no « applied » line for a
// window that no longer exists.
func TestFoldersWaitTraceForgottenWithClosedWindow(t *testing.T) {
	t.Parallel()
	var logs []string
	r := &Runner{cfg: Config{FoldersWaitLog: 2 * time.Minute, BackoffMin: time.Second, BackoffMax: time.Minute}}
	r.deps.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	t0 := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	r.noteWait(t0, []string{"W2"}, waitKey)
	r.unavailable(t0.Add(time.Second), errors.New("cmux clear-status: signal: killed"))
	r.folderWins = []string{"W1"}
	r.forgetClosedWindows()
	r.clearWait(t0.Add(time.Hour))
	if l := waitLines(logs, "applied after"); len(l) != 0 {
		t.Fatalf("a closed window's wait never applies: %q", l)
	}
}
