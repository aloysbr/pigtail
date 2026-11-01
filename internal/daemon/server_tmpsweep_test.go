package daemon

import (
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/daemon/tmpsweep"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The zero Config runs no sweeper (daemon tests never sweep the real $TMPDIR),
// "off" neither, "observe" builds one whose journal lands in the store.
func TestServerTmpSweepOff(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, mode := range []tmpsweep.Mode{"", tmpsweep.ModeOff} {
		s, err := New(Config{SocketPath: testSocketPath(t), TmpSweep: mode}, st, events.New(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if s.tmpSweep != nil {
			t.Errorf("mode %q: sweeper constructed, want none", mode)
		}
	}
	s, err := New(Config{SocketPath: testSocketPath(t), TmpSweep: tmpsweep.ModeObserve}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.tmpSweep == nil || s.tmpSweep.Mode != tmpsweep.ModeObserve {
		t.Fatalf("observe: sweeper = %+v", s.tmpSweep)
	}
	e := pigtail.JournalEntry{ID: "tmp-would-sweep:1", Kind: "tmp-would-sweep", Scope: string(scope.Master()),
		Summary: "n=1 bytes=1 oldest=3h0m0s", Source: "proc:pigtaild"}
	if err := s.tmpSweep.Journal(e); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListJournal(store.JournalFilter{Kind: "tmp-would-sweep"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
}
