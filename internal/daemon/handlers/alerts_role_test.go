package handlers

import (
	"net/http"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/roles"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// seedRoleAlert writes the row the roles sweep writes for a raised episode.
func seedRoleAlert(t *testing.T, st *store.Store, sc, name, subject string, key, at time.Time, state string) string {
	t.Helper()
	e := &pigtail.JournalEntry{ID: roles.AlertID(name, key), Timestamp: at, Kind: "role-alert", Scope: sc, SubjectID: subject,
		Summary: "Role " + name + " " + state + ": wakeup due never fired", Source: "pigtail:roles/" + name + "?state=" + state}
	if _, err := st.AddJournal(e); err != nil {
		t.Fatal(err)
	}
	return e.ID
}

func TestListAlertsIncludesRole(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	t0 := time.Now().Add(-40 * time.Minute).UTC()
	seedRespawnLine(t, st, "perso", "failed", "r1", "sub_p", "d", t0.Add(time.Minute), false)
	id := seedRoleAlert(t, st, "perso", "pilote", "sub_p", t0, t0, "silent")
	got := getAlerts(t, r, perso)
	if len(got) != 2 || got[0].ID != id || got[1].ID != "r1" {
		t.Fatalf("alerts = %+v, want the role alert then r1 (oldest first)", got)
	}
	a := got[0]
	if a.Kind != pigtail.AlertKindRole || a.State != "silent" || a.Scope != "perso" || a.SubjectID != "sub_p" ||
		a.SubjectTitle == "" || a.AgeSeconds < 2390 {
		t.Fatalf("role alert = %+v", a)
	}
	// A recovery closes it.
	if _, err := st.AddJournal(&pigtail.JournalEntry{ID: roles.RecoveredID("pilote", t0), Timestamp: time.Now().UTC(), Kind: "role-recovered",
		Scope: "perso", SubjectID: "sub_p", Summary: "recovered", Source: "pigtail:roles/pilote?state=ok"}); err != nil {
		t.Fatal(err)
	}
	if got := getAlerts(t, r, perso); len(got) != 1 || got[0].ID != "r1" {
		t.Fatalf("after recovery: %+v", got)
	}
}

func TestListAlertsRoleHiddenOtherScope(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	now := time.Now().UTC()
	seedRoleAlert(t, st, "perso", "pilote", "sub_p", now.Add(-time.Hour), now.Add(-time.Hour), "silent")
	if got := getAlerts(t, r, work); len(got) != 0 {
		t.Fatalf("work sees %+v", got)
	}
	if got := getAlerts(t, r, perso); len(got) != 1 {
		t.Fatalf("master sees %+v", got)
	}
}

func TestAckRoleAlertByHand(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	now := time.Now().UTC()
	id := seedRoleAlert(t, st, "perso", "pilote", "sub_p", now.Add(-time.Hour), now.Add(-time.Hour), "silent")
	code, res, body := ackAlert(t, r, perso, id, pigtail.AlertAckRequest{})
	if code != 200 || !res.Resolved || res.ID != id {
		t.Fatalf("ack = %d %s", code, body)
	}
	if got := getAlerts(t, r, perso); len(got) != 0 {
		t.Fatalf("still open: %+v", got)
	}
	rows, err := st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: "alert-resolved"})
	if err != nil || len(rows) != 1 || rows[0].ID != "alert-resolved:"+id || rows[0].SubjectID != "sub_p" {
		t.Fatalf("resolution = %+v, %v", rows, err)
	}
	// Acked, not recovered: still unrecovered for the sweep.
	un, err := st.UnrecoveredRoleAlerts(scope.Any)
	if err != nil || len(un) != 1 || !un[0].Acked {
		t.Fatalf("unrecovered = %+v, %v", un, err)
	}
}

func TestAckRoleAlertTwiceIdempotent(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	now := time.Now().UTC()
	id := seedRoleAlert(t, st, "perso", "pilote", "sub_p", now.Add(-time.Hour), now.Add(-time.Hour), "absent")
	ackAlert(t, r, perso, id, pigtail.AlertAckRequest{})
	code, res, body := ackAlert(t, r, perso, id, pigtail.AlertAckRequest{})
	if code != 200 || res.Resolved {
		t.Fatalf("second ack = %d %s", code, body)
	}
}

func TestAckRoleAlertSessionMustBeOnSubject(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	now := time.Now().UTC()
	id := seedRoleAlert(t, st, "perso", "pilote", "sub_p", now.Add(-time.Hour), now.Add(-time.Hour), "silent")
	seedPeer(t, st, "0f3c9a1e-1111-4222-8333-444455556666", "perso", "sub_other")
	if code, _, body := ackAlert(t, r, perso, id, pigtail.AlertAckRequest{SessionID: "0f3c9a1e-1111-4222-8333-444455556666"}); code != http.StatusConflict {
		t.Fatalf("foreign session ack = %d %s", code, body)
	}
	seedPeer(t, st, "1f3c9a1e-1111-4222-8333-444455556666", "perso", "sub_p")
	if code, res, body := ackAlert(t, r, perso, id, pigtail.AlertAckRequest{SessionID: "1f3c9a1e-1111-4222-8333-444455556666"}); code != 200 || !res.Resolved {
		t.Fatalf("subject session ack = %d %s", code, body)
	}
}

func TestAckRoleAlertOtherScopeIsUnknown(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	now := time.Now().UTC()
	id := seedRoleAlert(t, st, "perso", "pilote", "sub_p", now.Add(-time.Hour), now.Add(-time.Hour), "silent")
	if code, _, body := ackAlert(t, r, work, id, pigtail.AlertAckRequest{}); code != http.StatusNotFound {
		t.Fatalf("work acks perso = %d %s", code, body)
	}
}

func TestResolveSubjectLeavesRoleAlerts(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	now := time.Now().UTC()
	seedRoleAlert(t, st, "perso", "pilote", "sub_p", now.Add(-time.Hour), now.Add(-time.Hour), "silent")
	seedRespawnLine(t, st, "perso", "failed", "r1", "sub_p", "d", now, false)
	rec := doAs(t, r, perso, "POST", "/alerts/resolve", pigtail.AlertResolveRequest{SubjectID: "sub_p", Cause: "relaunched"})
	if rec.Code != 200 {
		t.Fatalf("resolve = %d %s", rec.Code, rec.Body)
	}
	got := getAlerts(t, r, perso)
	if len(got) != 1 || got[0].Kind != pigtail.AlertKindRole {
		t.Fatalf("after resolve: %+v", got)
	}
}
