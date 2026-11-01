package admission

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func (f *fakeSampler) setSwapouts(bytes uint64) {
	f.mu.Lock()
	f.mem.Swapouts = bytes
	f.mu.Unlock()
}

// withHistory attaches a history in a temp dir to the harness's controller.
func (h *harness) withHistory(t *testing.T) *History {
	t.Helper()
	hist := NewHistory(filepath.Join(t.TempDir(), historyFileName))
	h.c.history = hist
	return hist
}

func readAll(t *testing.T, h *History) []pigtail.AdmissionRecord {
	t.Helper()
	recs, err := h.Read(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// A wait is split by what caused it: first the job ceiling, then memory once
// a lease ahead is gone but the machine has not recovered.
func TestHistorySplitsTheWaitByReason(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 2
	h := newHarness(t, cfg, 40*gib)
	hist := h.withHistory(t)

	a := h.enqueue(t, "a", "tsc")
	other := h.enqueue(t, "other", "tsc") // keeps a lease on the books throughout
	b := h.enqueue(t, "b", "tsc")
	if !ready(a) || !ready(other) || ready(b) {
		t.Fatal("setup: a and other run, b waits for the ceiling")
	}
	h.clock.advance(10 * time.Second)
	h.s.set(5*gib, 1) // below the floor: memory takes over once a is gone
	h.c.Tick()
	h.c.Release(h.c.Grant(a))
	if info := h.c.Position(b); info.Reason != ReasonMemory {
		t.Fatalf("setup: b now waits for %q, want memory", info.Reason)
	}
	h.clock.advance(20 * time.Second)
	h.s.set(40*gib, 1)
	h.c.Tick()
	if !ready(b) {
		t.Fatal("setup: b must be admitted once memory is back")
	}
	h.clock.advance(time.Minute)
	h.c.Release(h.c.Grant(b))
	h.c.Release(h.c.Grant(other))

	recs := readAll(t, hist)
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}
	r := recs[1]
	if r.Group != "b" || r.Outcome != pigtail.AdmissionOutcomeReleased {
		t.Fatalf("record = %+v", r)
	}
	if r.WaitedMS != 30000 || r.HeldMS != 60000 {
		t.Fatalf("waited %d held %d, want 30000 and 60000", r.WaitedMS, r.HeldMS)
	}
	want := map[string]int64{"max_jobs": 10000, "memory": 20000}
	if fmt.Sprint(r.WaitByReasonMS) != fmt.Sprint(want) {
		t.Fatalf("wait_by_reason_ms = %v, want %v", r.WaitByReasonMS, want)
	}
	if r.AvailAtEnqueue != 40*gib || r.CostBytes == 0 || !r.EnqueuedAt.Equal(r.TS.Add(-90*time.Second)) {
		t.Fatalf("record = %+v", r)
	}
	if recs[0].WaitByReasonMS != nil || recs[0].WaitedMS != 0 {
		t.Fatalf("an immediate grant waited for nothing, got %+v", recs[0])
	}
}

// What the machine swapped out while a command ran is taken from the
// cumulative counter, and the worst pressure it lived through is kept.
func TestHistoryRecordsSwapAndPressureWhileRunning(t *testing.T) {
	h := newHarness(t, testConfig(), 40*gib)
	hist := h.withHistory(t)
	h.s.setSwapouts(5 * gib) // since boot, before the command
	a := h.enqueue(t, "a", "tsc")
	if !ready(a) {
		t.Fatal("setup: a must run")
	}
	h.clock.advance(20 * time.Second)
	h.s.setSwapouts(5*gib + 100<<20)
	h.s.set(40*gib, 2)
	h.c.Tick()
	h.clock.advance(20 * time.Second)
	h.s.setSwapouts(5*gib + 300<<20)
	h.s.set(40*gib, 1)
	h.c.Tick()
	h.c.Release(h.c.Grant(a))

	r := readAll(t, hist)[0]
	if r.SwapoutBytesDuring != 300<<20 {
		t.Fatalf("swapout_bytes_during = %d, want %d", r.SwapoutBytesDuring, 300<<20)
	}
	if r.MaxPressure != 2 {
		t.Fatalf("max_pressure = %d, want 2", r.MaxPressure)
	}
}

func TestHistoryRecordsRevokedAndCancelled(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 1
	h := newHarness(t, cfg, 40*gib)
	hist := h.withHistory(t)

	a := h.enqueue(t, "a", "tsc")
	b := h.enqueue(t, "b", "tsc")
	h.clock.advance(5 * time.Second)
	h.c.Cancel(b)
	h.clock.advance(time.Hour)
	h.c.Tick() // past MaxHold: a is revoked
	if len(h.c.Status().Leases) != 0 {
		t.Fatal("setup: a must be revoked")
	}
	h.c.Release(h.c.Grant(a)) // already revoked: no second line

	recs := readAll(t, hist)
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2: %+v", len(recs), recs)
	}
	c, r := recs[0], recs[1]
	if c.Outcome != pigtail.AdmissionOutcomeCancelled || c.Group != "b" || c.WaitedMS != 5000 || c.HeldMS != 0 ||
		c.WaitByReasonMS["max_jobs"] != 5000 {
		t.Fatalf("cancelled record = %+v", c)
	}
	if r.Outcome != pigtail.AdmissionOutcomeRevoked || r.Group != "a" || r.HeldMS != time.Hour.Milliseconds()+5000 {
		t.Fatalf("revoked record = %+v", r)
	}
}

// A grant made by the MaxStarve guarantee is flagged degraded.
func TestHistoryFlagsStarvedGrants(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 1
	cfg.MaxStarve = time.Minute
	h := newHarness(t, cfg, 40*gib)
	hist := h.withHistory(t)
	a := h.enqueue(t, "a", "tsc")
	b := h.enqueue(t, "b", "tsc")
	h.clock.advance(2 * time.Minute)
	h.c.Tick()
	if !ready(b) {
		t.Fatal("setup: b must be admitted past MaxStarve")
	}
	h.c.Release(h.c.Grant(b))
	h.c.Release(h.c.Grant(a))
	recs := readAll(t, hist)
	if len(recs) != 2 || !recs[0].Degraded || recs[1].Degraded {
		t.Fatalf("records = %+v", recs)
	}
}

// The history is measurement: a write that fails is logged, and admission
// carries on as if nothing happened.
func TestHistoryWriteFailureDoesNotBlock(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 1
	h := newHarness(t, cfg, 40*gib)
	var mu sync.Mutex
	var logged []string
	h.c.logf = func(f string, a ...any) { mu.Lock(); logged = append(logged, fmt.Sprintf(f, a...)); mu.Unlock() }
	h.c.history = NewHistory(filepath.Join(t.TempDir(), "missing", "dir", historyFileName))

	a := h.enqueue(t, "a", "tsc")
	b := h.enqueue(t, "b", "tsc")
	h.c.Release(h.c.Grant(a))
	if !ready(b) {
		t.Fatal("a failed history write must not keep the next one waiting")
	}
	h.c.Release(h.c.Grant(b))
	if n := len(h.c.Status().Leases); n != 0 {
		t.Fatalf("%d leases left on the books", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logged) != 2 || !strings.Contains(logged[0], "history") {
		t.Fatalf("logged = %q", logged)
	}
}

func TestHistoryRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, historyFileName)
	h := NewHistory(path)
	if err := h.Rotate(10); err != nil {
		t.Fatalf("no file yet is not an error: %v", err)
	}
	old := pigtail.AdmissionRecord{TS: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Group: "old"}
	if err := h.Append(old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".1", []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.Rotate(1 << 20); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("under the limit the file must stay put")
	}
	if err := h.Rotate(10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("past the limit the file must move, stat = %v", err)
	}
	if err := h.Append(pigtail.AdmissionRecord{TS: old.TS.Add(time.Hour), Group: "new"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("new file: %v, %v", fi, err)
	}
	// Read spans both files, oldest first, and filters on since; the old .1
	// (and its unreadable line) was overwritten.
	recs, err := h.Read(time.Time{})
	if err != nil || len(recs) != 2 || recs[0].Group != "old" || recs[1].Group != "new" {
		t.Fatalf("read = %+v, %v", recs, err)
	}
	recs, _ = h.Read(old.TS.Add(time.Minute))
	if len(recs) != 1 || recs[0].Group != "new" {
		t.Fatalf("since filter: %+v", recs)
	}
}

func TestHistoryPath(t *testing.T) {
	if HistoryPath("") != "" {
		t.Fatal("no database, no history")
	}
	if got := HistoryPath("/x/pigtail.db"); got != "/x/admission-history.jsonl" {
		t.Fatalf("HistoryPath = %q", got)
	}
	if NewHistory("") != nil {
		t.Fatal("an empty path must yield the nil (disabled) history")
	}
}

// TestHistoryRecordsCompressorWhileRunning: what the compressor did while a
// command ran, recorded even with the gate off — it is what recalibrates it.
func TestHistoryRecordsCompressorWhileRunning(t *testing.T) {
	h := newHarness(t, testConfig(), 40*gib) // CompressBytesPerSec 0: gate off
	hist := h.withHistory(t)
	h.s.setCompress(5*gib, 5*gib)
	a := h.enqueue(t, "a", "tsc")
	if !ready(a) {
		t.Fatal("setup: a must run")
	}
	h.clock.advance(10 * time.Second)
	h.s.setCompress(5*gib+100<<20, 5*gib+60<<20) // +160 MiB in 10 s = 16 MiB/s
	h.c.Tick()
	h.clock.advance(10 * time.Second)
	h.s.setCompress(5*gib+200<<20, 5*gib+100<<20) // +140 MiB in 10 s = 14 MiB/s
	h.c.Tick()
	h.c.Release(h.c.Grant(a))

	r := readAll(t, hist)[0]
	if r.CompressBytesDuring != 300<<20 {
		t.Fatalf("compress_bytes_during = %d, want %d", r.CompressBytesDuring, 300<<20)
	}
	if r.MaxCompressRate != 16<<20 {
		t.Fatalf("max_compress_rate = %d, want %d", r.MaxCompressRate, 16<<20)
	}
}

func TestHistoryRecordCarriesFocus(t *testing.T) {
	h := newHarness(t, testConfig(), 40*gib)
	hist := h.withHistory(t)
	f := h.focusEnqueue(t, "f", "tsc", true)
	n := h.focusEnqueue(t, "n", "tsc", false)
	h.c.Release(h.c.Grant(f))
	h.c.Release(h.c.Grant(n))
	recs := readAll(t, hist)
	if len(recs) != 2 || !recs[0].Focus || recs[1].Focus {
		t.Fatalf("records = %+v", recs)
	}
	raw, err := os.ReadFile(hist.path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if !strings.Contains(lines[0], `"focus":true`) || strings.Contains(lines[1], `"focus"`) {
		t.Fatalf("lines = %v", lines)
	}
}

func TestCancelledRecordCarriesFocus(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 1
	h := newHarness(t, cfg, 40*gib)
	hist := h.withHistory(t)
	h.enqueue(t, "x", "tsc")
	b := h.focusEnqueue(t, "b", "tsc", true)
	h.c.Cancel(b)
	recs := readAll(t, hist)
	if len(recs) != 1 || recs[0].Outcome != pigtail.AdmissionOutcomeCancelled || !recs[0].Focus {
		t.Fatalf("records = %+v", recs)
	}
}

func TestStatusJSONFocusOnlyBoolean(t *testing.T) {
	st := pigtail.AdmissionStatus{Queue: []pigtail.AdmissionWaiterView{{Group: "g", Class: "tsc", Focus: true}}}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"focus":true`) {
		t.Fatalf("json = %s", raw)
	}
	for _, k := range []string{"focus_name", `"subject`, `"title`} {
		if strings.Contains(string(raw), k) {
			t.Fatalf("json leaks %q: %s", k, raw)
		}
	}
}

// The exit code a client reports before releasing lands on the released
// line; a lease released without a report, or revoked, carries none.
func TestHistoryRecordsTheExitCode(t *testing.T) {
	cfg := testConfig()
	cfg.MaxJobs = 4
	h := newHarness(t, cfg, 40*gib)
	hist := h.withHistory(t)

	a := h.enqueue(t, "a", "tsc")
	b := h.enqueue(t, "b", "tsc")
	c := h.enqueue(t, "c", "tsc")
	d := h.enqueue(t, "d", "tsc")
	if !ready(a) || !ready(b) || !ready(c) || !ready(d) {
		t.Fatal("setup: all four run")
	}
	ga, gb, gc, gd := h.c.Grant(a), h.c.Grant(b), h.c.Grant(c), h.c.Grant(d)

	if h.c.ReportExit("nope", 1) {
		t.Fatal("an unknown id must report false")
	}
	if !h.c.ReportExit(ga.ID, 3) || !h.c.ReportExit(gb.ID, 0) || !h.c.ReportExit(gd.ID, 9) {
		t.Fatal("a booked lease must accept its exit")
	}
	h.c.Release(ga)
	h.c.Release(gb)
	h.c.Release(gc) // no report
	if h.c.ReportExit(ga.ID, 4) {
		t.Fatal("a released lease must not accept an exit")
	}
	h.clock.advance(time.Hour + time.Second)
	h.c.Tick() // past MaxHold: d is revoked, after it reported
	if h.c.ReportExit(gd.ID, 5) {
		t.Fatal("a revoked lease must not accept an exit")
	}

	recs := readAll(t, hist)
	if len(recs) != 4 {
		t.Fatalf("got %d records, want 4: %+v", len(recs), recs)
	}
	byGroup := map[string]pigtail.AdmissionRecord{}
	for _, r := range recs {
		byGroup[r.Group] = r
	}
	if r := byGroup["a"]; r.ExitCode == nil || *r.ExitCode != 3 {
		t.Fatalf("a: exit_code = %v, want 3", r.ExitCode)
	}
	if r := byGroup["b"]; r.ExitCode == nil || *r.ExitCode != 0 {
		t.Fatalf("b: exit_code = %v, want 0 written", r.ExitCode)
	}
	if r := byGroup["c"]; r.ExitCode != nil {
		t.Fatalf("c: exit_code = %v, want none", *r.ExitCode)
	}
	if r := byGroup["d"]; r.Outcome != pigtail.AdmissionOutcomeRevoked || r.ExitCode != nil {
		t.Fatalf("d: %+v, want revoked with no exit_code", r)
	}
}

func TestHistoryCountsTheCalmWait(t *testing.T) {
	h := newHarness(t, calmConfig(), 60*gib)
	hist := h.withHistory(t)
	h.s.setCompress(1*gib, 1*gib)
	h.enqueue(t, "a", "tsc")
	h.churn(t, 2*time.Second, 600<<20)
	b := h.enqueue(t, "b", "build")
	h.churn(t, 70*time.Second, 0)
	if !ready(b) {
		t.Fatal("setup: b must start once calm")
	}
	h.c.Release(h.c.Grant(b))
	if r := readAll(t, hist)[0]; r.WaitByReasonMS["compress_calm"] < 55_000 {
		t.Fatalf("wait by reason = %v, want the calm wait counted as compress_calm", r.WaitByReasonMS)
	}
}

func TestHistoryRecordsTheCompressorAtAdmission(t *testing.T) {
	cfg := testConfig() // compressor gate off: nothing holds b back
	cfg.CompressCalmWindow = time.Minute
	h := newHarness(t, cfg, 40*gib)
	hist := h.withHistory(t)
	h.s.setCompress(1*gib, 1*gib)
	h.enqueue(t, "a", "tsc")
	h.churn(t, 2*time.Second, 200<<20) // 400 MiB in 2 s
	h.churn(t, 4*time.Second, 0)       // still inside the 10 s window
	b := h.enqueue(t, "b", "tsc")
	if !ready(b) {
		t.Fatal("setup: b must run")
	}
	h.c.Release(h.c.Grant(b))
	r := readAll(t, hist)[0]
	total := float64(400 << 20)
	if want10, wantCalm := uint64(total/6), uint64(total/6); r.AdmitCompressRate != want10 || r.AdmitCompressCalmRate != wantCalm {
		t.Fatalf("at admission: 10 s %d, calm %d; want %d and %d", r.AdmitCompressRate, r.AdmitCompressCalmRate, want10, wantCalm)
	}
	raw, err := os.ReadFile(hist.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"admit_compress_rate":`, `"admit_compress_calm_rate":`} {
		if !strings.Contains(string(raw), k) {
			t.Fatalf("history line lacks %s: %s", k, raw)
		}
	}
}

func TestHistoryCalmRateIsZeroWhenTheCalmWindowIsZero(t *testing.T) {
	cfg := testConfig()
	cfg.CompressCalmWindow = 0 // window 0: the calm rate is never measured
	h := newHarness(t, cfg, 40*gib)
	hist := h.withHistory(t)
	h.s.setCompress(1*gib, 1*gib)
	h.enqueue(t, "a", "tsc")
	h.churn(t, 2*time.Second, 200<<20)
	// Traffic and time pass without a Tick, so only b's enqueue reads it:
	// the zero is then the window's doing, not an unread counter.
	h.clock.advance(time.Second)
	h.s.mu.Lock()
	h.s.mem.Decompressions += 400 << 20
	h.s.mu.Unlock()
	h.clock.advance(11 * time.Second)
	b := h.enqueue(t, "b", "tsc")
	if !ready(b) {
		t.Fatal("setup: b must run")
	}
	h.c.Release(h.c.Grant(b))
	if r := readAll(t, hist)[0]; r.AdmitCompressCalmRate != 0 {
		t.Fatalf("calm rate %d with a zero calm window; want 0", r.AdmitCompressCalmRate)
	}
}
