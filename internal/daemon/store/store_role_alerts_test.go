package store

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/roles"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

var raT0 = time.Date(2026, 10, 30, 11, 0, 0, 0, time.UTC)

func raAdd(t *testing.T, st *Store, sc, id, kind string, at time.Time) {
	t.Helper()
	e := &pigtail.JournalEntry{ID: id, Scope: sc, Timestamp: at, Kind: kind, Summary: "s " + id, Source: "pigtail:roles/x"}
	if _, err := st.AddJournal(e); err != nil {
		t.Fatal(err)
	}
}

func raRaise(t *testing.T, st *Store, sc, name string, key time.Time) {
	raAdd(t, st, sc, roles.AlertID(name, key), RoleAlertKind, key.Add(10*time.Minute))
}

func TestUnrecoveredRoleAlerts(t *testing.T) {
	st := openMem(t)
	k1, k2, k3 := raT0, raT0.Add(time.Hour), raT0.Add(2*time.Hour)
	for _, k := range []time.Time{k1, k2, k3} {
		raRaise(t, st, "perso", "pilote", k)
	}
	raAdd(t, st, "perso", roles.RecoveredID("pilote", k1), RoleRecoveredKind, k1.Add(time.Hour))
	raAdd(t, st, "perso", "alert-resolved:"+roles.AlertID("pilote", k3), "alert-resolved", k3.Add(time.Hour))
	got, err := st.UnrecoveredRoleAlerts(scope.Any)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d, %v", len(got), err)
	}
	if !got[0].Key.Equal(k2) || got[0].Acked || !got[1].Key.Equal(k3) || !got[1].Acked || got[0].Name != "pilote" {
		t.Fatalf("rows = %+v", got)
	}
}

func TestOpenRoleAlertsExcludesAcked(t *testing.T) {
	st := openMem(t)
	raRaise(t, st, "perso", "pilote", raT0)
	raRaise(t, st, "perso", "other", raT0)
	raAdd(t, st, "perso", "alert-resolved:"+roles.AlertID("other", raT0), "alert-resolved", raT0.Add(time.Hour))
	got, err := st.OpenRoleAlerts(scope.Any)
	if err != nil || len(got) != 1 || got[0].Name != "pilote" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestRecoveredRoleKeys(t *testing.T) {
	st := openMem(t)
	k1, k2 := raT0, raT0.Add(time.Hour)
	raAdd(t, st, "perso", roles.RecoveredID("pilote", k1), RoleRecoveredKind, k1)
	raAdd(t, st, "perso", roles.RecoveredID("pilote", k2), RoleRecoveredKind, k2)
	raAdd(t, st, "perso", roles.RecoveredID("pilote-2", k2), RoleRecoveredKind, k2)
	raAdd(t, st, "perso", roles.RecoveredID("other", k1), RoleRecoveredKind, k1)
	got, err := st.RecoveredRoleKeys(scope.Scope("perso"), "pilote")
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestRoleAlertsScoped(t *testing.T) {
	st := openMem(t)
	raRaise(t, st, "perso", "pilote", raT0)
	raRaise(t, st, "work-x", "other", raT0)
	got, err := st.UnrecoveredRoleAlerts(scope.Scope("work-x"))
	if err != nil || len(got) != 1 || got[0].Name != "other" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if all, _ := st.UnrecoveredRoleAlerts(scope.Any); len(all) != 2 {
		t.Fatalf("any = %d", len(all))
	}
}
