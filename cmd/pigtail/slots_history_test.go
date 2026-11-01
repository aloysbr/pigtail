package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const gib = 1 << 30

func released(class, repo string, held time.Duration, swap uint64) pigtail.AdmissionRecord {
	return pigtail.AdmissionRecord{Class: class, Repo: repo, Outcome: pigtail.AdmissionOutcomeReleased,
		HeldMS: held.Milliseconds(), SwapoutBytesDuring: swap, CostBytes: 2 * gib}
}

// historyFixture: tsc on "web" has a baseline (three clean runs, median 60s)
// and two swapped runs; jest on "api" has too few clean runs to judge.
func historyFixture() []pigtail.AdmissionRecord {
	recs := []pigtail.AdmissionRecord{
		released("tsc", "web", 50*time.Second, 0),
		released("tsc", "web", 60*time.Second, 1<<20), // under the threshold: clean
		released("tsc", "web", 70*time.Second, 0),
		released("tsc", "web", 90*time.Second, 1*gib),
		released("tsc", "web", 150*time.Second, 2*gib),
		released("jest", "api", 40*time.Second, 0),
		released("jest", "api", 100*time.Second, 1*gib),
	}
	// A memory wait with 4 GiB free: 64 GiB more clears 2 GiB + 6 GiB floor.
	w1 := released("tsc", "web", 60*time.Second, 0)
	w1.Class, w1.Repo = "eslint", "web"
	w1.WaitedMS, w1.AvailAtEnqueue = 120000, 4*gib
	w1.WaitByReasonMS = map[string]int64{"memory": 100000, "max_jobs": 20000}
	// A config-dominated wait: not counted as memory-bound.
	w2 := pigtail.AdmissionRecord{Class: "vitest-browser", Repo: "web", Outcome: pigtail.AdmissionOutcomeCancelled,
		WaitedMS: 300000, WaitByReasonMS: map[string]int64{"class_limit": 290000, "pressure": 10000}}
	// A memory wait for a command that would not fit even with the extra
	// memory (its cost is huge).
	w3 := pigtail.AdmissionRecord{Class: "nx", Repo: "web", Outcome: pigtail.AdmissionOutcomeRevoked,
		WaitedMS: 60000, AvailAtEnqueue: 1 * gib, CostBytes: 100 * gib,
		WaitByReasonMS: map[string]int64{"swapout": 60000}}
	return append(recs, w1, w2, w3)
}

func TestSummarizeHistory(t *testing.T) {
	t.Parallel()
	s := summarizeHistory(historyFixture(), 6*gib, 64*gib)
	if s.Commands != 10 || s.Outcomes["released"] != 8 || s.Outcomes["cancelled"] != 1 || s.Outcomes["revoked"] != 1 {
		t.Fatalf("counts: %d %v", s.Commands, s.Outcomes)
	}
	if s.WaitRAM != 170*time.Second || s.WaitConfig != 310*time.Second {
		t.Fatalf("RAM %s config %s", s.WaitRAM, s.WaitConfig)
	}
	if s.WaitByReason["memory"] != 100*time.Second || s.WaitByReason["class_limit"] != 290*time.Second {
		t.Fatalf("by reason %v", s.WaitByReason)
	}

	byKey := map[string]runGroup{}
	for _, g := range s.Groups {
		byKey[g.Class+"/"+g.Repo] = g
	}
	tsc := byKey["tsc/web"]
	if tsc.Clean != 3 || tsc.Swapped != 2 || tsc.CleanMed != 60*time.Second || tsc.SwapMed != 120*time.Second {
		t.Fatalf("tsc/web = %+v", tsc)
	}
	// (90−60) + (150−60)
	if !tsc.EnoughBaseline || tsc.Lost != 120*time.Second {
		t.Fatalf("tsc/web lost = %s (baseline %v)", tsc.Lost, tsc.EnoughBaseline)
	}
	jest := byKey["jest/api"]
	if jest.EnoughBaseline || jest.Lost != 0 {
		t.Fatalf("jest/api must be insufficient: %+v", jest)
	}
	if s.SwapLost != 120*time.Second {
		t.Fatalf("swap lost = %s", s.SwapLost)
	}

	// Two memory-bound waits (eslint, nx); only eslint fits with +64 GiB, and
	// only its RAM part is given back.
	if s.RAMWaits != 2 || s.Avoidable != 1 || s.AvoidableWait != 100*time.Second {
		t.Fatalf("simulation: %d/%d, %s", s.Avoidable, s.RAMWaits, s.AvoidableWait)
	}
	if s.Recoverable() != 220*time.Second {
		t.Fatalf("recoverable = %s", s.Recoverable())
	}

	// Without the extra memory, the eslint wait no longer clears the floor.
	if s0 := summarizeHistory(historyFixture(), 6*gib, 0); s0.Avoidable != 0 {
		t.Fatalf("no extra memory spares nothing, got %d", s0.Avoidable)
	}
}

func TestPrintHistory(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printHistory(&buf, summarizeHistory(historyFixture(), 6*gib, 64*gib), "7d")
	out := buf.String()
	for _, want := range []string{"10 commands", "released 8", "RAM 2m50s", "config 5m10s", "insufficient",
		"1 of 2 memory-bound waits spared", "recoverable: 3m40s", "caveat:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}

	buf.Reset()
	printHistory(&buf, summarizeHistory(nil, 6*gib, 64*gib), "7d")
	if !strings.Contains(buf.String(), "no admitted command") {
		t.Fatalf("empty history: %s", buf.String())
	}
}

func TestParseSpanAndSize(t *testing.T) {
	t.Parallel()
	if d, err := parseSpan("7d"); err != nil || d != 7*24*time.Hour {
		t.Fatalf("7d = %s, %v", d, err)
	}
	if d, err := parseSpan("36h"); err != nil || d != 36*time.Hour {
		t.Fatalf("36h = %s, %v", d, err)
	}
	for _, bad := range []string{"", "0d", "xd", "-1h"} {
		if _, err := parseSpan(bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
	for in, want := range map[string]uint64{"64G": 64 * gib, "64GiB": 64 * gib, "64gb": 64 * gib, "512M": 512 << 20} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Fatalf("%s = %d, %v", in, got, err)
		}
	}
	if _, err := parseSize("64"); err == nil {
		t.Fatal("a size without unit must be refused")
	}
}

func TestReasonsLineCountsCompressAsRAM(t *testing.T) {
	t.Parallel()
	by := map[string]time.Duration{"compress": 90 * time.Second, "max_jobs": time.Minute}
	if got := reasonsLine(by, true); !strings.Contains(got, "compress 1m30s") {
		t.Fatalf("RAM line = %q", got)
	}
	if got := reasonsLine(by, false); strings.Contains(got, "compress") {
		t.Fatalf("config line = %q", got)
	}
}

func TestReasonsLineCountsCompressCalmAsRAM(t *testing.T) {
	by := map[string]time.Duration{"compress_calm": 4 * time.Minute, "max_jobs": time.Minute}
	if got := reasonsLine(by, true); !strings.Contains(got, "compress_calm 4m") {
		t.Fatalf("RAM line = %q", got)
	}
	if got := reasonsLine(by, false); strings.Contains(got, "compress_calm") {
		t.Fatalf("config line = %q", got)
	}
}

// A wait on the disk is not one more memory would have spared: it is listed,
// on the configuration side.
func TestReasonsLineListsDiskOutsideTheRAMWaits(t *testing.T) {
	by := map[string]time.Duration{"disk": 2 * time.Minute, "memory": time.Minute}
	if got := reasonsLine(by, false); !strings.Contains(got, "disk 2m0s") {
		t.Fatalf("non-RAM reasons lack disk: %q", got)
	}
	if got := reasonsLine(by, true); strings.Contains(got, "disk") {
		t.Fatalf("disk must not count as a RAM wait: %q", got)
	}
}
