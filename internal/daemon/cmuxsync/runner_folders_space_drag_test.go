package cmuxsync

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// A hand drag into a space folder writes the space (lot 3 of
// sub_x000000000000033, spec rules 3–9): a bound tab's subject, an off-topic
// tab's declaration; elsewhere nothing is written and the tab is filed back.
// Against the simulated cmux, in both sort modes, with addLag under the guard.

// spaceCalls counts the drag writes the runner asks.
type spaceCalls struct{ subject, offTopic int }

// withSpaceDrag wires the three drag Deps over the harness's real store, as
// the daemon does (Tâche 7), with a SpaceScope stub: every space of
// testFolderOrder is perso's but « Garden », acme's.
func withSpaceDrag(c *spaceCalls) func(*Config, *Deps) {
	return func(_ *Config, d *Deps) {
		st, now := d.Store, d.Now
		d.SubjectSpaceSet = func(id, sp, from string) error {
			c.subject++
			_, err := st.SetSubjectSpaceByDrag(id, sp, from, now())
			return err
		}
		d.OffTopicSpaceSet = func(ws, sp, from string) error {
			c.offTopic++
			_, err := st.SetOffTopicSpaceByDrag(ws, sp, from, now())
			return err
		}
		d.SpaceScope = func(name string) (string, bool) {
			if name == space.NoSubject || name == space.Standby || !slices.Contains(testFolderOrder, name) {
				return "", false
			}
			if name == "Garden" {
				return "acme", true
			}
			return "perso", true
		}
	}
}

// withSettle: ArrangeSettle and FocusPoll well under the lag, so that a
// round can fall inside it.
func withSettle(c *Config, _ *Deps) {
	c.ArrangeSettle = lagUnderGuard() / 4
	c.FocusPoll = lagUnderGuard() / 4
}

// spaceDragWorld is filedWorld with the drag wired, the settle under the lag,
// and every engine group write lagged by lagUnderGuard().
func spaceDragWorld(t *testing.T, ns bool, opts ...func(*Config, *Deps)) (*folderHarness, *folderWorld, *spaceCalls) {
	t.Helper()
	c := &spaceCalls{}
	h, fw := filedWorld(t, ns, append([]func(*Config, *Deps){withSpaceDrag(c), withSettle}, opts...)...)
	fw.addLag = lagUnderGuard()
	return h, fw, c
}

// occupy gives folder a session of its own (a subject of that space), so that
// the folder exists: filedWorld only makes Terrace, Hedge and « Inbox ».
func occupy(h *folderHarness, folder string) string {
	h.t.Helper()
	ws := "WS-O" + strings.ToUpper(folder[:2]) // the fake reads lower case as no workspace ID
	h.sessionIn(ws, folder)
	h.converge(10)
	if h.fw.groupNamed(folder) == nil {
		h.t.Fatalf("instrument: no folder %s after a session in it", folder)
	}
	return ws
}

func gid(t *testing.T, fw *folderWorld, name string) string {
	t.Helper()
	gr := fw.groupNamed(name)
	if gr == nil {
		t.Fatalf("instrument: no group %s", name)
	}
	return gr.ID
}

// slowRound reaches the next slow listing (the one a drag is read at), then
// lets the rounds it owes run.
func slowRound(h *folderHarness) {
	h.step(31 * time.Second)
	h.converge(6)
}

// writesFor are the membership writes since mark aimed at ws (group-add,
// group-remove).
func writesFor(fw *folderWorld, mark int, ws string) []string {
	return slices.DeleteFunc(slices.Clone(fw.folderWrites()[mark:]), func(w string) bool { return !strings.HasSuffix(w, " "+ws) })
}

func logged(h *folderHarness, sub string) int {
	n := 0
	for _, l := range h.logs {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// keyOnAdd turns cmux key right after the engine's add of ws.
func keyOnAdd(fw *folderWorld, ws string) {
	fw.onWrite = func(call string) {
		if strings.HasPrefix(call, "group-add ") && strings.HasSuffix(call, " "+ws) {
			fw.key = true
			fw.onWrite = nil
		}
	}
}

func subjectSpace(h *folderHarness, id string) string {
	h.t.Helper()
	sub, err := h.st.GetSubject(id)
	if err != nil {
		h.t.Fatal(err)
	}
	return sub.Space
}

func offTopicSpace(h *folderHarness, ws string) string {
	h.t.Helper()
	o, err := h.st.GetOffTopic(ws)
	if err != nil {
		h.t.Fatal(err)
	}
	return o.Space
}

func journalKinds(h *folderHarness, kind string) []pigtail.JournalEntry {
	h.t.Helper()
	es, err := h.st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: kind})
	if err != nil {
		h.t.Fatal(err)
	}
	return es
}

func logNotes(h *folderHarness, sub string) []string {
	h.t.Helper()
	es, err := h.st.ListLog(sub, time.Time{})
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		if e.Kind == pigtail.LogNote {
			out = append(out, e.Text)
		}
	}
	return out
}

// noSpaceWrite fails if any drag write or trace happened.
func noSpaceWrite(t *testing.T, h *folderHarness, c *spaceCalls, what string) {
	t.Helper()
	if c.subject+c.offTopic != 0 {
		t.Fatalf("%s: %d subject and %d off-topic space writes, want none", what, c.subject, c.offTopic)
	}
	if n := len(journalKinds(h, pigtail.JournalKindSubjectSpaceDrag)) + len(journalKinds(h, pigtail.JournalKindOffTopicSpaceDrag)); n != 0 {
		t.Fatalf("%s: %d drag journal lines, want none", what, n)
	}
}

func TestSpaceDragBoundNoAddBack(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, _ := spaceDragWorld(t, ns)
		sub := h.subjectIn("Terrace")
		fw.drag("WS-A", gid(t, fw, "Hedge"))
		mark := len(fw.folderWrites())
		slowRound(h)
		if w := writesFor(fw, mark, "WS-A"); len(w) != 0 || fw.folderOf("WS-A") != "Hedge" {
			t.Fatalf("WS-A in %q, writes %q: it stays where it was dropped, no write", fw.folderOf("WS-A"), w)
		}
		if got := subjectSpace(h, sub); got != "Hedge" {
			t.Fatalf("subject space %q, want Hedge", got)
		}
		if logged(h, "space set by drag") != 1 {
			t.Fatalf("no log line: %q", h.logs)
		}
		if !slices.Contains(logNotes(h, sub), "space dragged: Terrace → Hedge") {
			t.Fatalf("subject log %q", logNotes(h, sub))
		}
		if js := journalKinds(h, pigtail.JournalKindSubjectSpaceDrag); len(js) != 1 || js[0].SubjectID != sub || js[0].Scope != "perso" {
			t.Fatalf("journal %+v", js)
		}
		h.assertQuiet("bound drag")
	})
}

func TestSpaceDragMovesSiblings(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, _ := spaceDragWorld(t, ns)
		h.sessionOn(winB, "WS-K", "Terrace") // the same subject, in window K
		h.converge(10)
		if got := fw.folderIn(winB, "WS-K"); got != "Terrace" {
			t.Fatalf("instrument: WS-K in K's %q", got)
		}
		fw.drag("WS-A", gid(t, fw, "Hedge"))
		for i := 0; i < 3; i++ {
			slowRound(h)
		}
		if a, b, k := fw.folderOf("WS-A"), fw.folderOf("WS-B"), fw.folderIn(winB, "WS-K"); a != "Hedge" || b != "Hedge" || k != "Hedge" {
			t.Fatalf("A in %q, B in %q, K's WS-K in %q: the subject's tabs follow into Hedge", a, b, k)
		}
		h.assertQuiet("siblings")
	})
}

func TestSpaceDragLandsInBand(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, _ := spaceDragWorld(t, ns)
		h.setSubjectStatus(h.subjectIn("Terrace"), pigtail.StatusBuilding)
		h.converge(10)
		h.assertQuiet("building world")
		fw.drag("WS-A", gid(t, fw, "Hedge")) // at the bottom of Hedge, under WS-C (inbox)
		if m := fw.members("Hedge"); !slices.Equal(m, []string{"WS-C", "WS-A"}) {
			t.Fatalf("instrument: Hedge %q", m)
		}
		mark := len(fw.folderWrites())
		slowRound(h)
		h.converge(6)
		m := fw.members("Hedge")
		ia, ib, ic := slices.Index(m, "WS-A"), slices.Index(m, "WS-B"), slices.Index(m, "WS-C")
		if ia < 0 || ib < 0 || ic < 0 || ia > ic || ib > ic {
			t.Fatalf("Hedge %q: the building tabs above the inbox one", m)
		}
		if w := writesFor(fw, mark, "WS-A"); len(w) != 0 {
			t.Fatalf("WS-A moved by %q: only an order write may place it", w)
		}
		h.assertQuiet("band")
	})
}

func TestSpaceDragConflictFirstWins(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, c := spaceDragWorld(t, ns)
		occupy(h, "Trellis")
		sub := h.subjectIn("Terrace")
		order := fw.windows[win]
		if mi, di := slices.Index(order, fw.groupNamed("Hedge").AnchorID), slices.Index(order, fw.groupNamed("Trellis").AnchorID); mi > di {
			t.Fatalf("instrument: Hedge (%d) not listed before Trellis (%d): %q", mi, di, order)
		}
		fw.drag("WS-A", gid(t, fw, "Hedge"))
		fw.drag("WS-B", gid(t, fw, "Trellis"))
		slowRound(h)
		if c.subject != 1 || subjectSpace(h, sub) != "Hedge" {
			t.Fatalf("%d writes, space %q: want one, Hedge (first listed)", c.subject, subjectSpace(h, sub))
		}
		if logged(h, "already moved this pass") != 1 {
			t.Fatalf("no conflict line: %q", h.logs)
		}
		h.converge(6)
		if fw.folderOf("WS-B") != "Hedge" {
			t.Fatalf("WS-B in %q, want it to follow the winner", fw.folderOf("WS-B"))
		}
		h.assertQuiet("conflict")
	})
}

// Two tabs of one subject dropped in the same folder in one listing are one
// gesture: the second is no conflict, so nothing is logged for it.
func TestSpaceDragSiblingsSameFolderNoConflictLine(t *testing.T) { // mutation: drop the same-folder check → a « already moved » line
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, c := spaceDragWorld(t, ns)
		sub := h.subjectIn("Terrace")
		fw.drag("WS-A", gid(t, fw, "Hedge"))
		fw.drag("WS-B", gid(t, fw, "Hedge"))
		slowRound(h)
		if c.subject != 1 || subjectSpace(h, sub) != "Hedge" {
			t.Fatalf("%d writes, space %q: want one, Hedge", c.subject, subjectSpace(h, sub))
		}
		if n := logged(h, "already moved this pass"); n != 0 {
			t.Fatalf("%d conflict lines for a same-folder drop: %q", n, h.logs)
		}
		h.converge(6)
		h.assertQuiet("same folder")
	})
}

func TestSpaceDragBackToThemeClears(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, _ := spaceDragWorld(t, ns)
		sub := h.subjectIn("Terrace")
		fw.add(win, "OT", "OT") // keeps « Terrace » alive once A and B leave it
		h.offTopicAs("OT", "perso", "Terrace", "r")
		h.setSubjectSpace(sub, "Hedge")
		h.converge(10)
		if fw.folderOf("WS-A") != "Hedge" || fw.folderOf("OT") != "Terrace" {
			t.Fatalf("instrument: WS-A in %q", fw.folderOf("WS-A"))
		}
		fw.drag("WS-A", gid(t, fw, "Terrace"))
		slowRound(h)
		if got := subjectSpace(h, sub); got != "" {
			t.Fatalf("space %q, want '' (its theme's)", got)
		}
		if !slices.Contains(logNotes(h, sub), "space dragged: Hedge → (theme: Terrace)") {
			t.Fatalf("subject log %q", logNotes(h, sub))
		}
		h.converge(6)
		if fw.folderOf("WS-B") != "Terrace" {
			t.Fatalf("WS-B in %q", fw.folderOf("WS-B"))
		}
		h.assertQuiet("back to theme")
	})
}

func TestSpaceDragFocusWins(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, _ := spaceDragWorld(t, ns)
		sub := h.subjectIn("Terrace")
		h.focusOn("F", fSubject(sub))
		h.converge(10)
		if fw.folderOf("WS-A") != "F" {
			t.Fatalf("instrument: WS-A in %q", fw.folderOf("WS-A"))
		}
		fw.drag("WS-A", gid(t, fw, "Hedge"))
		slowRound(h)
		h.converge(6)
		if got := subjectSpace(h, sub); got != "Hedge" {
			t.Fatalf("space %q, want Hedge written", got)
		}
		if fw.folderOf("WS-A") != "F" {
			t.Fatalf("WS-A in %q, want back in the focus", fw.folderOf("WS-A"))
		}
		h.assertQuiet("focus wins")
	})
}

func TestSpaceDragFromStandby(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, _ := spaceDragWorld(t, ns)
		sub := h.subjectIn("Terrace")
		h.putStandby("WS-A")
		h.converge(10)
		if fw.folderOf("WS-A") != space.Standby {
			t.Fatalf("instrument: WS-A in %q", fw.folderOf("WS-A"))
		}
		fw.drag("WS-A", gid(t, fw, "Hedge"))
		slowRound(h)
		h.converge(6)
		if got := subjectSpace(h, sub); got != "Hedge" {
			t.Fatalf("space %q, want Hedge", got)
		}
		if fw.folderOf("WS-A") != space.Standby {
			t.Fatalf("WS-A in %q, want back in « Standby »", fw.folderOf("WS-A"))
		}
		h.assertQuiet("from standby")
	})
	t.Run("maintenance", func(t *testing.T) {
		bothModes(t, func(t *testing.T, ns bool) {
			h, fw, _ := spaceDragWorld(t, ns, withMacNeed)
			sub := h.subjectIn("Terrace")
			h.macWait("WS-A", "90 min")
			h.converge(10)
			if fw.folderOf("WS-A") != space.MacMaintenance {
				t.Fatalf("instrument: WS-A in %q", fw.folderOf("WS-A"))
			}
			fw.drag("WS-A", gid(t, fw, "Hedge"))
			slowRound(h)
			h.converge(6)
			if got := subjectSpace(h, sub); got != "Hedge" {
				t.Fatalf("space %q, want Hedge", got)
			}
			if fw.folderOf("WS-A") != space.MacMaintenance {
				t.Fatalf("WS-A in %q, want back in « Maintenance »", fw.folderOf("WS-A"))
			}
			h.assertQuiet("from maintenance")
		})
	})
	// A drag into the subject's effective space is no change: nothing logged,
	// the subject not marked moved, so a real drag of a sibling in the same
	// listing, listed after it, still applies.
	t.Run("into effective space", func(t *testing.T) {
		bothModes(t, func(t *testing.T, ns bool) {
			h, fw, c := spaceDragWorld(t, ns)
			sub := h.subjectIn("Terrace")
			occupy(h, "Trellis")
			h.setSubjectSpace(sub, "Hedge")
			h.sessionIn("WS-A2", "Terrace") // a third tab of the subject
			h.putStandby("WS-A2")
			h.converge(10)
			if fw.folderOf("WS-A2") != space.Standby || fw.folderOf("WS-B") != "Hedge" {
				t.Fatalf("instrument: WS-A2 in %q, WS-B in %q", fw.folderOf("WS-A2"), fw.folderOf("WS-B"))
			}
			fw.drag("WS-A2", gid(t, fw, "Hedge")) // its effective space
			fw.drag("WS-B", gid(t, fw, "Trellis"))
			if a2, b := h.index("WS-A2"), h.index("WS-B"); a2 > b {
				t.Fatalf("instrument: WS-A2 (%d) not listed before WS-B (%d): %q", a2, b, fw.windows[win])
			}
			slowRound(h)
			if c.subject != 1 || subjectSpace(h, sub) != "Trellis" {
				t.Fatalf("%d writes, space %q: the sibling's drag to Trellis must apply", c.subject, subjectSpace(h, sub))
			}
			if n := logged(h, "already moved this pass") + logged(h, "space drag refused"); n != 0 {
				t.Fatalf("a no-change drag logged: %q", h.logs)
			}
			h.converge(6)
			if fw.folderOf("WS-A2") != space.Standby {
				t.Fatalf("WS-A2 in %q, want back in « Standby »", fw.folderOf("WS-A2"))
			}
		})
	})
}

func TestSpaceDragOffTopicRewrites(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, _ := spaceDragWorld(t, ns)
		fw.add(win, "OT", "OT")
		h.offTopicAs("OT", "perso", "Terrace", "r")
		h.converge(10)
		if fw.folderOf("OT") != "Terrace" {
			t.Fatalf("instrument: OT in %q", fw.folderOf("OT"))
		}
		fw.drag("OT", gid(t, fw, "Hedge"))
		slowRound(h)
		h.converge(6)
		if got := offTopicSpace(h, "OT"); got != "Hedge" {
			t.Fatalf("off-topic space %q, want Hedge", got)
		}
		if fw.folderOf("OT") != "Hedge" {
			t.Fatalf("OT in %q, want it to stay in Hedge", fw.folderOf("OT"))
		}
		if got := offTopicSpace(h, "Shed"); got != "" {
			t.Fatalf("Shed space %q, want untouched", got)
		}
		if js := journalKinds(h, pigtail.JournalKindOffTopicSpaceDrag); len(js) != 1 || js[0].Scope != "perso" {
			t.Fatalf("journal %+v", js)
		}
		h.assertQuiet("off-topic drag")
	})
}

func TestSpaceDragOffTopicMasterReadsAnySpace(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, _ := spaceDragWorld(t, ns)
		occupy(h, "Garden")
		fw.add(win, "OT", "OT")
		h.offTopicAs("OT", "perso", "Terrace", "r")
		h.converge(10)
		fw.drag("OT", gid(t, fw, "Garden"))
		slowRound(h)
		h.converge(6)
		if got := offTopicSpace(h, "OT"); got != "Garden" {
			t.Fatalf("off-topic space %q, want Garden (the master reads every scope)", got)
		}
		h.assertQuiet("master any space")
	})
}

func TestSpaceDragThemeSpaceUntouched(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, _ := spaceDragWorld(t, ns)
		sub := h.subjectIn("Terrace")
		fw.drag("WS-A", gid(t, fw, "Hedge"))
		slowRound(h)
		h.converge(6)
		if subjectSpace(h, sub) != "Hedge" {
			t.Fatalf("space %q, want Hedge written by the drag", subjectSpace(h, sub))
		}
		th, err := h.st.GetTheme(h.themeOf("Terrace"))
		if err != nil || th.Space != "Terrace" {
			t.Fatalf("theme space %q (%v): a drag never moves the theme", th.Space, err)
		}
		h.setThemeSpace(h.themeOf("Terrace"), "Trellis")
		h.converge(10)
		if fw.folderOf("WS-A") != "Hedge" || fw.folderOf("WS-B") != "Hedge" {
			t.Fatalf("A in %q, B in %q: a subject with its own space ignores its theme's change", fw.folderOf("WS-A"), fw.folderOf("WS-B"))
		}
		h.assertQuiet("theme untouched")
	})
}

func TestSpaceDragTwiceTwoJournalLines(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, _ := spaceDragWorld(t, ns)
		occupy(h, "Trellis")
		fw.drag("WS-A", gid(t, fw, "Hedge"))
		slowRound(h)
		slowRound(h)
		fw.drag("WS-A", gid(t, fw, "Trellis"))
		slowRound(h)
		js := journalKinds(h, pigtail.JournalKindSubjectSpaceDrag)
		if len(js) != 2 || js[0].ID == js[1].ID {
			t.Fatalf("journal %+v, want two lines with distinct ids", js)
		}
		if got := subjectSpace(h, h.subjectIn("Terrace")); got != "Trellis" {
			t.Fatalf("space %q, want Trellis", got)
		}
	})
}

func TestSpaceDragWriteFailsSkipsRound(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, _ := spaceDragWorld(t, ns)
		sub := h.subjectIn("Terrace")
		calls := 0
		set := h.r.deps.SubjectSpaceSet
		h.r.deps.SubjectSpaceSet = func(id, sp, from string) error {
			calls++
			if calls == 1 {
				return errors.New("SQLITE_BUSY")
			}
			return set(id, sp, from)
		}
		fw.drag("WS-A", gid(t, fw, "Hedge"))
		mark := len(fw.folderWrites())
		slowRound(h)
		if calls != 1 || subjectSpace(h, sub) != "" {
			t.Fatalf("first round: %d calls, space %q", calls, subjectSpace(h, sub))
		}
		if w := writesFor(fw, mark, "WS-A"); len(w) != 0 || fw.folderOf("WS-A") != "Hedge" {
			t.Fatalf("write failing: WS-A in %q, writes %q; want neither filed back nor moved", fw.folderOf("WS-A"), w)
		}
		if n := logged(h, "space drag: "); n != 1 || logged(h, "SQLITE_BUSY") != 1 {
			t.Fatalf("%d failure lines: %q", n, h.logs)
		}
		slowRound(h)
		if calls != 2 || subjectSpace(h, sub) != "Hedge" {
			t.Fatalf("next slow round: %d calls, space %q; want judged again and written", calls, subjectSpace(h, sub))
		}
		if fw.folderOf("WS-A") != "Hedge" {
			t.Fatalf("WS-A in %q", fw.folderOf("WS-A"))
		}
		h.assertQuiet("write fails")
	})
}

func TestSpaceDragOtherScopeRefused(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, c := spaceDragWorld(t, ns)
		occupy(h, "Garden")
		fw.drag("WS-A", gid(t, fw, "Garden"))
		slowRound(h)
		h.converge(6)
		if logged(h, "space drag refused: Garden is not a space of scope perso") != 1 {
			t.Fatalf("no refusal line: %q", h.logs)
		}
		noSpaceWrite(t, h, c, "other scope")
		if fw.folderOf("WS-A") != "Terrace" {
			t.Fatalf("WS-A in %q, want back in Terrace", fw.folderOf("WS-A"))
		}
		h.assertQuiet("other scope")
	})
}

func TestSpaceDragOffTopicOtherScopeRefused(t *testing.T) {
	scopetest.Three(t)
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, c := spaceDragWorld(t, ns)
		occupy(h, "Garden")
		fw.add(win, "OT", "OT")
		h.offTopicAs("OT", "globex", "Terrace", "r")
		h.converge(10)
		if fw.folderOf("OT") != "Terrace" {
			t.Fatalf("instrument: OT in %q", fw.folderOf("OT"))
		}
		fw.drag("OT", gid(t, fw, "Garden"))
		slowRound(h)
		h.converge(6)
		if logged(h, "space drag refused") != 1 {
			t.Fatalf("no refusal line: %q", h.logs)
		}
		noSpaceWrite(t, h, c, "off-topic other scope")
		if offTopicSpace(h, "OT") != "Terrace" || fw.folderOf("OT") != "Terrace" {
			t.Fatalf("space %q, OT in %q: want unchanged, filed back", offTopicSpace(h, "OT"), fw.folderOf("OT"))
		}
		h.assertQuiet("off-topic other scope")
	})
}

// Regression guards (green on the base once it compiles; the mutation that
// turns each red is named).

func TestSpaceDragElsewhereReturns(t *testing.T) { // mutation: spaces = every d.folders → a refusal line
	t.Parallel()
	cases := []struct {
		name string
		opts []func(*Config, *Deps)
		set  func(t *testing.T, h *folderHarness, fw *folderWorld)
		home string
	}{
		{name: "inbox", home: "Terrace", set: func(t *testing.T, h *folderHarness, fw *folderWorld) {
			fw.drag("WS-A", gid(t, fw, space.NoSubject))
		}},
		{name: "focus", home: "Terrace", set: func(t *testing.T, h *folderHarness, fw *folderWorld) {
			occupy(h, "Trellis")
			h.focusOn("F", fSubject(h.subjectIn("Trellis")))
			h.converge(10)
			fw.drag("WS-A", gid(t, fw, "F"))
		}},
		{name: "standby", home: "Terrace", set: func(t *testing.T, h *folderHarness, fw *folderWorld) {
			occupy(h, "Trellis")
			h.putStandby("WS-OTR")
			h.converge(10)
			fw.drag("WS-A", gid(t, fw, space.Standby))
		}},
		{name: "maintenance", home: "Terrace", opts: []func(*Config, *Deps){withMacNeed}, set: func(t *testing.T, h *folderHarness, fw *folderWorld) {
			occupy(h, "Trellis")
			h.macWait("WS-OTR", "90 min")
			h.converge(10)
			fw.drag("WS-A", gid(t, fw, space.MacMaintenance))
		}},
		{name: "no folder", home: "Terrace", set: func(t *testing.T, h *folderHarness, fw *folderWorld) {
			fw.dragOut("WS-A")
		}},
		{name: "manual group", home: "Terrace", set: func(t *testing.T, h *folderHarness, fw *folderWorld) {
			fw.manualGroup("MG", "Hedge", "WS-M", false, "WS-A")
		}},
		{name: "off-topic to inbox", home: "Terrace", set: func(t *testing.T, h *folderHarness, fw *folderWorld) {
			fw.add(win, "OT", "OT")
			h.offTopicAs("OT", "perso", "Terrace", "r")
			h.converge(10)
			fw.drag("OT", gid(t, fw, space.NoSubject))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bothModes(t, func(t *testing.T, ns bool) {
				h, fw, c := spaceDragWorld(t, ns, tc.opts...)
				tc.set(t, h, fw)
				ws := "WS-A"
				if strings.HasPrefix(tc.name, "off-topic") {
					ws = "OT"
				}
				slowRound(h)
				h.converge(6)
				noSpaceWrite(t, h, c, tc.name)
				if n := logged(h, "space drag"); n != 0 {
					t.Fatalf("%s: drag lines %q", tc.name, h.logs)
				}
				if got := fw.folderOf(ws); got != tc.home {
					t.Fatalf("%s: %s in %q, want back in %s", tc.name, ws, got, tc.home)
				}
			})
		})
	}
}

func TestSpaceDragPinnedNotRead(t *testing.T) { // mutation: drop !d.pinned[ws] → a write
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, c := spaceDragWorld(t, ns)
		fw.pin("WS-A")
		h.converge(6)
		fw.drag("WS-A", gid(t, fw, "Hedge"))
		slowRound(h)
		h.converge(6)
		noSpaceWrite(t, h, c, "pinned")
		if w := fw.ws["WS-A"]; w == nil || !w.Pinned {
			t.Fatal("WS-A no longer pinned")
		}
		if got := fw.folderOf("WS-A"); got != "Terrace" {
			t.Fatalf("pinned WS-A in %q, want filed back in Terrace", got)
		}
	})
}

func TestSpaceDragUnboundWritesNothing(t *testing.T) { // D2·t2
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, c := spaceDragWorld(t, ns)
		fw.add(win, "WS-Y", "WS-Y")
		listedOnce(h, "WS-Y")
		fw.drag("WS-Y", gid(t, fw, "Hedge"))
		mark := len(fw.folderWrites())
		slowRound(h)
		noSpaceWrite(t, h, c, "young unbound")
		if w := writesFor(fw, mark, "WS-Y"); len(w) != 0 || fw.folderOf("WS-Y") != "Hedge" {
			t.Fatalf("young WS-Y in %q, writes %q: it stays where dropped, no op", fw.folderOf("WS-Y"), w)
		}
		h.pastGrace()
		h.converge(10)
		noSpaceWrite(t, h, c, "old unbound")
		if got := fw.folderOf("WS-Y"); got != space.NoSubject {
			t.Fatalf("old WS-Y in %q, want « Inbox »", got)
		}
	})
}

func TestSpaceDragBeforeBindWritesNothing(t *testing.T) { // mutation: ignore ident → Hedge written
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, c := spaceDragWorld(t, ns)
		fw.add(win, "WS-Y", "WS-Y")
		listedOnce(h, "WS-Y")
		fw.drag("WS-Y", gid(t, fw, "Hedge"))
		h.bindIn("WS-Y", "Terrace") // bound before the next listing
		slowRound(h)
		h.converge(6)
		noSpaceWrite(t, h, c, "drag before bind")
		if got := fw.folderOf("WS-Y"); got != "Terrace" {
			t.Fatalf("WS-Y in %q, want its subject's space", got)
		}
	})
}

func TestSpaceDragBeforeDeclareWritesNothing(t *testing.T) { // mutation: ignore ident → Hedge written
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, c := spaceDragWorld(t, ns)
		fw.add(win, "WS-Y", "WS-Y")
		listedOnce(h, "WS-Y")
		fw.drag("WS-Y", gid(t, fw, "Hedge"))
		h.offTopicAs("WS-Y", "perso", "Trellis", "r")
		slowRound(h)
		h.converge(6)
		noSpaceWrite(t, h, c, "drag before declare")
		if got := offTopicSpace(h, "WS-Y"); got != "Trellis" {
			t.Fatalf("space %q, want Trellis kept", got)
		}
	})
}

func TestSpaceNoFlapAddLag(t *testing.T) { // mutation: DragReadGuard 0 (3c for 3b) → Terrace written
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, c := spaceDragWorld(t, ns)
		var addAt time.Time
		fw.onWrite = func(call string) {
			if addAt.IsZero() && strings.HasPrefix(call, "group-add ") && strings.HasSuffix(call, " WS-A") {
				addAt = h.now
			}
		}
		h.setThemeSpace(h.themeOf("Terrace"), "Hedge")
		for i := 0; i < 12; i++ {
			h.step(lagUnderGuard() / 5) // rounds inside the lag: the list shows the add late
		}
		h.converge(6)
		if addAt.IsZero() {
			t.Fatal("instrument: the engine never added WS-A")
		}
		inLag := slices.ContainsFunc(fw.groupListsAt, func(at time.Time) bool { return !at.Before(addAt) && at.Before(addAt.Add(fw.addLag)) })
		if !inLag {
			t.Fatalf("instrument: no group list read inside the lag after %v (%v)", addAt, fw.groupListsAt)
		}
		noSpaceWrite(t, h, c, "add lag")
		if fw.folderOf("WS-A") != "Hedge" {
			t.Fatalf("WS-A in %q", fw.folderOf("WS-A"))
		}
		h.assertQuiet("add lag")
	})
}

func TestSpaceNoFlapWhileKey(t *testing.T) { // mutation: drop noteSpaceWrite's defer → Hedge written
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, c := spaceDragWorld(t, ns)
		occupy(h, "Trellis")
		sub := h.subjectIn("Terrace")
		thm := h.themeOf("Terrace")
		keyOnAdd(fw, "WS-A")
		h.setThemeSpace(thm, "Hedge") // the engine adds WS-A to Hedge, cmux turns key
		h.converge(4)
		if !fw.key {
			t.Fatal("instrument: cmux not key after the add")
		}
		h.setThemeSpace(thm, "Trellis") // while key: the filing changes
		h.converge(2)
		fw.key = false
		h.step(DragReadGuard + time.Second)
		h.converge(8)
		noSpaceWrite(t, h, c, "while key")
		if got := subjectSpace(h, sub); got != "" {
			t.Fatalf("space %q, want '': the engine's own add read as a drag", got)
		}
		if fw.folderOf("WS-A") != "Trellis" {
			t.Fatalf("WS-A in %q, want Trellis", fw.folderOf("WS-A"))
		}
		h.assertQuiet("while key")
	})
}

func TestSpaceDragRestartJudgesNothing(t *testing.T) {
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, c := spaceDragWorld(t, ns)
		fw.drag("WS-A", gid(t, fw, "Hedge"))
		h.restart() // before any listing judged it
		slowRound(h)
		h.converge(6)
		noSpaceWrite(t, h, c, "restart")
		if got := fw.folderOf("WS-A"); got != "Terrace" {
			t.Fatalf("WS-A in %q, want filed back", got)
		}
	})
}

// A folder stop then resume makes the next listing the base (Tâche 5 note):
// a tab moved while the folders were stopped is no hand drag.
func TestSpaceDragAcrossFolderResumeJudgesNothing(t *testing.T) { // mutation: keep spaceBase on resume → Hedge written
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, c := spaceDragWorld(t, ns)
		h.r.stopFolders("test stop")
		h.converge(2)
		fw.drag("WS-A", gid(t, fw, "Hedge"))
		slowRound(h)
		h.r.SetEnabled(true)
		slowRound(h)
		h.converge(6)
		noSpaceWrite(t, h, c, "resume")
		if got := fw.folderOf("WS-A"); got != "Terrace" {
			t.Fatalf("WS-A in %q, want filed back", got)
		}
	})
}

// listedOnce lets a trusted listing show ws before the gesture (Tâche 6
// note): a tab never listed is based, never judged.
func listedOnce(h *folderHarness, ws string) {
	h.t.Helper()
	slowRound(h)
	if _, ok := h.r.wins[win].spaceBase[ws]; !ok {
		h.t.Fatalf("instrument: %s not in the drag base after a slow round", ws)
	}
}

// Switched off then on again (Tâche 5 note, same gesture as the folders
// resume): a tab moved while the loop was off is no hand drag.
func TestSpaceDragAcrossLoopOffOnJudgesNothing(t *testing.T) { // mutation: keep spaceBase on off → on → Hedge written
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, c := spaceDragWorld(t, ns)
		listedOnce(h, "WS-A") // the base predates the switch-off
		h.r.SetEnabled(false)
		h.converge(2)
		fw.drag("WS-A", gid(t, fw, "Hedge"))
		h.step(time.Minute)
		h.r.SetEnabled(true)
		slowRound(h)
		h.converge(6)
		noSpaceWrite(t, h, c, "off → on")
		if got := fw.folderOf("WS-A"); got != "Terrace" {
			t.Fatalf("WS-A in %q, want filed back", got)
		}
	})
}

// An off-topic tab whose space is inferred from the subject its reason cites
// (no explicit space), dragged from Standby into that very folder, is no
// change: no write, no journal line.
func TestSpaceDragOffTopicIntoInferredSpaceIsNoChange(t *testing.T) { // mutation: drop the m.folder == f.OffTopic guard → a write
	t.Parallel()
	bothModes(t, func(t *testing.T, ns bool) {
		h, fw, c := spaceDragWorld(t, ns)
		sub := h.subjectIn("Terrace")
		fw.add(win, "OT", "OT")
		h.session("s-OT", "OT", "idle", true)
		h.offTopicAs("OT", "perso", "", "revue de "+sub)
		h.putStandby("OT")
		h.converge(10)
		if fw.folderOf("OT") != space.Standby {
			t.Fatalf("instrument: OT in %q, want Standby", fw.folderOf("OT"))
		}
		fw.drag("OT", gid(t, fw, "Terrace")) // the space inferred from sub_…
		slowRound(h)
		h.converge(6)
		noSpaceWrite(t, h, c, "drag into the inferred space")
		if got := offTopicSpace(h, "OT"); got != "" {
			t.Fatalf("off-topic space %q, want it left unset", got)
		}
		if fw.folderOf("OT") != space.Standby {
			t.Fatalf("OT in %q, want back in Standby", fw.folderOf("OT"))
		}
	})
}
