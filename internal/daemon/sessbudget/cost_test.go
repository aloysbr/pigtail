package sessbudget

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const gib = 1 << 30

func row(id, repo, status string, fp uint64, cpu float64) pigtail.SessionSampleRow {
	return pigtail.SessionSampleRow{SessionID: id, Repo: repo, Status: status, FootprintBytes: fp, CPUPercent: cpu}
}

func TestCostAggregatesByTypeCPUMemoryPeak(t *testing.T) {
	samples := []Sample{
		{TS: t0, Sessions: []pigtail.SessionSampleRow{
			row("a", "pigtail", "busy", gib/2, 40),
			row("b", "pigtail", "busy", gib, 20),
			row("c", "pigtail", "idle", gib/2, 2),
			row("d", "garden", "waiting", gib/4, 0),
		}},
		{TS: t0.Add(30 * time.Second), Sessions: []pigtail.SessionSampleRow{
			row("a", "pigtail", "busy", gib+gib/2, 60),
		}},
	}
	got := Aggregate("24h", samples)
	if got.Window != "24h" || got.Samples != 2 {
		t.Fatalf("header: %+v", got)
	}
	if len(got.Types) != 3 {
		t.Fatalf("types: %+v", got.Types)
	}
	// Sorted by memory mean, descending: busy 1.0, idle 0.5, waiting 0.25 -> 0.3.
	busy := got.Types[0]
	if busy.Repo != "pigtail" || busy.Status != "busy" || busy.Sessions != 2 {
		t.Fatalf("first type: %+v", busy)
	}
	if busy.CPUAvgPct != 40 || busy.MemoryAvgGB != 1.0 || busy.MemoryPeakGB != 1.5 {
		t.Fatalf("busy numbers (cpu mean over rows, memory mean over rows, peak): %+v", busy)
	}
	if got.Types[1].Status != "idle" || got.Types[2].Repo != "garden" || got.Types[2].Status != "waiting" {
		t.Fatalf("order / waiting is its own type: %+v", got.Types)
	}
	if got.Types[1].Sessions != 1 {
		t.Fatalf("idle sessions: %+v", got.Types[1])
	}
}

func TestCostEmpty(t *testing.T) {
	got := Aggregate("1h", nil)
	if got.Samples != 0 || got.Types == nil || len(got.Types) != 0 {
		t.Fatalf("empty: %+v (types must be [] not null)", got)
	}
}
