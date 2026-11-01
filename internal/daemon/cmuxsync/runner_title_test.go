package cmuxsync

import (
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func revertNotes(h *harness) (out []string) {
	for _, n := range h.notes() {
		if strings.Contains(n, "titre de l'onglet") {
			out = append(out, n)
		}
	}
	return out
}

// D5: a bound workspace renamed by hand gets its short title back at the
// next trusted pass, with one note; then it stays put (no fight).
func TestRunnerBoundRenameReverted(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	w := h.world.add(winA, wsA, "Metric login")
	w.CustomTitle, w.HasCustomTitle = "Metric login", true
	h.bind(wsA)
	h.step(0) // pigtail poses "Sidebar pigtail", prev_title = "Metric login"

	w.Title, w.CustomTitle = "Mon nom", "Mon nom"
	h.world.calls = nil
	h.step(31 * time.Second) // slow tick: fresh listing, revert
	if got := h.world.writes(); countPrefix(got, "rename ") != 1 || got[0] != "rename "+wsA+" Sidebar pigtail" {
		t.Fatalf("revert writes %q", got)
	}
	b := h.binding(wsA)
	if b.TitleOwner != pigtail.CmuxOwnerPigtail || b.AppliedTitle != "Sidebar pigtail" || b.PrevTitle != "Metric login" || !b.PrevTitleCustom {
		t.Fatalf("binding %+v", b)
	}
	want := "titre de l'onglet remis à « Sidebar pigtail » (renommé à la main en « Mon nom ») — pour renommer, change le titre court"
	if n := revertNotes(h); len(n) != 1 || n[0] != want {
		t.Fatalf("notes %q", n)
	}

	// Stable: fast passes on the old listing and later fresh listings
	// neither rename again nor note again.
	h.world.calls = nil
	for i := 0; i < 70; i++ {
		h.step(time.Second)
	}
	if got := h.world.writes(); countPrefix(got, "rename ") != 0 {
		t.Fatalf("renamed again: %q", got)
	}
	if n := revertNotes(h); len(n) != 1 {
		t.Fatalf("noted again: %q", n)
	}

	// Detach still gives back the name from before the link, never the manual one.
	b = h.binding(wsA) // read before the delete, as TestRunnerRenamesBoundWorkspaceAndCapturesPrevTitle
	if err := h.st.DeleteCmuxBinding(subID, wsA); err != nil {
		t.Fatal(err)
	}
	h.r.Released(b)
	h.world.calls = nil
	h.step(time.Second)
	if got := h.world.writes(); countPrefix(got, "rename "+wsA+" Metric login") != 1 {
		t.Fatalf("restore writes %q", got)
	}
}

// Name cleared in cmux (automatic title back): also reverted, its own note.
func TestRunnerBoundClearedNameReverted(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.bind(wsA)
	h.step(0)
	w := h.world.ws[wsA]
	w.Title, w.CustomTitle, w.HasCustomTitle = "✳ auto", "", false
	h.world.calls = nil
	h.step(31 * time.Second)
	if got := h.world.writes(); countPrefix(got, "rename "+wsA+" Sidebar pigtail") != 1 {
		t.Fatalf("writes %q", got)
	}
	if n := revertNotes(h); len(n) != 1 || !strings.Contains(n[0], "(nom effacé à la main)") {
		t.Fatalf("notes %q", n)
	}
}

// Something undoes every rename (cmux echo, another tool): the loop stops
// after maxTitleReverts in titleRevertWindow — one revert note, one breaker
// note — and a new short title re-arms it.
func TestRunnerTitleRevertNoFightLoop(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.bind(wsA)
	h.step(0)
	h.world.onList = func() {
		w := h.world.ws[wsA]
		w.Title, w.CustomTitle, w.HasCustomTitle = "Mon nom", "Mon nom", true
	}
	h.world.calls = nil
	for i := 0; i < 40; i++ { // 40 fresh listings, ~20 min
		h.step(31 * time.Second)
	}
	if n := countPrefix(h.world.writes(), "rename "); n != maxTitleReverts {
		t.Fatalf("%d renames, want %d", n, maxTitleReverts)
	}
	if n := revertNotes(h); len(n) != 2 || !strings.Contains(n[0], "cesse de le remettre") { // newest first
		t.Fatalf("notes %q", n)
	}
	sub, _ := h.st.GetSubject(subID)
	sub.ShortTitle = "Autre titre"
	if err := h.st.UpdateSubject(sub); err != nil {
		t.Fatal(err)
	}
	h.world.calls = nil
	h.step(time.Second)
	if got := h.world.writes(); countPrefix(got, "rename "+wsA+" Autre titre") != 1 {
		t.Fatalf("new short title not posed: %q", got)
	}
}

// An unbound workspace renamed by hand is never touched (green on base: guard).
func TestRunnerUnboundRenameUntouched(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsB, "nas")
	h.bind(wsA)
	h.step(0)
	w := h.world.ws[wsB]
	w.Title, w.CustomTitle, w.HasCustomTitle = "#201 relance", "#201 relance", true
	h.world.calls = nil
	for i := 0; i < 70; i++ {
		h.step(time.Second)
	}
	for _, c := range h.world.writes() {
		if strings.Contains(c, wsB) && (strings.HasPrefix(c, "rename") || strings.HasPrefix(c, "clear-name")) {
			t.Fatalf("unbound workspace touched: %q", c)
		}
	}
}

// The loop's view carries the title the sidebar shows: the short title of a
// bound workspace (being put back), an unbound one's as cmux shows it.
func TestRunnerStateCarriesShownTitle(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsB, "✳ nas")
	h.bind(wsA)
	h.step(0)
	h.step(time.Second)
	byID := func() map[string]pigtail.CmuxSidebarWorkspace {
		got := map[string]pigtail.CmuxSidebarWorkspace{}
		for _, w := range h.r.State().Workspaces {
			got[w.CmuxWorkspaceID] = w
		}
		return got
	}
	got := byID()
	if w := got[wsA]; w.Title != "Sidebar pigtail" || w.TitleOwner != pigtail.CmuxOwnerPigtail {
		t.Fatalf("bound: %+v", w)
	}
	if w := got[wsB]; w.Title != "nas" || w.DisplayName != "" {
		t.Fatalf("unbound: %+v (display_name is the handler's)", w)
	}
	// Unbound, renamed by hand with a leading symbol: kept whole.
	w := h.world.ws[wsB]
	w.Title, w.CustomTitle, w.HasCustomTitle = "#201 relance", "#201 relance", true
	// Bound, renamed by hand: the view shows the short title being put back.
	a := h.world.ws[wsA]
	a.Title, a.CustomTitle = "fait", "fait"
	h.step(31 * time.Second)
	got = byID()
	if got[wsB].Title != "#201 relance" || got[wsA].Title != "Sidebar pigtail" {
		t.Fatalf("after renames: %+v / %+v", got[wsA], got[wsB])
	}
}
