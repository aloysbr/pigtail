package claudeusage

import (
	"errors"
	"testing"
	"time"
)

// aheadPick: source m (perso), target i (perso), both read at pickNow.
func aheadPick(t *testing.T, mSel, mSw *float64, m5, m7 float64, iAge time.Duration, i7 float64) Choice {
	t.Helper()
	mp, ip := rankedCell("mp", "perso", "m", 0), rankedCell("ip", "perso", "i", 1)
	mp.SelectPct, mp.SwitchPct = mSel, mSw
	files := map[string][]byte{
		fileOf(mp): usageFile(uuidM, time.Minute, &win{m5, 2 * hour}, &win{m7, day}),
		fileOf(ip): usageFile(uuidI, iAge, &win{10, 2 * hour}, &win{i7, 5 * day}),
	}
	return mustPick(t, []CellInput{mp, ip}, files, PickOptions{Scope: "perso"})
}

func TestAheadTargetMoves(t *testing.T) {
	ch := aheadPick(t, nil, nil, 10, 98, time.Minute, 40)
	got, why := AheadTarget(ch, uuidM)
	if got == nil || got.AccountUUID != uuidI {
		t.Fatalf("target = %v (%q), want i", got, why)
	}
}

func TestAheadTargetStale(t *testing.T) {
	ch := aheadPick(t, nil, nil, 10, 98, 45*time.Minute, 40)
	if ch.Group != GroupStale {
		t.Fatalf("precondition: group %s", ch.Group)
	}
	if got, why := AheadTarget(ch, uuidM); got != nil || why != "no fresh account under its ceiling" {
		t.Fatalf("got %v %q", got, why)
	}
}

func TestAheadTargetAllOver(t *testing.T) {
	ch := aheadPick(t, nil, nil, 10, 98, time.Minute, 95)
	if ch.Group != GroupOverMargin {
		t.Fatalf("precondition: group %s", ch.Group)
	}
	if got, why := AheadTarget(ch, uuidM); got != nil || why != "no fresh account under its ceiling" {
		t.Fatalf("got %v %q", got, why)
	}
}

func TestAheadDecisionNoReading(t *testing.T) {
	mp, ip := rankedCell("mp", "perso", "m", 0), rankedCell("ip", "perso", "i", 1)
	files := map[string][]byte{
		fileOf(mp): usageFile(uuidM, time.Minute, &win{10, 2 * hour}, &win{98, day}),
		fileOf(ip): noCacheCell(uuidI),
	}
	ch := mustPick(t, []CellInput{mp, ip}, files, PickOptions{Scope: "perso"})
	if ch.Group != GroupNoReading {
		t.Fatalf("precondition: group %s", ch.Group)
	}
	if got, why := AheadTarget(ch, uuidM); got != nil || why != "no fresh account under its ceiling" {
		t.Fatalf("got %v %q", got, why)
	}
}

func TestAheadTargetReasons(t *testing.T) {
	ch := aheadPick(t, nil, nil, 10, 97.9, time.Minute, 40)
	if got, why := AheadTarget(ch, uuidM); got != nil || why != "source under its switch threshold" {
		t.Fatalf("97.9: got %v %q", got, why)
	}
	ch = aheadPick(t, fp(70), fp(78), 10, 78, time.Minute, 40)
	if got, why := AheadTarget(ch, uuidM); got == nil || got.AccountUUID != uuidI {
		t.Fatalf("m at 78: got %v %q, want i", got, why)
	}
	ch = aheadPick(t, fp(70), fp(78), 10, 77.9, time.Minute, 40)
	if got, why := AheadTarget(ch, uuidM); got != nil || why != "source under its switch threshold" {
		t.Fatalf("m at 77.9: got %v %q", got, why)
	}
	if got, why := AheadTarget(ch, "99999999-9999-4999-8999-999999999999"); got != nil || why != "source not in ranking" {
		t.Fatalf("absent source: got %v %q", got, why)
	}
	ch = aheadPick(t, nil, nil, 98.2, 10, time.Minute, 40)
	var src Candidate
	for _, c := range ch.Ranking {
		if c.AccountUUID == uuidM {
			src = c
		}
	}
	if w, p, ok := src.OverSwitch(); w != "5h" || p != 98.2 || !ok {
		t.Fatalf("OverSwitch = %q %v %v, want 5h 98.2 true", w, p, ok)
	}
}

func TestSnapshotOverSwitch(t *testing.T) {
	mp := rankedCell("mp", "perso", "m", 0)
	read := func(five, seven *win) Snapshot {
		return snapshots([]CellInput{mp}, map[string][]byte{fileOf(mp): usageFile(uuidM, time.Minute, five, seven)})[0]
	}
	d := Thresholds{90, 98}
	cases := []struct {
		name string
		s    Snapshot
		win  string
		pct  float64
		ok   bool
	}{
		{"7d at 98", read(&win{10, hour}, &win{98, day}), "7d", 98, true},
		{"7d at 97.9", read(&win{10, hour}, &win{97.9, day}), "", 0, false},
		{"5h at 98 reset passed", read(&win{98, -time.Minute}, &win{10, day}), "", 0, false},
		{"5h at 98", read(&win{98, hour}, &win{10, day}), "5h", 98, true},
	}
	errSnap := read(&win{10, hour}, &win{99, day})
	errSnap.Err = errors.New("boom")
	cases = append(cases, struct {
		name string
		s    Snapshot
		win  string
		pct  float64
		ok   bool
	}{"err", errSnap, "", 0, false})
	noRead := snapshots([]CellInput{mp}, map[string][]byte{fileOf(mp): noCacheCell(uuidM)})[0]
	cases = append(cases, struct {
		name string
		s    Snapshot
		win  string
		pct  float64
		ok   bool
	}{"no reading", noRead, "", 0, false})
	for _, c := range cases {
		w, p, ok := SnapshotOverSwitch(c.s, d, pickNow)
		if w != c.win || p != c.pct || ok != c.ok {
			t.Errorf("%s: got %q %v %v, want %q %v %v", c.name, w, p, ok, c.win, c.pct, c.ok)
		}
	}
}
