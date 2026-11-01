package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Deps.Paused is the « Paused » pill's read of the session_pause table
// (sub_x000000000000036, task 9).
func TestServerWiresPausedPill(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	s, err := New(Config{SocketPath: testSocketPath(t), DBPath: filepath.Join(t.TempDir(), "pigtail.db")},
		st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := s.cmuxSyncDeps()
	if deps.Paused == nil {
		t.Fatal("Deps.Paused nil")
	}
	if err := st.BeginPause(pigtail.SessionPause{SessionID: "s1", PID: 7, SurfaceID: "x", CmuxWorkspaceID: "WS1", State: pigtail.PauseStopping, Since: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got, err := deps.Paused(); err != nil || len(got) != 1 || got[0].SessionID != "s1" {
		t.Fatalf("Paused = %v, %v", got, err)
	}
	if deps.PauseGone == nil {
		t.Fatal("Deps.PauseGone nil")
	}
	if err := deps.PauseGone("s1", "WS1"); err != nil {
		t.Fatal(err)
	}
	if got, err := deps.Paused(); err != nil || len(got) != 0 {
		t.Fatalf("after PauseGone: %v, %v", got, err)
	}
}
