package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// seedDiskLine writes the line the disk watcher writes (diskwatch.write),
// straight into the store: GET /alerts reads the journal and nothing else.
func seedDiskLine(t *testing.T, st *store.Store, kind string, free uint64, at time.Time) {
	t.Helper()
	e := &pigtail.JournalEntry{
		ID: fmt.Sprintf("%s:%d", kind, at.UnixNano()), Timestamp: at, Kind: kind, Scope: string(scope.Master()),
		Summary: fmt.Sprintf("free_bytes=%d threshold_bytes=%d floor_bytes=%d", free, 40<<30, 20<<30),
		Source:  "proc:pigtaild",
	}
	if _, err := st.AddJournal(e); err != nil {
		t.Fatal(err)
	}
}

func TestListAlertsShowsTheDiskAlertToEveryScope(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	t0 := time.Now().Add(-10 * time.Minute).UTC()
	seedDiskLine(t, st, "disk-low", 31<<30, t0)
	seedRespawnLine(t, st, "perso", "failed", "r-1", "sub_p", "no fresh Claude", t0.Add(time.Minute), false)

	master := caller{scope: string(scope.Master())}
	for _, c := range []caller{master, work, perso} {
		got := getAlerts(t, r, c)
		var disk *pigtail.AlertView
		for i := range got {
			if got[i].Kind == pigtail.AlertKindDisk {
				disk = &got[i]
			}
		}
		if disk == nil {
			t.Fatalf("caller %q does not see the disk alert: %+v", c.scope, got)
		}
		if disk.FreeBytes != 31<<30 || disk.FloorBytes != 20<<30 {
			t.Errorf("caller %q: free/floor = %d/%d, want 31/20 GiB (what the bar's notification quotes)", c.scope, disk.FreeBytes, disk.FloorBytes)
		}
		if disk.State != "low" || disk.Detail != "31 GB free on disk" || disk.Scope != "" || disk.SubjectID != "" || disk.SubjectTitle != "" {
			t.Errorf("caller %q: disk alert = %+v", c.scope, disk)
		}
		if disk.AgeSeconds < 590 {
			t.Errorf("age = %d, want ~600", disk.AgeSeconds)
		}
	}
}

// The probe the plan asks for: a non-master caller reads the disk alert, and
// the body of the answer holds no declared scope name (the line is written
// under the master scope; it must not leak that name to a lesser caller).
func TestDiskAlertBodyNamesNoScope(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	seedDiskLine(t, st, "disk-low", 31<<30, time.Now().Add(-time.Minute).UTC())
	for _, c := range []caller{work, perso} {
		rec := doAs(t, r, c, "GET", "/alerts", nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"kind":"disk"`) {
			t.Fatalf("GET /alerts as %q = %d %s", c.scope, rec.Code, rec.Body)
		}
		body := strings.ToLower(rec.Body.String())
		for _, name := range scope.Active().Names() {
			if strings.Contains(body, strings.ToLower(string(name))) {
				t.Errorf("caller %q: the disk alert body names the scope %q:\n%s", c.scope, name, rec.Body)
			}
		}
		if strings.Contains(body, `"scope"`) {
			t.Errorf("the scope key must be omitted for a machine alert:\n%s", rec.Body)
		}
	}
}

func TestDiskAlertClosesWithTheNextDiskOK(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	t0 := time.Now().Add(-time.Hour).UTC()
	seedDiskLine(t, st, "disk-low", 31<<30, t0)
	seedDiskLine(t, st, "disk-ok", 60<<30, t0.Add(10*time.Minute))
	if got := getAlerts(t, r, perso); len(got) != 0 {
		t.Fatalf("a disk-ok after the disk-low closes the alert: %+v", got)
	}
	seedDiskLine(t, st, "disk-low", 12<<30, t0.Add(20*time.Minute))
	got := getAlerts(t, r, perso)
	if len(got) != 1 || got[0].Detail != "12 GB free on disk" {
		t.Fatalf("a newer disk-low reopens it: %+v", got)
	}
}

func TestDiskAlertCannotBeAcked(t *testing.T) {
	t.Parallel()
	r, st := alertsRouter(t)
	seedDiskLine(t, st, "disk-low", 31<<30, time.Now().Add(-time.Minute).UTC())
	got := getAlerts(t, r, perso)
	if len(got) != 1 {
		t.Fatalf("setup: %+v", got)
	}
	rec := doAs(t, r, perso, "POST", "/alerts/"+got[0].ID+"/ack", nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "disk") {
		t.Fatalf("ack of the disk alert = %d %s, want an explicit 400", rec.Code, rec.Body)
	}
	if n := len(getAlerts(t, r, perso)); n != 1 {
		t.Fatalf("the alert must stay open, %d left", n)
	}
}
