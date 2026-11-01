package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestWorkspaceRoundTrip(t *testing.T) {
	s := newTestStore(t)

	proj := &pigtail.Project{
		ID:             "prj_test",
		Name:           "test",
		RootPath:       "/tmp/test",
		DefaultBackend: "git",
		PortRanges:     map[string]pigtail.PortRange{"dev": {3000, 3099}},
	}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	ws := &pigtail.Workspace{
		ID:          "ws_abc",
		ProjectID:   proj.ID,
		Intent:      "say hello",
		State:       pigtail.StateBuilding,
		Path:        "/tmp/test-feat-hello",
		Ports:       map[string]int{"dev": 3001},
		BackendKind: "git",
		BackendData: map[string]interface{}{"branch": "feat/hello"},
	}
	if err := s.CreateWorkspace(ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	got, err := s.GetWorkspace(ws.ID)
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if got.Intent != ws.Intent || got.State != ws.State {
		t.Fatalf("round-trip mismatch: got %+v", got)
	}
	if got.Ports["dev"] != 3001 {
		t.Fatalf("ports mismatch: %v", got.Ports)
	}
	if got.BackendData["branch"] != "feat/hello" {
		t.Fatalf("backend_data mismatch: %v", got.BackendData)
	}
}

func TestAgentSessionIntentRoundTrip(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateProject(&pigtail.Project{
		ID: "prj_as", Name: "as", RootPath: "/tmp/as", DefaultBackend: "git",
		PortRanges: map[string]pigtail.PortRange{"dev": {3000, 3099}},
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateWorkspace(&pigtail.Workspace{
		ID: "ws_as", ProjectID: "prj_as", Intent: "x", State: pigtail.StateBuilding,
		Path: "/tmp/as-ws", Ports: map[string]int{"dev": 3001}, BackendKind: "git",
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := s.CreateAgentSession(&pigtail.AgentSession{
		ID: "agt_as", WorkspaceID: "ws_as", AgentKind: "claude",
		Intent: "refactor the auth helper", State: pigtail.AgentStateRunning,
		TmuxSessionName: "pigtail-ws_as-agt_as",
	}); err != nil {
		t.Fatalf("create agent session: %v", err)
	}
	sessions, err := s.ListAgentSessionsByWorkspace("ws_as")
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Intent != "refactor the auth helper" {
		t.Fatalf("intent round-trip mismatch: %q", sessions[0].Intent)
	}
}

func TestListWorkspacesByState(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateProject(&pigtail.Project{ID: "prj_x", Name: "x", RootPath: "/", DefaultBackend: "git", PortRanges: map[string]pigtail.PortRange{}}); err != nil {
		t.Fatal(err)
	}
	for i, st := range []pigtail.WorkspaceState{pigtail.StateBuilding, pigtail.StateBuilding, pigtail.StateShipped} {
		err := s.CreateWorkspace(&pigtail.Workspace{
			ID: idn("ws", i), ProjectID: "prj_x", Intent: "x", State: st,
			Path: "/", Ports: map[string]int{}, BackendKind: "git",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	building, err := s.ListWorkspaces(pigtail.StateBuilding)
	if err != nil {
		t.Fatal(err)
	}
	if len(building) != 2 {
		t.Fatalf("expected 2 building, got %d", len(building))
	}
	all, err := s.ListWorkspaces("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 total, got %d", len(all))
	}
}

func idn(prefix string, i int) string { return prefix + "_" + string(rune('a'+i)) }

// TestProjectPortEnvsAndDefaults proves the new fields survive a
// CreateProject → GetProject round-trip when set, and are nil otherwise
// (the latter matches a row inserted under the pre-migration schema).
func TestProjectPortEnvsAndDefaults(t *testing.T) {
	s := newTestStore(t)
	p := &pigtail.Project{
		ID:             "prj_rich",
		Name:           "rich",
		RootPath:       "/tmp/rich",
		DefaultBackend: "git",
		PortRanges:     map[string]pigtail.PortRange{"simulator": {9000, 9099}},
		PortEnvs:       map[string]string{"simulator": "PIGTAIL_SIM_PORT"},
		Defaults: &pigtail.ProjectDefaults{
			Agent:        "claude",
			BaseRef:      "develop",
			BranchPrefix: "research/",
		},
	}
	if err := s.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProject(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PortEnvs["simulator"] != "PIGTAIL_SIM_PORT" {
		t.Errorf("PortEnvs: %v", got.PortEnvs)
	}
	if got.Defaults == nil || got.Defaults.BranchPrefix != "research/" {
		t.Errorf("Defaults: %+v", got.Defaults)
	}

	// Project without the new fields stays nil after round-trip.
	bare := &pigtail.Project{
		ID:             "prj_bare",
		Name:           "bare",
		RootPath:       "/tmp/bare",
		DefaultBackend: "git",
		PortRanges:     map[string]pigtail.PortRange{"dev": {3000, 3099}},
	}
	if err := s.CreateProject(bare); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetProject(bare.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PortEnvs != nil {
		t.Errorf("bare project should have nil PortEnvs, got %v", got.PortEnvs)
	}
	if got.Defaults != nil {
		t.Errorf("bare project should have nil Defaults, got %+v", got.Defaults)
	}
}

func TestRecordRunaway(t *testing.T) {
	s := newTestStore(t)

	proj := &pigtail.Project{
		ID:             "prj_run",
		Name:           "run",
		RootPath:       "/tmp/run",
		DefaultBackend: "git",
		PortRanges:     map[string]pigtail.PortRange{"dev": {3000, 3099}},
	}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	ws := &pigtail.Workspace{
		ID:          "ws_run",
		ProjectID:   proj.ID,
		Intent:      "spin",
		State:       pigtail.StateBuilding,
		Path:        "/tmp/run-feat-spin",
		Ports:       map[string]int{"dev": 3001},
		BackendKind: "git",
	}
	if err := s.CreateWorkspace(ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	// Fresh workspace carries no runaway marker.
	got, err := s.GetWorkspace(ws.ID)
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if got.LastRunawayAt != nil || got.LastRunawayReason != "" {
		t.Fatalf("fresh workspace has runaway marker: %v %q", got.LastRunawayAt, got.LastRunawayReason)
	}

	if err := s.RecordRunaway(ws.ID, "runaway_cpu"); err != nil {
		t.Fatalf("record runaway: %v", err)
	}
	got, err = s.GetWorkspace(ws.ID)
	if err != nil {
		t.Fatalf("get workspace after record: %v", err)
	}
	if got.LastRunawayAt == nil {
		t.Fatal("last_runaway_at not set")
	}
	if got.LastRunawayReason != "runaway_cpu" {
		t.Errorf("reason: %q, want runaway_cpu", got.LastRunawayReason)
	}

	// The marker also survives the list path (separate scan call sites).
	list, err := s.ListWorkspaces("")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, w := range list {
		if w.ID == ws.ID {
			found = true
			if w.LastRunawayAt == nil || w.LastRunawayReason != "runaway_cpu" {
				t.Errorf("list lost runaway marker: %v %q", w.LastRunawayAt, w.LastRunawayReason)
			}
		}
	}
	if !found {
		t.Fatal("workspace missing from list")
	}
}

func TestUpdateWorkspacePath(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateProject(&pigtail.Project{
		ID: "prj_mv", Name: "mv", RootPath: "/tmp/mv", DefaultBackend: "git",
		PortRanges: map[string]pigtail.PortRange{"dev": {3000, 3099}},
	}); err != nil {
		t.Fatal(err)
	}
	ws := &pigtail.Workspace{
		ID: "ws_mv", ProjectID: "prj_mv", Intent: "x", State: pigtail.StateBuilding,
		Path: "/tmp/mv/old", Ports: map[string]int{"dev": 3001}, BackendKind: "git",
		BackendData: map[string]interface{}{"branch": "feat/old", "base_branch": "main"},
	}
	if err := s.CreateWorkspace(ws); err != nil {
		t.Fatal(err)
	}

	// Path-only update leaves backend_data and state untouched.
	if err := s.UpdateWorkspacePath(ws.ID, "/tmp/mv/new", nil); err != nil {
		t.Fatalf("update path: %v", err)
	}
	got, err := s.GetWorkspace(ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "/tmp/mv/new" {
		t.Errorf("path = %q, want /tmp/mv/new", got.Path)
	}
	if got.State != pigtail.StateBuilding {
		t.Errorf("state changed to %q", got.State)
	}
	if got.BackendData["branch"] != "feat/old" {
		t.Errorf("backend_data branch changed: %v", got.BackendData)
	}

	// Update with backend_data rewrites the branch.
	if err := s.UpdateWorkspacePath(ws.ID, "/tmp/mv/newer", map[string]any{
		"branch": "feat/renamed", "base_branch": "main",
	}); err != nil {
		t.Fatalf("update path+data: %v", err)
	}
	got, err = s.GetWorkspace(ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "/tmp/mv/newer" {
		t.Errorf("path = %q, want /tmp/mv/newer", got.Path)
	}
	if got.BackendData["branch"] != "feat/renamed" {
		t.Errorf("branch = %v, want feat/renamed", got.BackendData["branch"])
	}
}

func TestThemeRoundTrip(t *testing.T) {
	s := newTestStore(t)
	th := &pigtail.Theme{
		ID: "thm_meeting", Name: "Meeting", Kind: "project",
		VaultLink: "Globex/Projects/rain-gauge", CreatedAt: time.UnixMilli(1000).UTC(),
	}
	if err := s.CreateTheme(th); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetTheme("thm_meeting")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Meeting" || got.Kind != "project" || got.VaultLink != "Globex/Projects/rain-gauge" {
		t.Fatalf("mismatch: %+v", got)
	}
	if _, err := s.GetTheme("thm_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	list, err := s.ListThemes(scope.Any)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v len=%d", err, len(list))
	}
}

func TestUpdateTheme(t *testing.T) {
	s := newTestStore(t)
	th := &pigtail.Theme{
		ID: "thm_u", Name: "Old", Kind: "project",
		VaultLink: "Globex/Projects/old", CreatedAt: time.UnixMilli(1000).UTC(),
	}
	if err := s.CreateTheme(th); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Partial update: only name.
	name := "New name"
	if err := s.UpdateTheme("thm_u", pigtail.ThemeUpdate{Name: &name}); err != nil {
		t.Fatalf("update name: %v", err)
	}
	got, _ := s.GetTheme("thm_u")
	if got.Name != "New name" || got.VaultLink != "Globex/Projects/old" {
		t.Fatalf("name-only update wrong: %+v", got)
	}
	if got.Kind != "project" || !got.CreatedAt.Equal(time.UnixMilli(1000).UTC()) {
		t.Fatalf("immutable fields changed: %+v", got)
	}

	// Update both name and vault_link.
	name2, link2 := "Newer", "Globex/Projects/newer"
	if err := s.UpdateTheme("thm_u", pigtail.ThemeUpdate{Name: &name2, VaultLink: &link2}); err != nil {
		t.Fatalf("update both: %v", err)
	}
	got, _ = s.GetTheme("thm_u")
	if got.Name != "Newer" || got.VaultLink != "Globex/Projects/newer" {
		t.Fatalf("both update wrong: %+v", got)
	}

	// Missing theme → ErrNotFound.
	if err := s.UpdateTheme("thm_nope", pigtail.ThemeUpdate{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDeleteThemeAndCount(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s) // thm_x
	if err := s.CreateSubject(&pigtail.Subject{
		ID: "sub_d", ThemeID: themeID, Title: "t", Slug: "t", Status: pigtail.StatusInbox,
		CreatedAt: time.UnixMilli(1).UTC(), UpdatedAt: time.UnixMilli(1).UTC(),
	}); err != nil {
		t.Fatalf("create subject: %v", err)
	}

	n, err := s.CountSubjectsByTheme(themeID)
	if err != nil || n != 1 {
		t.Fatalf("count = %d err=%v, want 1", n, err)
	}
	n, err = s.CountSubjectsByTheme("thm_other")
	if err != nil || n != 0 {
		t.Fatalf("count other = %d err=%v, want 0", n, err)
	}

	// DeleteTheme itself does not guard (the handler does); but once the
	// subject is gone the row deletes and a second delete is ErrNotFound.
	if err := s.DeleteTheme("thm_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing: want ErrNotFound, got %v", err)
	}
	if err := s.DeleteTheme(themeID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetTheme(themeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("theme still present after delete: %v", err)
	}
}

func TestSubjectThemeReassignment(t *testing.T) {
	s := newTestStore(t)
	src := seedTheme(t, s) // thm_x
	dst := &pigtail.Theme{ID: "thm_y", Name: "Y", Kind: "area", VaultLink: "Globex/Areas/y", CreatedAt: time.UnixMilli(2).UTC()}
	if err := s.CreateTheme(dst); err != nil {
		t.Fatalf("create dst theme: %v", err)
	}
	sub := &pigtail.Subject{
		ID: "sub_r", ThemeID: src, Title: "t", Slug: "t", Status: pigtail.StatusInbox,
		CreatedAt: time.UnixMilli(1).UTC(), UpdatedAt: time.UnixMilli(1).UTC(),
	}
	if err := s.CreateSubject(sub); err != nil {
		t.Fatalf("create subject: %v", err)
	}

	sub.ThemeID = "thm_y"
	sub.UpdatedAt = time.UnixMilli(5).UTC()
	if err := s.UpdateSubject(sub); err != nil {
		t.Fatalf("reassign: %v", err)
	}
	got, _ := s.GetSubject("sub_r")
	if got.ThemeID != "thm_y" {
		t.Fatalf("theme not reassigned: %+v", got)
	}
	if n, _ := s.CountSubjectsByTheme("thm_x"); n != 0 {
		t.Fatalf("source theme still has %d subjects", n)
	}
	if n, _ := s.CountSubjectsByTheme("thm_y"); n != 1 {
		t.Fatalf("dest theme has %d subjects, want 1", n)
	}
}

func TestSubjectMigrationCreatesTables(t *testing.T) {
	s := newTestStore(t)
	for _, name := range []string{"themes", "subjects", "subject_phases", "subject_log"} {
		var got string
		err := s.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, name,
		).Scan(&got)
		if err != nil {
			t.Fatalf("table %q not found: %v", name, err)
		}
	}
}

// seedTheme inserts a theme so subjects have a valid parent.
func seedTheme(t *testing.T, s *Store) string {
	t.Helper()
	th := &pigtail.Theme{ID: "thm_x", Name: "X", Kind: "project", VaultLink: "Globex/Projects/x", CreatedAt: time.UnixMilli(1).UTC()}
	if err := s.CreateTheme(th); err != nil {
		t.Fatalf("seed theme: %v", err)
	}
	return th.ID
}

func TestSubjectCreateGet(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)

	sub := &pigtail.Subject{
		ID: "sub_1", ThemeID: themeID, Title: "checklist style", Slug: "checklist-style",
		Status: pigtail.StatusInbox, Impact: 4, Effort: 2, Urgency: 3, Rank: 0,
		Deps: []string{"sub_0"}, VaultPath: "Globex/Projects/x/subjects/checklist-style",
		CreatedAt: time.UnixMilli(2000).UTC(), UpdatedAt: time.UnixMilli(2000).UTC(),
	}
	if err := s.CreateSubject(sub); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetSubject("sub_1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "checklist style" || got.Impact != 4 || got.Effort != 2 || got.Urgency != 3 {
		t.Fatalf("scores mismatch: %+v", got)
	}
	if got.Rank != 0 {
		t.Fatalf("rank: want 0 (unset), got %d", got.Rank)
	}
	if len(got.Deps) != 1 || got.Deps[0] != "sub_0" {
		t.Fatalf("deps mismatch: %+v", got.Deps)
	}
	if _, err := s.GetSubject("sub_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestWorkspaceForSubject(t *testing.T) {
	s := newTestStore(t)

	// Seed: project (workspace FK), theme (subject FK), subject "sub_1".
	if err := s.CreateProject(&pigtail.Project{
		ID: "prj_test", Name: "test", RootPath: "/tmp/test", DefaultBackend: "git",
		PortRanges: map[string]pigtail.PortRange{},
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	themeID := seedTheme(t, s)
	if err := s.CreateSubject(&pigtail.Subject{
		ID: "sub_1", ThemeID: themeID, Title: "S", Slug: "s", Status: pigtail.StatusInbox,
		CreatedAt: time.UnixMilli(1).UTC(), UpdatedAt: time.UnixMilli(1).UTC(),
	}); err != nil {
		t.Fatalf("create subject: %v", err)
	}

	ws := &pigtail.Workspace{ID: "ws_link", ProjectID: "prj_test", State: pigtail.StateBuilding, Path: "/tmp/x", BackendKind: "git"}
	if err := s.CreateWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePhase(&pigtail.SubjectPhase{ID: "ph_1", SubjectID: "sub_1", Kind: "implement", State: "active", WorkspaceID: ws.ID, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	got, err := s.WorkspaceForSubject("sub_1")
	if err != nil {
		t.Fatalf("want ws, got err %v", err)
	}
	if got.ID != ws.ID {
		t.Fatalf("got %s want %s", got.ID, ws.ID)
	}

	if _, err := s.WorkspaceForSubject("sub_none"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	mkSubject := func(id string) {
		t.Helper()
		if err := s.CreateSubject(&pigtail.Subject{
			ID: id, ThemeID: themeID, Title: "S", Slug: id, Status: pigtail.StatusInbox,
			CreatedAt: time.UnixMilli(1).UTC(), UpdatedAt: time.UnixMilli(1).UTC(),
		}); err != nil {
			t.Fatalf("create subject %s: %v", id, err)
		}
	}

	// Dangling link: a phase whose workspace_id points to a row that does not
	// exist is skipped. As the only phase, the subject has no live worktree.
	t.Run("dangling link skipped", func(t *testing.T) {
		mkSubject("sub_dangling")
		if err := s.CreatePhase(&pigtail.SubjectPhase{ID: "ph_dangling", SubjectID: "sub_dangling", Kind: "implement", State: "active", WorkspaceID: "ws_ghost", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.WorkspaceForSubject("sub_dangling"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("want ErrNotFound for dangling link, got %v", err)
		}
	})

	// Terminal-only: a phase pointing to a shipped (terminal) workspace is
	// skipped — work is done, reuse would duplicate it.
	t.Run("terminal workspace skipped", func(t *testing.T) {
		mkSubject("sub_terminal")
		shipped := &pigtail.Workspace{ID: "ws_shipped", ProjectID: "prj_test", State: pigtail.StateShipped, Path: "/tmp/shipped", BackendKind: "git"}
		if err := s.CreateWorkspace(shipped); err != nil {
			t.Fatal(err)
		}
		if err := s.CreatePhase(&pigtail.SubjectPhase{ID: "ph_term", SubjectID: "sub_terminal", Kind: "implement", State: "done", WorkspaceID: shipped.ID, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.WorkspaceForSubject("sub_terminal"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("want ErrNotFound for terminal workspace, got %v", err)
		}
	})

	// Terminal then active: an earlier phase points to an archived (terminal)
	// workspace, a later phase to an active one → the active one is returned.
	t.Run("terminal phase then active phase", func(t *testing.T) {
		mkSubject("sub_mixed")
		archived := &pigtail.Workspace{ID: "ws_archived", ProjectID: "prj_test", State: pigtail.StateArchived, Path: "/tmp/archived", BackendKind: "git"}
		if err := s.CreateWorkspace(archived); err != nil {
			t.Fatal(err)
		}
		active := &pigtail.Workspace{ID: "ws_active", ProjectID: "prj_test", State: pigtail.StateBuilding, Path: "/tmp/active", BackendKind: "git"}
		if err := s.CreateWorkspace(active); err != nil {
			t.Fatal(err)
		}
		// Earlier phase (terminal) created first so it sorts ahead.
		if err := s.CreatePhase(&pigtail.SubjectPhase{ID: "ph_old", SubjectID: "sub_mixed", Kind: "research", State: "done", WorkspaceID: archived.ID, CreatedAt: time.UnixMilli(1)}); err != nil {
			t.Fatal(err)
		}
		if err := s.CreatePhase(&pigtail.SubjectPhase{ID: "ph_new", SubjectID: "sub_mixed", Kind: "implement", State: "active", WorkspaceID: active.ID, CreatedAt: time.UnixMilli(2)}); err != nil {
			t.Fatal(err)
		}
		got, err := s.WorkspaceForSubject("sub_mixed")
		if err != nil {
			t.Fatalf("want active ws, got err %v", err)
		}
		if got.ID != active.ID {
			t.Fatalf("got %s want %s", got.ID, active.ID)
		}
	})
}

func mkSubject(id, theme, status string, rank int) *pigtail.Subject {
	return &pigtail.Subject{
		ID: id, ThemeID: theme, Title: id, Slug: id, Status: status, Rank: rank,
		CreatedAt: time.UnixMilli(1).UTC(), UpdatedAt: time.UnixMilli(1).UTC(),
	}
}

func TestSubjectListOrderAndFilter(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	// ranks: a=2, b=1, c=0(unset) → expect b, a, then c (unset last).
	for _, sub := range []*pigtail.Subject{
		mkSubject("a", themeID, pigtail.StatusInbox, 2),
		mkSubject("b", themeID, pigtail.StatusBuilding, 1),
		mkSubject("c", themeID, pigtail.StatusInbox, 0),
	} {
		if err := s.CreateSubject(sub); err != nil {
			t.Fatalf("create %s: %v", sub.ID, err)
		}
	}

	all, err := s.ListSubjects(SubjectFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	gotOrder := []string{all[0].ID, all[1].ID, all[2].ID}
	if gotOrder[0] != "b" || gotOrder[1] != "a" || gotOrder[2] != "c" {
		t.Fatalf("order = %v, want [b a c]", gotOrder)
	}

	inbox, err := s.ListSubjects(SubjectFilter{Status: pigtail.StatusInbox})
	if err != nil || len(inbox) != 2 {
		t.Fatalf("status filter: %v len=%d", err, len(inbox))
	}
}

func TestSubjectUpdate(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	sub := mkSubject("a", themeID, pigtail.StatusInbox, 0)
	if err := s.CreateSubject(sub); err != nil {
		t.Fatalf("create: %v", err)
	}
	sub.Status = pigtail.StatusBuilding
	sub.Impact, sub.Effort, sub.Urgency, sub.Rank = 5, 1, 4, 3
	sub.Deps = []string{"sub_dep"}
	sub.UpdatedAt = time.UnixMilli(9999).UTC()
	if err := s.UpdateSubject(sub); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := s.GetSubject("a")
	if got.Status != pigtail.StatusBuilding || got.Impact != 5 || got.Rank != 3 {
		t.Fatalf("update not persisted: %+v", got)
	}
	if len(got.Deps) != 1 || got.Deps[0] != "sub_dep" {
		t.Fatalf("deps not persisted: %+v", got.Deps)
	}
	if !got.UpdatedAt.Equal(time.UnixMilli(9999).UTC()) {
		t.Fatalf("updated_at: %v", got.UpdatedAt)
	}
}

func TestSubjectPhases(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	if err := s.CreateSubject(mkSubject("a", themeID, pigtail.StatusInbox, 1)); err != nil {
		t.Fatalf("subject: %v", err)
	}
	ph := &pigtail.SubjectPhase{
		ID: "phs_1", SubjectID: "a", Kind: pigtail.PhaseResearch,
		State: pigtail.PhasePending, CreatedAt: time.UnixMilli(5).UTC(),
	}
	if err := s.CreatePhase(ph); err != nil {
		t.Fatalf("create phase: %v", err)
	}
	// Move to active and link a workspace.
	if err := s.UpdatePhase("phs_1", pigtail.PhaseActive, "ws_42"); err != nil {
		t.Fatalf("update phase: %v", err)
	}
	list, err := s.ListPhasesBySubject("a")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v len=%d", err, len(list))
	}
	if list[0].State != pigtail.PhaseActive || list[0].WorkspaceID != "ws_42" {
		t.Fatalf("phase not updated: %+v", list[0])
	}
}

func TestSubjectLog(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	if err := s.CreateSubject(mkSubject("a", themeID, pigtail.StatusInbox, 1)); err != nil {
		t.Fatalf("subject: %v", err)
	}
	entries := []*pigtail.SubjectLogEntry{
		{ID: "log_1", SubjectID: "a", Timestamp: time.UnixMilli(100).UTC(), Kind: pigtail.LogIdea, Text: "old idea"},
		{ID: "log_2", SubjectID: "a", Timestamp: time.UnixMilli(300).UTC(), Kind: pigtail.LogDelivery, Text: "shipped PR"},
	}
	for _, e := range entries {
		if err := s.AppendLog(e); err != nil {
			t.Fatalf("append %s: %v", e.ID, err)
		}
	}
	// All, newest first.
	all, err := s.ListLog("a", time.Time{})
	if err != nil || len(all) != 2 {
		t.Fatalf("list all: %v len=%d", err, len(all))
	}
	if all[0].ID != "log_2" {
		t.Fatalf("want newest first, got %s", all[0].ID)
	}
	// Since cutoff at 200ms → only log_2.
	recent, err := s.ListLog("a", time.UnixMilli(200).UTC())
	if err != nil || len(recent) != 1 || recent[0].ID != "log_2" {
		t.Fatalf("since filter: %v %+v", err, recent)
	}
}

func TestNextSubject(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)

	// blocker (rank 1) is not done → top-ranked "blocked" must be skipped.
	blocker := mkSubject("blocker", themeID, pigtail.StatusBuilding, 5)
	blocked := mkSubject("blocked", themeID, pigtail.StatusInbox, 1)
	blocked.Deps = []string{"blocker"}
	ready := mkSubject("ready", themeID, pigtail.StatusInbox, 2)
	for _, sub := range []*pigtail.Subject{blocker, blocked, ready} {
		if err := s.CreateSubject(sub); err != nil {
			t.Fatalf("create %s: %v", sub.ID, err)
		}
	}

	// "blocked" has the best rank but an unmet dep → expect "ready".
	got, err := s.NextSubject(scope.Scope("globex"))
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if got.ID != "ready" {
		t.Fatalf("next = %s, want ready", got.ID)
	}

	// Once the blocker is done, "blocked" (rank 1) wins.
	blocker.Status = pigtail.StatusDone
	if err := s.UpdateSubject(blocker); err != nil {
		t.Fatalf("update blocker: %v", err)
	}
	got, err = s.NextSubject(scope.Scope("globex"))
	if err != nil {
		t.Fatalf("next 2: %v", err)
	}
	if got.ID != "blocked" {
		t.Fatalf("next = %s, want blocked", got.ID)
	}
}

func TestNextSubjectSkipsDropped(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)

	dropped := mkSubject("dropped", themeID, pigtail.StatusDropped, 1)
	open := mkSubject("open", themeID, pigtail.StatusInbox, 2)
	for _, sub := range []*pigtail.Subject{dropped, open} {
		if err := s.CreateSubject(sub); err != nil {
			t.Fatalf("create %s: %v", sub.ID, err)
		}
	}
	got, err := s.NextSubject(scope.Scope("globex"))
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if got.ID != "open" {
		t.Fatalf("next = %s, want open (a dropped subject is closed)", got.ID)
	}
}

// TestNextSubjectDroppedDepUnblocks: a dropped dependency unblocks,
// like done and captured; a parked one still blocks.
func TestNextSubjectDroppedDepUnblocks(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)

	a := mkSubject("A", themeID, pigtail.StatusParked, 5)
	b := mkSubject("B", themeID, pigtail.StatusInbox, 1)
	b.Deps = []string{"A"}
	for _, sub := range []*pigtail.Subject{a, b} {
		if err := s.CreateSubject(sub); err != nil {
			t.Fatalf("create %s: %v", sub.ID, err)
		}
	}
	if got, err := s.NextSubject(scope.Scope("globex")); err == nil && got.ID == "B" {
		t.Fatalf("A parked: next = B, want B blocked")
	}
	for _, st := range []string{pigtail.StatusDropped, pigtail.StatusDone, pigtail.StatusCaptured} {
		a.Status = st
		if err := s.UpdateSubject(a); err != nil {
			t.Fatal(err)
		}
		got, err := s.NextSubject(scope.Scope("globex"))
		if err != nil || got.ID != "B" {
			t.Fatalf("A %s: next = %v, %v, want B", st, got, err)
		}
	}
}

func TestNextSubjectNone(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.NextSubject(scope.Scope("globex")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSubjectSourceRoundTripAndFilter(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)

	local := mkSubject("loc", themeID, pigtail.StatusInbox, 1)
	local.Source = pigtail.SourceLocal
	board := mkSubject("brd", themeID, pigtail.StatusInbox, 2)
	board.Source = pigtail.SourceBoard
	board.ShortID = "ABC-1124"
	board.BoardID = "019eb57a-9590-7857-9a0a-31928bee146c"
	board.Section = "019eb57e-2bd4-7a18-a754-f2a809ff5741"
	for _, sub := range []*pigtail.Subject{local, board} {
		if err := s.CreateSubject(sub); err != nil {
			t.Fatalf("create %s: %v", sub.ID, err)
		}
	}

	got, _ := s.GetSubject("brd")
	if got.Source != pigtail.SourceBoard || got.ShortID != "ABC-1124" || got.BoardID == "" || got.Section == "" {
		t.Fatalf("board fields not persisted: %+v", got)
	}

	locals, err := s.ListSubjects(SubjectFilter{Source: pigtail.SourceLocal})
	if err != nil || len(locals) != 1 || locals[0].ID != "loc" {
		t.Fatalf("source filter local: %v %+v", err, locals)
	}
	boards, _ := s.ListSubjects(SubjectFilter{Source: pigtail.SourceBoard})
	if len(boards) != 1 || boards[0].ID != "brd" {
		t.Fatalf("source filter board: %+v", boards)
	}
	all, _ := s.ListSubjects(SubjectFilter{})
	if len(all) != 2 {
		t.Fatalf("no-filter should return both, got %d", len(all))
	}
}

// TestSubjectBoardCardRoundTrip mirrors the CLI `set board_card=…` flow at the
// store level: create a plain (local) subject, attach a board card via
// read-modify-write, then re-read it. board_card is source-agnostic, so the
// subject stays source=local while carrying the link.
func TestSubjectBoardCardRoundTrip(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)

	sub := mkSubject("loc", themeID, pigtail.StatusInbox, 1)
	if err := s.CreateSubject(sub); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got, _ := s.GetSubject("loc"); got.BoardCard != "" {
		t.Fatalf("board_card should be empty on create, got %q", got.BoardCard)
	}

	sub.BoardCard = "ABC-1204"
	if err := s.UpdateSubject(sub); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := s.GetSubject("loc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.BoardCard != "ABC-1204" {
		t.Fatalf("board_card not persisted: %q", got.BoardCard)
	}
	if got.Source != pigtail.SourceLocal {
		t.Fatalf("source should stay local, got %q", got.Source)
	}
}

func TestSubjectSourceColumns(t *testing.T) {
	s := newTestStore(t)
	for _, col := range []string{"source", "short_id", "board_id", "section", "board_card"} {
		var c int
		// pragma table_info exposes each column; count the matching name.
		rows, err := s.db.Query(`SELECT name FROM pragma_table_info('subjects') WHERE name = ?`, col)
		if err != nil {
			t.Fatalf("pragma: %v", err)
		}
		for rows.Next() {
			c++
		}
		rows.Close()
		if c != 1 {
			t.Fatalf("subjects.%s missing (found %d)", col, c)
		}
	}
}

func TestUpdateWorkspacePorts(t *testing.T) {
	st, _ := Open(":memory:")
	defer st.Close()
	ws := &pigtail.Workspace{ID: "ws_p", ProjectID: "prj", Path: "/x", Ports: map[string]int{"dev": 3000}, State: pigtail.StateBuilding, BackendKind: "git"}
	if err := st.CreateWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	_, _ = st.GetWorkspace("ws_p") // warm the cache
	if err := st.UpdateWorkspacePorts("ws_p", map[string]int{"dev": 3013}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetWorkspace("ws_p")
	if got.Ports["dev"] != 3013 {
		t.Fatalf("ports %v (stale cache?)", got.Ports)
	}
	used, _ := st.UsedPorts("dev")
	if !used[3013] || used[3000] {
		t.Fatalf("UsedPorts %v", used)
	}
	if err := st.UpdateWorkspacePorts("ws_missing", map[string]int{"dev": 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err %v, want ErrNotFound", err)
	}
}

func TestThemeSpaceRoundTrip(t *testing.T) {
	s := newTestStore(t)
	th := &pigtail.Theme{ID: "thm_s", Name: "Chat", Kind: "project", VaultLink: "Perso/Projects/chat", Space: "Terrace", CreatedAt: time.Now()}
	if err := s.CreateTheme(th); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTheme("thm_s")
	if got.Space != "Terrace" {
		t.Fatalf("space = %q", got.Space)
	}
	empty := ""
	if err := s.UpdateTheme("thm_s", pigtail.ThemeUpdate{Space: &empty}); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetTheme("thm_s"); got.Space != "" {
		t.Fatalf("space not cleared: %q", got.Space)
	}
}

// statusLogLines returns the subject's status log entries, newest first.
func statusLogLines(t *testing.T, s *Store, id string) []pigtail.SubjectLogEntry {
	t.Helper()
	logs, err := s.ListLog(id, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var out []pigtail.SubjectLogEntry
	for _, l := range logs {
		if l.Kind == pigtail.LogStatus {
			out = append(out, l)
		}
	}
	return out
}

// TestUpdateSubjectLogsStatusChange: a status change writes one status
// line "<old> → <new>", stamped with the update's UpdatedAt.
func TestUpdateSubjectLogsStatusChange(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	sub := mkSubject("sub_a", themeID, pigtail.StatusUnderstanding, 1)
	if err := s.CreateSubject(sub); err != nil {
		t.Fatal(err)
	}
	sub.Status = pigtail.StatusDesigning
	sub.UpdatedAt = time.UnixMilli(5000).UTC()
	if err := s.UpdateSubject(sub); err != nil {
		t.Fatal(err)
	}
	logs := statusLogLines(t, s, "sub_a")
	if len(logs) != 1 || logs[0].Text != "understanding → designing" {
		t.Fatalf("status lines = %+v, want one \"understanding → designing\"", logs)
	}
	if !logs[0].Timestamp.Equal(sub.UpdatedAt) {
		t.Fatalf("ts = %v, want UpdatedAt %v", logs[0].Timestamp, sub.UpdatedAt)
	}
	if !strings.HasPrefix(logs[0].ID, pigtail.PrefixLog) {
		t.Fatalf("log id = %q, want %s prefix", logs[0].ID, pigtail.PrefixLog)
	}
}

// TestUpdateSubjectSameStatusNoLog: rewriting the same
// status, or changing only the rank, writes no status line.
func TestUpdateSubjectSameStatusNoLog(t *testing.T) {
	s := newTestStore(t)
	themeID := seedTheme(t, s)
	sub := mkSubject("sub_a", themeID, pigtail.StatusBuilding, 1)
	if err := s.CreateSubject(sub); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSubject(sub); err != nil {
		t.Fatal(err)
	}
	sub.Rank = 7
	if err := s.UpdateSubject(sub); err != nil {
		t.Fatal(err)
	}
	if logs := statusLogLines(t, s, "sub_a"); len(logs) != 0 {
		t.Fatalf("status lines = %+v, want none", logs)
	}
	if err := s.UpdateSubject(mkSubject("sub_missing", themeID, pigtail.StatusBuilding, 1)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of an unknown subject = %v, want ErrNotFound", err)
	}
}

func TestThemeDeadlineCarriedRoundTrip(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateTheme(&pigtail.Theme{ID: "thm_d", Name: "D", Kind: "project", VaultLink: "Perso/Projects/d",
		Space: "Allotment", Deadline: "2026-10-31", Carried: true, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTheme("thm_d")
	if err != nil || got.Deadline != "2026-10-31" || !got.Carried {
		t.Fatalf("get = %+v, %v", got, err)
	}
	list, err := s.ListThemes(scope.Any)
	if err != nil || len(list) != 1 || list[0].Deadline != "2026-10-31" || !list[0].Carried {
		t.Fatalf("list = %+v, %v", list, err)
	}
	empty := ""
	if err := s.UpdateTheme("thm_d", pigtail.ThemeUpdate{Deadline: &empty}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTheme("thm_d")
	if got.Deadline != "" || !got.Carried || got.Name != "D" || got.Space != "Allotment" {
		t.Fatalf("after clear = %+v", got)
	}
	f := false
	if err := s.UpdateTheme("thm_d", pigtail.ThemeUpdate{Carried: &f}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTheme("thm_d")
	if got.Carried || got.Deadline != "" {
		t.Fatalf("after carried=false = %+v", got)
	}
	if err := s.UpdateTheme("thm_nope", pigtail.ThemeUpdate{Carried: &f}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing theme: %v", err)
	}
}

// A fresh store under the neutral builtin table: a theme of scope personal
// is accepted, and a theme with no scope and any relative link takes it.
func TestFreshStoreAcceptsBuiltinScope(t *testing.T) {
	t.Cleanup(scope.Use(scope.Builtin()))
	s := newTestStore(t)
	now := time.Now()
	if err := s.CreateTheme(&pigtail.Theme{ID: "thm_p", Name: "P", Kind: "project", VaultLink: "Notes/p", Scope: "personal", CreatedAt: now}); err != nil {
		t.Fatalf("theme of scope personal: %v", err)
	}
	th := &pigtail.Theme{ID: "thm_q", Name: "Q", Kind: "project", VaultLink: "Projects/q", CreatedAt: now}
	if err := s.CreateTheme(th); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTheme("thm_q")
	if err != nil || got.Scope != "personal" {
		t.Fatalf("theme with no scope: %+v, %v; want scope personal", got, err)
	}
}
