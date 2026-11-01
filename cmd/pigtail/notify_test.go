package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// TestWorkspaceIDForDirDistinguishesHomonymousWorktrees is the regression test
// for the workspace resolver: it used to match on filepath.Base(ws.Path) over
// the daemon's GLOBAL workspace list. Since `main` is the main worktree of
// every pigtail project, two registered projects (the ordinary case) made the
// loop return the first project's `main` for a stop made in the second's.
// Consequence: the deliberate stop was marked on the wrong workspace, so the
// server the user just stopped came back as a ✝ death on the next sweep —
// a frontal violation of "a deliberate `pigtail stop` never shows up as a
// death". On the pre-fix code this test fails: it gets ws_a for projectB/main.
func TestWorkspaceIDForDirDistinguishesHomonymousWorktrees(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	projA := filepath.Join(root, "projectA", "main")
	projB := filepath.Join(root, "projectB", "main")
	for _, d := range []string{projA, projB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wss := []pigtail.Workspace{
		{ID: "ws_a", Path: projA},
		{ID: "ws_b", Path: projB},
	}

	cases := []struct {
		name string
		dir  string
		want string
	}{
		{"first project's main", projA, "ws_a"},
		{"second project's homonymous main", projB, "ws_b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := workspaceIDForDir(wss, tc.dir)
			if !ok {
				t.Fatalf("workspaceIDForDir(%q) found nothing", tc.dir)
			}
			if got != tc.want {
				t.Fatalf("workspaceIDForDir(%q) = %q, want %q", tc.dir, got, tc.want)
			}
		})
	}
}

// TestWorkspaceIDForDirNoMatchIsSilent: a worktree the daemon does not know
// (never registered, or already removed) must resolve to nothing rather than
// to an arbitrary neighbour. Marking a stop on the wrong workspace is worse
// than not marking it at all — the cost of silence is one false death, the
// cost of a wrong mark is a real death swallowed.
func TestWorkspaceIDForDirNoMatchIsSilent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	known := filepath.Join(root, "projectA", "main")
	if err := os.MkdirAll(known, 0o755); err != nil {
		t.Fatal(err)
	}
	wss := []pigtail.Workspace{{ID: "ws_a", Path: known}}

	if id, ok := workspaceIDForDir(wss, filepath.Join(root, "projectZ", "main")); ok {
		t.Fatalf("an unknown worktree must not resolve, got %q", id)
	}
	if id, ok := workspaceIDForDir(nil, known); ok {
		t.Fatalf("an empty workspace list must not resolve, got %q", id)
	}
}

// TestWorkspaceIDForDirIgnoresPathSpelling: the daemon stores the path it was
// given and the CLI derives one from the cwd, so the two can differ by a
// trailing slash, a "." segment, or a symlinked prefix (/tmp → /private/tmp on
// macOS) while naming the same directory. Comparing the raw strings would miss
// the match and silently degrade to "no mark" — a false death per stop.
func TestWorkspaceIDForDirIgnoresPathSpelling(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "projectA", "main")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	wss := []pigtail.Workspace{{ID: "ws_a", Path: dir + string(filepath.Separator)}}

	got, ok := workspaceIDForDir(wss, filepath.Join(dir, "."))
	if !ok || got != "ws_a" {
		t.Fatalf("workspaceIDForDir = (%q, %v), want (ws_a, true)", got, ok)
	}
}
