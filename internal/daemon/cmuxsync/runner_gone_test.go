package cmuxsync

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// logCollector replaces the harness' silent Logf.
func (h *harness) collectLogs() *[]string {
	var logs []string
	h.r.deps.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	return &logs
}

func countContaining(lines []string, subs ...string) int {
	n := 0
outer:
	for _, l := range lines {
		for _, s := range subs {
			if !strings.Contains(l, s) {
				continue outer
			}
		}
		n++
	}
	return n
}

func hasWriteOn(calls []string, ws string) bool {
	for _, c := range calls {
		f := strings.Fields(c)
		if len(f) > 1 && f[1] == ws {
			return true
		}
	}
	return false
}

// CA1, apply path: a write answering not_found skips the tab, never pauses the loop.
func TestWriteToGoneWorkspaceKeepsLoopRunning(t *testing.T) {
	t.Parallel()
	cfg := on()
	cfg.ReconcileInterval = 5 * time.Second
	h := newHarness(t, cfg)
	logs := h.collectLogs()
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsB, "b")
	h.step(0) // healthy: listing and reconcile done, nothing to write

	h.world.goneOn = map[string]bool{wsA: true}
	h.session("sa", wsA, "waiting", true) // a pill to write on A and on B
	h.session("sb", wsB, "waiting", true)
	h.world.calls = nil
	h.step(time.Second)

	if !h.r.State().CmuxAvailable {
		t.Fatalf("loop paused by a closed tab; logs %q", *logs)
	}
	if n := countContaining(*logs, "writes paused") + countContaining(*logs, "listing failed"); n != 0 {
		t.Fatalf("paused or listing failed: %q", *logs)
	}
	if n := countContaining(*logs, "workspace gone"); n != 1 {
		t.Fatalf("want one workspace-gone line, got %d: %q", n, *logs)
	}
	if n := countContaining(*logs, "workspace gone", "list-status"); n != 0 {
		t.Fatalf("the line must name the write op, not list-status: %q", *logs)
	}
	if !hasWriteOn(h.world.writes(), wsB) {
		t.Fatalf("B not written in the same pass: %q", h.world.calls)
	}

	// Past ReconcileInterval, before the next listing: A is out of the cache.
	h.world.calls = nil
	for i := 0; i < 8; i++ {
		h.step(time.Second)
	}
	if hasWriteOn(h.world.calls, wsA) {
		t.Fatalf("A still touched after it went: %q", h.world.calls)
	}

	// Then really closed: the next listing does not bring it back.
	h.world.close(wsA)
	h.world.goneOn = nil
	h.world.calls = nil
	h.setStatus("sb", wsB, "idle") // B's pill changes: a write on B only
	for i := 0; i < 70; i++ {
		h.step(time.Second)
	}
	if hasWriteOn(h.world.calls, wsA) {
		t.Fatalf("A written after the listing: %q", h.world.calls)
	}
	if !hasWriteOn(h.world.writes(), wsB) {
		t.Fatalf("B no longer written: %q", h.world.calls)
	}
}

// CA2, restore path: the vanished tab is dropped, the others restored.
func TestRestoreOnGoneWorkspaceDropped(t *testing.T) {
	t.Parallel()
	cfg := on()
	cfg.ReconcileInterval = time.Hour
	h := newHarness(t, cfg)
	logs := h.collectLogs()
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsB, "b")
	h.step(0)

	h.world.goneOn = map[string]bool{wsA: true}
	for _, ws := range []string{wsA, wsB} {
		h.r.Released(restoreBinding(ws))
	}
	h.world.calls = nil
	h.step(time.Second)

	if !h.r.State().CmuxAvailable {
		t.Fatalf("loop paused by a closed tab; logs %q", *logs)
	}
	if n := countContaining(*logs, "writes paused"); n != 0 {
		t.Fatalf("paused: %q", *logs)
	}
	if n := countContaining(*logs, "workspace gone", "rename"); n != 1 {
		t.Fatalf("want one restore line naming rename: %q", *logs)
	}
	if got := h.world.ws[wsB].Title; got != "Mon nom" {
		t.Fatalf("B not restored: %q (calls %q)", got, h.world.calls)
	}
	h.world.calls = nil
	for i := 0; i < 3; i++ {
		h.step(time.Second)
	}
	if hasWriteOn(h.world.calls, wsA) {
		t.Fatalf("A requeued: %q", h.world.calls)
	}
}

// CA1, reconcile path: a list-status answering Tab not found skips that tab.
func TestReconcileOnGoneWorkspaceSkipsIt(t *testing.T) {
	t.Parallel()
	cfg := on()
	cfg.ReconcileInterval = 5 * time.Second
	h := newHarness(t, cfg)
	logs := h.collectLogs()
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsB, "b")
	h.step(0)
	h.world.goneOn = map[string]bool{wsA: true}
	h.world.calls = nil
	h.step(6 * time.Second) // reconcile due, listing not

	if !h.r.State().CmuxAvailable || countContaining(*logs, "listing failed") != 0 || countContaining(*logs, "writes paused") != 0 {
		t.Fatalf("reconcile on a gone tab broke the loop: %q", *logs)
	}
	if countPrefix(h.world.calls, "list-status "+wsB) != 1 {
		t.Fatalf("B not reconciled: %q", h.world.calls)
	}
	if n := countContaining(*logs, "workspace gone", "list-status"); n != 1 {
		t.Fatalf("want one list-status gone line: %q", *logs)
	}
	h.world.calls = nil
	h.step(6 * time.Second)
	if countPrefix(h.world.calls, "list-status "+wsA) != 0 {
		t.Fatalf("A still reconciled: %q", h.world.calls)
	}
}

// CA3: any other write error keeps pausing the loop.
func TestWriteKilledStillPauses(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	logs := h.collectLogs()
	h.world.add(winA, wsA, "a")
	h.step(0)
	h.session("s1", wsA, "waiting", true)
	h.world.writeDown = errors.New("signal: killed")
	h.step(time.Second)
	if h.r.State().CmuxAvailable || countContaining(*logs, "writes paused") != 1 {
		t.Fatalf("a killed write must pause: %q", *logs)
	}
}

func restoreBinding(ws string) pigtail.SubjectCmuxBinding {
	return pigtail.SubjectCmuxBinding{CmuxWorkspaceID: ws, SubjectID: subID, AppliedTitle: "Sidebar pigtail",
		PrevTitle: "Mon nom", PrevTitleCustom: true, TitleOwner: "pigtail", DescriptionOwner: "pigtail"}
}
