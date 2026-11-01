package rammon

import (
	"testing"
	"time"
)

func TestWindowWork(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	cases := []struct {
		name      string
		samples   []activitySample
		wantWork  float64
		wantSpan  time.Duration
		wantConns int
	}{
		{"no sample is no evidence", nil, 0, 0, 0},
		{"one sample is no evidence", []activitySample{{at(0), 10, 0}}, 0, 0, 0},
		{"sums the steps over the real span", []activitySample{{at(0), 10, 0}, {at(16), 10.5, 0}, {at(40), 11, 0}}, 1, 40 * time.Second, 0},
		{"the anchor's connections predate the window", []activitySample{{at(0), 10, 3}, {at(16), 10, 1}}, 0, 16 * time.Second, 1},
		{"non-positive span is no CPU evidence, connections still count", []activitySample{{at(30), 10, 0}, {at(10), 12, 2}}, 0, 0, 2},
		{"the largest sweep, not the sum: polling never adds up to a burst", []activitySample{{at(0), 10, 0}, {at(16), 10, 2}, {at(32), 10, 1}, {at(48), 10, 2}}, 0, 48 * time.Second, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work, span, conns := windowWork(tc.samples)
			if work != tc.wantWork || span != tc.wantSpan || conns != tc.wantConns {
				t.Fatalf("windowWork = (%v, %s, %d), want (%v, %s, %d)", work, span, conns, tc.wantWork, tc.wantSpan, tc.wantConns)
			}
		})
	}
}

// A child that exits takes its cumulative CPU with it, so the subtree sum
// drops. The drop is not negative work, and the work done before it stays.
func TestWindowWorkClampsDrops(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := []activitySample{
		{t0, 10, 0},
		{t0.Add(16 * time.Second), 19, 0}, // +9: a compile in a worker
		{t0.Add(32 * time.Second), 12, 0}, // worker exited: -7
		{t0.Add(48 * time.Second), 12.5, 0},
	}
	if work, _, _ := windowWork(s); work != 9.5 {
		t.Fatalf("work = %v, want 9.5 (9 + 0.5, the drop clamped to 0)", work)
	}
}

func TestTrimWindowKeepsOneAnchorAtOrBeforeTheWindowStart(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var s []activitySample
	for i := 0; i <= 30; i++ { // every 16 s for 480 s
		s = append(s, activitySample{at: t0.Add(time.Duration(16*i) * time.Second), cpu: float64(i)})
	}
	now := s[len(s)-1].at
	cutoff := now.Add(-5 * time.Minute)
	got := trimWindow(s, now, 5*time.Minute)
	if got[0].at.After(cutoff) {
		t.Fatalf("dropped the anchor: oldest kept sample %s is inside the window, so the span is < 5 min", got[0].at)
	}
	if !got[1].at.After(cutoff) {
		t.Fatalf("kept more than one sample at or before the window start: %s and %s", got[0].at, got[1].at)
	}
	if got[len(got)-1] != s[len(s)-1] {
		t.Fatal("dropped the newest sample")
	}
}

func TestTrimWindowKeepsEverythingWhileNotFull(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := []activitySample{{t0, 1, 0}, {t0.Add(16 * time.Second), 2, 0}, {t0.Add(32 * time.Second), 3, 0}}
	if got := trimWindow(s, s[2].at, 5*time.Minute); len(got) != 3 {
		t.Fatalf("kept %d samples, want all 3 while the window is not full", len(got))
	}
}

func TestCountNewPeers(t *testing.T) {
	cases := []struct {
		name      string
		prev, cur []string
		want      int
	}{
		{"same set", []string{"127.0.0.1:50001"}, []string{"127.0.0.1:50001"}, 0},
		{"one closed, none opened", []string{"127.0.0.1:50001", "127.0.0.1:50002"}, []string{"127.0.0.1:50001"}, 0},
		{"one new (a reload's websocket)", []string{"127.0.0.1:50001"}, []string{"127.0.0.1:50003"}, 1},
		{"order does not matter", []string{"a:1", "b:2"}, []string{"b:2", "a:1"}, 0},
		{"from nothing", nil, []string{"a:1", "b:2"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := countNewPeers(tc.prev, tc.cur); got != tc.want {
				t.Fatalf("countNewPeers = %d, want %d", got, tc.want)
			}
		})
	}
}
