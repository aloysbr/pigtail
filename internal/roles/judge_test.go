package roles

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var (
	now0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	role = pigtail.Role{Scope: "perso", Name: "pilote", SubjectID: "sub", Cadence: 30 * time.Minute,
		Grace: 10 * time.Minute, Stall: 20 * time.Minute, CreatedAt: now0.Add(-24 * time.Hour)}
)

func idle() SessionObs {
	return SessionObs{SessionID: "s", Native: "idle", StatusSince: now0.Add(-time.Hour)}
}

func TestDeadlineCases(t *testing.T) {
	due := now0.Add(5 * time.Minute)
	cases := []struct {
		name   string
		l      claudesession.LoopState
		want   time.Time
		driven bool
	}{
		{"pending arm", claudesession.LoopState{ArmAt: now0, ScheduledFor: due, TurnAt: now0}, due, true},
		{"arm consumed by loop fire", claudesession.LoopState{ArmAt: now0.Add(-time.Hour), ScheduledFor: due, LoopFireAt: now0.Add(-50 * time.Minute), FireAt: now0.Add(-50 * time.Minute)}, now0.Add(-20 * time.Minute), true},
		{"cron fire only", claudesession.LoopState{FireAt: now0.Add(-time.Hour)}, now0.Add(-30 * time.Minute), true},
		{"turn only", claudesession.LoopState{TurnAt: now0.Add(-10 * time.Minute)}, now0.Add(20 * time.Minute), false},
		{"nothing", claudesession.LoopState{}, role.CreatedAt.Add(30 * time.Minute), false},
	}
	for _, c := range cases {
		got, driven := Deadline(role, c.l)
		if !got.Equal(c.want) || driven != c.driven {
			t.Errorf("%s: %v %v, want %v %v", c.name, got, driven, c.want, c.driven)
		}
	}
}

func TestJudgeErrIsUnknown(t *testing.T) {
	v := Judge(role, Observation{Err: errors.New("boom")}, now0)
	if v.State != StateUnknown || v.Reason != "boom" || v.Alerting() {
		t.Fatalf("%+v", v)
	}
}

func TestJudgeIdleWithinDeadlineOK(t *testing.T) {
	l := claudesession.LoopState{TurnAt: now0.Add(-5 * time.Minute)}
	if v := Judge(role, Observation{Live: []SessionObs{idle()}, Loop: l}, now0); v.State != StateOK {
		t.Fatalf("%+v", v)
	}
}

func TestJudgePendingWakeupWins(t *testing.T) {
	l := claudesession.LoopState{ArmAt: now0.Add(-3 * time.Hour), ScheduledFor: now0.Add(5 * time.Minute), TurnAt: now0.Add(-2 * time.Hour)}
	if v := Judge(role, Observation{Live: []SessionObs{idle()}, Loop: l}, now0); v.State != StateOK || !v.Deadline.After(now0) {
		t.Fatalf("%+v", v)
	}
}

func TestJudgeMissedWakeupSilent(t *testing.T) {
	due := now0.Add(-11 * time.Minute)
	l := claudesession.LoopState{ArmAt: now0.Add(-time.Hour), ScheduledFor: due}
	v := Judge(role, Observation{Live: []SessionObs{idle()}, Loop: l}, now0)
	if v.State != StateSilent || !v.Episode.Equal(due) || !strings.Contains(v.Reason, "wakeup due 11:49 never fired") {
		t.Fatalf("%+v", v)
	}
}

func TestJudgeHumanTurnDoesNotMoveWakeupDeadline(t *testing.T) {
	l := claudesession.LoopState{ArmAt: now0.Add(-time.Hour), ScheduledFor: now0.Add(-40 * time.Minute), TurnAt: now0.Add(-time.Minute)}
	if v := Judge(role, Observation{Live: []SessionObs{idle()}, Loop: l}, now0); v.State != StateSilent {
		t.Fatalf("%+v", v)
	}
}

func TestJudgeCadenceOnlyTurnMovesDeadline(t *testing.T) {
	old := claudesession.LoopState{TurnAt: now0.Add(-50 * time.Minute)}
	if v := Judge(role, Observation{Live: []SessionObs{idle()}, Loop: old}, now0); v.State != StateSilent || !strings.Contains(v.Reason, "no turn since") {
		t.Fatalf("old: %+v", v)
	}
	fresh := claudesession.LoopState{TurnAt: now0.Add(-time.Minute)}
	if v := Judge(role, Observation{Live: []SessionObs{idle()}, Loop: fresh}, now0); v.State != StateOK {
		t.Fatalf("fresh: %+v", v)
	}
}

func TestJudgeBusyUnderStallIsOK(t *testing.T) {
	s := SessionObs{Native: "busy", ActivityMtime: now0.Add(-19 * time.Minute)}
	l := claudesession.LoopState{TurnAt: now0.Add(-5 * time.Hour)}
	if v := Judge(role, Observation{Live: []SessionObs{s}, Loop: l}, now0); v.State != StateOK || v.Reason != "busy" {
		t.Fatalf("%+v", v)
	}
}

func TestJudgeBusyPastStallStuck(t *testing.T) {
	m := now0.Add(-21 * time.Minute)
	v := Judge(role, Observation{Live: []SessionObs{{Native: "busy", ActivityMtime: m}}}, now0)
	if v.State != StateStuck || !v.Episode.Equal(m) {
		t.Fatalf("%+v", v)
	}
}

func TestJudgeNoSessionAbsentAfterGrace(t *testing.T) {
	due := now0.Add(-11 * time.Minute)
	l := claudesession.LoopState{ArmAt: now0.Add(-time.Hour), ScheduledFor: due}
	v := Judge(role, Observation{Loop: l}, now0)
	if v.State != StateAbsent || !v.Episode.Equal(due) {
		t.Fatalf("%+v", v)
	}
	l.ScheduledFor = now0.Add(-5 * time.Minute)
	if v := Judge(role, Observation{Loop: l}, now0); v.State != StateOK {
		t.Fatalf("within grace: %+v", v)
	}
}

func TestJudgeWaitingReason(t *testing.T) {
	l := claudesession.LoopState{ArmAt: now0.Add(-time.Hour), ScheduledFor: now0.Add(-time.Hour)}
	s := SessionObs{Native: "waiting", StatusSince: now0.Add(-30 * time.Minute)}
	v := Judge(role, Observation{Live: []SessionObs{s}, Loop: l}, now0)
	if v.State != StateSilent || v.Reason != "waiting for you since 11:30" {
		t.Fatalf("%+v", v)
	}
}

func TestJudgeEpisodeNotBeforeCreation(t *testing.T) {
	r := role
	r.CreatedAt = now0.Add(-5 * time.Minute)
	l := claudesession.LoopState{ArmAt: now0.Add(-time.Hour), ScheduledFor: now0.Add(-40 * time.Minute)}
	v := Judge(r, Observation{Live: []SessionObs{idle()}, Loop: l}, now0)
	if v.State != StateSilent || !v.Episode.Equal(r.CreatedAt) {
		t.Fatalf("%+v", v)
	}
}

func TestJudgeReasonTimesAreLocal(t *testing.T) {
	prev := time.Local
	time.Local = time.FixedZone("plus2", 2*3600)
	t.Cleanup(func() { time.Local = prev })
	due := now0.Add(-11 * time.Minute) // 11:49Z = 13:49 local
	l := claudesession.LoopState{ArmAt: now0.Add(-time.Hour), ScheduledFor: due}
	v := Judge(role, Observation{Loop: l}, now0)
	if !strings.Contains(v.Reason, "due 13:49") {
		t.Fatalf("reason = %q, want local 13:49", v.Reason)
	}
}

// TestMain pins the zone: the reasons print local times.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

func TestJudgeEpisodeIsMillisecondPrecise(t *testing.T) {
	// A CreatedAt from time.Now() carries nanoseconds; journal ids keep
	// milliseconds, so a key must too or a closed episode never matches.
	r := role
	r.CreatedAt = now0.Add(time.Hour).Add(123456789 * time.Nanosecond)
	v := Judge(r, Observation{Loop: claudesession.LoopState{ArmAt: now0.Add(-time.Hour), ScheduledFor: now0.Add(-20 * time.Minute)}}, now0.Add(2*time.Hour))
	if v.Episode.IsZero() || v.Episode.Nanosecond()%1_000_000 != 0 {
		t.Fatalf("episode %v keeps sub-millisecond digits", v.Episode)
	}
}
