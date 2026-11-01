package store

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// TestGetWorkspaceDoesNotCacheARowWrittenMeanwhile: a GetWorkspace that read
// the row before a concurrent write, and caches it after the write's
// invalidation, would serve the stale row for good (a workspace whose hooks
// stay "running" after they ended: TestNewWorkspaceHooksOutliveTheClient,
// 8 failures in 300 runs under load). The write lands exactly in that window
// through the afterWorkspaceRead seam: no goroutine, no clock.
func TestGetWorkspaceDoesNotCacheARowWrittenMeanwhile(t *testing.T) {
	s := newTestStore(t)
	seedWorkspace(t, s, "ws_r")
	at := time.UnixMilli(1_700_000_000_000).UTC()
	if err := s.SetWorkspaceHooks("ws_r", pigtail.HooksResult{Status: pigtail.HooksRunning, Log: "/l", At: at}); err != nil {
		t.Fatal(err)
	}
	fired := false
	s.afterWorkspaceRead = func(id string) {
		if fired || id != "ws_r" {
			return
		}
		fired = true
		if err := s.SetWorkspaceHooks("ws_r", pigtail.HooksResult{Status: pigtail.HooksOK, Log: "/l", At: at}); err != nil {
			t.Error(err)
		}
	}
	if ws, err := s.GetWorkspace("ws_r"); err != nil || ws.Hooks == nil || ws.Hooks.Status != pigtail.HooksRunning {
		t.Fatalf("first read = %+v, %v; want the row as read (running)", ws, err)
	}
	if !fired {
		t.Fatal("the seam never ran: the write did not land between the read and the cache")
	}
	ws, err := s.GetWorkspace("ws_r")
	if err != nil {
		t.Fatal(err)
	}
	if ws.Hooks == nil || ws.Hooks.Status != pigtail.HooksOK {
		t.Fatalf("hooks after the write = %+v, want %s (a stale row was cached)", ws.Hooks, pigtail.HooksOK)
	}
}
