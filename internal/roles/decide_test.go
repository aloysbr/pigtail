package roles

import (
	"testing"
	"time"
)

var k1 = now0.Add(-2 * time.Hour)

func silentV(k time.Time) Verdict { return Verdict{State: StateSilent, Episode: k, Deadline: k} }

func TestDecideRaisesOnce(t *testing.T) {
	if a := Decide(nil, nil, silentV(k1), false, now0); !a.Raise.Equal(k1) {
		t.Fatalf("%+v", a)
	}
	for _, st := range []State{StateSilent, StateStuck, StateAbsent} {
		v := Verdict{State: st, Episode: now0}
		if a := Decide([]Episode{{Key: k1}}, nil, v, false, now0); !a.Raise.IsZero() || len(a.Recover) != 0 {
			t.Fatalf("%s: %+v", st, a)
		}
	}
}

func TestDecideRecoversOnlyOnFutureDeadline(t *testing.T) {
	open := []Episode{{Key: k1}, {Key: now0.Add(-time.Hour), Acked: true}}
	if a := Decide(open, nil, Verdict{State: StateOK, Deadline: now0.Add(-time.Minute)}, false, now0); len(a.Recover) != 0 {
		t.Fatalf("past deadline recovered: %+v", a)
	}
	a := Decide(open, nil, Verdict{State: StateOK, Deadline: now0.Add(time.Minute)}, false, now0)
	if len(a.Recover) != 2 || !a.Raise.IsZero() {
		t.Fatalf("%+v", a)
	}
}

func TestDecideNeverReraisesClosedKey(t *testing.T) {
	a := Decide(nil, []time.Time{k1}, silentV(k1), false, now0)
	if !a.Raise.IsZero() || !a.Reclosed.Equal(k1) {
		t.Fatalf("%+v", a)
	}
	k2 := k1.Add(time.Hour)
	if a := Decide(nil, []time.Time{k1}, silentV(k2), false, now0); !a.Raise.Equal(k2) || !a.Reclosed.IsZero() {
		t.Fatalf("%+v", a)
	}
}

func TestDecideUnknownAndHeldWriteNothing(t *testing.T) {
	open := []Episode{{Key: k1}}
	if a := Decide(open, nil, Unknown("x"), false, now0); a.Raise != (time.Time{}) || len(a.Recover) != 0 {
		t.Fatalf("unknown: %+v", a)
	}
	if a := Decide(nil, nil, silentV(k1), true, now0); !a.Raise.IsZero() {
		t.Fatalf("held raise: %+v", a)
	}
	if a := Decide(open, nil, Verdict{State: StateOK, Deadline: now0.Add(time.Hour)}, true, now0); len(a.Recover) != 0 {
		t.Fatalf("held recover: %+v", a)
	}
}

func TestDecideClosedKeyMatchesAtMillisecond(t *testing.T) {
	key := time.Date(2026, 10, 30, 12, 0, 0, 123_000_000, time.UTC)
	v := Verdict{State: StateSilent, Episode: key.Add(456789 * time.Nanosecond)}
	a := Decide(nil, []time.Time{key}, v, false, key.Add(time.Hour))
	if !a.Raise.IsZero() || a.Reclosed.IsZero() {
		t.Fatalf("%+v", a)
	}
}
