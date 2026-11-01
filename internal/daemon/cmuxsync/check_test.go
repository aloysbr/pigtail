package cmuxsync

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
)

// wsA, wsB (compute_test.go) and wsC (runner_test.go) are the package's.
const (
	surfA1 = "AAAA0000-0000-4000-8000-0000000000A1"
	surfA2 = "AAAA0000-0000-4000-8000-0000000000A2"
	surfB1 = "BBBB0000-0000-4000-8000-0000000000B1"
	surfC1 = "CCCC0000-0000-4000-8000-0000000000C1"
)

// D13: one pill per workspace holding a surface in defect — counted, red
// when one is blocked or failed, orange when `--fix` can lift it; a surface
// closed, or in a workspace whose surfaces are unknown, lights nothing.
func TestCheckPillOnDefectWorkspace(t *testing.T) {
	t.Parallel()
	ws := []Workspace{
		{ID: wsA, Surfaces: map[string]bool{surfA1: true, surfA2: true}},
		{ID: wsB, Surfaces: map[string]bool{surfB1: true}},
		{ID: wsC}, // surfaces unknown (cmux tree failed): never a pill
	}
	defects := []CheckDefect{
		{SurfaceID: surfA1},
		{SurfaceID: surfA2, Blocked: true},
		{SurfaceID: "bbbb0000-0000-4000-8000-0000000000b1"}, // lower case, as a report may carry it
		{SurfaceID: surfC1},                                 // in wsC, whose surfaces are unknown
		{SurfaceID: "DDDD0000-0000-4000-8000-0000000000D1"}, // closed since the check
		{SurfaceID: ""},
	}
	res := Compute(Input{Now: time.Now(), Workspaces: ws, CheckDefects: defects})
	a := res.Desired[wsA].Statuses()[KeyCmuxCheck]
	if a.Value != "Check · 2 left" || a.Color != "#FF3B30" || a.Icon != "stethoscope" || a.Priority != 85 {
		t.Fatalf("wsA pill %+v, want red \"Check · 2 left\"", a)
	}
	b := res.Desired[wsB].Statuses()[KeyCmuxCheck]
	if b.Value != "Check · 1 to fix" || b.Color != "#FF9500" || b.Priority != 85 {
		t.Fatalf("wsB pill %+v, want orange \"Check · 1 to fix\"", b)
	}
	if _, ok := res.Desired[wsC].Statuses()[KeyCmuxCheck]; ok {
		t.Fatal("wsC: a pill while its surfaces are unknown")
	}
	if _, ok := res.Desired[wsA].Statuses()[KeyAttention]; ok {
		t.Fatal("the check pill must not light the attention pill")
	}
	// T-l: under the attention pill, above the maintenance pill.
	if attentionPriority <= a.Priority || a.Priority <= maintenancePriority {
		t.Fatalf("priority %d, want between %d and %d", a.Priority, maintenancePriority, attentionPriority)
	}
}

// The pill goes with the next clean report: Diff clears the key, nothing else.
func TestCheckPillClearedWhenClean(t *testing.T) {
	t.Parallel()
	ws := []Workspace{{ID: wsA, Surfaces: map[string]bool{surfA1: true}}}
	applied := map[string]Applied{}
	for _, op := range Diff(Compute(Input{Now: time.Now(), Workspaces: ws, CheckDefects: []CheckDefect{{SurfaceID: surfA1}}}).Desired, applied) {
		Record(applied, op)
	}
	if _, ok := applied[wsA].Statuses[KeyCmuxCheck]; !ok {
		t.Fatal("no pill posed")
	}
	ops := Diff(Compute(Input{Now: time.Now(), Workspaces: ws}).Desired, applied)
	if len(ops) != 1 || ops[0].Kind != OpClearStatus || ops[0].Status.Key != KeyCmuxCheck {
		t.Fatalf("ops %+v, want one clear of %s", ops, KeyCmuxCheck)
	}
}

// pigtail writes only its own keys: pigtail-chk is one of them, so Apply
// takes it, a reconcile keeps it and a switch-off clears it.
func TestCheckPillOwnedKey(t *testing.T) {
	t.Parallel()
	if KeyCmuxCheck != "pigtail-chk" || !Owns(KeyCmuxCheck) {
		t.Fatalf("key %q owned %v", KeyCmuxCheck, Owns(KeyCmuxCheck))
	}
	e := checkStatus(CheckPill{Left: 1})
	if a := SeedApplied([]cmux.StatusEntry{e}, nil); a.Statuses[KeyCmuxCheck] != e {
		t.Fatalf("reconcile dropped the pill: %+v", a.Statuses)
	}
	ops := ClearOwnedOps(map[string]Applied{wsA: {Statuses: map[string]cmux.StatusEntry{KeyCmuxCheck: e}}})
	if len(ops) != 1 || ops[0].Status.Key != KeyCmuxCheck {
		t.Fatalf("switch-off ops %+v", ops)
	}
}
