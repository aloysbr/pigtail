package handlers

import (
	"os"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// A snapshot refreshes live rows but never resurrects a cleanly ended one
// whose process is gone.
func TestRecordSnapshotKeepsEndedSessionsEnded(t *testing.T) {
	t.Parallel()
	_, st := newTestRouter(t)
	rl := NewRelay(&Services{Store: st}, RelayConfig{})
	at := time.UnixMilli(1000).UTC()
	for _, id := range []string{"ended", "live"} {
		if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: id, UpdatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.EndPeer("ended", at); err != nil {
		t.Fatal(err)
	}
	err := rl.RecordSnapshot([]pigtail.PeerSession{
		{SessionID: "ended", PID: os.Getpid(), ConfigDir: t.TempDir(), CmuxWorkspaceID: "W"},
		{SessionID: "live", CmuxWorkspaceID: "W2", Status: "idle"},
		{SessionID: ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := st.GetPeer("ended"); p.EndedAt == nil || p.CmuxWorkspaceID != "" {
		t.Fatalf("ended session resurrected: %+v", p)
	}
	if p, _ := st.GetPeer("live"); p.CmuxWorkspaceID != "W2" || !p.UpdatedAt.After(at) || p.Source != "snapshot" {
		t.Fatalf("live session not refreshed: %+v", p)
	}
}

// A snapshot of a session whose cell still says work (an old binary, a cell
// table not yet renamed) is stored under the canonical name.
func TestPeersSnapshotWritesCanonicalScope(t *testing.T) {
	t.Parallel()
	_, st := newTestRouter(t)
	rl := NewRelay(&Services{Store: st}, RelayConfig{})
	if err := rl.RecordSnapshot([]pigtail.PeerSession{{SessionID: "s", Scope: "gx"}}); err != nil {
		t.Fatal(err)
	}
	if p, err := st.GetPeer("s"); err != nil || p.Scope != "globex" {
		t.Fatalf("snapshot scope = %+v, %v; want globex", p, err)
	}
}
