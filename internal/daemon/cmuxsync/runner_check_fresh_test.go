package cmuxsync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// freshHarness: a native harness with workspace wsA / surface surfA1 open, a
// check report that always answers defects, and a PidAlive over a test set.
type freshHarness struct {
	*nativeHarness
	defects []CheckDefect
	pids    map[int]bool
	logs    []string
}

func newFreshHarness(t *testing.T) *freshHarness {
	t.Helper()
	h := &freshHarness{nativeHarness: newNativeHarness(t, on()), pids: map[int]bool{}}
	h.world.add(winA, wsA, "a")
	h.world.openSurface(wsA, surfA1)
	h.r.deps.CmuxCheck = func() ([]CheckDefect, error) { return h.defects, nil }
	h.r.deps.PidAlive = func(pid int) bool { return h.pids[pid] }
	h.r.deps.Logf = func(f string, a ...any) { h.logs = append(h.logs, fmt.Sprintf(f, a...)) }
	return h
}

// liveOn upserts a peer of the surface; started zero = no native file.
func (h *freshHarness) liveOn(id string, pid int, started time.Time, kind string, alive bool) {
	h.t.Helper()
	h.liveOnSurface(surfA1, id, pid, started, kind, alive)
}

func (h *freshHarness) liveOnSurface(surface, id string, pid int, started time.Time, kind string, alive bool) {
	h.t.Helper()
	h.alive[id] = alive
	if err := h.st.UpsertPeer(&pigtail.PeerSession{SessionID: id, CmuxWorkspaceID: wsA, CmuxSurfaceID: surface,
		PID: pid, ConfigDir: h.cfg, Status: "idle", UpdatedAt: h.now}); err != nil {
		h.t.Fatal(err)
	}
	if started.IsZero() || pid <= 0 {
		return
	}
	b, _ := json.Marshal(claudesession.Native{PID: pid, SessionID: id, Status: "idle", Kind: kind, StartedAt: started.UnixMilli()})
	if err := os.WriteFile(filepath.Join(h.cfg, "sessions", strconv.Itoa(pid)+".json"), b, 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *freshHarness) pillSet() bool {
	_, ok := h.world.status[wsA][KeyCmuxCheck]
	return ok
}

func (h *freshHarness) dropLines() []string {
	var out []string
	for _, l := range h.logs {
		if strings.Contains(l, "cmux check pill: dropped") {
			out = append(out, l)
		}
	}
	return out
}

func TestRunnerCheckPillDropsWhenSurfaceServesAnotherProcess(t *testing.T) {
	t.Parallel()
	for _, blocked := range []bool{false, true} {
		h := newFreshHarness(t)
		h.defects = []CheckDefect{{SurfaceID: surfA1, Blocked: blocked, PID: 4000, At: h.now}}
		h.pids[4000] = true
		h.step(0)
		if !h.pillSet() {
			t.Fatalf("blocked=%v: no pill while the reported process runs: %q", blocked, h.world.writes())
		}
		h.pids[4000], h.pids[5001] = false, true
		h.liveOn("new", 5001, h.now, "interactive", true)
		h.world.calls = nil
		h.step(0)
		if !hasCall(h.world.writes(), "clear-status "+wsA+" pigtail-chk") || h.pillSet() {
			t.Fatalf("blocked=%v: pill not dropped: %q", blocked, h.world.writes())
		}
	}
}

func TestRunnerCheckDropLoggedOnce(t *testing.T) {
	t.Parallel()
	h := newFreshHarness(t)
	h.defects = []CheckDefect{{SurfaceID: surfA1, PID: 4000, At: h.now}}
	h.liveOn("new", 5001, h.now, "interactive", true)
	h.pids[5001] = true
	h.step(0)
	want := "cmuxsync: cmux check pill: dropped surface " + surfA1 + ", reported pid 4000 ended, surface now served by pid 5001"
	if got := h.dropLines(); len(got) != 1 || got[0] != want {
		t.Fatalf("drop lines = %q, want [%q]", got, want)
	}
	h.step(0)
	h.step(0)
	if got := h.dropLines(); len(got) != 1 {
		t.Fatalf("same state logged again: %q", got)
	}
	// A second, different dropped surface logs its own line.
	h.world.openSurface(wsA, surfA2)
	h.liveOnSurface(surfA2, "other", 5002, h.now, "interactive", true)
	h.pids[5002] = true
	h.defects = append(h.defects, CheckDefect{SurfaceID: surfA2, PID: 4002, At: h.now})
	h.step(31 * time.Second)
	if got := h.dropLines(); len(got) != 2 || !strings.Contains(got[1], surfA2) {
		t.Fatalf("second surface: drop lines = %q", got)
	}
}

func TestRunnerCheckPillKeptWhileReportedProcessRuns(t *testing.T) {
	t.Parallel()
	h := newFreshHarness(t)
	h.defects = []CheckDefect{{SurfaceID: surfA1, PID: 4000, At: h.now}}
	h.pids[4000], h.pids[5001] = true, true
	h.liveOn("new", 5001, h.now, "interactive", true)
	h.step(0)
	if !h.pillSet() {
		t.Fatalf("pill dropped while the reported process runs: %q", h.world.writes())
	}
}

func TestRunnerCheckPillKeptOnUnprovenSession(t *testing.T) {
	t.Parallel()
	cases := map[string]func(h *freshHarness){
		"ghost peer":     func(h *freshHarness) { h.liveOn("new", 5001, h.now, "interactive", false) },
		"no native file": func(h *freshHarness) { h.liveOn("new", 5001, time.Time{}, "interactive", true) },
		"pid 0":          func(h *freshHarness) { h.liveOn("new", 0, h.now, "interactive", true) },
		"sdk-cli kind":   func(h *freshHarness) { h.liveOn("new", 5001, h.now, "sdk-cli", true) },
	}
	for name, setup := range cases {
		h := newFreshHarness(t)
		h.defects = []CheckDefect{{SurfaceID: surfA1, PID: 4000, At: h.now}}
		h.pids[5001] = true
		setup(h)
		h.step(0)
		if !h.pillSet() {
			t.Errorf("%s: pill dropped on an unproven session: %q", name, h.world.writes())
		}
	}
}

func TestRunnerCheckPillKeptWithoutReportedPID(t *testing.T) {
	t.Parallel()
	for name, started := range map[string]func(h *freshHarness) time.Time{
		"started at the report": func(h *freshHarness) time.Time { return h.now },
		"started before":        func(h *freshHarness) time.Time { return h.now.Add(-time.Minute) },
		"no native file":        func(h *freshHarness) time.Time { return time.Time{} },
	} {
		h := newFreshHarness(t)
		h.defects = []CheckDefect{{SurfaceID: surfA1, At: h.now}}
		h.pids[5001] = true
		h.liveOn("new", 5001, started(h), "interactive", true)
		h.step(0)
		if !h.pillSet() {
			t.Errorf("%s: pill dropped for a defect with no reported pid: %q", name, h.world.writes())
		}
	}
}

func TestRunnerCheckPillDropsNoPidRowWhenSessionStartedAfterReport(t *testing.T) {
	t.Parallel()
	h := newFreshHarness(t)
	at := h.now
	h.defects = []CheckDefect{{SurfaceID: surfA1, At: at}}
	h.pids[5001] = true
	h.liveOn("new", 5001, at.Add(time.Minute), "interactive", true)
	h.step(0)
	if h.pillSet() {
		t.Fatalf("pill kept though a session started after the report serves the surface: %q", h.world.writes())
	}
	want := "cmuxsync: cmux check pill: dropped surface " + surfA1 + ", no process at report time, surface now served by pid 5001 started after the report"
	if got := h.dropLines(); len(got) != 1 || got[0] != want {
		t.Fatalf("drop lines = %q, want [%q]", got, want)
	}
}

func TestRunnerCheckPillKeptNoPidRowWhenSessionStartedBefore(t *testing.T) {
	t.Parallel()
	cases := map[string]func(h *freshHarness, at time.Time){
		"started at the report": func(h *freshHarness, at time.Time) { h.liveOn("new", 5001, at, "interactive", true) },
		"started before":        func(h *freshHarness, at time.Time) { h.liveOn("new", 5001, at.Add(-time.Minute), "interactive", true) },
		"no native file":        func(h *freshHarness, at time.Time) { h.liveOn("new", 5001, time.Time{}, "interactive", true) },
		"ghost peer":            func(h *freshHarness, at time.Time) { h.liveOn("new", 5001, at.Add(time.Minute), "interactive", false) },
		"pid 0":                 func(h *freshHarness, at time.Time) { h.liveOn("new", 0, at.Add(time.Minute), "interactive", true) },
		"sdk-cli kind":          func(h *freshHarness, at time.Time) { h.liveOn("new", 5001, at.Add(time.Minute), "sdk-cli", true) },
	}
	for name, setup := range cases {
		h := newFreshHarness(t)
		at := h.now
		h.defects = []CheckDefect{{SurfaceID: surfA1, At: at}}
		h.pids[5001] = true
		setup(h, at)
		h.step(0)
		if !h.pillSet() {
			t.Errorf("%s: pill dropped without proof: %q", name, h.world.writes())
		}
	}
}
