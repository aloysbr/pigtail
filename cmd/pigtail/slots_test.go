package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestPrintCPUSlotsOff(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printCPUSlots(&buf, pigtail.CPUSlotsResponse{Budget: 0})
	out := buf.String()
	if !strings.Contains(out, "off") {
		t.Fatalf("budget 0 must say the arbiter is off, got:\n%s", out)
	}
	if strings.Contains(out, "GROUP") {
		t.Fatalf("budget 0 must not print a group table, got:\n%s", out)
	}
}

func TestPrintCPUSlotsNoActiveGroup(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printCPUSlots(&buf, pigtail.CPUSlotsResponse{Budget: 5, Held: 0, Waiting: 0})
	out := buf.String()
	if !strings.Contains(out, "budget=5") {
		t.Fatalf("must report the budget, got:\n%s", out)
	}
	if !strings.Contains(out, "no active group") {
		t.Fatalf("no groups must say so, got:\n%s", out)
	}
	if strings.Contains(out, "GROUP") {
		t.Fatalf("no groups must not print an empty table header, got:\n%s", out)
	}
}

func TestPrintCPUSlotsShowsGroups(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printCPUSlots(&buf, pigtail.CPUSlotsResponse{
		Budget:  5,
		Held:    3,
		Waiting: 1,
		Groups: []pigtail.CPUSlotGroupView{
			{Group: "web:vitest", Held: 3, Waiting: 1, Share: 5},
		},
	})
	out := buf.String()
	for _, want := range []string{"budget=5", "held=3", "waiting=1", "GROUP", "web:vitest"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestPrintAdmissionOff(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printAdmission(&buf, pigtail.AdmissionStatus{})
	if !strings.Contains(buf.String(), "off") || strings.Contains(buf.String(), "RUNNING") {
		t.Fatalf("got:\n%s", buf.String())
	}
}

func TestPrintAdmissionShowsLeasesQueueAndCosts(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printAdmission(&buf, pigtail.AdmissionStatus{
		MaxJobs: 6, FloorBytes: 6 << 30, AvailableBytes: 24 << 30, EffectiveBytes: 21 << 30, Pressure: 1,
		Leases: []pigtail.AdmissionLeaseView{{Group: "fix-a", Class: "tsc", RSSBytes: 1 << 30, PeakBytes: 1 << 30, CostBytes: 3 << 29, AgeSeconds: 40, WaitedMS: 62000, Label: "npx tsc --noEmit"}},
		Queue:  []pigtail.AdmissionWaiterView{{Group: "feat-b", Class: "jest", CostBytes: 3 << 30, AgeSeconds: 12, Reason: "memory"}},
		Costs:  []pigtail.AdmissionCostView{{Class: "tsc", Repo: "/r", EWMABytes: 1300 << 20, Samples: 4}},
	})
	out := buf.String()
	for _, want := range []string{"jobs=1/6", "waiting=1", "pressure=normal", "RUNNING", "fix-a", "npx tsc --noEmit", "WAITED", "1m2s", "WAITING", "feat-b", "memory", "learned costs", "(4 runs)", "MEM", "learned costs (EWMA of peak memory)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "RSS") {
		t.Errorf("the lease figure is a footprint now, not RSS:\n%s", out)
	}
}

// The line Ada read green during the 2026-10-21 incident.
func TestPrintAdmissionShowsTheCompressor(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printAdmission(&buf, pigtail.AdmissionStatus{
		MaxJobs: 3, FloorBytes: 6 << 30, AvailableBytes: 16 << 30, EffectiveBytes: 15 << 30, Pressure: 1,
		CompressorBytes: 21 << 30, UnusedBytes: 512 << 20, CompressRateBytesPerSec: 40 << 20,
	})
	if want := "swapout=0 MiB/s · compressor=21.0 GiB · unused=512 MiB · compress=40 MiB/s\n"; !strings.Contains(buf.String(), want) {
		t.Fatalf("missing %q in:\n%s", want, buf.String())
	}
}

func TestSlotsPrintsSessionsSection(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printSessionBudget(&buf, pigtail.SessionBudgetState{
		Enabled: true, Busy: 9, Budget: 20, Gate: "closed", Reason: "démarrage progressif, 40 s",
		DeferredWakes: pigtail.DeferredWakes{Count: 2, OldestAgeSeconds: 180},
	})
	want := "Sessions : 9/20 au travail · porte fermée (démarrage progressif, 40 s)\n" +
		"réveils différés : 2 (plus ancien 3 min)\n"
	if buf.String() != want {
		t.Fatalf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
	buf.Reset()
	printSessionBudget(&buf, pigtail.SessionBudgetState{Enabled: true, Busy: 3, Budget: 20, Gate: "open"})
	if want := "Sessions : 3/20 au travail · porte ouverte\n"; buf.String() != want {
		t.Fatalf("open gate: %q, want %q", buf.String(), want)
	}
}

func TestSlotsOmitsSessionsWhenDisabled(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printSessionBudget(&buf, pigtail.SessionBudgetState{Enabled: false, Busy: 9, Budget: 0,
		DeferredWakes: pigtail.DeferredWakes{Count: 2, OldestAgeSeconds: 180}})
	if buf.Len() != 0 {
		t.Fatalf("disabled budget printed:\n%s", buf.String())
	}
}

func TestSlotsDeferredLineOmittedWhenZero(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printSessionBudget(&buf, pigtail.SessionBudgetState{Enabled: true, Busy: 9, Budget: 20, Gate: "open"})
	if strings.Contains(buf.String(), "réveils différés") {
		t.Fatalf("deferred line at k = 0:\n%s", buf.String())
	}
}

// The command reads GET /session-budget as the caller's scope and prints the
// section after the admission one; enabled:false omits it.
func TestSlotsCommandSessionsSection(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		var scopeHdr string
		mux := http.NewServeMux()
		mux.HandleFunc("/cpu/slots", func(w http.ResponseWriter, _ *http.Request) {
			writeData(w, http.StatusOK, pigtail.CPUSlotsResponse{Budget: 5})
		})
		mux.HandleFunc("/admission", func(w http.ResponseWriter, _ *http.Request) {
			writeData(w, http.StatusOK, pigtail.AdmissionStatus{})
		})
		mux.HandleFunc("/session-budget", func(w http.ResponseWriter, r *http.Request) {
			scopeHdr = r.Header.Get(pigtail.HeaderScope)
			writeData(w, http.StatusOK, pigtail.SessionBudgetState{Enabled: enabled, Busy: 9, Budget: 20, Gate: "open"})
		})
		s := &stubDaemon{srv: httptest.NewServer(mux)}
		out, err := runSlotsOut(t, s)
		s.srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		has := strings.Contains(out, "Sessions : 9/20 au travail · porte ouverte")
		if has != enabled {
			t.Fatalf("enabled=%v: sessions section shown=%v:\n%s", enabled, has, out)
		}
		if scopeHdr == "" {
			t.Fatal("slots did not send the caller's scope (newScopedClient)")
		}
	}
}

func TestPrintAdmissionMarksFocusRow(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printAdmission(&buf, pigtail.AdmissionStatus{
		MaxJobs: 6, FloorBytes: 6 << 30, AvailableBytes: 24 << 30, EffectiveBytes: 21 << 30, Pressure: 1,
		Queue: []pigtail.AdmissionWaiterView{
			{Group: "plain-b", Class: "jest", CostBytes: 3 << 30, Reason: "memory"},
			{Group: "focus-c", Class: "jest", CostBytes: 3 << 30, Focus: true},
		},
	})
	for _, line := range strings.Split(buf.String(), "\n") {
		switch {
		case strings.Contains(line, "focus-c") && !strings.HasPrefix(strings.TrimLeft(line, " "), "*"):
			t.Errorf("focus row not marked: %q", line)
		case strings.Contains(line, "plain-b") && strings.Contains(line, "*"):
			t.Errorf("non-focus row marked: %q", line)
		}
	}
}

// runSlotsOut runs `pigtail slots` against s and returns its stdout.
func runSlotsOut(t *testing.T, s *stubDaemon) (string, error) {
	t.Helper()
	testutil.SerialOnly(t) // swaps os.Stdout
	var out string
	var err error
	withStubDaemon(t, s, func() {
		stdout := os.Stdout
		rd, wr, perr := os.Pipe()
		if perr != nil {
			t.Fatal(perr)
		}
		os.Stdout = wr
		cmd := slotsCmd()
		cmd.SetArgs(nil)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err = cmd.Execute()
		_ = wr.Close()
		os.Stdout = stdout
		b, _ := io.ReadAll(rd)
		out = string(b)
	})
	return out, err
}

// startHistoryDaemon serves canned envelopes for the two history routes; a
// nil cpu answers 404 like a daemon older than GET /cpu/slots/history.
func startHistoryDaemon(t *testing.T, cpu *pigtail.CPUSlotHistory) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/admission/history", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": pigtail.AdmissionHistory{FloorBytes: 6 << 30, Records: []pigtail.AdmissionRecord{}}})
	})
	mux.HandleFunc("/cpu/slots/history", func(w http.ResponseWriter, _ *http.Request) {
		if cpu == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("404 page not found\n")) // the mux's own body, trailing newline included
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": cpu})
	})
	dir := testutil.MkdirTemp(t, "slots-")
	sock := filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	pointClientAt(t, sock)
	t.Cleanup(func() { _ = srv.Close(); _ = os.RemoveAll(dir) })
}

func cpuRecords() *pigtail.CPUSlotHistory {
	now := time.Now()
	return &pigtail.CPUSlotHistory{Records: []pigtail.CPUSlotRecord{
		{TS: now, Group: "ws1:vitest", EnqueuedAt: now, WaitedMS: 1500, HeldMS: 4000, Outcome: pigtail.CPUSlotOutcomeReleased},
		{TS: now, Group: "ws2:vitest", EnqueuedAt: now, WaitedMS: 900, Outcome: pigtail.CPUSlotOutcomeCancelled},
	}}
}

func TestSlotsHistoryShowsCPU(t *testing.T) {
	startHistoryDaemon(t, cpuRecords())
	var out, errb bytes.Buffer
	if err := runSlotsHistory(context.Background(), newClient(), "7d", "64G", false, &out, &errb); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "cpu slots") || !strings.Contains(s, "2 holds") {
		t.Fatalf("the CPU section is missing:\n%s", s)
	}
	if !strings.Contains(s, "released 1") || !strings.Contains(s, "cancelled 1") {
		t.Fatalf("outcomes missing:\n%s", s)
	}
}

func TestSlotsHistoryJSONShape(t *testing.T) {
	startHistoryDaemon(t, cpuRecords())
	var out, errb bytes.Buffer
	if err := runSlotsHistory(context.Background(), newClient(), "7d", "64G", true, &out, &errb); err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	for _, k := range []string{"floor_bytes", "records", "cpu_slots"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("key %q missing: %s", k, out.String())
		}
	}
	var recs []pigtail.CPUSlotRecord
	if err := json.Unmarshal(got["cpu_slots"], &recs); err != nil || len(recs) != 2 {
		t.Fatalf("cpu_slots = %s (%v)", got["cpu_slots"], err)
	}
}

func TestSlotsHistoryCPUUnavailable(t *testing.T) {
	startHistoryDaemon(t, nil)
	var out, errb bytes.Buffer
	if err := runSlotsHistory(context.Background(), newClient(), "7d", "64G", false, &out, &errb); err != nil {
		t.Fatalf("an old daemon must not fail the command: %v", err)
	}
	if !strings.Contains(out.String(), "cpu slots history = unavailable (") {
		t.Fatalf("unavailable line missing:\n%s", out.String())
	}
	if strings.Contains(out.String(), "\n)") {
		t.Fatalf("the error text must be trimmed before the closing paren:\n%q", out.String())
	}
	out.Reset()
	errb.Reset()
	if err := runSlotsHistory(context.Background(), newClient(), "7d", "64G", true, &out, &errb); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "cpu_slots") {
		t.Fatalf("cpu_slots must be omitted when unavailable: %s", out.String())
	}
	if errb.Len() == 0 {
		t.Fatal("the reason must go to stderr")
	}
}

func TestPrintAdmissionShowsTheCalmGate(t *testing.T) {
	st := pigtail.AdmissionStatus{
		MaxJobs: 3, FloorBytes: 6 << 30, AvailableBytes: 16 << 30, EffectiveBytes: 15 << 30, Pressure: 1,
		CompressGateBytesPerSec: 16 << 20, CompressCalmWindowSeconds: 60, CompressCalmRateBytesPerSec: 40 << 20,
		CompressCalmClasses: []string{"build", "vitest-browser"},
		Queue:               []pigtail.AdmissionWaiterView{{Group: "ds", Class: "build", CostBytes: 8 << 30, AgeSeconds: 400, Reason: "compress_calm"}},
	}
	var buf bytes.Buffer
	printAdmission(&buf, st)
	want := "calm gate: build, vitest-browser start only under 16 MiB/s of compressor traffic over 10s and over 1m0s (now 40 MiB/s over 1m0s) — no wait lifts it\n"
	if !strings.Contains(buf.String(), want) || !strings.Contains(buf.String(), "compress_calm") {
		t.Fatalf("missing %q or the compress_calm reason in:\n%s", want, buf.String())
	}
	st.CompressCalmMaxStarveSeconds = 600
	buf.Reset()
	printAdmission(&buf, st)
	if !strings.Contains(buf.String(), "— a wait past 10m0s lifts it\n") {
		t.Fatalf("the rollback flag must show:\n%s", buf.String())
	}
	st.CompressCalmWindowSeconds = 0
	buf.Reset()
	printAdmission(&buf, st)
	if strings.Contains(buf.String(), "calm gate") {
		t.Fatalf("no line when the rule is off:\n%s", buf.String())
	}
}

func TestPrintAdmissionShowsTheDisk(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printAdmission(&buf, pigtail.AdmissionStatus{
		MaxJobs: 4, DiskKnown: true, DiskFreeBytes: 154 << 30, DiskFloorBytes: 20 << 30,
	})
	if want := "disk: 154 GB free (floor 20 GB)"; !strings.Contains(buf.String(), want) {
		t.Fatalf("no disk line %q in:\n%s", want, buf.String())
	}

	buf.Reset()
	printAdmission(&buf, pigtail.AdmissionStatus{MaxJobs: 4, DiskKnown: false, DiskFloorBytes: 20 << 30})
	if want := "disk: unknown (floor 20 GB)"; !strings.Contains(buf.String(), want) {
		t.Fatalf("an unread disk must say so, want %q in:\n%s", want, buf.String())
	}

	buf.Reset()
	printAdmission(&buf, pigtail.AdmissionStatus{MaxJobs: 4, DiskKnown: true, DiskFreeBytes: 154 << 30})
	if want := "disk: 154 GB free (no floor)"; !strings.Contains(buf.String(), want) {
		t.Fatalf("floor off must say so, want %q in:\n%s", want, buf.String())
	}
}

// V2 (criterion 6), command half: `pigtail slots` itself prints the disk line
// from what GET /admission serves.
func TestSlotsCommandShowsTheDisk(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/cpu/slots", func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, pigtail.CPUSlotsResponse{Budget: 5})
	})
	mux.HandleFunc("/admission", func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, pigtail.AdmissionStatus{
			MaxJobs: 4, DiskKnown: true, DiskFreeBytes: 154 << 30, DiskFloorBytes: 20 << 30,
		})
	})
	mux.HandleFunc("/session-budget", func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, pigtail.SessionBudgetState{})
	})
	s := &stubDaemon{srv: httptest.NewServer(mux)}
	out, err := runSlotsOut(t, s)
	s.srv.Close()
	if err != nil {
		t.Fatal(err)
	}
	if want := "disk: 154 GB free (floor 20 GB)"; !strings.Contains(out, want) {
		t.Fatalf("pigtail slots lacks %q:\n%s", want, out)
	}
}
