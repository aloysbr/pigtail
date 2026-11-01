package handlers

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// GET /peers decorates only the rows it serves: dead peers are skipped before
// decorate (one cmux-sidebar State() read per decorated row), not after.
func TestGetPeersDecoratesLiveRowsOnly(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctl := &fakeSidebarCtl{}
	svc := &Services{Store: st, Logf: t.Logf, CmuxSidebar: ctl}
	rl := NewRelay(svc, RelayConfig{
		MarkerDir:     t.TempDir(),
		WaitMarkerDir: t.TempDir(),
		Alive:         func(p pigtail.PeerSession) bool { return p.SessionID == "live" },
	})
	r := chi.NewRouter()
	RegisterRelayRoutes(r, rl, testResponder{})

	// Seeded in the store: POST /peers would end the dead ones itself.
	ids := []string{"live"}
	for i := 0; i < 20; i++ {
		ids = append(ids, fmt.Sprintf("dead-%d", i))
	}
	for _, id := range ids {
		if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: id, Scope: string(scope.Default()), UpdatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}

	rec := doJSON(t, r, "GET", "/peers", nil)
	if rec.Code != 200 {
		t.Fatalf("GET /peers = %d %s", rec.Code, rec.Body)
	}
	var got []pigtail.PeerSession
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "live" {
		t.Fatalf("served %+v (body %s), want only the live peer", got, rec.Body)
	}
	if ctl.states != 1 {
		t.Fatalf("decorated %d rows, want 1", ctl.states)
	}
}
