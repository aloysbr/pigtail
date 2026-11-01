package cmuxsync

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/waittype"
)

// liveDesired is the raw Compute output of n live sessions of type ty.
func liveDesired(ty waittype.Type, n int, reason string) Desired {
	l := liveSummary{any: true, typ: ty, reason: reason}
	l.count[ty.Gravity()] = n
	d := Desired{WorkspaceID: wsA, Live: l}
	d.resolve()
	return d
}

// rawDesired is the raw Compute output a step describes: n untyped waits, or
// one dead surface.
func rawDesired(raw Attention, n int) Desired {
	switch raw {
	case AttentionWaiting:
		return liveDesired(waittype.Unknown, n, "")
	case AttentionDead:
		d := Desired{WorkspaceID: wsA, DeadSurfaces: 1}
		d.resolve()
		return d
	}
	return Desired{WorkspaceID: wsA}
}

type step struct {
	at    time.Duration // since t0
	raw   Attention
	count int
	want  Attention
	wantN int
}

func runSteps(t *testing.T, steps []step) {
	t.Helper()
	var dm Damper
	for i, s := range steps {
		desired := map[string]Desired{wsA: rawDesired(s.raw, s.count)}
		dm.Apply(t0.Add(s.at), desired)
		if got := desired[wsA]; got.Attention != s.want || got.WaitingCount != s.wantN {
			t.Fatalf("step %d (+%v, raw %q): got %q ×%d, want %q ×%d", i, s.at, s.raw, got.Attention, got.WaitingCount, s.want, s.wantN)
		}
	}
}

func TestDamperWaitingUpImmediatelyDownAfterHold(t *testing.T) {
	t.Parallel()
	runSteps(t, []step{
		{0, AttentionNone, 0, AttentionNone, 0},
		{time.Second, AttentionWaiting, 1, AttentionWaiting, 1},                       // up at once
		{2 * time.Second, AttentionNone, 0, AttentionWaiting, 1},                      // answered: held
		{3 * time.Second, AttentionNone, 0, AttentionWaiting, 1},                      // 1 s out: still held
		{3*time.Second + 999*time.Millisecond, AttentionNone, 0, AttentionWaiting, 1}, // 1.999 s out
		{4 * time.Second, AttentionNone, 0, AttentionNone, 0},                         // 2 s out of waiting: down
	})
}

func TestDamperNoBlinkOnAnswerThenNewQuestion(t *testing.T) {
	t.Parallel()
	runSteps(t, []step{
		{0, AttentionWaiting, 1, AttentionWaiting, 1},
		{time.Second, AttentionNone, 0, AttentionWaiting, 1},
		{1500 * time.Millisecond, AttentionWaiting, 2, AttentionWaiting, 2}, // asks again: hold restarts
		{3 * time.Second, AttentionNone, 0, AttentionWaiting, 2},
		{4500 * time.Millisecond, AttentionNone, 0, AttentionWaiting, 2},
		{5 * time.Second, AttentionNone, 0, AttentionNone, 0},
	})
}

func TestDamperDeadNeedsTwoObservationsOneSecondApart(t *testing.T) {
	t.Parallel()
	runSteps(t, []step{
		{0, AttentionDead, 0, AttentionNone, 0},                      // first sight
		{500 * time.Millisecond, AttentionDead, 0, AttentionNone, 0}, // second, too soon
		{time.Second, AttentionDead, 0, AttentionDead, 1},            // ≥ 1 s, ≥ 2 obs
		{2 * time.Second, AttentionNone, 0, AttentionNone, 0},        // resumed: gone at once
		{3 * time.Second, AttentionDead, 0, AttentionNone, 0},        // counting restarts
	})
}

func TestDamperPidReplacedIsAbsorbed(t *testing.T) {
	t.Parallel()
	runSteps(t, []step{
		{0, AttentionDead, 0, AttentionNone, 0},
		{time.Second, AttentionNone, 0, AttentionNone, 0}, // new pid took over
		{2 * time.Second, AttentionDead, 0, AttentionNone, 0},
	})
}

// A dead surface needs two observations ≥ DeadConfirm apart; meanwhile the
// live wait keeps showing, then Dead (more severe) takes over.
func TestDamperDeadConfirmsUnderALiveWait(t *testing.T) {
	t.Parallel()
	var dm Damper
	ss := []Session{declared("a", "idle", waittype.Question, "q"), deadOn("b")}
	w := Workspace{ID: wsA, WindowID: "W", Surfaces: surfaces("S-a", "S-b")}
	run := func(at time.Time) Desired {
		res := Compute(Input{Now: at, Workspaces: []Workspace{w}, Sessions: ss})
		dm.Apply(at, res.Desired)
		return res.Desired[wsA]
	}
	if d := run(t0); d.WaitType != waittype.Question {
		t.Fatalf("first observation shows %q, want the Question", d.WaitType)
	}
	if d := run(t0.Add(DeadConfirm)); d.Attention != AttentionDead || d.WaitingCount != 1 {
		t.Fatalf("confirmed: %q ×%d", d.Attention, d.WaitingCount)
	}
}

func TestDamperForgetsClosedWorkspaces(t *testing.T) {
	t.Parallel()
	var dm Damper
	dm.Apply(t0, map[string]Desired{wsA: liveDesired(waittype.Unknown, 1, "")})
	dm.Apply(t0.Add(time.Second), map[string]Desired{})
	desired := map[string]Desired{wsA: {WorkspaceID: wsA}}
	dm.Apply(t0.Add(1500*time.Millisecond), desired)
	if desired[wsA].Attention != AttentionNone {
		t.Fatalf("a reopened workspace inherited old state: %+v", desired[wsA])
	}
}
