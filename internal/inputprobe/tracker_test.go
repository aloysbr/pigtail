package inputprobe

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func tgt(i int) Target {
	return Target{Surface: fmt.Sprintf("S-%02d", i), PID: 100 + i, SessionID: fmt.Sprintf("sid-%02d", i)}
}

func verdict(v Verdict) func(Target) Result { return func(Target) Result { return Result{Verdict: v} } }

// alive1 records an Alive of another session at at: the positive control.
func control(tr *Tracker, at time.Time) {
	tr.Record(Target{Surface: "S-CTL", SessionID: "sid-ctl"}, Result{Verdict: VerdictAlive}, at)
}

func TestTrackerDueEveryFiveMinutes(t *testing.T) {
	tr := NewTracker(5 * time.Minute)
	if !tr.Due("sid", t0) {
		t.Fatal("never probed: due")
	}
	tr.Record(Target{Surface: "S", SessionID: "sid"}, Result{Verdict: VerdictAlive}, t0)
	if tr.Due("sid", t0.Add(4*time.Minute+49*time.Second)) {
		t.Fatal("due before 4 min 50 s")
	}
	if !tr.Due("sid", t0.Add(4*time.Minute+50*time.Second)) {
		t.Fatal("not due at 4 min 50 s (every - 10 s)")
	}
}

func TestTrackerDeadStaysDeadOnUnknown(t *testing.T) {
	tr := NewTracker(5 * time.Minute)
	x := Target{Surface: "s-a", SessionID: "sid"}
	tr.Record(x, Result{Verdict: VerdictDead}, t0)
	tr.Record(x, Result{Verdict: VerdictDead}, t0.Add(time.Minute))
	control(tr, t0.Add(time.Minute))
	tr.Record(x, Result{Verdict: VerdictUnknown}, t0.Add(6*time.Minute))
	if !tr.Dead("sid") || !reflect.DeepEqual(tr.DeadSurfaces(t0.Add(3*time.Minute)), []string{"S-A"}) {
		t.Fatalf("dead %v surfaces %v", tr.Dead("sid"), tr.DeadSurfaces(t0.Add(3*time.Minute)))
	}
}

func TestTrackerDeadNeedsTwoDeadInARow(t *testing.T) {
	x := Target{Surface: "S-A", SessionID: "sid"}
	dead, unk, alive := Result{Verdict: VerdictDead}, Result{Verdict: VerdictUnknown}, Result{Verdict: VerdictAlive}
	// Dead: nothing shown yet.
	tr := NewTracker(5 * time.Minute)
	control(tr, t0)
	tr.Record(x, dead, t0)
	if tr.Dead("sid") || len(tr.DeadSurfaces(t0.Add(3*time.Minute))) != 0 {
		t.Fatal("a single Dead must show nothing")
	}
	// Dead, Unknown, Dead: shown (Unknown neither counts nor resets).
	tr.Record(x, unk, t0.Add(time.Minute))
	tr.Record(x, dead, t0.Add(2*time.Minute))
	if !tr.Dead("sid") || !reflect.DeepEqual(tr.DeadSurfaces(t0.Add(3*time.Minute)), []string{"S-A"}) {
		t.Fatalf("Dead, Unknown, Dead: dead %v surfaces %v", tr.Dead("sid"), tr.DeadSurfaces(t0.Add(3*time.Minute)))
	}
	// Dead, Alive, Dead: absent (the Alive cancelled the hold).
	tr2 := NewTracker(5 * time.Minute)
	control(tr2, t0)
	tr2.Record(x, dead, t0)
	tr2.Record(x, alive, t0.Add(time.Minute))
	tr2.Record(x, dead, t0.Add(2*time.Minute))
	if tr2.Dead("sid") || len(tr2.DeadSurfaces(t0.Add(3*time.Minute))) != 0 {
		t.Fatal("Dead, Alive, Dead must not confirm")
	}
}

func TestTrackerConfirmDueAfterFiftySeconds(t *testing.T) {
	tr := NewTracker(5 * time.Minute)
	x := Target{Surface: "S-A", SessionID: "sid"}
	tr.Record(x, Result{Verdict: VerdictDead}, t0)
	if tr.Due("sid", t0.Add(49*time.Second)) || !tr.Due("sid", t0.Add(50*time.Second)) {
		t.Fatal("a first Dead is due again at +50 s, not before")
	}
	// Two ticks 55 s apart: the confirmation is due at the second.
	var probed []string
	probe := func(tg Target) Result { probed = append(probed, tg.SessionID); return Result{Verdict: VerdictDead} }
	tr2 := NewTracker(5 * time.Minute)
	Pass(tr2, []Target{x}, probe, t0, 4)
	Pass(tr2, []Target{x}, probe, t0.Add(55*time.Second), 4)
	if !reflect.DeepEqual(probed, []string{"sid", "sid"}) || !tr2.Dead("sid") {
		t.Fatalf("probed %v dead %v: want the confirmation at the 2nd tick", probed, tr2.Dead("sid"))
	}
	// A rotation session is due at +4 min 50 s.
	tr3 := NewTracker(5 * time.Minute)
	tr3.Record(Target{Surface: "S-B", SessionID: "b"}, Result{Verdict: VerdictAlive}, t0)
	if !tr3.Due("b", t0.Add(4*time.Minute+50*time.Second)) {
		t.Fatal("a rotation session must be due at 4 min 50 s")
	}
	// Pass probes the confirmation before a session 5 min overdue.
	tr4 := NewTracker(5 * time.Minute)
	tr4.Record(Target{Surface: "S-A", SessionID: "a"}, Result{Verdict: VerdictDead}, t0.Add(10*time.Minute))
	tr4.Record(Target{Surface: "S-B", SessionID: "b"}, Result{Verdict: VerdictAlive}, t0)
	var order []string
	Pass(tr4, []Target{{Surface: "S-A", SessionID: "a"}, {Surface: "S-B", SessionID: "b"}},
		func(tg Target) Result { order = append(order, tg.SessionID); return Result{Verdict: VerdictAlive} },
		t0.Add(11*time.Minute), 1)
	if !reflect.DeepEqual(order, []string{"a"}) {
		t.Fatalf("probed %v, want the confirmation first (max 1)", order)
	}
}

func TestTrackerDeadNeedsTwoAliveToClear(t *testing.T) {
	tr := NewTracker(5 * time.Minute)
	x := Target{Surface: "S-A", SessionID: "sid"}
	seq := []struct {
		v    Verdict
		dead bool
	}{
		{VerdictDead, false},   // first Dead: on hold
		{VerdictDead, true},    // confirmed
		{VerdictAlive, true},   // one alive keeps it dead
		{VerdictUnknown, true}, // unknown does not touch the count
		{VerdictDead, true},    // a dead resets the count
		{VerdictAlive, true},
		{VerdictUnknown, true},
		{VerdictAlive, false}, // two alive in a row (unknown between does not count)
		{VerdictAlive, false},
	}
	for i, s := range seq {
		tr.Record(x, Result{Verdict: s.v}, t0.Add(time.Duration(i)*5*time.Minute))
		if tr.Dead("sid") != s.dead {
			t.Fatalf("step %d (%s): dead %v, want %v", i, s.v, tr.Dead("sid"), s.dead)
		}
	}
	// alive, dead, alive: never two in a row
	tr2 := NewTracker(5 * time.Minute)
	for _, v := range []Verdict{VerdictDead, VerdictDead, VerdictAlive, VerdictDead, VerdictAlive} {
		tr2.Record(x, Result{Verdict: v}, t0)
	}
	if !tr2.Dead("sid") {
		t.Fatal("a dead between two alive must restart the count")
	}
}

func TestTrackerHidesDeadWithoutRecentAlive(t *testing.T) {
	tr := NewTracker(5 * time.Minute)
	x := Target{Surface: "S-A", SessionID: "sid"}
	tr.Record(x, Result{Verdict: VerdictDead}, t0)
	tr.Record(x, Result{Verdict: VerdictDead}, t0.Add(time.Minute))
	if !tr.Dead("sid") {
		t.Fatal("two Dead confirm the state")
	}
	if len(tr.DeadSurfaces(t0.Add(3*time.Minute))) != 0 || !tr.DeadHeld(t0.Add(3*time.Minute)) {
		t.Fatalf("surfaces %v held %v: no session answered, the state is held, not shown", tr.DeadSurfaces(t0.Add(3*time.Minute)), tr.DeadHeld(t0.Add(3*time.Minute)))
	}
}

func TestTrackerShowsDeadOnceAnyAliveSeen(t *testing.T) {
	tr := NewTracker(5 * time.Minute)
	x := Target{Surface: "S-A", SessionID: "sid"}
	tr.Record(x, Result{Verdict: VerdictDead}, t0)
	tr.Record(x, Result{Verdict: VerdictDead}, t0.Add(time.Minute))
	control(tr, t0.Add(2*time.Minute)) // after the first Dead
	if !reflect.DeepEqual(tr.DeadSurfaces(t0.Add(3*time.Minute)), []string{"S-A"}) || tr.DeadHeld(t0.Add(3*time.Minute)) {
		t.Fatalf("surfaces %v held %v", tr.DeadSurfaces(t0.Add(3*time.Minute)), tr.DeadHeld(t0.Add(3*time.Minute)))
	}
	// the Alive at the very time of the first Dead also proves.
	tr2 := NewTracker(5 * time.Minute)
	control(tr2, t0)
	tr2.Record(x, Result{Verdict: VerdictDead}, t0)
	tr2.Record(x, Result{Verdict: VerdictDead}, t0.Add(time.Minute))
	if !reflect.DeepEqual(tr2.DeadSurfaces(t0.Add(3*time.Minute)), []string{"S-A"}) {
		t.Fatalf("surfaces %v: an Alive at the first Dead's time proves", tr2.DeadSurfaces(t0.Add(3*time.Minute)))
	}
}

func TestTrackerAliveOlderThanFirstDeadDoesNotProve(t *testing.T) {
	tr := NewTracker(5 * time.Minute)
	x := Target{Surface: "S-A", SessionID: "sid"}
	control(tr, t0)
	tr.Record(x, Result{Verdict: VerdictDead}, t0.Add(20*time.Minute))
	tr.Record(x, Result{Verdict: VerdictDead}, t0.Add(21*time.Minute))
	if len(tr.DeadSurfaces(t0.Add(3*time.Minute))) != 0 || !tr.DeadHeld(t0.Add(3*time.Minute)) {
		t.Fatalf("surfaces %v: an Alive older than the first Dead proves nothing", tr.DeadSurfaces(t0.Add(3*time.Minute)))
	}
}

func TestTrackerAliveProofExpiresAfter30Min(t *testing.T) {
	tr := NewTracker(5 * time.Minute)
	x := Target{Surface: "S-A", SessionID: "sid"}
	tr.Record(x, Result{Verdict: VerdictDead}, t0)
	tr.Record(x, Result{Verdict: VerdictDead}, t0.Add(time.Minute))
	control(tr, t0.Add(2*time.Minute))
	if len(tr.DeadSurfaces(t0.Add(3*time.Minute))) != 1 {
		t.Fatal("proven while the Alive is recent")
	}
	tr.Record(x, Result{Verdict: VerdictDead}, t0.Add(33*time.Minute)) // 31 min after the Alive
	if len(tr.DeadSurfaces(t0.Add(33*time.Minute))) != 0 {
		t.Fatalf("surfaces %v: the proof is older than 30 min", tr.DeadSurfaces(t0.Add(33*time.Minute)))
	}
}

func TestTrackerForgetClearsEndedSession(t *testing.T) {
	tr := NewTracker(5 * time.Minute)
	tr.Record(Target{Surface: "S-A", SessionID: "sid"}, Result{Verdict: VerdictDead}, t0)
	tr.Record(Target{Surface: "S-A", SessionID: "sid"}, Result{Verdict: VerdictDead}, t0)
	control(tr, t0)
	tr.Forget(map[string]bool{"other": true})
	if tr.Dead("sid") || len(tr.DeadSurfaces(t0.Add(3*time.Minute))) != 0 || !tr.Due("sid", t0) {
		t.Fatal("an ended session must be forgotten")
	}
}

func TestTrackerForgetKeepsBusyLiveSession(t *testing.T) {
	tr := NewTracker(5 * time.Minute)
	tr.Record(Target{Surface: "S-A", SessionID: "sid"}, Result{Verdict: VerdictDead}, t0)
	tr.Record(Target{Surface: "S-A", SessionID: "sid"}, Result{Verdict: VerdictDead}, t0)
	// busy now: absent from the targets, still in Live
	Pass(tr, nil, verdict(VerdictAlive), t0.Add(10*time.Minute), 4)
	tr.Forget(map[string]bool{"sid": true})
	if !tr.Dead("sid") {
		t.Fatal("a live session busy for a moment must keep its dead state")
	}
}

func TestPassSkipsNotDueAndCapsPerTick(t *testing.T) {
	tr := NewTracker(5 * time.Minute)
	targets := []Target{tgt(1), tgt(2), tgt(3), tgt(4), tgt(5), tgt(6)}
	// sid-06 probed 1 min ago: not due; sid-05 probed 9 min ago, sid-04 6 min ago.
	tr.Record(tgt(6), Result{Verdict: VerdictAlive}, t0.Add(-time.Minute))
	tr.Record(tgt(5), Result{Verdict: VerdictAlive}, t0.Add(-9*time.Minute))
	tr.Record(tgt(4), Result{Verdict: VerdictAlive}, t0.Add(-6*time.Minute))
	var probed []string
	Pass(tr, targets, func(x Target) Result { probed = append(probed, x.SessionID); return Result{Verdict: VerdictAlive} }, t0, 4)
	// never probed (1, 2, 3) first, then the most overdue (5): 4 is left.
	want := []string{"sid-01", "sid-02", "sid-03", "sid-05"}
	if !reflect.DeepEqual(probed, want) {
		t.Fatalf("probed %v, want %v", probed, want)
	}
}

func TestPassMostOverdueFirstWithMoreThan20(t *testing.T) {
	const n = 30
	tr := NewTracker(5 * time.Minute)
	var targets []Target
	for i := 0; i < n; i++ {
		targets = append(targets, tgt(i))
	}
	last := map[string]time.Time{}
	maxGap := time.Duration(0)
	for m := 0; m < 120; m++ {
		now := t0.Add(time.Duration(m) * time.Minute)
		Pass(tr, targets, func(x Target) Result {
			if l, ok := last[x.SessionID]; ok && now.Sub(l) > maxGap {
				maxGap = now.Sub(l)
			}
			last[x.SessionID] = now
			return Result{Verdict: VerdictAlive}
		}, now, 4)
	}
	if len(last) != n {
		t.Fatalf("%d sessions probed, want %d: one starved", len(last), n)
	}
	if limit := time.Duration(n/4+1) * time.Minute; maxGap > limit {
		t.Fatalf("a session waited %s between probes, want at most %s", maxGap, limit)
	}
}

func TestPassReportsTransitions(t *testing.T) {
	tr := NewTracker(5 * time.Minute)
	first := Pass(tr, []Target{tgt(1)}, verdict(VerdictDead), t0, 4)
	if len(first) != 0 {
		t.Fatalf("a first Dead is no transition: %+v", first)
	}
	second := Pass(tr, []Target{tgt(1)}, verdict(VerdictDead), t0.Add(ConfirmAfter), 4)
	if len(second) != 1 || !second[0].Dead {
		t.Fatalf("transitions %+v, want the confirmation", second)
	}
	if again := Pass(tr, []Target{tgt(1)}, verdict(VerdictDead), t0.Add(6*time.Minute), 4); len(again) != 0 {
		t.Fatalf("no change, transitions %+v", again)
	}
}

// The proof window runs on the caller's clock: a pill shown stays shown only
// while an answer is less than ProofWindow old, even when nothing is recorded.
func TestTrackerHoldsShownDeadWhenProofGoesStale(t *testing.T) {
	tr := NewTracker(5 * time.Minute)
	x := Target{Surface: "S-A", SessionID: "sid"}
	control(tr, t0)
	tr.Record(x, Result{Verdict: VerdictDead}, t0)
	tr.Record(x, Result{Verdict: VerdictDead}, t0.Add(time.Minute))
	if got := tr.DeadSurfaces(t0.Add(2 * time.Minute)); !reflect.DeepEqual(got, []string{"S-A"}) {
		t.Fatalf("shown %v, want S-A", got)
	}
	late := t0.Add(31 * time.Minute) // nothing recorded since
	if got := tr.DeadSurfaces(late); len(got) != 0 || !tr.DeadHeld(late) {
		t.Fatalf("at +31 min: shown %v held %v; want held, not shown", got, tr.DeadHeld(late))
	}
}
