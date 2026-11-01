package daemon

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// An alert outlives the daemon: posted to one server, read by a new server
// opened on the same database file (D3).
func TestAlertsSurviveDaemonRestart(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	db := filepath.Join(t.TempDir(), "p.db")
	serve := func() (*Server, *store.Store) {
		st, err := store.Open(db)
		if err != nil {
			t.Fatal(err)
		}
		s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return s, st
	}
	s1, st1 := serve()
	b, _ := json.Marshal(pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnAbandoned, RunID: "r-restart", SessionID: "s-none", Detail: "d"})
	req := httptest.NewRequest("POST", "/cmux-workspaces/22222222-BBBB-4BBB-8BBB-222222222222/respawn", bytes.NewReader(b))
	req.Header.Set(pigtail.HeaderScope, "perso")
	s1.router.ServeHTTP(httptest.NewRecorder(), req)
	_ = st1.Close()

	s2, st2 := serve()
	defer st2.Close()
	req = httptest.NewRequest("GET", "/alerts", nil)
	req.Header.Set(pigtail.HeaderScope, "perso")
	rec := httptest.NewRecorder()
	s2.router.ServeHTTP(rec, req)
	var env struct{ Data pigtail.AlertsResponse }
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if rec.Code != 200 || len(env.Data.Alerts) != 1 || env.Data.Alerts[0].ID != "r-restart" {
		t.Fatalf("after restart: %d %s", rec.Code, rec.Body)
	}
}
