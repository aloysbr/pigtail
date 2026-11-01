package waittype

import (
	"testing"
	"time"
)

var e0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func TestHeldOver(t *testing.T) {
	alive := func(pid int) bool { return pid == 4242 }
	for _, tc := range []struct {
		name   string
		until  time.Time
		holder int
		want   bool
	}{
		{"until passed", e0.Add(-time.Second), 4242, true},
		{"until ahead, holder alive", e0.Add(time.Hour), 4242, false},
		{"holder dead", e0.Add(time.Hour), 999, true},
		{"holder dead, no until", time.Time{}, 999, true},
		{"no holder, no until", time.Time{}, 0, false},
		{"no holder, until ahead", e0.Add(time.Hour), 0, false},
	} {
		if got := HeldOver(e0, tc.until, tc.holder, alive); got != tc.want {
			t.Errorf("%s: HeldOver = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestFailureReset(t *testing.T) {
	for _, tc := range []struct {
		name  string
		until time.Time
		want  bool
	}{
		{"no until", time.Time{}, false},
		{"before its reset", e0.Add(time.Second), false},
		{"at its reset", e0, false},
		{"after its reset", e0.Add(-time.Second), true},
	} {
		if got := FailureReset(e0, tc.until); got != tc.want {
			t.Errorf("%s: FailureReset = %v, want %v", tc.name, got, tc.want)
		}
	}
}
