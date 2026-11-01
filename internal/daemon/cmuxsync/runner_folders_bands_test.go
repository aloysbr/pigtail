package cmuxsync

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Maturity bands at the runner (D2, D3, D16, D18, D29 of
// sub_x000000000000014), against the simulated cmux, never the real sidebar.
// A subject's status changes through Store.UpdateSubject (setSubjectStatus),
// the daemon's own path.

// bandsWorld files, without the sort, one tab per status in « Hedge »:
// B1 building, D1 designing, U1 understanding, I1 inbox, each its own
// subject of the Hedge theme, created in that order. It returns the
// subject ids by tab.
func bandsWorld(t *testing.T, opts ...func(*Config, *Deps)) (*folderHarness, *folderWorld, map[string]string) {
	t.Helper()
	h, fw := newFolderHarness(t, append([]func(*Config, *Deps){noSort}, opts...)...)
	thm := h.themeOf("Hedge")
	subs := map[string]string{}
	for _, c := range []struct{ ws, status string }{{"B1", "building"}, {"D1", "designing"}, {"U1", "understanding"}, {"I1", "inbox"}} {
		id := "sub_" + strings.ToLower(c.ws)
		h.subjectOfTheme(thm, id)
		h.setSubjectStatus(id, c.status)
		h.sessionOf(c.ws, id)
		subs[c.ws] = id
	}
	h.converge(10)
	h.assertQuiet("bands world")
	if got := fw.members("Hedge"); !slices.Equal(got, []string{"B1", "D1", "U1", "I1"}) {
		t.Fatalf("world: Hedge = %q", got)
	}
	return h, fw, subs
}

func TestRunnerStatusChangeMovesOnlyThatTab(t *testing.T) {
	t.Parallel()
	h, fw, subs := bandsWorld(t)
	mark := len(fw.folderWrites())
	h.setSubjectStatus(subs["U1"], "building")
	h.converge(6)
	if got := fw.members("Hedge"); !slices.Equal(got, []string{"B1", "U1", "D1", "I1"}) {
		t.Fatalf("Hedge = %q, want [B1 U1 D1 I1]", got)
	}
	if w := fw.folderWrites()[mark:]; len(w) != 1 {
		t.Fatalf("writes %q, want one", w)
	}
	h.assertQuiet("after the status change")
}

func TestRunnerNoStatusChangeNoReorder(t *testing.T) {
	t.Parallel()
	h, fw, _ := bandsWorld(t)
	mark := len(fw.folderWrites())
	h.converge(5)
	if w := fw.folderWrites()[mark:]; len(w) != 0 {
		t.Fatalf("5 passes, no change, yet writes %q", w)
	}
}

// D2: a band changes the order, a gravity change does not (no sort).
func TestRunnerGravityChangeNoMove(t *testing.T) {
	t.Parallel()
	h, fw, _ := bandsWorld(t)
	mark := len(fw.folderWrites())
	h.wait("s-I1", "arbitrage")
	h.converge(6)
	if w := fw.folderWrites()[mark:]; len(w) != 0 {
		t.Fatalf("a gravity change moved tabs: %q", w)
	}
}

// D16: an unpinned tab enters the bottom of its band.
func TestRunnerUnpinGoesBottomOfBand(t *testing.T) {
	t.Parallel()
	h, fw, _ := bandsWorld(t)
	h.subjectOfTheme(h.themeOf("Hedge"), "sub_p")
	h.setSubjectStatus("sub_p", "designing")
	h.sessionOf("P", "sub_p")
	fw.pin("P")
	h.converge(10)
	if got := fw.members("Hedge"); !slices.Equal(got, []string{"P", "B1", "D1", "U1", "I1"}) {
		t.Fatalf("pinned P not at the head of Hedge: %q", got)
	}
	h.pastGrace()             // a young tab keeps its slot (D40)
	fw.ws["P"].Pinned = false // unpinned by hand
	h.converge(10)
	if got := fw.members("Hedge"); !slices.Equal(got, []string{"B1", "D1", "P", "U1", "I1"}) {
		t.Fatalf("Hedge = %q, want P at the bottom of the designing band", got)
	}
	h.assertQuiet("after the unpin")
}

// D3: a pin by hand on a bound tab outside its folder files it; cmux puts
// an added pinned tab at the head (P24), so no repin.
func TestRunnerHandPinIsFiled(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t, noSort)
	h.sessionIn("WS-A", "Hedge")
	fw.pin("WS-A")
	h.converge(10)
	if fw.folderOf("WS-A") != "Hedge" || fw.adds("WS-A") != 1 {
		t.Fatalf("pinned bound tab: folder %q, adds %d", fw.folderOf("WS-A"), fw.adds("WS-A"))
	}
	for _, c := range fw.calls {
		if f := strings.Fields(c); len(f) > 0 && (f[0] == "pin" || f[0] == "unpin") {
			t.Fatalf("a pin written by the loop: %q", c)
		}
	}
	h.assertQuiet("pinned filed")
}

// D18: a pinned tab whose subject is in an open focus is filed in the focus.
func TestRunnerPinnedInFocusFiledInFocus(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t, noSort)
	h.sessionIn("WS-P", "Hedge")
	h.converge(10)
	fw.pin("WS-P")
	h.focusOn(offsite, fTheme(h.themeOf("Hedge")))
	h.converge(12)
	if fw.folderOf("WS-P") != offsite {
		t.Fatalf("pinned in focus filed in %q", fw.folderOf("WS-P"))
	}
}

// Review Focus 5: a tab born under its caller (⌘N afterCurrent) then bound
// to a subject of the same band stays under its caller.
func TestRunnerBornUnderCallerStaysInBand(t *testing.T) {
	t.Parallel()
	h, fw, subs := bandsWorld(t)
	h.sessionOf("I2", subs["I1"])
	h.converge(10)
	fw.bornNextTo("N2", "I1")
	h.session("s-N2", "N2", "idle", true)
	if err := h.st.CreateCmuxBinding(&pigtail.SubjectCmuxBinding{CmuxWorkspaceID: "N2", SubjectID: subs["I1"], Source: "attach", BoundAt: h.now}); err != nil {
		t.Fatal(err)
	}
	h.converge(10)
	got := fw.members("Hedge")
	if i := slices.Index(got, "N2"); i < 1 || got[i-1] != "I1" {
		t.Fatalf("Hedge = %q, want N2 right under I1", got)
	}
}

// Review Focus 4: a daemon restart forgets the entering tabs; an unpinned tab
// left at the head joins its band by the relative order, one write, then
// nothing.
func TestRunnerRestartUnpinnedAtHeadConverges(t *testing.T) {
	t.Parallel()
	h, fw, subs := bandsWorld(t)
	h.sessionOf("I2", subs["I1"])
	h.converge(10)
	h.assertQuiet("I2 filed")
	moveBefore(fw, "I2", "B1")
	h.restart()
	mark := len(fw.folderWrites())
	h.converge(10)
	if got := fw.members("Hedge"); !slices.Equal(got, []string{"B1", "D1", "U1", "I2", "I1"}) {
		t.Fatalf("Hedge = %q, want I2 at the top of the inbox band", got)
	}
	if w := fw.folderWrites()[mark:]; len(w) != 1 {
		t.Fatalf("writes %q, want one", w)
	}
	h.assertQuiet("after the restart")
}

// D14: « Standby » is never sorted in bands.
func TestRunnerStandbyNeverBanded(t *testing.T) {
	t.Parallel()
	h, fw, _ := bandsWorld(t)
	h.putStandby("I1")
	h.converge(10)
	h.putStandby("B1")
	h.converge(10)
	if got := fw.members(space.Standby); !slices.Equal(got, []string{"I1", "B1"}) {
		t.Fatalf("Standby = %q, want [I1 B1] (no bands)", got)
	}
	h.assertQuiet("standby")
}

// D3: pinned tabs never bring back a « Prio » folder.
func TestRunnerNoPrioFolderEverCreated(t *testing.T) {
	t.Parallel()
	h, fw, _ := bandsWorld(t)
	fw.pin("B1")
	fw.pin("I1")
	h.converge(10)
	if fw.groupNamed(space.Prio) != nil {
		t.Fatalf("a « Prio » folder: %+v", fw.groups)
	}
	for _, c := range fw.folderWrites() {
		if strings.HasPrefix(c, "group-create "+space.Prio) {
			t.Fatalf("« Prio » created: %q", c)
		}
	}
}

// Inverted from TestStandbyPinnedStaysOnTop (D13) by D29 of
// sub_x000000000000014: a pinned tab whose session is in Standby has its
// Standby lifted once (Deps.StandbyPinned) and is filed in its area folder,
// never in « Standby »; the next passes write nothing.
func TestStandbyHandPinClearsStandby(t *testing.T) {
	t.Parallel()
	h, fw := standbyWorld(t)
	fw.pin("WS-S")
	h.converge(4)
	h.putStandby("WS-S")
	h.converge(10)
	if n := len(h.standbyPinned); n != 1 {
		t.Fatalf("StandbyPinned called %d times (%q), want once", n, h.standbyPinned)
	}
	if sb, _ := h.st.GetStandby("s-WS-S"); sb != nil {
		t.Fatalf("Standby kept: %+v", sb)
	}
	if got := fw.folderOf("WS-S"); got != "Hedge" {
		t.Fatalf("pinned tab in %q, want Hedge", got)
	}
	if !slices.ContainsFunc(h.logs, func(l string) bool { return strings.Contains(l, "standby cleared by pin: s-WS-S") }) {
		t.Fatalf("no log line: %q", h.logs)
	}
	h.assertQuiet("hand pin")
	if n := len(h.standbyPinned); n != 1 {
		t.Fatalf("StandbyPinned called again: %q", h.standbyPinned)
	}
}

// D29, the CLI's order: Standby set on an unpinned tab stays; a pin by hand
// posed afterwards lifts it, once.
func TestRunnerCliStandbyOnUnpinnedTabKept(t *testing.T) {
	t.Parallel()
	h, fw := standbyWorld(t)
	h.putStandby("WS-S")
	h.converge(10)
	h.assertQuiet("standby")
	if len(h.standbyPinned) != 0 || fw.folderOf("WS-S") != space.Standby {
		t.Fatalf("unpinned in Standby: lifts %q, folder %q", h.standbyPinned, fw.folderOf("WS-S"))
	}
	if sb, _ := h.st.GetStandby("s-WS-S"); sb == nil {
		t.Fatal("Standby lost")
	}
	fw.pin("WS-S")
	h.converge(10)
	h.assertQuiet("pinned")
	if len(h.standbyPinned) != 1 {
		t.Fatalf("StandbyPinned = %q, want once", h.standbyPinned)
	}
	if sb, _ := h.st.GetStandby("s-WS-S"); sb != nil {
		t.Fatalf("Standby kept after the pin: %+v", sb)
	}
}

// The CLI's order on a pinned tab (D18): unpin, then Standby. The next fast
// pass still holds the listing cached before the unpin; it must not read that
// stale pin as a hand pin (D29): the lift decides on a fresh listing.
func TestRunnerCliStandbyOnPinnedTabKept(t *testing.T) {
	t.Parallel()
	h, fw := standbyWorld(t)
	fw.pin("WS-S")
	h.converge(4)
	h.assertQuiet("pinned")
	fw.ws["WS-S"].Pinned = false // CLI: unpin first
	h.putStandby("WS-S")         // then SetSessionStandby
	h.step(1 * time.Second)      // one fast pass, listing < 30 s old
	h.converge(10)
	if len(h.standbyPinned) != 0 {
		t.Fatalf("StandbyPinned called on a CLI standby: %q", h.standbyPinned)
	}
	if sb, _ := h.st.GetStandby("s-WS-S"); sb == nil {
		t.Fatal("Standby lifted by a stale pin")
	}
	if got := fw.folderOf("WS-S"); got != space.Standby {
		t.Fatalf("folder %q, want Standby", got)
	}
}

// D39, acceptance t1 item 3: without the sort, a folder never moves on its own.
// X (« Hedge », no pinned member) is above Y (« Terrace », a pinned bound
// member at its head); a band change in either, or a pin by hand in X, moves
// one tab alone (`reorder-workspace`), never a block: a whole reorder would
// lift the folder holding a pinned member (cmux probe P26 a).
func TestNoSortPinnedFolderNeverMoves(t *testing.T) {
	t.Parallel()
	h, fw := newFolderHarness(t, noSort)
	tx, ty := h.themeOf("Hedge"), h.themeOf("Terrace")
	subs := map[string]string{}
	for _, c := range []struct{ ws, theme, status string }{
		{"B1", tx, "building"}, {"D1", tx, "designing"}, {"I1", tx, "inbox"},
		{"YP", ty, "designing"}, {"D2", ty, "designing"}, {"I2", ty, "inbox"},
	} {
		id := "sub_" + strings.ToLower(c.ws)
		h.subjectOfTheme(c.theme, id)
		h.setSubjectStatus(id, c.status)
		h.sessionOf(c.ws, id)
		subs[c.ws] = id
		h.converge(10)
	}
	fw.pin("YP") // pinned by hand: last of the pinned block once filed (P24 a, e)
	h.converge(10)
	h.pastGrace()
	h.converge(10)
	h.assertQuiet("world")
	gx, gy := fw.groupNamed("Hedge"), fw.groupNamed("Terrace")
	seq := fw.blockSeq(win)
	if gx == nil || gy == nil || len(seq) < 2 || seq[0] != gx.ID || seq[1] != gy.ID {
		t.Fatalf("setup: blocks %q, want X (Hedge) then Y (Terrace): %q", seq, fw.windows[win])
	}
	if got := fw.members("Terrace"); len(got) != 3 || got[0] != "YP" || !fw.ws["YP"].Pinned {
		t.Fatalf("setup: Y = %q, want the pinned YP above D2 and I2", got)
	}
	if got := fw.members("Hedge"); !slices.Equal(got, []string{"B1", "D1", "I1"}) {
		t.Fatalf("setup: X = %q", got)
	}
	step := func(what string, change func(), wantWrites []string) {
		t.Helper()
		mark := len(fw.folderWrites())
		change()
		h.converge(10)
		if got := fw.blockSeq(win); !slices.Equal(got, seq) {
			t.Fatalf("%s: blocks %q, want %q — a folder moved: %q", what, got, seq, fw.windows[win])
		}
		if wantWrites != nil {
			if got := fw.folderWrites()[mark:]; !slices.Equal(got, wantWrites) {
				t.Fatalf("%s: writes %q, want %q", what, got, wantWrites)
			}
		}
	}
	// (1) a band change in X: I1 to the bottom of the building band.
	step("band change in X", func() { h.setSubjectStatus(subs["I1"], "building") }, []string{"place I1 --after B1"})
	if got := fw.members("Hedge"); !slices.Equal(got, []string{"B1", "I1", "D1"}) {
		t.Fatalf("X = %q, want [B1 I1 D1]", got)
	}
	// (2) a band change in Y: I2 under the pinned one, the pin kept at the head.
	step("band change in Y", func() { h.setSubjectStatus(subs["I2"], "building") }, []string{"place I2 --after YP"})
	if got := fw.members("Terrace"); !slices.Equal(got, []string{"YP", "I2", "D2"}) || !fw.ws["YP"].Pinned {
		t.Fatalf("Y = %q, want [YP I2 D2], YP pinned", got)
	}
	// (3) a pin by hand in X (its first: the head of X), then a band change in Y.
	step("pin in X", func() { fw.pin("D1") }, nil)
	step("band change in Y after the pin", func() { h.setSubjectStatus(subs["D2"], "inbox") }, nil)
	h.assertQuiet("after the moves")
}
