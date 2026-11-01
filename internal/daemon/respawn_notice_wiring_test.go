package daemon

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// server.go must hand the relay to the respawn route: a final state with a
// run writes its journal line through the real daemon.
func TestServerWiresRespawnNotifier(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(pigtail.CmuxRespawnRequest{State: pigtail.CmuxRespawnAbandoned, RunID: "r-wire", SessionID: "s-none", Detail: "d"})
	req := httptest.NewRequest("POST", "/cmux-workspaces/22222222-BBBB-4BBB-8BBB-222222222222/respawn", bytes.NewReader(b))
	req.Header.Set(pigtail.HeaderScope, "perso")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	js, err := st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: "respawn-abandoned"})
	if err != nil || len(js) != 1 {
		t.Fatalf("POST = %d %s; journal = %+v, %v — the respawn route has no notifier", rec.Code, rec.Body, js, err)
	}
	// CA8b: the real loop is constructed but off (no --cmux-sidebar): the
	// pill is said skipped, never "ok".
	var env struct {
		Data pigtail.CmuxRespawnNotice `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if rec.Code != 200 || env.Data.Pill != "skipped: this daemon runs no cmux sidebar loop" {
		t.Fatalf("loop off: POST = %d %s, want pill skipped", rec.Code, rec.Body)
	}
}
