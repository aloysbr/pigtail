package sessionlaunch

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/spawnreg"
)

// TestBurstAtCountsLaunchesInWindow: BurstMax launches inside BurstWindow
// make the burst Full; at FreesAt the oldest has left the window and the
// next launch passes.
func TestBurstAtCountsLaunchesInWindow(t *testing.T) {
	now := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	var es []spawnreg.Entry
	es = append(es, spawnreg.Entry{TS: now.Add(-BurstWindow - time.Minute)}) // out of the window
	for i := 0; i < BurstMax; i++ {
		es = append(es, spawnreg.Entry{TS: now.Add(-9*time.Minute + time.Duration(i)*time.Second)})
	}
	alive := func(int) bool { return false }
	b := BurstAt(es, now, alive)
	if b.Recent != BurstMax || !b.Full() {
		t.Fatalf("burst = %+v (full %v), want %d and full", b, b.Full(), BurstMax)
	}
	if want := now.Add(-9 * time.Minute).Add(BurstWindow); !b.FreesAt().Equal(want) {
		t.Fatalf("FreesAt = %v, want %v", b.FreesAt(), want)
	}
	if later := BurstAt(es, b.FreesAt(), alive); later.Full() {
		t.Fatalf("at FreesAt the burst is still full: %+v", later)
	}
	if one := BurstAt(es[:BurstMax], now, alive); one.Full() {
		t.Fatalf("%d launches in the window read as full", BurstMax-1)
	}
}
