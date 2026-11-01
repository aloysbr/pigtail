package cmuxsync

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/maintenance"
)

func TestMaintenancePillOnEveryLiveWorkspaceAndClearedOnLift(t *testing.T) {
	t.Parallel()
	ws := []Workspace{{ID: "AAAAAAAA-0000-0000-0000-000000000001"}, {ID: "AAAAAAAA-0000-0000-0000-000000000002"}}
	res := Compute(Input{Now: time.Now(), Workspaces: ws, Maintenance: &MaintenancePill{Value: "Maintenance · freeze · late", Color: "#FF3B30"}})
	applied := map[string]Applied{}
	ops := Diff(res.Desired, applied)
	n := 0
	for _, op := range ops {
		if op.Kind == OpSetStatus && op.Status.Key == KeyMaintenance {
			if op.Status.Value != "Maintenance · freeze · late" || op.Status.Color != "#FF3B30" || op.Status.Icon != "wrench.and.screwdriver.fill" ||
				op.Status.Priority != 80 || op.Status.Priority >= attentionPriority {
				t.Fatalf("pill %+v", op.Status)
			}
			n++
			Record(applied, op)
		}
	}
	if n != 2 {
		t.Fatalf("pills set: %d", n)
	}
	lifted := Compute(Input{Now: time.Now(), Workspaces: ws})
	clears := 0
	for _, op := range Diff(lifted.Desired, applied) {
		if op.Kind == OpClearStatus && op.Status.Key == KeyMaintenance {
			clears++
		}
	}
	if clears != 2 {
		t.Fatalf("clears: %d", clears)
	}
	if !Owns(KeyMaintenance) {
		t.Fatal("key not owned: Apply would refuse it")
	}
}

func TestMaintenancePillStateError(t *testing.T) {
	t.Parallel()
	var logged []string
	r := &Runner{deps: Deps{
		Maintenance: func() (maintenance.State, bool, error) {
			return maintenance.State{}, false, errors.New("unexpected EOF")
		},
		MaintenanceDir: "/state/dir",
		Logf:           func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) },
	}}
	for i := 0; i < 3; i++ {
		mp := r.maintenancePill(time.Now())
		if mp == nil || mp.Value != "Maintenance · state error" || mp.Color != "#FF3B30" {
			t.Fatalf("unreadable state must show the error pill, got %+v", mp)
		}
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "unreadable") || strings.Count(logged[0], "/state/dir/maintenance.json") != 1 {
		t.Fatalf("log once per distinct error, got %q", logged)
	}
	// D9: red, same rank (80) as the other maintenance pills.
	d := Desired{Maintenance: r.maintenancePill(time.Now())}
	e := d.Statuses()[KeyMaintenance]
	if e.Value != "Maintenance · state error" || e.Color != "#FF3B30" || e.Priority != 80 || e.Icon != "wrench.and.screwdriver.fill" {
		t.Fatalf("error pill entry %+v", e)
	}
}

func ownerPill(value, color string) *MaintenancePill {
	return &MaintenancePill{Value: value, Color: color, Owner: "sid-owner", OwnerValue: "Maintenance · freeze · set here"}
}

func mntEntry(t *testing.T, res Result, ws string) cmux.StatusEntry {
	t.Helper()
	e, ok := res.Desired[ws].Statuses()[KeyMaintenance]
	if !ok {
		t.Fatalf("%s: no maintenance pill", ws)
	}
	return e
}

// D1: only the owner's workspace wears the key, whatever other sessions it carries.
func TestMaintenanceOwnerTileMarked(t *testing.T) {
	t.Parallel()
	ws := []Workspace{{ID: wsA}, {ID: wsB}, {ID: wsC}}
	ss := []Session{
		sess("sid-owner", wsA, surfA1, "busy", true),
		sess("sid-other", wsA, "S2", "idle", true), // same workspace: the whole tile is marked
		sess("sid-b", wsB, surfB1, "busy", true),
	}
	res := Compute(Input{Now: t0, Workspaces: ws, Sessions: ss, Maintenance: ownerPill("Maintenance · freeze", "#FF9500")})

	e := mntEntry(t, res, wsA)
	if e.Value != "Maintenance · freeze · set here" || e.Icon != "key.fill" || e.Color != "#FF9500" || e.Priority != 80 || e.Key != KeyMaintenance {
		t.Fatalf("owner tile %+v", e)
	}
	for _, id := range []string{wsB, wsC} {
		if e := mntEntry(t, res, id); e.Value != "Maintenance · freeze" || e.Icon != "wrench.and.screwdriver.fill" || e.Color != "#FF9500" || e.Priority != 80 {
			t.Fatalf("%s: common pill changed %+v", id, e)
		}
	}
}

// Autonomous decision: a non-interactive owner (claude -p) marks its tile too,
// although sessionsByWorkspace drops non-interactive sessions.
func TestMaintenanceOwnerTileMarkedNonInteractive(t *testing.T) {
	t.Parallel()
	ws := []Workspace{{ID: wsA}, {ID: wsB}}
	owner := sess("sid-owner", wsA, surfA1, "busy", true)
	owner.Kind = "print"
	ss := []Session{owner, sess("sid-b", wsB, surfB1, "busy", true)}
	res := Compute(Input{Now: t0, Workspaces: ws, Sessions: ss, Maintenance: ownerPill("Maintenance · freeze", "#FF9500")})

	if e := mntEntry(t, res, wsA); e.Value != "Maintenance · freeze · set here" || e.Icon != "key.fill" {
		t.Fatalf("non-interactive owner tile not marked %+v", e)
	}
	if e := mntEntry(t, res, wsB); e.Icon != "wrench.and.screwdriver.fill" {
		t.Fatalf("other tile changed %+v", e)
	}
}

// Late: red on the owner's tile too, the key kept.
func TestMaintenanceOwnerTileLate(t *testing.T) {
	t.Parallel()
	ws := []Workspace{{ID: wsA}, {ID: wsB}}
	ss := []Session{sess("sid-owner", wsA, surfA1, "busy", true)}
	p := ownerPill("Maintenance · freeze · late", "#FF3B30")
	res := Compute(Input{Now: t0, Workspaces: ws, Sessions: ss, Maintenance: p})
	if e := mntEntry(t, res, wsA); e.Icon != "key.fill" || e.Color != "#FF3B30" || e.Value != p.OwnerValue {
		t.Fatalf("late owner tile %+v", e)
	}
	if e := mntEntry(t, res, wsB); e.Icon != "wrench.and.screwdriver.fill" || e.Color != "#FF3B30" {
		t.Fatalf("late common tile %+v", e)
	}
}

// D2: an owner empty, dead, ended, unlisted or in a closed workspace marks no tile.
func TestMaintenanceOwnerNotMarked(t *testing.T) {
	t.Parallel()
	ws := []Workspace{{ID: wsA}, {ID: wsB}}
	dead := sess("sid-owner", wsA, surfA1, "idle", false)
	ended := sess("sid-owner", wsA, surfA1, "idle", true)
	ended.Ended = true
	closed := sess("sid-owner", "DEADBEEF-0000-4000-8000-000000000009", "S9", "busy", true)
	noOwner := ownerPill("Maintenance", "#FF9500")
	noOwner.Owner = ""
	cases := map[string]struct {
		p  *MaintenancePill
		ss []Session
	}{
		"empty owner":      {noOwner, []Session{sess("sid-owner", wsA, surfA1, "busy", true)}},
		"owner unlisted":   {ownerPill("Maintenance", "#FF9500"), []Session{sess("sid-b", wsB, surfB1, "busy", true)}},
		"owner dead":       {ownerPill("Maintenance", "#FF9500"), []Session{dead}},
		"owner ended":      {ownerPill("Maintenance", "#FF9500"), []Session{ended}},
		"workspace closed": {ownerPill("Maintenance", "#FF9500"), []Session{closed}},
		"error pill":       {&MaintenancePill{Value: "Maintenance · state error", Color: "#FF3B30"}, []Session{sess("sid-owner", wsA, surfA1, "busy", true)}},
	}
	for name, c := range cases {
		res := Compute(Input{Now: t0, Workspaces: ws, Sessions: c.ss, Maintenance: c.p})
		for _, id := range []string{wsA, wsB} {
			if e := mntEntry(t, res, id); e.Icon != "wrench.and.screwdriver.fill" || e.Value != c.p.Value {
				t.Errorf("%s: %s marked %+v", name, id, e)
			}
		}
		if len(res.Desired) != 2 {
			t.Errorf("%s: desired for a non-live workspace: %d", name, len(res.Desired))
		}
	}
}

// The owner appears, then dies, the maintenance staying on: only its tile is
// rewritten; on lift its pill clears like the others.
func TestMaintenanceOwnerTransitionRewritesOnlyItsTile(t *testing.T) {
	t.Parallel()
	ws := []Workspace{{ID: wsA}, {ID: wsB}}
	p := ownerPill("Maintenance · freeze", "#FF9500")
	applied := map[string]Applied{}
	apply := func(res Result) []Op {
		var got []Op
		for _, op := range Diff(res.Desired, applied) {
			if op.Status.Key == KeyMaintenance {
				got = append(got, op)
				Record(applied, op)
			}
		}
		return got
	}
	if ops := apply(Compute(Input{Now: t0, Workspaces: ws, Maintenance: p})); len(ops) != 2 {
		t.Fatalf("common pills: %+v", ops)
	}
	ops := apply(Compute(Input{Now: t0, Workspaces: ws, Sessions: []Session{sess("sid-owner", wsA, surfA1, "busy", true)}, Maintenance: p}))
	if len(ops) != 1 || ops[0].WorkspaceID != wsA || ops[0].Kind != OpSetStatus || ops[0].Status.Icon != "key.fill" {
		t.Fatalf("owner appears: %+v", ops)
	}
	ops = apply(Compute(Input{Now: t0, Workspaces: ws, Sessions: []Session{sess("sid-owner", wsA, surfA1, "idle", false)}, Maintenance: p}))
	if len(ops) != 1 || ops[0].WorkspaceID != wsA || ops[0].Status.Icon != "wrench.and.screwdriver.fill" || ops[0].Status.Value != "Maintenance · freeze" {
		t.Fatalf("owner dies: %+v", ops)
	}
	apply(Compute(Input{Now: t0, Workspaces: ws, Sessions: []Session{sess("sid-owner", wsA, surfA1, "busy", true)}, Maintenance: p}))
	ops = apply(Compute(Input{Now: t0, Workspaces: ws, Sessions: []Session{sess("sid-owner", wsA, surfA1, "busy", true)}}))
	if len(ops) != 2 || ops[0].Kind != OpClearStatus || ops[1].Kind != OpClearStatus {
		t.Fatalf("lift: %+v", ops)
	}
}
