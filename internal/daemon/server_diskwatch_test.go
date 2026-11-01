package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/diskwatch"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const gib = uint64(1) << 30

// 0 (an unset Config) builds no watcher; < 0 builds one that only closes an
// alert a previous run left open; > 0 is the threshold in GiB. The floor in
// the payload is 0 when admission is off.
func TestServerDiskWatchConfig(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, tc := range []struct {
		name      string
		cfg       Config
		want      bool
		threshold uint64
		floor     uint64
	}{
		{"unset", Config{AdmitMaxJobs: 4, AdmitDiskFloorGB: 20}, false, 0, 0},
		{"empty", Config{}, false, 0, 0},
		{"cut", Config{DiskAlertGB: -1, AdmitMaxJobs: 4, AdmitDiskFloorGB: 20}, true, 0, 20 * gib},
		{"threshold", Config{DiskAlertGB: 40, AdmitMaxJobs: 4, AdmitDiskFloorGB: 20}, true, 40 * gib, 20 * gib},
		{"admission off", Config{DiskAlertGB: 40, AdmitMaxJobs: 0, AdmitDiskFloorGB: 20}, true, 40 * gib, 0},
	} {
		tc.cfg.SocketPath = testSocketPath(t)
		s, err := New(tc.cfg, st, events.New(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if (s.diskWatch != nil) != tc.want {
			t.Fatalf("%s: watcher built = %v, want %v", tc.name, s.diskWatch != nil, tc.want)
		}
		if tc.want && (s.diskWatch.AlertBytes != tc.threshold || s.diskWatch.FloorBytes != tc.floor) {
			t.Errorf("%s: alert %d floor %d, want %d %d", tc.name, s.diskWatch.AlertBytes, s.diskWatch.FloorBytes, tc.threshold, tc.floor)
		}
	}
}

// An alert left open by a previous run is closed at start when the alert is
// cut (flag 0 or -1), so nothing stays open with no loop to close it.
func TestServerDiskWatchCutClosesAStaleAlert(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AddJournal(&pigtail.JournalEntry{ID: "disk-low:1", Timestamp: time.Now().Add(-time.Hour).UTC(), Kind: "disk-low",
		Scope: string(scope.Master()), Summary: "free_bytes=1 threshold_bytes=2 floor_bytes=3", Source: "proc:pigtaild"}); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{SocketPath: testSocketPath(t), DiskAlertGB: -1}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.diskWatch.Start()
	if e, err := diskwatch.OpenAlert(st); err != nil || e != nil {
		t.Fatalf("open alert after start = %v, %v; want closed", e, err)
	}
}

// The whole path: a crossing writes the master-scope line, publishes disk.low
// on the bus, and GET /alerts shows it to a caller of any scope; the next
// crossing back closes it.
func TestServerDiskWatchEndToEnd(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	bus := events.New()
	s, err := New(Config{SocketPath: testSocketPath(t), DiskAlertGB: 40, AdmitMaxJobs: 4, AdmitDiskFloorGB: 20}, st, bus, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	free := 31 * gib
	s.diskWatch.Free = func() (uint64, error) { return free, nil }
	sub := bus.Subscribe("disk.*")
	defer sub.Close()
	s.diskWatch.Start()
	s.diskWatch.Tick()

	select {
	case e := <-sub.C:
		if e.Type != pigtail.EventDiskLow || e.Scope != "" {
			t.Fatalf("event = %+v, want an untagged disk.low", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no disk.low event")
	}
	rows, err := st.ListJournal(store.JournalFilter{Kind: "disk-low"})
	if err != nil || len(rows) != 1 || rows[0].Scope != string(scope.Master()) {
		t.Fatalf("journal = %+v, %v", rows, err)
	}

	alerts := func(hdr string) []pigtail.AlertView {
		req := httptest.NewRequest("GET", "/alerts", nil)
		req.Header.Set(pigtail.HeaderScope, hdr)
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /alerts = %d %s", rec.Code, rec.Body)
		}
		var env struct {
			Data pigtail.AlertsResponse `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		return env.Data.Alerts
	}
	for _, name := range scope.Active().Names() {
		got := alerts(string(name))
		if len(got) != 1 || got[0].Kind != pigtail.AlertKindDisk || got[0].Scope != "" || !strings.Contains(got[0].Detail, "31 GB free") {
			t.Fatalf("scope %q sees %+v", name, got)
		}
	}
	free = 80 * gib
	s.diskWatch.Tick()
	for _, name := range scope.Active().Names() {
		if got := alerts(string(name)); len(got) != 0 {
			t.Fatalf("scope %q still sees %+v after the disk-ok", name, got)
		}
	}
}

// A restart under the threshold re-reads the open alert from the journal and
// writes nothing new.
func TestServerDiskWatchRestartKeepsTheAlert(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	first, err := New(Config{SocketPath: testSocketPath(t), DiskAlertGB: 40}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	first.diskWatch.Free = func() (uint64, error) { return 10 * gib, nil }
	first.diskWatch.Start()
	first.diskWatch.Tick()

	second, err := New(Config{SocketPath: testSocketPath(t), DiskAlertGB: 40}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	second.diskWatch.Free = func() (uint64, error) { return 10 * gib, nil }
	second.diskWatch.Start()
	second.diskWatch.Tick()
	rows, err := st.ListJournal(store.JournalFilter{Kind: "disk-low"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("a restart under the threshold wrote %d disk-low lines, want 1 (%v)", len(rows), err)
	}
}

// Guard: disk.low and disk.ok carry no scope tag and are not subject-bearing,
// so every subscriber receives them (sse.go eventVisible).
func TestSSEDiskEventsAreVisibleToEveryScope(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	bus := events.New()
	srv, err := New(Config{SocketPath: testSocketPath(t)}, st, bus, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	bus.Publish(pigtail.Event{Type: pigtail.EventDiskLow, Data: map[string]interface{}{"free_bytes": 1}})
	bus.Publish(pigtail.Event{Type: pigtail.EventDiskOK, Data: map[string]interface{}{"free_bytes": 2}})
	for _, name := range scope.Active().Names() {
		body := streamAs(t, srv, string(name)).Body.String()
		for _, want := range []string{"disk.low", "disk.ok"} {
			if !strings.Contains(body, want) {
				t.Errorf("subscriber %q lost %s:\n%s", name, want, body)
			}
		}
	}
	if body := streamAs(t, srv, "").Body.String(); !strings.Contains(body, "disk.low") {
		t.Errorf("a headerless subscriber lost disk.low:\n%s", body)
	}
}
