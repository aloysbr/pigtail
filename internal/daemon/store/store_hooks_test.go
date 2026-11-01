package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// TestSetWorkspaceHooksRoundTripAndCache: Get caches the pointer
// (cacheWorkspace); a Set that left the cache alone would serve "running"
// after the hooks ended.
func TestSetWorkspaceHooksRoundTripAndCache(t *testing.T) {
	s := newTestStore(t)
	seedWorkspace(t, s, "ws_h")                       // projet + workspace minimal, même forme que les tests voisins
	if _, err := s.GetWorkspace("ws_h"); err != nil { // warm the cache
		t.Fatal(err)
	}
	at := time.UnixMilli(1_700_000_000_000).UTC()
	if err := s.SetWorkspaceHooks("ws_h", pigtail.HooksResult{Status: pigtail.HooksRunning, Log: "/l/hooks.log", At: at}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkspaceHooks("ws_h", pigtail.HooksResult{Status: pigtail.HooksFailed, Hook: "husky", Error: "hook husky failed: exit status 3", Log: "/l/hooks.log", At: at}); err != nil {
		t.Fatal(err)
	}
	ws, err := s.GetWorkspace("ws_h")
	if err != nil {
		t.Fatal(err)
	}
	want := pigtail.HooksResult{Status: pigtail.HooksFailed, Hook: "husky", Error: "hook husky failed: exit status 3", Log: "/l/hooks.log", At: at}
	if ws.Hooks == nil || *ws.Hooks != want {
		t.Fatalf("hooks = %+v, want %+v", ws.Hooks, want)
	}
	if err := s.SetWorkspaceHooks("ws_missing", want); !errors.Is(err, ErrNotFound) {
		t.Fatalf("set on a missing workspace = %v, want ErrNotFound", err)
	}
}

// TestFailRunningHooks: a daemon that stopped mid-hook left "running" behind;
// the next start turns it into a failure that says so, and touches nothing else.
func TestFailRunningHooks(t *testing.T) {
	s := newTestStore(t)
	seedWorkspace(t, s, "ws_run")
	seedWorkspace(t, s, "ws_ok")
	_ = s.SetWorkspaceHooks("ws_run", pigtail.HooksResult{Status: pigtail.HooksRunning, Log: "/l", At: time.Now()})
	_ = s.SetWorkspaceHooks("ws_ok", pigtail.HooksResult{Status: pigtail.HooksOK, Log: "/l", At: time.Now()})
	n, err := s.FailRunningHooks("interrupted: the daemon stopped before the hooks ended")
	if err != nil || n != 1 {
		t.Fatalf("FailRunningHooks = %d, %v; want 1", n, err)
	}
	run, _ := s.GetWorkspace("ws_run")
	ok, _ := s.GetWorkspace("ws_ok")
	if run.Hooks.Status != pigtail.HooksFailed || !strings.Contains(run.Hooks.Error, "daemon stopped") {
		t.Fatalf("ws_run hooks = %+v", run.Hooks)
	}
	if ok.Hooks.Status != pigtail.HooksOK {
		t.Fatalf("ws_ok hooks = %+v, want untouched", ok.Hooks)
	}
}

// seedWorkspace records a minimal project (once) and a workspace id in it,
// hooks unknown (NULL), the shape of the neighbouring store tests.
func seedWorkspace(t *testing.T, s *Store, id string) {
	t.Helper()
	if _, err := s.GetProject("prj_hk"); err != nil {
		if err := s.CreateProject(&pigtail.Project{ID: "prj_hk", Name: "hk", RootPath: "/tmp/hk", DefaultBackend: "git"}); err != nil {
			t.Fatalf("create project: %v", err)
		}
	}
	now := time.Now().UTC()
	if err := s.CreateWorkspace(&pigtail.Workspace{ID: id, ProjectID: "prj_hk", Intent: "x", State: pigtail.StateBuilding,
		Path: "/tmp/hk/" + id, BackendKind: "git", CreatedAt: now, LastActivityAt: now}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
}

// TestClaimWorkspaceHooks: a rerun claims the record in one conditional
// UPDATE, so two runs of the same workspace never overlap.
func TestClaimWorkspaceHooks(t *testing.T) {
	s := newTestStore(t)
	seedWorkspace(t, s, "ws_c")
	_ = s.SetWorkspaceHooks("ws_c", pigtail.HooksResult{Status: pigtail.HooksFailed, Hook: "husky", Error: "exit 3", Log: "/l"})
	if _, err := s.GetWorkspace("ws_c"); err != nil { // warm the cache
		t.Fatal(err)
	}
	if err := s.ClaimWorkspaceHooks("ws_c", "/l"); err != nil {
		t.Fatalf("claim on a failed record = %v", err)
	}
	ws, _ := s.GetWorkspace("ws_c")
	if ws.Hooks == nil || ws.Hooks.Status != pigtail.HooksRunning || ws.Hooks.Hook != "" || ws.Hooks.Error != "" {
		t.Fatalf("hooks after claim = %+v, want running, hook and error cleared", ws.Hooks)
	}
	if err := s.ClaimWorkspaceHooks("ws_c", "/l"); !errors.Is(err, ErrHooksRunning) {
		t.Fatalf("second claim = %v, want ErrHooksRunning", err)
	}
	if err := s.ClaimWorkspaceHooks("ws_missing", "/l"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claim on a missing workspace = %v, want ErrNotFound", err)
	}
	seedWorkspace(t, s, "ws_unknown") // before migration 0023: hooks NULL
	if err := s.ClaimWorkspaceHooks("ws_unknown", "/l"); err != nil {
		t.Fatalf("claim on an unknown record = %v", err)
	}
}
