package handlers

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Tâche 4 (sub_x000000000000041): pigtaild types /compact in a session in
// Standby, under the wake's guards, proven by the native status or reported.
// Every test goes through sweepIdle, the one call site.

// standbyWakeFixture: s-alpha (pid 7, surf) idle, unfocused, input empty, in
// Standby, no message pending.
func standbyWakeFixture(t *testing.T) (*relayFixture, *fakeWake, *wakeLog) {
	t.Helper()
	f, fw, lg := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	if err := f.st.SetStandby("s-alpha", f.now); err != nil {
		t.Fatal(err)
	}
	fw.resetCounts()
	return f, fw, lg
}

func (f *relayFixture) standbyRow(t *testing.T, s string) pigtail.SessionStandby {
	t.Helper()
	sb, err := f.st.GetStandby(s)
	if err != nil || sb == nil {
		t.Fatalf("standby row of %s: %+v %v", s, sb, err)
	}
	return *sb
}

// advanceNative moves the native status time of pid forward (the session
// left idle, or a fresh process wrote its start).
func (fw *fakeWake) advanceNative(pid int, by int64) {
	n := fw.natives[pid]
	n.StatusUpdatedAt += by
	fw.natives[pid] = n
}

// screenWithInput is the idle screen with draft typed in the input box.
func screenWithInput(t *testing.T, draft string) string {
	t.Helper()
	s := screenFixture(t, "screen-draft.txt")
	if strings.Count(s, "hello") != 1 {
		t.Fatal("screen-draft.txt lost its draft line")
	}
	return strings.Replace(s, "hello", draft, 1)
}

// The common case and the call site: nothing pending, a session in Standby.
func TestCompactWithNoPendingMessage(t *testing.T) {
	t.Parallel()
	f, fw, _ := standbyWakeFixture(t)
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.compacts, []string{surf}) || len(fw.sent) != 0 {
		t.Fatalf("compacts %v, wakes %v; want one /compact in surf", fw.compacts, fw.sent)
	}
	sb := f.standbyRow(t, "s-alpha")
	if sb.CompactSentAt == nil || sb.CompactTries != 1 || sb.CompactPID != 7 || sb.CompactStatusAt != 1000 {
		t.Fatalf("row after the send: %+v", sb)
	}
}

// Proven by the native status: typed once, never again.
func TestCompactTypedOnceInStandby(t *testing.T) {
	t.Parallel()
	f, fw, lg := standbyWakeFixture(t)
	f.rl.sweepIdle()
	fw.advanceNative(7, 5)
	f.advance(time.Minute)
	f.rl.sweepIdle()
	f.rl.sweepIdle()
	if len(fw.compacts) != 1 {
		t.Fatalf("compacts = %v, want one", fw.compacts)
	}
	if sb := f.standbyRow(t, "s-alpha"); sb.CompactedAt == nil {
		t.Fatalf("not marked compacted: %+v", sb)
	}
	if lg.count("wake: s-alpha standby: compacted") != 1 {
		t.Fatalf("compacted line:\n%s", lg)
	}
}

// Not proven: resent after compactProofWait, given up after maxCompactTries.
func TestCompactRetriedWhenNotProven(t *testing.T) {
	t.Parallel()
	f, fw, lg := standbyWakeFixture(t)
	f.rl.svc.Bus = events.New()
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionStandbyCompactFailed))
	defer sub.Close()
	f.rl.sweepIdle()
	f.advance(compactProofWait - time.Second)
	f.rl.sweepIdle()
	if len(fw.compacts) != 1 {
		t.Fatalf("resent before compactProofWait: %v", fw.compacts)
	}
	f.advance(2 * time.Second)
	f.rl.sweepIdle()
	if sb := f.standbyRow(t, "s-alpha"); len(fw.compacts) != 2 || sb.CompactTries != 2 {
		t.Fatalf("compacts %v, tries %d; want 2 and 2", fw.compacts, sb.CompactTries)
	}
	f.advance(compactProofWait + time.Second)
	f.rl.sweepIdle()
	f.advance(compactProofWait + time.Second)
	f.rl.sweepIdle()
	f.advance(compactProofWait + time.Second)
	f.rl.sweepIdle()
	if len(fw.compacts) != maxCompactTries {
		t.Fatalf("compacts = %v, want %d", fw.compacts, maxCompactTries)
	}
	if sb := f.standbyRow(t, "s-alpha"); sb.CompactFailedAt == nil {
		t.Fatalf("not marked failed: %+v", sb)
	}
	if lg.count("wake: s-alpha standby: /compact not proven after 3 sends") != 1 {
		t.Fatalf("failure line:\n%s", lg)
	}
	e := nextStandbyEvent(t, sub)
	if e.Type != pigtail.EventSessionStandbyCompactFailed || e.Scope != "globex" || e.Data["session_id"] != "s-alpha" || e.Data["tries"] != 3 {
		t.Fatalf("event %+v", e)
	}
	if r, _ := e.Data["reason"].(string); r == "" {
		t.Fatalf("event without a reason: %+v", e)
	}
}

// Our /compact left in the input (Enter swallowed): failed at once, never
// erased (D9).
func TestCompactFailsWhenLeftInInput(t *testing.T) {
	t.Parallel()
	f, fw, lg := standbyWakeFixture(t)
	f.rl.sweepIdle()
	fw.screen = screenWithInput(t, "/compact")
	f.advance(compactProofWait + time.Second)
	f.rl.sweepIdle()
	if len(fw.compacts) != 1 {
		t.Fatalf("compacts = %v, want the first one only", fw.compacts)
	}
	if sb := f.standbyRow(t, "s-alpha"); sb.CompactFailedAt == nil {
		t.Fatalf("not marked failed: %+v", sb)
	}
	if lg.count("/compact not proven after 1 sends (/compact left in the input)") != 1 {
		t.Fatalf("failure line:\n%s", lg)
	}
	f.advance(compactProofWait + time.Second)
	f.rl.sweepIdle()
	if len(fw.compacts) != 1 {
		t.Fatalf("typed after the failure: %v", fw.compacts)
	}
}

// The dimmed suggestion of an empty box is not our text left in it.
func TestCompactPlaceholderNotLeftInInput(t *testing.T) {
	t.Parallel()
	f, fw, _ := standbyWakeFixture(t)
	f.rl.sweepIdle()
	fw.screen = screenWithInput(t, `Try "how does main.go work?"`)
	f.advance(compactProofWait + time.Second)
	f.rl.sweepIdle()
	if sb := f.standbyRow(t, "s-alpha"); len(fw.compacts) != 2 || sb.CompactFailedAt != nil {
		t.Fatalf("compacts %v, row %+v; want a normal resend", fw.compacts, sb)
	}
}

func TestCompactHeldByCheckRunning(t *testing.T) {
	t.Parallel()
	f, fw, _ := standbyWakeFixture(t)
	f.rl.cfg.Wake.CheckRunning = func() bool { return true }
	f.rl.sweepIdle()
	if fw.cmuxCalls != 0 || len(fw.compacts) != 0 {
		t.Fatalf("%d cmux calls, compacts %v; want none", fw.cmuxCalls, fw.compacts)
	}
}

// The wake's listing failed: the compaction would need it, skipped this sweep.
func TestCompactSkippedWhenListingFails(t *testing.T) {
	t.Parallel()
	f, fw, _ := standbyWakeFixture(t)
	fw.natives[8] = claudesession.Native{SessionID: "s-gamma", Status: "idle", Kind: "interactive", StatusUpdatedAt: 1000}
	f.wakePeers(t, gammaPeer())
	f.sendTo(t, fw, "s-gamma")
	fw.listErr = errors.New("cmux sessions: signal: killed")
	f.rl.sweepIdle()
	if fw.listCalls != 1 || len(fw.compacts) != 0 {
		t.Fatalf("%d listings, compacts %v; want 1 and none", fw.listCalls, fw.compacts)
	}
	fw.listErr = nil
	fw.listing = cmuxListingOf(cmuxEntry("s-alpha", surf, 7, "s-alpha"), cmuxEntry("s-gamma", surf2, 8, "s-gamma"))
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.compacts, []string{surf}) {
		t.Fatalf("compacts = %v, want [surf]", fw.compacts)
	}
}

func TestCompactHeldByFocus(t *testing.T) {
	t.Parallel()
	f, fw, _ := standbyWakeFixture(t)
	fw.focused = surf
	f.rl.sweepIdle()
	if len(fw.compacts) != 0 {
		t.Fatalf("typed into the focused pane: %v", fw.compacts)
	}
}

func TestCompactHeldByDraft(t *testing.T) {
	t.Parallel()
	f, fw, _ := standbyWakeFixture(t)
	fw.screen = screenFixture(t, "screen-draft.txt")
	f.rl.sweepIdle()
	if sb := f.standbyRow(t, "s-alpha"); len(fw.compacts) != 0 || sb.CompactTries != 0 {
		t.Fatalf("compacts %v, tries %d over a draft", fw.compacts, sb.CompactTries)
	}
}

func TestCompactHeldByDeclaredWait(t *testing.T) {
	t.Parallel()
	f, fw, _ := standbyWakeFixture(t)
	declareWait(t, f, "s-alpha", "review")
	f.rl.sweepIdle()
	if len(fw.compacts) != 0 || fw.cmuxCalls != 0 {
		t.Fatalf("compacts %v, %d cmux calls under a declared wait", fw.compacts, fw.cmuxCalls)
	}
}

func TestCompactHeldByBusy(t *testing.T) {
	t.Parallel()
	f, fw, _ := standbyWakeFixture(t)
	n := fw.natives[7]
	n.Status = "busy"
	fw.natives[7] = n
	f.rl.sweepIdle()
	if len(fw.compacts) != 0 || fw.cmuxCalls != 0 {
		t.Fatalf("compacts %v, %d cmux calls on a busy session", fw.compacts, fw.cmuxCalls)
	}
}

func TestCompactNoStandbyNoCmux(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	fw.resetCounts()
	f.rl.sweepIdle()
	if fw.cmuxCalls != 0 {
		t.Fatalf("%d cmux calls with nothing to do", fw.cmuxCalls)
	}
}

// A wake and a compaction in one sweep share the listing and the focus.
func TestCompactAndWakeShareOneListing(t *testing.T) {
	t.Parallel()
	f, fw, _ := standbyWakeFixture(t)
	fw.natives[8] = claudesession.Native{SessionID: "s-gamma", Status: "idle", Kind: "interactive", StatusUpdatedAt: 1000}
	fw.listing = cmuxListingOf(cmuxEntry("s-alpha", surf, 7, "s-alpha"), cmuxEntry("s-gamma", surf2, 8, "s-gamma"))
	f.wakePeers(t, gammaPeer())
	f.sendTo(t, fw, "s-gamma")
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.sent, []string{surf2}) || !reflect.DeepEqual(fw.compacts, []string{surf}) {
		t.Fatalf("wakes %v, compacts %v", fw.sent, fw.compacts)
	}
	if fw.listCalls != 1 || fw.focusCalls != 1 {
		t.Fatalf("%d listings, %d identify; want 1 and 1", fw.listCalls, fw.focusCalls)
	}
}

// Écart 3: a refusal of the compaction is one line, not one per sweep.
func TestCompactRefusalNotedOnce(t *testing.T) {
	t.Parallel()
	f, fw, lg := standbyWakeFixture(t)
	fw.focused = surf
	for range 3 {
		f.rl.sweepIdle()
	}
	if n := lg.count("wake: s-alpha standby: held: pane has keyboard focus"); n != 1 {
		t.Fatalf("refusal noted %d times:\n%s", n, lg)
	}
}

// The wake's and the compaction's notes are kept apart, and a claim that
// empties the inbox does not make the compaction's refusal be noted again.
func TestWakeAndCompactNotesApart(t *testing.T) {
	t.Parallel()
	f, fw, lg := standbyWakeFixture(t)
	f.sendTo(t, fw, "s-alpha")
	fw.focused = surf
	for range 3 {
		f.rl.sweepIdle()
	}
	if w, c := lg.count("wake: s-alpha held: standby (D16)"), lg.count("wake: s-alpha standby: held: pane has keyboard focus"); w != 1 || c != 1 {
		t.Fatalf("wake noted %d, compaction noted %d; want 1 and 1:\n%s", w, c, lg)
	}
	if _, err := f.st.ClaimPending("s-alpha", "prompt", f.clock()); err != nil {
		t.Fatal(err)
	}
	if n, _ := f.st.CountPending("s-alpha", f.clock()); n != 0 {
		t.Fatalf("pending after the claim = %d", n)
	}
	for range 3 {
		f.rl.sweepIdle()
	}
	if c := lg.count("wake: s-alpha standby: held: pane has keyboard focus"); c != 1 {
		t.Fatalf("compaction noted %d times after the claim:\n%s", c, lg)
	}
}

// The proof is read on the pid the /compact was sent to only.
func TestCompactProofNeedsSamePid(t *testing.T) {
	t.Parallel()
	f, fw, _ := standbyWakeFixture(t)
	f.rl.sweepIdle()
	fw.natives[9] = claudesession.Native{SessionID: "s-alpha", Status: "idle", Kind: "interactive", StatusUpdatedAt: 5000}
	f.advance(time.Minute)
	f.rl.sweepIdle()
	if sb := f.standbyRow(t, "s-alpha"); sb.CompactedAt != nil {
		t.Fatalf("proven on another pid's status: %+v", sb)
	}
}

// D24: a relaunch loses the send; never a proof, a new send on the new pid.
func TestCompactResentAfterRelaunch(t *testing.T) {
	t.Parallel()
	f, fw, _ := standbyWakeFixture(t)
	f.rl.sweepIdle()
	p := alphaPeer()
	p.PID = 9
	f.peers(t, p)
	fw.natives[9] = claudesession.Native{SessionID: "s-alpha", Status: "idle", Kind: "interactive", StatusUpdatedAt: 2000}
	fw.listing = cmuxListingOf(cmuxEntry("s-alpha", surf, 9, "s-alpha"))
	f.advance(10 * time.Second)
	f.rl.sweepIdle()
	sb := f.standbyRow(t, "s-alpha")
	if sb.CompactedAt != nil || len(fw.compacts) != 2 || sb.CompactPID != 9 || sb.CompactTries != 2 {
		t.Fatalf("compacts %v, row %+v; want a resend on pid 9", fw.compacts, sb)
	}
	fw.advanceNative(9, 5)
	f.advance(10 * time.Second)
	f.rl.sweepIdle()
	if sb := f.standbyRow(t, "s-alpha"); sb.CompactedAt == nil || len(fw.compacts) != 2 {
		t.Fatalf("compacts %v, row %+v; want proven on pid 9", fw.compacts, sb)
	}
}

// D24: a compacted Standby is never compacted again, even after a resume.
func TestResumedCompactedNotRecompacted(t *testing.T) {
	t.Parallel()
	f, fw, _ := standbyWakeFixture(t)
	if err := f.st.MarkStandbyCompacted("s-alpha", f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.st.EndPeer("s-alpha", f.now); err != nil {
		t.Fatal(err)
	}
	p := alphaPeer()
	p.PID = 9
	f.peers(t, p)
	fw.natives[9] = claudesession.Native{SessionID: "s-alpha", Status: "idle", Kind: "interactive", StatusUpdatedAt: 2000}
	fw.listing = cmuxListingOf(cmuxEntry("s-alpha", surf, 9, "s-alpha"))
	fw.resetCounts()
	f.rl.sweepIdle()
	if len(fw.compacts) != 0 || fw.screenCalls != 0 {
		t.Fatalf("compacts %v, %d read-screen; want none", fw.compacts, fw.screenCalls)
	}
}

// D-A (verify N2 of PR #210): the Stop watcher's long wait stays open during
// the Standby by design (Tâche 10), held by it — it claims nothing, so it
// cannot wake the session against our /compact. That watcher never holds
// the compaction; the box stays closed after it.
func TestCompactIgnoresWatcherHeldByStandby(t *testing.T) {
	t.Parallel()
	f, fw, lg := standbyWakeFixture(t)
	f.sendTo(t, fw, "s-alpha")
	f.rl.register("s-alpha") // the open long wait of claude-inbox-wait
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.compacts, []string{surf}) || len(fw.sent) != 0 {
		t.Fatalf("compacts %v, wakes %v; want one /compact, no wake line\n%s", fw.compacts, fw.sent, lg)
	}
	if lg.count("standby: held: watched") != 0 {
		t.Fatalf("held as watched:\n%s", lg)
	}
	if cr := f.longWait(t, "s-alpha"); len(cr.Messages) != 0 {
		t.Fatalf("the watcher was served after the /compact: %+v", cr.Messages)
	}
	if n, _ := f.st.CountPending("s-alpha", f.clock()); n != 1 {
		t.Fatalf("pending = %d, want 1 (kept for Ada' prompt)", n)
	}
}

// The D26 recheck right before typing: a watcher that (re)registers during
// the cmux calls is held by the Standby too, the /compact goes.
func TestCompactRecheckIgnoresWatcherHeldByStandby(t *testing.T) {
	t.Parallel()
	f, fw, _ := standbyWakeFixture(t)
	fw.onScreen = func() { f.rl.register("s-alpha") }
	f.rl.sweepIdle()
	if !reflect.DeepEqual(fw.compacts, []string{surf}) {
		t.Fatalf("compacts %v; want one /compact", fw.compacts)
	}
}

// Control: the Standby ends during the cmux calls (the user's prompt) → the
// watcher serves again and nothing is typed.
func TestCompactRecheckRefusesWhenStandbyEnds(t *testing.T) {
	t.Parallel()
	f, fw, lg := standbyWakeFixture(t)
	fw.onScreen = func() {
		f.rl.register("s-alpha")
		if _, err := f.st.ClearStandby("s-alpha", f.clock()); err != nil {
			t.Fatal(err)
		}
	}
	f.rl.sweepIdle()
	if len(fw.compacts) != 0 {
		t.Fatalf("typed /compact after the Standby ended: %v\n%s", fw.compacts, lg)
	}
}
