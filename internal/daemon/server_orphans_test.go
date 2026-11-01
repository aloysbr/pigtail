package daemon

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/orphans"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// TestServerOrphanReaperOff proves the zero Config runs no reaper (daemon
// tests and the live-pass daemon never kill), "off" neither, and "observe"
// builds one in observe mode whose journal lands in the store.
func TestServerOrphanReaperOff(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	for _, mode := range []orphans.Mode{"", orphans.ModeOff} {
		s, err := New(Config{SocketPath: testSocketPath(t), OrphanReaper: mode}, st, events.New(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if s.orphans != nil {
			t.Errorf("mode %q: reaper constructed, want none", mode)
		}
	}

	s, err := New(Config{SocketPath: testSocketPath(t), OrphanReaper: orphans.ModeObserve, OrphanReapAfter: 20 * time.Minute},
		st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.orphans == nil {
		t.Fatal("observe: no reaper")
	}
	if s.orphans.Mode != orphans.ModeObserve || s.orphans.After != 20*time.Minute {
		t.Errorf("reaper mode %q after %v", s.orphans.Mode, s.orphans.After)
	}

	// The journal validates, then stores.
	bad := pigtail.JournalEntry{ID: "orphan-tree:1:1", Kind: "orphan-tree-would-reap", Summary: "x\ny", Source: "proc:1"}
	if err := s.orphans.Journal(bad); err == nil {
		t.Error("an invalid entry was accepted")
	}
	good := pigtail.JournalEntry{ID: "orphan-tree:42:7", Timestamp: time.Unix(7, 0), Kind: "orphan-tree-would-reap",
		Scope: string(scope.Master()), Summary: "shell · orphaned 15m0s · 1 pids · CPU 0.0%", Source: "proc:42"}
	if err := s.orphans.Journal(good); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListJournal(store.JournalFilter{Kind: "orphan-tree-would-reap"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "orphan-tree:42:7" {
		t.Errorf("journal rows = %+v", rows)
	}
}
