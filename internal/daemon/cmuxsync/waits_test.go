package cmuxsync

import (
	"reflect"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

type obs struct {
	at       time.Duration // observation time, from t0
	status   string
	statusAt time.Duration // native statusUpdatedAt, from t0
	alive    bool
	want     []string // sources to clear
}

func runTracker(t *testing.T, name string, s Session, steps []obs) {
	t.Helper()
	runTrackerWith(t, name, &WaitTracker{}, s, steps)
}

func runTrackerWith(t *testing.T, name string, wt *WaitTracker, s Session, steps []obs) {
	t.Helper()
	for i, o := range steps {
		s.Status, s.NativeStatusAt, s.Alive = o.status, t0.Add(o.statusAt), o.alive
		got := wt.Observe(t0.Add(o.at), s)
		if !reflect.DeepEqual(got, o.want) {
			t.Fatalf("%s, step %d (%s since +%v): clear %v, want %v", name, i, o.status, o.statusAt, got, o.want)
		}
		for _, src := range got {
			switch src {
			case pigtail.WaitSourceAuto:
				s.Auto = Wait{}
			case pigtail.WaitSourceDeclared:
				s.Declared = Wait{}
			case pigtail.WaitSourceHeld:
				s.Held = Wait{}
			}
		}
	}
	wt.Sweep(map[string]bool{})
	if len(wt.s) != 0 {
		t.Fatalf("%s: sweep kept %d sessions", name, len(wt.s))
	}
}

// The automatic wait ends when the native status leaves "waiting" after it
// was recorded (the user answered the dialog). A hook that fires before the
// dialog shows (PreToolUse, status still busy) is not ended by that busy.
func TestTrackerAutoEndsWhenTheDialogCloses(t *testing.T) {
	t.Parallel()
	s := sess("s1", wsA, "S1", "busy", true)
	s.Auto = Wait{Type: waittype.Question, Since: t0.Add(10 * time.Second)}
	runTracker(t, "auto", s, []obs{
		{at: 10 * time.Second, status: "busy", statusAt: 0, alive: true},
		{at: 11 * time.Second, status: "waiting", statusAt: 10*time.Second + 50*time.Millisecond, alive: true},
		{at: 40 * time.Second, status: "busy", statusAt: 39 * time.Second, alive: true, want: []string{pigtail.WaitSourceAuto}},
	})
}

// The declared wait ends at the next turn: the agent handed back (idle),
// then the user prompted (busy). A dialog in the middle of the declaring
// turn (busy → waiting → busy) is not a new turn.
func TestTrackerDeclaredEndsAtTheNextTurn(t *testing.T) {
	t.Parallel()
	s := sess("s1", wsA, "S1", "busy", true)
	s.Declared = Wait{Type: waittype.Arbitrage, Since: t0.Add(5 * time.Second)}
	runTracker(t, "declared", s, []obs{
		{at: 5 * time.Second, status: "busy", statusAt: 0, alive: true},
		{at: 6 * time.Second, status: "waiting", statusAt: 6 * time.Second, alive: true},
		{at: 8 * time.Second, status: "busy", statusAt: 8 * time.Second, alive: true},
		{at: 9 * time.Second, status: "idle", statusAt: 9 * time.Second, alive: true},
		{at: 3 * time.Hour, status: "idle", statusAt: 9 * time.Second, alive: true},
		{at: 3*time.Hour + time.Second, status: "busy", statusAt: 3 * time.Hour, alive: true, want: []string{pigtail.WaitSourceDeclared}},
	})
}

// Working is cleared like any declaration, at the next turn — including one
// the harness starts on its own when background work completes (no prompt,
// so no UserPromptSubmit hook): the session reads idle, then busy again.
func TestTrackerWorkingEndsWhenTheAgentResumes(t *testing.T) {
	t.Parallel()
	s := sess("s1", wsA, "S1", "busy", true)
	s.Declared = Wait{Type: waittype.Working, Reason: "CI #9205", Since: t0.Add(5 * time.Second)}
	runTracker(t, "working", s, []obs{
		{at: 5 * time.Second, status: "busy", statusAt: 0, alive: true},
		{at: 6 * time.Second, status: "idle", statusAt: 6 * time.Second, alive: true},
		{at: 10 * time.Minute, status: "busy", statusAt: 10 * time.Minute, alive: true, want: []string{pigtail.WaitSourceDeclared}},
	})
}

// After a daemon restart the loop never saw the declaring turn: a busy turn
// newer than the declaration is the next one.
func TestTrackerDeclaredAfterRestart(t *testing.T) {
	t.Parallel()
	s := sess("s1", wsA, "S1", "busy", true)
	s.Declared = Wait{Type: waittype.Review, Since: t0}
	runTracker(t, "restart", s, []obs{
		{at: time.Hour, status: "busy", statusAt: time.Hour, alive: true, want: []string{pigtail.WaitSourceDeclared}},
	})
}

// A session gone (pid dead, no SessionEnd) waits for nothing — confirmed on
// two observations WaitGoneConfirm apart, so one unreadable native file does
// not drop a decision.
func TestTrackerGoneSessionClearsEverything(t *testing.T) {
	t.Parallel()
	s := sess("s1", wsA, "S1", "idle", true)
	s.Declared = Wait{Type: waittype.Blocked, Since: t0}
	s.Auto = Wait{Type: waittype.Permission, Since: t0}
	runTracker(t, "gone", s, []obs{
		{at: time.Second, status: "idle", statusAt: 0, alive: false},
		{at: 2 * time.Second, status: "idle", statusAt: 0, alive: true},
		{at: 3 * time.Second, status: "idle", statusAt: 0, alive: false},
		{at: 3*time.Second + WaitGoneConfirm, status: "idle", statusAt: 0, alive: false,
			want: []string{pigtail.WaitSourceAuto, pigtail.WaitSourceDeclared}},
	})
}

// Without a native reading (status time unknown) nothing is judged.
func TestTrackerNeedsTheNativeFile(t *testing.T) {
	t.Parallel()
	var wt WaitTracker
	s := sess("s1", wsA, "S1", "busy", true)
	s.Declared = Wait{Type: waittype.Review, Since: t0}
	if got := wt.Observe(t0.Add(time.Hour), s); got != nil {
		t.Fatalf("judged without a native file: %v", got)
	}
}

// A2: Limit ends at its reset, or when the session goes busy again after it
// (auto resume, or a prompt the hook missed) — never on idle.
func TestTrackerLimitEndsAtResetOrBusy(t *testing.T) {
	t.Parallel()
	s := sess("s1", wsA, "S1", "idle", true)
	s.Auto = Wait{Type: waittype.Limit, Since: t0, Until: t0.Add(time.Hour)}
	runTracker(t, "limit/reset", s, []obs{
		{at: time.Second, status: "idle", statusAt: 100 * time.Millisecond, alive: true},
		{at: 30 * time.Minute, status: "idle", statusAt: 100 * time.Millisecond, alive: true},
		{at: time.Hour + time.Second, status: "idle", statusAt: 100 * time.Millisecond, alive: true, want: []string{pigtail.WaitSourceAuto}},
	})
	runTracker(t, "limit/busy", s, []obs{
		{at: time.Second, status: "idle", statusAt: 100 * time.Millisecond, alive: true},
		{at: 2 * time.Minute, status: "busy", statusAt: 2 * time.Minute, alive: true, want: []string{pigtail.WaitSourceAuto}},
	})
}

// Review Focus 2: the busy read before the hook (statusUpdatedAt older than
// the wait) does not end it.
func TestTrackerFailureNotEndedByTheBusyBeforeIt(t *testing.T) {
	t.Parallel()
	s := sess("s1", wsA, "S1", "busy", true)
	s.Auto = Wait{Type: waittype.APIError, Since: t0.Add(10 * time.Second)}
	runTracker(t, "api", s, []obs{
		{at: 10 * time.Second, status: "busy", statusAt: 0, alive: true},
		{at: 11 * time.Second, status: "idle", statusAt: 10*time.Second + 200*time.Millisecond, alive: true},
		{at: 60 * time.Second, status: "busy", statusAt: 59 * time.Second, alive: true, want: []string{pigtail.WaitSourceAuto}},
	})
}

// D3: Login ends only at the next prompt (the hook) or with the session.
func TestTrackerLoginOutlivesBusy(t *testing.T) {
	t.Parallel()
	s := sess("s1", wsA, "S1", "idle", true)
	s.Auto = Wait{Type: waittype.Login, Since: t0}
	runTracker(t, "login", s, []obs{
		{at: time.Second, status: "idle", statusAt: 100 * time.Millisecond, alive: true},
		{at: time.Minute, status: "busy", statusAt: time.Minute, alive: true},
		{at: 2 * time.Minute, status: "busy", statusAt: time.Minute, alive: false},
		{at: 2*time.Minute + 3*time.Second, status: "busy", statusAt: time.Minute, alive: false, want: []string{pigtail.WaitSourceAuto}},
	})
}

// holder is a pidAlive seam whose answer the test flips.
type holder struct{ alive bool }

func (h *holder) pidAlive(pid int) bool { return pid == 4242 && h.alive }

func heldTracker(h *holder) *WaitTracker { return &WaitTracker{pidAlive: h.pidAlive} }

// A held wait alone is judged (no early return): never ended by the next
// turn nor by idle → busy, only by its holder's death.
func TestTrackerHeldOutlivesTurns(t *testing.T) {
	t.Parallel()
	h := &holder{alive: true}
	wt := heldTracker(h)
	s := sess("s1", wsA, "S1", "busy", true)
	s.Held = Wait{Type: waittype.Mac, Since: t0.Add(5 * time.Second), HolderPID: 4242}
	runTrackerWith(t, "held/turns", wt, s, []obs{
		{at: 5 * time.Second, status: "busy", statusAt: 0, alive: true},
		{at: 6 * time.Second, status: "idle", statusAt: 6 * time.Second, alive: true},
		{at: 10 * time.Minute, status: "busy", statusAt: 10 * time.Minute, alive: true},
		{at: 11 * time.Minute, status: "idle", statusAt: 11 * time.Minute, alive: true},
	})
	h.alive = false
	got := wt.Observe(t0.Add(12*time.Minute), withNative(s, "idle", 11*time.Minute))
	if !reflect.DeepEqual(got, []string{pigtail.WaitSourceHeld}) {
		t.Fatalf("holder dead: clear %v, want [held]", got)
	}
}

func withNative(s Session, status string, at time.Duration) Session {
	s.Status, s.NativeStatusAt, s.Alive = status, t0.Add(at), true
	return s
}

// A held wait ends at its Until.
func TestTrackerHeldEndsAtUntil(t *testing.T) {
	t.Parallel()
	s := sess("s1", wsA, "S1", "idle", true)
	s.Held = Wait{Type: waittype.Mac, Since: t0, Until: t0.Add(time.Hour), HolderPID: 4242}
	runTrackerWith(t, "held/until", heldTracker(&holder{alive: true}), s, []obs{
		{at: time.Minute, status: "idle", statusAt: time.Second, alive: true},
		{at: time.Hour + time.Second, status: "idle", statusAt: time.Second, alive: true, want: []string{pigtail.WaitSourceHeld}},
	})
}

// Ended and a confirmed death end the held wait with the others.
func TestTrackerHeldEndsWithTheSession(t *testing.T) {
	t.Parallel()
	s := sess("s1", wsA, "S1", "idle", true)
	s.Declared = Wait{Type: waittype.Working, Since: t0}
	s.Held = Wait{Type: waittype.Mac, Since: t0, HolderPID: 4242}
	runTrackerWith(t, "held/gone", heldTracker(&holder{alive: true}), s, []obs{
		{at: time.Second, status: "idle", statusAt: 0, alive: false},
		{at: time.Second + WaitGoneConfirm, status: "idle", statusAt: 0, alive: false,
			want: []string{pigtail.WaitSourceDeclared, pigtail.WaitSourceHeld}},
	})
	wt := heldTracker(&holder{alive: true})
	e := s
	e.Ended = true
	if got := wt.Observe(t0.Add(time.Second), e); !reflect.DeepEqual(got, []string{pigtail.WaitSourceDeclared, pigtail.WaitSourceHeld}) {
		t.Fatalf("ended: clear %v", got)
	}
}

// F13: the held wait's end is judged before the early returns — no native
// reading, or a failure — so a dead holder ends it there too.
func TestTrackerHeldEndJudgedFirst(t *testing.T) {
	t.Parallel()
	dead := heldTracker(&holder{alive: false})
	s := sess("s1", wsA, "S1", "idle", true) // NativeStatusAt zero
	s.Held = Wait{Type: waittype.Mac, Since: t0, HolderPID: 4242}
	if got := dead.Observe(t0.Add(time.Minute), s); !reflect.DeepEqual(got, []string{pigtail.WaitSourceHeld}) {
		t.Fatalf("no native reading, holder dead: clear %v", got)
	}
	f := withNative(s, "idle", time.Second)
	f.Auto = Wait{Type: waittype.Limit, Since: t0, Until: t0.Add(time.Hour)}
	if got := dead.Observe(t0.Add(time.Minute), f); !reflect.DeepEqual(got, []string{pigtail.WaitSourceHeld}) {
		t.Fatalf("limit + holder dead: clear %v, want [held] (limit keeps its own rule)", got)
	}
	if got := dead.Observe(t0.Add(2*time.Hour), f); !reflect.DeepEqual(got, []string{pigtail.WaitSourceAuto, pigtail.WaitSourceHeld}) {
		t.Fatalf("limit reset + holder dead: clear %v", got)
	}
	// An unconfirmed death does not end the held wait, a dead holder does.
	g := s
	g.Alive = false
	if got := dead.Observe(t0.Add(3*time.Hour), g); !reflect.DeepEqual(got, []string{pigtail.WaitSourceHeld}) {
		t.Fatalf("session unreadable, holder dead: clear %v", got)
	}
	if got := heldTracker(&holder{alive: true}).Observe(t0.Add(3*time.Hour), g); got != nil {
		t.Fatalf("session unreadable once, holder alive: clear %v", got)
	}
}

// D38: the end of a held wait and of a failure is one rule, shared with the
// idle wake (waittype.HeldOver, waittype.FailureReset) — the sidebar and the
// wake must never disagree on whether a wait still shows.
func TestWaitEndParity(t *testing.T) {
	t.Parallel()
	now := t0.Add(time.Hour)
	untils := map[string]time.Time{"zero": {}, "passed": now.Add(-time.Second), "ahead": now.Add(time.Second)}
	for un, u := range untils {
		for _, pid := range []int{0, 4242, 999} {
			for _, alive := range []bool{true, false} {
				h := &holder{alive: alive}
				got := heldTracker(h).heldOver(now, Wait{Until: u, HolderPID: pid})
				if want := waittype.HeldOver(now, u, pid, h.pidAlive); got != want {
					t.Errorf("held until=%s pid=%d alive=%v: sidebar %v, waittype %v", un, pid, alive, got, want)
				}
			}
		}
		s := sess("s1", wsA, "S1", "idle", true)
		s.NativeStatusAt = t0.Add(time.Minute)
		s.Auto = Wait{Type: waittype.Limit, Since: t0, Until: u}
		cleared := reflect.DeepEqual((&WaitTracker{}).Observe(now, s), []string{pigtail.WaitSourceAuto})
		if want := waittype.FailureReset(now, u); cleared != want {
			t.Errorf("limit until=%s: sidebar clears %v, waittype %v", un, cleared, want)
		}
	}
}

// turnRanFn is a WaitTracker.turnRan answering a settable verdict.
type turnVerdict struct{ ran, known bool }

func (v *turnVerdict) fn(Session) (bool, bool) { return v.ran, v.known }

func declaredSession() Session {
	s := sess("s1", wsA, "S1", "busy", true)
	s.Declared = Wait{Type: waittype.Working, Since: t0.Add(5 * time.Second)}
	return s
}

// A busy flicker with no turn-opening entry (a background notification that
// runs no turn) is not the next turn.
func TestTrackerGhostBusyKeepsDeclared(t *testing.T) {
	v := &turnVerdict{known: true}
	runTrackerWith(t, "ghost", &WaitTracker{turnRan: v.fn}, declaredSession(), []obs{
		{at: 5 * time.Second, status: "busy", statusAt: 0, alive: true},
		{at: 6 * time.Second, status: "idle", statusAt: 6 * time.Second, alive: true},
		{at: 20 * time.Second, status: "busy", statusAt: 19 * time.Second, alive: true},
		{at: 21 * time.Second, status: "idle", statusAt: 20 * time.Second, alive: true},
		{at: 30 * time.Second, status: "idle", statusAt: 20 * time.Second, alive: true},
	})
}

// The turn that follows the ghost shows in the transcript: cleared.
func TestTrackerRealTurnAfterGhost(t *testing.T) {
	v := &turnVerdict{known: true}
	runTrackerWith(t, "ghost then turn", &WaitTracker{turnRan: v.fn}, declaredSession(), []obs{
		{at: 5 * time.Second, status: "busy", statusAt: 0, alive: true},
		{at: 6 * time.Second, status: "idle", statusAt: 6 * time.Second, alive: true},
		{at: 20 * time.Second, status: "busy", statusAt: 19 * time.Second, alive: true},
		{at: 21 * time.Second, status: "idle", statusAt: 20 * time.Second, alive: true},
		{at: 40 * time.Second, status: "busy", statusAt: 39 * time.Second, alive: true},
	})
	// The same, with the verdict turning true at the last observation.
	wt := &WaitTracker{turnRan: v.fn}
	s := declaredSession()
	steps := []obs{
		{at: 5 * time.Second, status: "busy", statusAt: 0, alive: true},
		{at: 6 * time.Second, status: "idle", statusAt: 6 * time.Second, alive: true},
		{at: 20 * time.Second, status: "busy", statusAt: 19 * time.Second, alive: true},
		{at: 21 * time.Second, status: "idle", statusAt: 20 * time.Second, alive: true},
	}
	runTrackerNoSweep(t, wt, s, steps)
	v.ran = true
	s.Status, s.NativeStatusAt, s.Alive = "idle", t0.Add(20*time.Second), true
	if got := wt.Observe(t0.Add(22*time.Second), s); !reflect.DeepEqual(got, []string{pigtail.WaitSourceDeclared}) {
		t.Fatalf("clear %v after the real turn, want declared", got)
	}
}

func runTrackerNoSweep(t *testing.T, wt *WaitTracker, s Session, steps []obs) {
	t.Helper()
	for i, o := range steps {
		s.Status, s.NativeStatusAt, s.Alive = o.status, t0.Add(o.statusAt), o.alive
		if got := wt.Observe(t0.Add(o.at), s); len(got) != 0 {
			t.Fatalf("step %d cleared %v", i, got)
		}
	}
}

// A short real turn may be back to idle before a full pass reads busy.
func TestTrackerShortTurnUnseenBusy(t *testing.T) {
	v := &turnVerdict{ran: true, known: true}
	runTrackerWith(t, "short turn", &WaitTracker{turnRan: v.fn}, declaredSession(), []obs{
		{at: 5 * time.Second, status: "busy", statusAt: 0, alive: true},
		{at: 6 * time.Second, status: "idle", statusAt: 6 * time.Second, alive: true},
		{at: 30 * time.Second, status: "idle", statusAt: 29 * time.Second, alive: true, want: []string{pigtail.WaitSourceDeclared}},
	})
}

// No verdict (unreadable transcript): today's rule. A busy a checkpoint saw
// (verdict « no turn » then) makes a later idle, once the verdict is unknown,
// the handing back of a turn: cleared. Without that busy, an idle alone is not.
func TestTrackerFallbackCheckpointBusy(t *testing.T) {
	v := &turnVerdict{known: true}
	wt := &WaitTracker{turnRan: v.fn}
	s := declaredSession()
	s.Alive = true
	observe := func(at time.Duration, status string, statusAt time.Duration) []string {
		s.Status, s.NativeStatusAt = status, t0.Add(statusAt)
		return wt.Observe(t0.Add(at), s)
	}
	observe(5*time.Second, "busy", 0)
	observe(6*time.Second, "idle", 6*time.Second)
	if got := observe(19*time.Second, "busy", 19*time.Second); len(got) != 0 {
		t.Fatalf("busy with a known « no turn »: %v", got)
	}
	v.known = false // the verdict is lost: the status alone decides
	if got := observe(21*time.Second, "idle", 20*time.Second); !reflect.DeepEqual(got, []string{pigtail.WaitSourceDeclared}) {
		t.Fatalf("idle after a busy seen, no verdict: %v, want declared", got)
	}
	// No busy seen: an idle moving again is not a turn.
	wt2 := &WaitTracker{turnRan: (&turnVerdict{}).fn}
	s2 := declaredSession()
	s2.Alive = true
	for i, o := range []struct {
		at, statusAt time.Duration
	}{{6, 6}, {26, 25}} {
		s2.Status, s2.NativeStatusAt = "idle", t0.Add(o.statusAt*time.Second)
		if got := wt2.Observe(t0.Add(o.at*time.Second), s2); len(got) != 0 {
			t.Fatalf("idle only, step %d: %v", i, got)
		}
	}
}

// The daemon restarted after the declaration, a pills-only checkpoint saw the
// busy (no verdict yet: not over), the next full pass reads idle: the real
// turn is confirmed then, not only at yet another status change.
func TestTrackerRestartBusyDeferredThenIdle(t *testing.T) {
	v := &turnVerdict{known: true}
	wt := &WaitTracker{turnRan: v.fn}
	s := declaredSession()
	s.Alive = true
	s.Status, s.NativeStatusAt = "busy", t0.Add(9*time.Second)
	if got := wt.Observe(t0.Add(10*time.Second), s); len(got) != 0 {
		t.Fatalf("deferred busy: %v", got)
	}
	v.ran = true // the full pass read the transcript: a turn ran
	s.Status, s.NativeStatusAt = "idle", t0.Add(12*time.Second)
	if got := wt.Observe(t0.Add(13*time.Second), s); !reflect.DeepEqual(got, []string{pigtail.WaitSourceDeclared}) {
		t.Fatalf("idle after the deferred restart busy: %v, want declared", got)
	}
}

// The daemon restarted mid-turn: a busy newer than the declaration is the
// next turn only if the transcript says so.
func TestTrackerRestartConfirmedByTranscript(t *testing.T) {
	v := &turnVerdict{known: true}
	wt := &WaitTracker{turnRan: v.fn}
	s := declaredSession()
	s.Status, s.NativeStatusAt, s.Alive = "busy", t0.Add(9*time.Second), true
	if got := wt.Observe(t0.Add(10*time.Second), s); len(got) != 0 {
		t.Fatalf("restart busy with no turn in the transcript: %v", got)
	}
	v.ran = true
	if got := wt.Observe(t0.Add(11*time.Second), s); !reflect.DeepEqual(got, []string{pigtail.WaitSourceDeclared}) {
		t.Fatalf("restart busy with a turn: %v", got)
	}
}
