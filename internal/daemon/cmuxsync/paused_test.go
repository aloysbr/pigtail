package cmuxsync

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// sub_x000000000000036 (T-g, task 9): a paused session's cmux workspace
// carries the « Paused » pill, its own key pigtail-pause.
func TestPausedPill(t *testing.T) {
	t.Parallel()
	const (
		paused   = "AAAAAAAA-0000-0000-0000-0000000000E1"
		stopping = "AAAAAAAA-0000-0000-0000-0000000000E2"
	)
	// The row carries the cmux workspace, distinct from the pigtail worktree
	// (gap 1 of the plan): the pill follows CmuxWorkspaceID, upper-cased.
	m := pausedWorkspaces([]pigtail.SessionPause{
		{SessionID: "s1", CmuxWorkspaceID: strings.ToLower(paused), State: pigtail.PausePaused},
		{SessionID: "s2", CmuxWorkspaceID: stopping, State: pigtail.PauseStopping},
		{SessionID: "s3", State: pigtail.PausePaused}, // no workspace: nothing
	})
	if len(m) != 1 || !m[paused] {
		t.Fatalf("pausedWorkspaces = %v, want only %s", m, paused)
	}

	ws := []Workspace{{ID: paused}, {ID: stopping}}
	res := Compute(Input{Now: time.Now(), Workspaces: ws, Paused: m})
	if !res.Desired[paused].Paused || res.Desired[stopping].Paused {
		t.Fatalf("Desired.Paused: paused=%v stopping=%v", res.Desired[paused].Paused, res.Desired[stopping].Paused)
	}
	e, ok := res.Desired[paused].Statuses()[KeyPaused]
	if !ok || e.Key != "pigtail-pause" || e.Value != "Paused" || e.Icon != "pause.circle" || e.Color != "#5E5CE6" || e.Priority != 75 {
		t.Fatalf("entry = %+v, want pigtail-pause Paused / pause.circle / #5E5CE6 / 75", e)
	}
	if _, ok := res.Desired[stopping].Statuses()[KeyPaused]; ok {
		t.Fatal("pill on a stopping row")
	}

	applied := map[string]Applied{}
	for _, op := range Diff(res.Desired, applied) {
		Record(applied, op)
	}
	gone := Compute(Input{Now: time.Now(), Workspaces: ws})
	clears := 0
	for _, op := range Diff(gone.Desired, applied) {
		if op.Kind == OpClearStatus && op.Status.Key == KeyPaused {
			clears++
		}
	}
	if clears != 1 {
		t.Fatalf("clears of the pause pill after the row went = %d, want 1", clears)
	}
}

func TestOwnsPausedKey(t *testing.T) {
	t.Parallel()
	if !Owns("pigtail-pause") {
		t.Fatal("pigtail-pause not owned: Apply would refuse the pill")
	}
	if !Owns(KeyPaused) {
		t.Fatalf("OwnedKeys = %v, want pigtail-pause owned", OwnedKeys)
	}
}

// A read error keeps the last successful map (the pill does not blink),
// logged once per distinct error.
func TestPausedReadErrorKeepsPill(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	var (
		rows    []pigtail.SessionPause
		readErr error
		logs    []string
	)
	h.r.deps.Paused = func() ([]pigtail.SessionPause, error) { return rows, readErr }
	h.r.deps.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }

	rows = []pigtail.SessionPause{{SessionID: "s1", CmuxWorkspaceID: wsA, State: pigtail.PausePaused}}
	h.step(0)
	if v := h.world.status[wsA][KeyPaused].Value; v != "Paused" {
		t.Fatalf("pill = %q, want Paused", v)
	}

	rows, readErr = nil, errors.New("database is locked")
	h.step(time.Second)
	h.step(time.Second)
	if v := h.world.status[wsA][KeyPaused].Value; v != "Paused" {
		t.Fatalf("read error dropped the pill (%q): want the last good map kept", v)
	}
	readErr = errors.New("disk I/O error")
	h.step(time.Second)
	n := 0
	for _, l := range logs {
		if strings.Contains(l, "list pauses") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("pause read errors logged %d times, want 2 (one per distinct error): %q", n, logs)
	}

	readErr = nil
	h.step(time.Second)
	if e, ok := h.world.status[wsA][KeyPaused]; ok {
		t.Fatalf("pill kept after the row went: %+v", e)
	}
}

// The pill is drawn only in the driven windows.
func TestPausedPillOnlyInAllowedWindows(t *testing.T) {
	t.Parallel()
	w := newHarness(t, Config{Enabled: true, Windows: []string{strings.ToLower(winB)}})
	w.world.add(winA, wsA, "a")
	w.world.add(winB, wsB, "b")
	w.r.deps.Paused = func() ([]pigtail.SessionPause, error) {
		return []pigtail.SessionPause{
			{SessionID: "s1", CmuxWorkspaceID: wsA, State: pigtail.PausePaused},
			{SessionID: "s2", CmuxWorkspaceID: wsB, State: pigtail.PausePaused},
		}, nil
	}
	w.step(0)
	if v := w.world.status[wsB][KeyPaused].Value; v != "Paused" {
		t.Fatalf("pill in the driven window = %q", v)
	}
	if e, ok := w.world.status[wsA][KeyPaused]; ok {
		t.Fatalf("pill outside the driven window: %+v", e)
	}
}

// Final review I1: a paused tab closed by hand leaves no ghost row. Two
// successful listings without its workspace end the pause (Deps.PauseGone);
// a stopping row, or one listing missed, ends nothing.
func TestPausePurgedWhenWorkspaceGone(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsB, "b") // keeps a window listed
	h.world.add(winA, wsA, "a")
	rows := []pigtail.SessionPause{
		{SessionID: "s1", CmuxWorkspaceID: wsA, State: pigtail.PausePaused},
		{SessionID: "s2", CmuxWorkspaceID: wsA, State: pigtail.PauseStopping},
		{SessionID: "s3", CmuxWorkspaceID: wsB, State: pigtail.PausePaused},
	}
	h.r.deps.Paused = func() ([]pigtail.SessionPause, error) { return rows, nil }
	var calls []string
	h.r.deps.PauseGone = func(session, ws string) error {
		calls = append(calls, session+"/"+ws)
		return nil
	}
	h.step(0)
	h.world.close(wsA)
	h.step(30 * time.Second)
	if len(calls) != 0 {
		t.Fatalf("after one listing: %v", calls)
	}
	h.step(30 * time.Second)
	if len(calls) != 1 || calls[0] != "s1/"+wsA {
		t.Fatalf("after two listings: %v, want only s1/%s", calls, wsA)
	}
}
