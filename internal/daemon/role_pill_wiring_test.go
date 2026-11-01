package daemon

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/roles"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The pill reads every scope (F-B2): an alert of a non-master scope shows.
func TestRoleAlertsWiring(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := time.Date(2026, 10, 30, 11, 0, 0, 0, time.UTC)
	if _, err := st.AddJournal(&pigtail.JournalEntry{
		ID: roles.AlertID("pilote", key), Scope: "globex", Kind: store.RoleAlertKind, SubjectID: "sub_x",
		Summary: "Role pilote stuck: x", Source: "pigtail:roles/pilote?state=stuck", Timestamp: key,
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.OpenRoleAlerts(scope.Any)
	if err != nil {
		t.Fatal(err)
	}
	got := roleAlertsForPill(rows)
	if len(got) != 1 || got[0].SubjectID != "sub_x" || got[0].Role != "pilote" || got[0].State != "stuck" || !got[0].Since.Equal(key) {
		t.Fatalf("got %+v", got)
	}
}
