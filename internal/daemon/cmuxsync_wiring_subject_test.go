package daemon

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// D29-A (Task 18): before the relay exists, the wiring's store fallback of
// ClearWaits keeps the wait a gone session's subject owns; a live session's
// clear closes it.
func TestServerWiresClearWaitsGoneWithoutRelay(t *testing.T) {
	for _, c := range []struct {
		gone bool
		want string
	}{{true, pigtail.WaitStateOpen}, {false, pigtail.WaitStateCleared}} {
		st, err := store.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		s, err := New(Config{SocketPath: testSocketPath(t)}, st, events.New(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		deps := s.cmuxSyncDeps()
		now := time.Now()
		if err := st.CreateTheme(&pigtail.Theme{ID: "thm_p", Name: "P", Kind: "ongoing", Scope: "perso", VaultLink: "Perso/Areas/p", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateSubject(&pigtail.Subject{ID: "sub_s", ThemeID: "thm_p", Title: "S", Slug: "s", Status: pigtail.StatusInbox, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: "s1", Name: "alpha", Scope: "perso"}); err != nil {
			t.Fatal(err)
		}
		tr, err := st.SetSessionWaitRecord("s1", pigtail.SessionWait{Type: "arbitrage", Source: pigtail.WaitSourceDeclared, Since: now},
			store.WaitMeta{Scope: "perso", SubjectID: "sub_s"})
		if err != nil {
			t.Fatal(err)
		}
		deps.ClearWaits("s1", []string{pigtail.WaitSourceDeclared}, c.gone)
		if ws, _ := st.SessionWaits("s1"); len(ws) != 0 {
			t.Fatalf("gone=%v: rows left %+v", c.gone, ws)
		}
		r, err := st.GetWaitRecord(tr.OpenedID)
		if err != nil || r.State != c.want {
			t.Fatalf("gone=%v: record = %+v %v, want %s", c.gone, r, err, c.want)
		}
		_ = st.Close()
	}
}
