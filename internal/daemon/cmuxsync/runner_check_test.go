package cmuxsync

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRunnerCmuxCheckPillTick(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.world.add(winA, wsC, "c")
	h.world.openSurface(wsA, surfA1)
	h.world.openSurface(wsC, surfC1)
	var (
		defects []CheckDefect
		readErr error
		reads   int
		logs    []string
	)
	h.r.deps.CmuxCheck = func() ([]CheckDefect, error) { reads++; return defects, readErr }
	h.r.deps.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }

	// A defect on a surface of wsA: the pill lands on wsA only.
	defects = []CheckDefect{{SurfaceID: strings.ToLower(surfA1)}}
	h.step(0)
	if !hasCall(h.world.writes(), "set-status "+wsA+" pigtail-chk Check · 1 to fix") {
		t.Fatalf("no pill on the workspace in defect: %q", h.world.writes())
	}
	if e := h.world.status[wsA][KeyCmuxCheck]; e.Priority != 85 || e.Color != "#FF9500" || e.Icon != "stethoscope" {
		t.Fatalf("pill entry %+v", e)
	}
	if _, ok := h.world.status[wsC][KeyCmuxCheck]; ok {
		t.Fatal("pill on a workspace with no defect")
	}

	// last.json is read at most once per SlowInterval (30 s), never per tick.
	defects = nil
	h.step(time.Second)
	if reads != 1 {
		t.Fatalf("reads = %d after 1 s, want 1", reads)
	}
	h.world.calls = nil
	h.step(30 * time.Second)
	if reads != 2 || !hasCall(h.world.writes(), "clear-status "+wsA+" pigtail-chk") {
		t.Fatalf("clean report: reads %d, writes %q; want the pill cleared", reads, h.world.writes())
	}

	// A blocked defect: red. Then an unreadable report: no pill, logged once.
	defects = []CheckDefect{{SurfaceID: surfA1, Blocked: true}}
	h.step(30 * time.Second)
	if e := h.world.status[wsA][KeyCmuxCheck]; e.Value != "Check · 1 left" || e.Color != "#FF3B30" {
		t.Fatalf("blocked defect: pill %+v", e)
	}
	defects, readErr = nil, errors.New("unexpected end of JSON input")
	h.step(30 * time.Second)
	h.step(30 * time.Second)
	if _, ok := h.world.status[wsA][KeyCmuxCheck]; ok {
		t.Fatal("unreadable report: the old pill stayed")
	}
	n := 0
	for _, l := range logs {
		if strings.Contains(l, "cmux check report unreadable") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("unreadable report logged %d times, want once: %q", n, logs)
	}

	// Switched off: the pill goes with the others, and last.json is no longer read.
	readErr, defects = nil, []CheckDefect{{SurfaceID: surfA1}}
	h.step(30 * time.Second)
	h.r.SetEnabled(false)
	h.step(time.Second)
	if _, ok := h.world.status[wsA][KeyCmuxCheck]; ok {
		t.Fatal("switch-off left the check pill")
	}
	before := reads
	h.step(time.Minute)
	if reads != before {
		t.Fatalf("switched off, last.json read %d more times", reads-before)
	}
}
