package store

import (
	"path/filepath"
	"testing"
)

// 0023: a workspace created before the migration reads Hooks == nil (unknown),
// never a failure; a status outside running|ok|failed is refused by the CHECK.
func TestMigration0023WorkspaceHooksUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-0023.db")
	db := migrateBefore(t, path, "0023")
	if _, err := db.Exec(`INSERT INTO projects (id, name, root_path, main_worktree, default_backend, port_ranges)
		VALUES ('prj_o','o','/tmp/o','main','git','{}')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, project_id, intent, state, path, ports, backend_kind, created_at, last_activity_at)
		VALUES ('ws_old','prj_o','old','building','/tmp/o/w','{}','git',1,1)`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	_ = db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open (applies 0023): %v", err)
	}
	defer s.Close()
	ws, err := s.GetWorkspace("ws_old")
	if err != nil {
		t.Fatal(err)
	}
	if ws.Hooks != nil {
		t.Fatalf("hooks after 0023 = %+v, want nil (unknown)", ws.Hooks)
	}
	list, err := s.ListWorkspaces("")
	if err != nil || len(list) != 1 || list[0].Hooks != nil {
		t.Fatalf("list = %+v, %v; want one workspace, hooks nil", list, err)
	}
	if _, err := s.db.Exec(`UPDATE workspaces SET hooks_status = 'broken' WHERE id = 'ws_old'`); err == nil {
		t.Fatal("an unknown hooks_status was accepted")
	}
}
