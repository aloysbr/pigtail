package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The branch reconciliation runs on a timer over every workspace, so the write
// it performs must be as narrow as the change it makes. UpdateWorkspacePath —
// the only existing way to touch backend_data — bumps last_activity_at, which
// would make every workspace look freshly active every few minutes.
//
// That is not cosmetic: the menu bar's Start list is sorted by
// last_activity_at, and `pigtail list` reads it as recency. A background
// bookkeeping pass must not be able to reorder what the user sees.

func TestUpdateWorkspaceBackendDataLeavesActivityAlone(t *testing.T) {
	st := seededStore(t, filepath.Join(t.TempDir(), "pigtail.db"))

	ws := &pigtail.Workspace{
		ID: "ws_a", ProjectID: "prj_backup", Path: "/repo/a",
		State:       pigtail.StateBuilding,
		BackendData: map[string]any{"branch": "feat/old", "base_branch": "main"},
	}
	if err := st.CreateWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	before, err := st.GetWorkspace("ws_a")
	if err != nil {
		t.Fatal(err)
	}
	// A gap wide enough that a bumped timestamp cannot be mistaken for the
	// original one.
	time.Sleep(5 * time.Millisecond)

	if err := st.UpdateWorkspaceBackendData("ws_a", map[string]any{
		"branch": "feat/new", "base_branch": "main",
	}); err != nil {
		t.Fatalf("UpdateWorkspaceBackendData: %v", err)
	}

	after, err := st.GetWorkspace("ws_a")
	if err != nil {
		t.Fatal(err)
	}
	if got := after.BackendData["branch"]; got != "feat/new" {
		t.Fatalf("branch = %v, want feat/new", got)
	}
	// Compared at millisecond precision, which is what the column actually
	// stores (UnixMilli). The first read comes from the workspace cache and
	// still carries the in-process value's microseconds; the second is
	// re-read from SQLite after the write invalidated that cache. Comparing
	// the raw values would fail on the truncation rather than on a bump —
	// which is exactly what the first version of this test did.
	beforeMs := before.LastActivityAt.UnixMilli()
	afterMs := after.LastActivityAt.UnixMilli()
	if afterMs != beforeMs {
		t.Fatalf("last_activity_at moved from %v to %v; a bookkeeping write must not "+
			"make a workspace look active", before.LastActivityAt, after.LastActivityAt)
	}
	if after.State != before.State {
		t.Fatalf("state changed from %v to %v", before.State, after.State)
	}
	if after.Path != before.Path {
		t.Fatalf("path changed from %q to %q", before.Path, after.Path)
	}
}

// The cache is keyed by workspace id; a write that skips the invalidation
// leaves readers serving the branch that just stopped being true.
func TestUpdateWorkspaceBackendDataInvalidatesTheCache(t *testing.T) {
	st := seededStore(t, filepath.Join(t.TempDir(), "pigtail.db"))
	ws := &pigtail.Workspace{
		ID: "ws_c", ProjectID: "prj_backup", Path: "/repo/c",
		State:       pigtail.StateBuilding,
		BackendData: map[string]any{"branch": "feat/cached"},
	}
	if err := st.CreateWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	// Warm the cache before writing, so a stale read is possible at all.
	if _, err := st.GetWorkspace("ws_c"); err != nil {
		t.Fatal(err)
	}

	if err := st.UpdateWorkspaceBackendData("ws_c", map[string]any{"branch": "feat/fresh"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorkspace("ws_c")
	if err != nil {
		t.Fatal(err)
	}
	if got.BackendData["branch"] != "feat/fresh" {
		t.Fatalf("branch = %v, want feat/fresh; the cache was not invalidated", got.BackendData["branch"])
	}
}
