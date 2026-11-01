package cmuxsync

import (
	"errors"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestRoleAgeBuckets(t *testing.T) {
	t.Parallel()
	for d, want := range map[time.Duration]string{
		2 * time.Minute: "0m", 7 * time.Minute: "5m", 59 * time.Minute: "55m",
		time.Hour: "1h", 150 * time.Minute: "2h", -time.Minute: "0m",
	} {
		if got := roleAge(d); got != want {
			t.Errorf("roleAge(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestRolePillCompute(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	in := Input{
		Now:        now,
		Workspaces: []Workspace{{ID: wsA}, {ID: wsB}},
		Bindings: []pigtail.SubjectCmuxBinding{
			{CmuxWorkspaceID: wsA, SubjectID: "s1"},
		},
		RoleAlerts: []RoleAlert{
			{SubjectID: "s1", Role: "pilote", State: "silent", Since: now.Add(-3 * time.Hour)},
			{SubjectID: "s1", Role: "late", State: "stuck", Since: now.Add(-10 * time.Minute)},
			{SubjectID: "s2", Role: "other", State: "absent", Since: now.Add(-time.Hour)},
		},
	}
	res := Compute(in)
	e, ok := res.Desired[wsA].Statuses()[KeyRole]
	if !ok || e.Key != "pigtail-role" || e.Value != "Role silent · pilote · 3h" || e.Priority != 87 ||
		e.Color != "#FF9F0A" || e.Icon != "exclamationmark.triangle.fill" {
		t.Fatalf("entry = %+v", e)
	}
	if _, ok := res.Desired[wsB].Statuses()[KeyRole]; ok {
		t.Fatal("pill on an unbound workspace")
	}
}

func TestOwnsRoleKey(t *testing.T) {
	t.Parallel()
	if !Owns("pigtail-role") {
		t.Fatal("pigtail-role not owned")
	}
}

func TestRunnerSetsAndClearsRolePill(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.bind(wsA)
	var (
		alerts  []RoleAlert
		readErr error
	)
	h.r.deps.RoleAlerts = func() ([]RoleAlert, error) { return alerts, readErr }
	alerts = []RoleAlert{{SubjectID: subID, Role: "pilote", State: "silent", Since: h.now.Add(-20 * time.Minute)}}
	h.step(0)
	if v := h.world.status[wsA][KeyRole].Value; v != "Role silent · pilote · 20m" {
		t.Fatalf("pill = %q", v)
	}
	readErr = errors.New("locked")
	alerts = nil
	h.step(time.Second)
	if _, ok := h.world.status[wsA][KeyRole]; !ok {
		t.Fatal("read error dropped the pill")
	}
	readErr = nil
	h.step(time.Second)
	if e, ok := h.world.status[wsA][KeyRole]; ok {
		t.Fatalf("pill kept after recovery: %+v", e)
	}
}
