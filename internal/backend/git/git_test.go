package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/envlocal"
	"github.com/aloysbr/pigtail/internal/ports"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Refactor auth flow":             "refactor-auth-flow",
		"Fix Date Picker LOCALE bug 123": "fix-date-picker-locale",
		"  hello   world ":               "hello-world",
		"":                               "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDedupePrefix(t *testing.T) {
	cases := []struct{ slug, prefix, want string }{
		{"feat-circle-detail", "feat/", "circle-detail"},
		{"circle-detail", "feat/", "circle-detail"},
		{"fix-bug", "feat/", "fix-bug"}, // different type word preserved
		{"feat", "feat/", ""},
		{"feature-x", "feat/", "feature-x"}, // not an exact type segment
		{"feat-x", "", "feat-x"},           // empty prefix returns slug unchanged
	}
	for _, c := range cases {
		if got := dedupePrefix(c.slug, c.prefix); got != c.want {
			t.Errorf("dedupePrefix(%q,%q)=%q want %q", c.slug, c.prefix, got, c.want)
		}
	}
}

func TestWorktreePath(t *testing.T) {
	got := WorktreePathFor(&pigtail.Project{RootPath: "/Users/x/dev/proj"}, "feat/auth-refactor")
	want := "/Users/x/dev/proj/feat-auth-refactor"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

type nilUsed struct{}

func (nilUsed) UsedPorts(_ string) (map[int]bool, error) { return nil, nil }

func TestCreateEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Container directory with main worktree inside.
	container := t.TempDir()
	mainDir := mustInitRepo(t, container)

	alloc := ports.New(nilUsed{})
	b := New(alloc)

	ws, err := b.Create(context.Background(),
		&pigtail.Project{
			ID:             "prj_test",
			RootPath:       container,
			MainWorktree:   "main",
			DefaultBackend: "git",
			PortRanges: map[string]pigtail.PortRange{
				"dev":       {38600, 38699},
				"storybook": {38700, 38799},
			},
		},
		backend.CreateIntent{Intent: "say hello", BaseRef: "main"},
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Worktree should be a child of the container.
	want := filepath.Join(container, "feat-say-hello")
	if ws.Path != want {
		t.Errorf("ws.Path = %q, want %q", ws.Path, want)
	}
	if _, err := os.Stat(filepath.Join(ws.Path, ".env.local")); err != nil {
		t.Errorf(".env.local missing: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(ws.Path, ".env.local"))
	s := string(body)
	if !strings.Contains(s, "PORT=") || !strings.Contains(s, "STORYBOOK_PORT=") {
		t.Errorf(".env.local content unexpected: %q", s)
	}
	if ws.BackendData["branch"] != "feat/say-hello" {
		t.Errorf("branch in backend_data: %v", ws.BackendData)
	}

	// Branch should exist in the main worktree.
	cmd := exec.Command("git", "branch", "--list", "feat/say-hello")
	cmd.Dir = mainDir
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "feat/say-hello") {
		t.Errorf("branch not created: %s", out)
	}
}

// mustInitRepo creates a "main" worktree inside container, runs `git init` and
// an empty initial commit in it, and returns the main worktree path.
func mustInitRepo(t *testing.T, container string) string {
	t.Helper()
	mainDir := filepath.Join(container, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = mainDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return mainDir
}

// TestCreateDedupesTypePrefix proves an intent that already starts with the
// type word ("feat say hello") does not yield a doubled branch prefix
// (feat/feat-…) nor a doubled worktree dir (feat-feat-…).
func TestCreateDedupesTypePrefix(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mustInitRepo(t, container)

	alloc := ports.New(nilUsed{})
	b := New(alloc)

	ws, err := b.Create(context.Background(),
		&pigtail.Project{
			ID:             "prj_dedupe",
			RootPath:       container,
			MainWorktree:   "main",
			DefaultBackend: "git",
			PortRanges: map[string]pigtail.PortRange{
				"dev":       {38600, 38699},
				"storybook": {38700, 38799},
			},
		},
		backend.CreateIntent{Intent: "feat say hello", BaseRef: "main"},
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if ws.BackendData["branch"] != "feat/say-hello" {
		t.Errorf("branch = %v, want feat/say-hello", ws.BackendData["branch"])
	}
	if filepath.Base(ws.Path) != "feat-say-hello" {
		t.Errorf("ws.Path = %q, want dir ending in feat-say-hello", ws.Path)
	}
}

// TestCreateRejectsCollision proves that creating a second workspace whose
// derived branch/worktree already exists fails loud with an ErrConflict
// APIError instead of a raw `git worktree add` error.
func TestCreateRejectsCollision(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mustInitRepo(t, container)
	b := New(ports.New(nilUsed{}))
	proj := &pigtail.Project{
		ID: "prj_test", RootPath: container, MainWorktree: "main", DefaultBackend: "git",
		PortRanges: map[string]pigtail.PortRange{"dev": {38600, 38699}, "storybook": {38700, 38799}},
	}
	if _, err := b.Create(context.Background(), proj, backend.CreateIntent{Intent: "say hello", BaseRef: "main"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := b.Create(context.Background(), proj, backend.CreateIntent{Intent: "say hello", BaseRef: "main"})
	if err == nil {
		t.Fatal("expected conflict on second create, got nil")
	}
	var ae *pigtail.APIError
	if !errors.As(err, &ae) || ae.Code != pigtail.ErrConflict {
		t.Fatalf("expected ErrConflict APIError, got %v", err)
	}
}

// TestCreateRejectsDirCollision proves the dir-exists arm of the collision
// guard fires independently of the branch-exists arm: the derived worktree
// DIRECTORY already exists but no matching git branch does, so the branch check
// passes and the os.Stat(dir) check must reject with an ErrConflict APIError.
func TestCreateRejectsDirCollision(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mustInitRepo(t, container)

	// Pre-create the worktree directory that intent "say hello" maps to
	// (feat/say-hello → feat-say-hello), but DO NOT create the branch.
	if err := os.MkdirAll(filepath.Join(container, "feat-say-hello"), 0o755); err != nil {
		t.Fatal(err)
	}

	b := New(ports.New(nilUsed{}))
	proj := &pigtail.Project{
		ID: "prj_dircol", RootPath: container, MainWorktree: "main", DefaultBackend: "git",
		PortRanges: map[string]pigtail.PortRange{"dev": {38600, 38699}, "storybook": {38700, 38799}},
	}
	_, err := b.Create(context.Background(), proj, backend.CreateIntent{Intent: "say hello", BaseRef: "main"})
	if err == nil {
		t.Fatal("expected conflict on dir collision, got nil")
	}
	var ae *pigtail.APIError
	if !errors.As(err, &ae) || ae.Code != pigtail.ErrConflict {
		t.Fatalf("expected ErrConflict APIError, got %v", err)
	}
}

func TestCleanupKeepsUnmergedBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root, ws := scratchRepoAndWorkspace(t, "test cleanup", []string{"feat/test-cleanup"})

	// Make the branch actually diverge from main with a committed change.
	if err := os.WriteFile(filepath.Join(ws.Path, "work.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", ws.Path, "add", "work.txt"},
		{"-C", ws.Path, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "work"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	b := New(nil)
	if _, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{}); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// Worktree gone
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Errorf("worktree still exists post-cleanup: %v", err)
	}

	// Unmerged branch should still exist (work would be lost otherwise)
	cmd := exec.Command("git", "branch", "--list", "feat/test-cleanup")
	cmd.Dir = root
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "feat/test-cleanup") {
		t.Errorf("unmerged branch should be kept by default, got: %q", string(out))
	}
}

func TestCleanupRefusesDirtyWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	_, ws := scratchRepoAndWorkspace(t, "dirty", []string{"feat/dirty"})

	// Make the worktree dirty
	if err := os.WriteFile(filepath.Join(ws.Path, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", ws.Path, "add", "dirty.txt").Run(); err != nil {
		t.Fatal(err)
	}

	b := New(nil)
	_, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{})
	if err == nil {
		t.Fatal("expected cleanup to refuse dirty worktree")
	}
	var apiErr *pigtail.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != pigtail.ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	// --force should succeed
	if _, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{Force: true}); err != nil {
		t.Fatalf("force cleanup: %v", err)
	}
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Errorf("worktree should be gone after force cleanup")
	}
}

func TestCleanupDeletesMergedBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root, ws := scratchRepoAndWorkspace(t, "merged", []string{"feat/merged"})

	// Make a commit in the worktree and merge it back to main
	if err := os.WriteFile(filepath.Join(ws.Path, "new.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", ws.Path, "add", "new.txt"},
		{"-C", ws.Path, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "add"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	// Merge the branch into main from the root repo
	for _, args := range [][]string{
		{"-C", root, "-c", "user.email=t@t", "-c", "user.name=t", "merge", "--no-ff", "-m", "merge", "feat/merged"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	b := New(nil)
	if _, err := b.Cleanup(context.Background(), ws, backend.CleanupOptions{}); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	out, _ := exec.Command("git", "-C", root, "branch", "--list", "feat/merged").CombinedOutput()
	if strings.Contains(string(out), "feat/merged") {
		t.Errorf("merged branch should be deleted, got: %s", out)
	}
}

// TestCreateHonorsPortEnvsAndDefaults proves the non-Node iOS-shaped
// pigtail.yaml from the task spec produces the right .env.local var name
// and the right branch prefix.
func TestCreateHonorsPortEnvsAndDefaults(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir := filepath.Join(container, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = mainDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	alloc := ports.New(nilUsed{})
	b := New(alloc)

	ws, err := b.Create(context.Background(),
		&pigtail.Project{
			ID:             "prj_ios",
			RootPath:       container,
			MainWorktree:   "main",
			DefaultBackend: "git",
			PortRanges:     map[string]pigtail.PortRange{"simulator": {39200, 39299}},
			PortEnvs:       map[string]string{"simulator": "PIGTAIL_SIM_PORT"},
			Defaults: &pigtail.ProjectDefaults{
				Agent:        "claude",
				BranchPrefix: "research/",
			},
		},
		backend.CreateIntent{Intent: "improve haptics", BaseRef: "main"},
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	branch, _ := ws.BackendData["branch"].(string)
	if !strings.HasPrefix(branch, "research/") {
		t.Errorf("branch should use Defaults.BranchPrefix, got %q", branch)
	}
	body, err := os.ReadFile(filepath.Join(ws.Path, ".env.local"))
	if err != nil {
		t.Fatalf(".env.local: %v", err)
	}
	if !strings.Contains(string(body), "PIGTAIL_SIM_PORT=") {
		t.Errorf(".env.local missing PIGTAIL_SIM_PORT, got %q", string(body))
	}
	if strings.Contains(string(body), "SIMULATOR_PORT=") {
		t.Errorf(".env.local should not fall back to UPPER_PORT when override is set, got %q", string(body))
	}
}

// TestCreateWritesPigtailOwner proves that `pigtail new` (Backend.Create)
// stamps the new worktree's .env.local with PIGTAIL_OWNER=<worktree name>,
// so an agent session — whose invocations don't share a stable POSIX
// session, see internal/devserver/owner.go — resolves a stable identity
// from the worktree it is in rather than a per-invocation session that dies
// within a second.
func TestCreateWritesPigtailOwner(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir := filepath.Join(container, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = mainDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	alloc := ports.New(nilUsed{})
	b := New(alloc)

	ws, err := b.Create(context.Background(),
		&pigtail.Project{
			ID:             "prj_owner",
			RootPath:       container,
			MainWorktree:   "main",
			DefaultBackend: "git",
			PortRanges:     map[string]pigtail.PortRange{"dev": {39300, 39399}},
		},
		backend.CreateIntent{Intent: "carry an owner", BaseRef: "main"},
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(ws.Path, ".env.local"))
	if err != nil {
		t.Fatalf(".env.local: %v", err)
	}
	wantOwner := "PIGTAIL_OWNER=" + filepath.Base(ws.Path)
	if !strings.Contains(string(body), wantOwner) {
		t.Errorf(".env.local missing %q, got: %q", wantOwner, string(body))
	}
}

// TestCreateInheritsParentEnvLocal proves that a worktree created via
// `pigtail dispatch` inherits the main worktree's .env.local content, with
// port lines rewritten to match the newly allocated ports.
func TestCreateInheritsParentEnvLocal(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir := filepath.Join(container, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = mainDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}

	parentEnv := "" +
		"# graphql endpoint — required\n" +
		"NEXT_PUBLIC_GRAPHQL_ENDPOINT=https://api.example.com/graphql\n" +
		"PORT=3000\n" +
		"# STORYBOOK_PORT=6006\n" +
		"FEATURE_FLAG_FOO=true\n" +
		"PIGTAIL_OWNER=main\n"
	if err := os.WriteFile(filepath.Join(mainDir, ".env.local"), []byte(parentEnv), 0o644); err != nil {
		t.Fatal(err)
	}

	alloc := ports.New(nilUsed{})
	b := New(alloc)
	ws, err := b.Create(context.Background(),
		&pigtail.Project{
			ID:             "prj_inherit",
			RootPath:       container,
			MainWorktree:   "main",
			DefaultBackend: "git",
			PortRanges: map[string]pigtail.PortRange{
				"dev":       {38600, 38699},
				"storybook": {38700, 38799},
			},
		},
		backend.CreateIntent{Intent: "inherit env", BaseRef: "main"},
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(ws.Path, ".env.local"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(got)

	if !strings.Contains(s, "NEXT_PUBLIC_GRAPHQL_ENDPOINT=https://api.example.com/graphql") {
		t.Errorf("parent var dropped, got: %q", s)
	}
	if !strings.Contains(s, "FEATURE_FLAG_FOO=true") {
		t.Errorf("parent var dropped, got: %q", s)
	}
	if !strings.Contains(s, "# graphql endpoint — required") {
		t.Errorf("parent comment dropped, got: %q", s)
	}
	// Commented STORYBOOK_PORT line in parent must be preserved verbatim;
	// the real assignment is appended at the end.
	if !strings.Contains(s, "# STORYBOOK_PORT=6006") {
		t.Errorf("commented line should be preserved, got: %q", s)
	}
	if strings.Contains(s, "PORT=3000") {
		t.Errorf("PORT should be rewritten away from parent value 3000, got: %q", s)
	}
	devPort := ws.Ports["dev"]
	if !strings.Contains(s, fmt.Sprintf("PORT=%d", devPort)) {
		t.Errorf("PORT should be %d, got: %q", devPort, s)
	}
	sbPort := ws.Ports["storybook"]
	if !strings.Contains(s, fmt.Sprintf("STORYBOOK_PORT=%d", sbPort)) {
		t.Errorf("STORYBOOK_PORT should be %d, got: %q", sbPort, s)
	}
	// The child worktree must NOT inherit the parent's PIGTAIL_OWNER
	// verbatim — without mergeEnvLocal rewriting it, a new worktree would
	// silently claim its parent's identity, and OwnerID would read it back
	// as if this were the parent's own session.
	if strings.Contains(s, "PIGTAIL_OWNER=main") {
		t.Errorf("PIGTAIL_OWNER should be rewritten away from the parent's value, got: %q", s)
	}
	wantOwner := "PIGTAIL_OWNER=" + filepath.Base(ws.Path)
	if !strings.Contains(s, wantOwner) {
		t.Errorf("expected %q, got: %q", wantOwner, s)
	}
	// PORT line should be in its original position, not duplicated.
	if strings.Count(s, "\nPORT=") != 1 && !strings.HasPrefix(s, "PORT=") {
		t.Errorf("PORT should appear exactly once, got: %q", s)
	}
	// BROWSER=none is stamped even though the parent never mentioned it: a
	// server started by `pigtail dev` must not pop a tab open.
	if !strings.Contains(s, "BROWSER=none") {
		t.Errorf("expected BROWSER=none to be stamped, got: %q", s)
	}
}

// TestCreateKeepsParentBrowserOverride pins the one way BROWSER differs from
// PIGTAIL_OWNER: it is a default, not an identity. A parent that deliberately
// names a browser must be inherited verbatim, not silently reset to "none" on
// every `pigtail new`.
func TestCreateKeepsParentBrowserOverride(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir := filepath.Join(container, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = mainDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(mainDir, ".env.local"),
		[]byte("PORT=3000\nBROWSER=firefox\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	alloc := ports.New(nilUsed{})
	b := New(alloc)
	ws, err := b.Create(context.Background(),
		&pigtail.Project{
			ID:             "prj_browser",
			RootPath:       container,
			MainWorktree:   "main",
			DefaultBackend: "git",
			PortRanges: map[string]pigtail.PortRange{
				"dev": {38800, 38899},
			},
		},
		backend.CreateIntent{Intent: "keep browser", BaseRef: "main"},
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(ws.Path, ".env.local"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, "BROWSER=firefox") {
		t.Errorf("parent BROWSER override should survive, got: %q", s)
	}
	if strings.Contains(s, "BROWSER=none") {
		t.Errorf("BROWSER should not be reset to none, got: %q", s)
	}
}

// TestMergeEnvLocal exercises the merge logic in isolation.
func TestMergeEnvLocal(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		assigns  []envAssign
		want     string
	}{
		{
			name:     "empty existing appends ports",
			existing: "",
			assigns:  []envAssign{{"PORT", "3001"}, {"STORYBOOK_PORT", "6007"}},
			want:     "PORT=3001\nSTORYBOOK_PORT=6007\n",
		},
		{
			name:     "rewrites existing PORT line in place",
			existing: "PORT=3000\nFOO=bar\n",
			assigns:  []envAssign{{"PORT", "3005"}},
			want:     "PORT=3005\nFOO=bar\n",
		},
		{
			name:     "preserves comments and blank lines",
			existing: "# top comment\n\nFOO=bar\n# inline\nPORT=3000\n",
			assigns:  []envAssign{{"PORT", "3010"}},
			want:     "# top comment\n\nFOO=bar\n# inline\nPORT=3010\n",
		},
		{
			name:     "commented PORT not rewritten, new PORT appended",
			existing: "# PORT=3000\nFOO=bar\n",
			assigns:  []envAssign{{"PORT", "3010"}},
			want:     "# PORT=3000\nFOO=bar\nPORT=3010\n",
		},
		{
			name:     "appends missing assignment with trailing newline",
			existing: "FOO=bar",
			assigns:  []envAssign{{"PORT", "3010"}},
			want:     "FOO=bar\nPORT=3010\n",
		},
		{
			name:     "no double rewrite if same name appears twice",
			existing: "PORT=3000\nPORT=3001\n",
			assigns:  []envAssign{{"PORT", "3010"}},
			want:     "PORT=3010\nPORT=3001\n",
		},
		{
			// envAssign.val is a string precisely so a non-numeric value
			// like a worktree name can be written — PIGTAIL_OWNER is the
			// motivating case, not a port.
			name:     "writes a non-numeric assignment",
			existing: "",
			assigns:  []envAssign{{"PIGTAIL_OWNER", "feat-x"}},
			want:     "PIGTAIL_OWNER=feat-x\n",
		},
		{
			// A worktree's .env.local is seeded from its parent's verbatim
			// (writeEnvLocal), so without this rewrite a new worktree would
			// inherit the PARENT's PIGTAIL_OWNER and silently claim its
			// identity. mergeEnvLocal is what corrects it.
			name:     "rewrites an inherited PIGTAIL_OWNER rather than keeping the parent's",
			existing: "PIGTAIL_OWNER=main\nPORT=3000\n",
			assigns:  []envAssign{{"PIGTAIL_OWNER", "feat-x"}, {"PORT", "3010"}},
			want:     "PIGTAIL_OWNER=feat-x\nPORT=3010\n",
		},
		{
			// A parent .env.local written by a shell user uses `export`, a form
			// the old name-matching did not recognise: the line was left alone
			// and a second, contradictory assignment was appended. The shell
			// would then apply the LAST one, so the worktree got its own port —
			// but the file now carries two PORT lines, and a reader taking the
			// first (as ReadPort used to) resolves the PARENT's port.
			name:     "rewrites an exported assignment in place",
			existing: "export PORT=3000\nFOO=bar\n",
			assigns:  []envAssign{{"PORT", "3010"}},
			want:     "export PORT=3010\nFOO=bar\n",
		},
		{
			// Rewriting `export PORT=…` as a bare `PORT=…` would be correct for
			// the shell but needlessly invasive on a hand-maintained file: the
			// existing form is preserved, only the value changes.
			name:     "preserves the export keyword and its indentation",
			existing: "  export\tPORT=3000\n",
			assigns:  []envAssign{{"PORT", "3010"}},
			want:     "  export\tPORT=3010\n",
		},
		{
			name:     "rewrites an exported PIGTAIL_OWNER instead of appending a duplicate",
			existing: "export PIGTAIL_OWNER=main\nexport PORT=3000\n",
			assigns:  []envAssign{{"PIGTAIL_OWNER", "feat-x"}, {"PORT", "3010"}},
			want:     "export PIGTAIL_OWNER=feat-x\nexport PORT=3010\n",
		},
		{
			// `export` only counts as the keyword when whitespace follows it,
			// so a variable whose name merely starts with those letters is a
			// different variable and must not be rewritten.
			name:     "a variable named exported is not the exported variable",
			existing: "exported=1\n",
			assigns:  []envAssign{{"PORT", "3010"}},
			want:     "exported=1\nPORT=3010\n",
		},
		{
			// Spaces around the `=` are tolerated on read (envlocal accepts
			// them) and normalised on rewrite — the result is a line a shell
			// actually treats as an assignment, where the input was not one.
			name:     "normalises spaces around the equals when rewriting",
			existing: "PORT = 3000\n",
			assigns:  []envAssign{{"PORT", "3010"}},
			want:     "PORT=3010\n",
		},
		{
			// The value is replaced whole, comment included: keeping a stale
			// `# dev` next to a freshly allocated port would be a lie, and
			// envlocal would read the new value correctly either way.
			name:     "replaces a value carrying a trailing comment",
			existing: "PORT=3000 # parent's dev port\n",
			assigns:  []envAssign{{"PORT", "3010"}},
			want:     "PORT=3010\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeEnvLocal([]byte(tc.existing), tc.assigns)
			if got != tc.want {
				t.Errorf("mergeEnvLocal:\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestCreateInheritsAnExportedParentEnvLocal is the end-to-end half of the
// shared-parser fix, on a parent .env.local written the way a shell user
// writes one. Before it, `export NAME=` was not recognised as an assignment:
// the parent's lines survived untouched and a second, contradicting assignment
// was appended below. The file then said two different things about PORT and
// PIGTAIL_OWNER — which one won depended on which reader you asked, and
// ReadPort (taking the first) answered with the PARENT's port.
func TestCreateInheritsAnExportedParentEnvLocal(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir := filepath.Join(container, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = mainDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}

	parentEnv := "" +
		"export PORT=3000\n" +
		"export PIGTAIL_OWNER=main\n" +
		"FEATURE_FLAG_FOO=true\n"
	if err := os.WriteFile(filepath.Join(mainDir, ".env.local"), []byte(parentEnv), 0o644); err != nil {
		t.Fatal(err)
	}

	b := New(ports.New(nilUsed{}))
	ws, err := b.Create(context.Background(),
		&pigtail.Project{
			ID:             "prj_export",
			RootPath:       container,
			MainWorktree:   "main",
			DefaultBackend: "git",
			PortRanges:     map[string]pigtail.PortRange{"dev": {38800, 38899}},
		},
		backend.CreateIntent{Intent: "inherit exported env", BaseRef: "main"},
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(ws.Path, ".env.local"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(raw)

	// Rewritten in place, not duplicated: exactly one assignment each.
	if n := countEnvAssignments(s, "PORT"); n != 1 {
		t.Errorf("PORT should be assigned exactly once, got %d:\n%s", n, s)
	}
	if n := countEnvAssignments(s, "PIGTAIL_OWNER"); n != 1 {
		t.Errorf("PIGTAIL_OWNER should be assigned exactly once, got %d:\n%s", n, s)
	}
	// The existing form is preserved — a hand-maintained file is not
	// reformatted just because a value changed.
	wantPort := fmt.Sprintf("export PORT=%d\n", ws.Ports["dev"])
	if !strings.Contains(s, wantPort) {
		t.Errorf("expected %q, got:\n%s", wantPort, s)
	}
	wantOwner := "export PIGTAIL_OWNER=" + filepath.Base(ws.Path) + "\n"
	if !strings.Contains(s, wantOwner) {
		t.Errorf("expected %q, got:\n%s", wantOwner, s)
	}
	if !strings.Contains(s, "FEATURE_FLAG_FOO=true") {
		t.Errorf("parent var dropped, got:\n%s", s)
	}
	// And the shared reader agrees with what is on disk.
	if got, ok := envlocal.Lookup(raw, "PORT"); !ok || got != strconv.Itoa(ws.Ports["dev"]) {
		t.Errorf("envlocal.Lookup(PORT) = (%q, %v), want %d", got, ok, ws.Ports["dev"])
	}
}

// countEnvAssignments counts the non-commented assignments of a variable,
// whatever form each one takes.
func countEnvAssignments(content, name string) int {
	n := 0
	for _, line := range strings.Split(content, "\n") {
		if got, _, ok := envlocal.ParseAssignment(line); ok && got == name {
			n++
		}
	}
	return n
}

// scratchRepoAndWorkspace creates a container with a fresh git repo in
// "main/", then runs GitBackend.Create with the given intent + branch override.
// Returns the main worktree path (for git commands) and the workspace.
func scratchRepoAndWorkspace(t *testing.T, intent string, branchParts []string) (mainDir string, ws *pigtail.Workspace) {
	t.Helper()
	container := t.TempDir()
	mainDir = filepath.Join(container, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = mainDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	alloc := ports.New(nilUsed{})
	b := New(alloc)
	branch := strings.Join(branchParts, "/")
	ws, err := b.Create(context.Background(),
		&pigtail.Project{
			ID:             "prj_t",
			RootPath:       container,
			MainWorktree:   "main",
			DefaultBackend: "git",
			PortRanges:     map[string]pigtail.PortRange{"dev": {39000, 39099}},
		},
		backend.CreateIntent{Intent: intent, BranchOverride: branch, BaseRef: "main"},
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return mainDir, ws
}

func TestStatusMerged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root, ws := scratchRepoAndWorkspace(t, "merged-status", []string{"feat/merged-status"})

	// Commit something on the branch
	if err := os.WriteFile(filepath.Join(ws.Path, "new.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", ws.Path, "add", "new.txt"},
		{"-C", ws.Path, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "add"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	b := New(nil)

	// Before merge: Merged should be false
	st, err := b.Status(context.Background(), ws)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Merged {
		t.Error("expected Merged=false before merge")
	}
	if st.AheadOfBase != 1 {
		t.Errorf("expected AheadOfBase=1, got %d", st.AheadOfBase)
	}

	// Merge the branch into main
	for _, args := range [][]string{
		{"-C", root, "-c", "user.email=t@t", "-c", "user.name=t", "merge", "--no-ff", "-m", "merge", "feat/merged-status"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	// After merge: Merged should be true
	st, err = b.Status(context.Background(), ws)
	if err != nil {
		t.Fatalf("status after merge: %v", err)
	}
	if !st.Merged {
		t.Error("expected Merged=true after merge")
	}
}

func TestMove(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root, ws := scratchRepoAndWorkspace(t, "move me", []string{"feat/move-me"})

	container := filepath.Dir(root)
	newPath := filepath.Join(container, "renamed-slug")

	b := New(nil)
	if err := b.Move(context.Background(), ws, newPath); err != nil {
		t.Fatalf("move: %v", err)
	}

	// Old path is gone, new path exists and is a git worktree.
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Errorf("old worktree path still exists: %v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new worktree path missing: %v", err)
	}
	out, err := exec.Command("git", "-C", newPath, "rev-parse", "--is-inside-work-tree").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("new path is not a git worktree: %v: %s", err, out)
	}

	// Branch must be unchanged.
	branchOut, _ := exec.Command("git", "-C", newPath, "rev-parse", "--abbrev-ref", "HEAD").CombinedOutput()
	if strings.TrimSpace(string(branchOut)) != "feat/move-me" {
		t.Errorf("branch changed by move: got %q", strings.TrimSpace(string(branchOut)))
	}

	// Idempotent no-op when src == dst.
	ws.Path = newPath
	if err := b.Move(context.Background(), ws, newPath); err != nil {
		t.Fatalf("idempotent move: %v", err)
	}
}

func TestRenameBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	_, ws := scratchRepoAndWorkspace(t, "rename branch", []string{"feat/old-branch"})

	b := New(nil)
	if err := b.RenameBranch(context.Background(), ws, "feat/new-branch"); err != nil {
		t.Fatalf("rename branch: %v", err)
	}

	// Worktree HEAD must now point at the new branch name.
	out, err := exec.Command("git", "-C", ws.Path, "rev-parse", "--abbrev-ref", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "feat/new-branch" {
		t.Errorf("HEAD branch = %q, want feat/new-branch", got)
	}

	// Old branch name should no longer exist.
	listOut, _ := exec.Command("git", "-C", ws.Path, "branch", "--list", "feat/old-branch").CombinedOutput()
	if strings.TrimSpace(string(listOut)) != "" {
		t.Errorf("old branch still present: %q", string(listOut))
	}
}

func TestDisplayRefReturnsBranch(t *testing.T) {
	b := &Backend{}
	ws := &pigtail.Workspace{
		Intent:      "add the cockpit redesign",
		BackendData: map[string]interface{}{"branch": "feat/cockpit-redesign"},
	}
	if got := b.DisplayRef(ws); got != "feat/cockpit-redesign" {
		t.Errorf("DisplayRef = %q, want branch", got)
	}
}

func TestDisplayRefFallsBackToIntent(t *testing.T) {
	b := &Backend{}
	ws := &pigtail.Workspace{Intent: "add the cockpit redesign", BackendData: map[string]interface{}{}}
	if got := b.DisplayRef(ws); got != "add the cockpit redesign" {
		t.Errorf("DisplayRef = %q, want Intent fallback", got)
	}
}

// --- auto-fetch of the base ref's remote ----------------------------------

// gitRun runs a git command in dir (dir="" → cwd) and returns trimmed stdout,
// failing the test on error.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// mustInitRepoWithRemote builds a bare remote with one commit on main, then
// clones it into <container>/main (so the main worktree has an `origin` remote
// and an `origin/main` tracking ref). It returns the main worktree path, the
// bare remote path, and a scratch working clone (seed) that pushes to the same
// remote — use seed to advance the remote tip and make origin/main stale.
func mustInitRepoWithRemote(t *testing.T, container string) (mainDir, remoteDir, seed string) {
	t.Helper()
	remoteDir = filepath.Join(t.TempDir(), "remote.git")
	gitRun(t, "", "init", "--bare", "-b", "main", remoteDir)

	seed = filepath.Join(t.TempDir(), "seed")
	gitRun(t, "", "clone", remoteDir, seed)
	gitRun(t, seed, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init")
	gitRun(t, seed, "push", "-u", "origin", "main")

	mainDir = filepath.Join(container, "main")
	gitRun(t, "", "clone", remoteDir, mainDir)
	return mainDir, remoteDir, seed
}

// advanceRemote adds an empty commit in seed and pushes it, returning the new
// remote tip SHA. The already-cloned main worktree's origin/main is now stale
// until it fetches.
func advanceRemote(t *testing.T, seed string) string {
	t.Helper()
	gitRun(t, seed, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "second")
	gitRun(t, seed, "push", "origin", "main")
	return gitRun(t, seed, "rev-parse", "HEAD")
}

func fetchTestProject(container string) *pigtail.Project {
	return &pigtail.Project{
		ID: "prj_fetch", RootPath: container, MainWorktree: "main", DefaultBackend: "git",
		PortRanges: map[string]pigtail.PortRange{"dev": {38600, 38699}, "storybook": {38700, 38799}},
	}
}

// TestCreateFetchesRemoteTrackingBase proves that a remote-tracking base
// (origin/main) is fetched before branching, so a worktree created after the
// remote moved starts from the true remote tip, not the last-known one.
func TestCreateFetchesRemoteTrackingBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir, _, seed := mustInitRepoWithRemote(t, container)

	firstSHA := gitRun(t, mainDir, "rev-parse", "origin/main")
	secondSHA := advanceRemote(t, seed)
	if firstSHA == secondSHA {
		t.Fatal("remote tip did not advance")
	}
	// Pre-condition: the clone's origin/main is still stale (no fetch yet).
	if got := gitRun(t, mainDir, "rev-parse", "origin/main"); got != firstSHA {
		t.Fatalf("precondition: origin/main = %s, want stale %s", got, firstSHA)
	}

	ws, err := New(ports.New(nilUsed{})).Create(context.Background(),
		fetchTestProject(container),
		backend.CreateIntent{Intent: "say hello", BaseRef: "origin/main"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Create must have fetched: origin/main now at the true remote tip.
	if got := gitRun(t, mainDir, "rev-parse", "origin/main"); got != secondSHA {
		t.Errorf("origin/main after create = %s, want fetched %s", got, secondSHA)
	}
	// And the new branch was cut from that fresh tip.
	if got := gitRun(t, ws.Path, "rev-parse", "HEAD"); got != secondSHA {
		t.Errorf("worktree HEAD = %s, want %s", got, secondSHA)
	}
}

// TestCreateNoFetchSkipsFetch proves NoFetch:true skips the auto-fetch, so the
// worktree branches from the stale last-known origin/main.
func TestCreateNoFetchSkipsFetch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir, _, seed := mustInitRepoWithRemote(t, container)

	firstSHA := gitRun(t, mainDir, "rev-parse", "origin/main")
	advanceRemote(t, seed)

	ws, err := New(ports.New(nilUsed{})).Create(context.Background(),
		fetchTestProject(container),
		backend.CreateIntent{Intent: "say hello", BaseRef: "origin/main", NoFetch: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := gitRun(t, mainDir, "rev-parse", "origin/main"); got != firstSHA {
		t.Errorf("origin/main after --no-fetch create = %s, want unchanged %s", got, firstSHA)
	}
	if got := gitRun(t, ws.Path, "rev-parse", "HEAD"); got != firstSHA {
		t.Errorf("worktree HEAD = %s, want stale %s", got, firstSHA)
	}
}

// TestCreateLocalBaseDoesNotFetch proves a bare local base ("main") has no
// remote to refresh, so origin/main is left untouched (no fetch attempted).
func TestCreateLocalBaseDoesNotFetch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir, _, seed := mustInitRepoWithRemote(t, container)

	firstSHA := gitRun(t, mainDir, "rev-parse", "origin/main")
	advanceRemote(t, seed)

	if _, err := New(ports.New(nilUsed{})).Create(context.Background(),
		fetchTestProject(container),
		backend.CreateIntent{Intent: "say hello", BaseRef: "main"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// A local base must not trigger a fetch — origin/main stays stale.
	if got := gitRun(t, mainDir, "rev-parse", "origin/main"); got != firstSHA {
		t.Errorf("origin/main after local-base create = %s, want unchanged %s (unexpected fetch)", got, firstSHA)
	}
}

// TestCreateFetchFailureIsNonFatal proves an unreachable remote (offline) does
// not abort worktree creation — Create falls back to the last-known
// origin/main and still succeeds.
func TestCreateFetchFailureIsNonFatal(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := t.TempDir()
	mainDir, _, _ := mustInitRepoWithRemote(t, container)

	firstSHA := gitRun(t, mainDir, "rev-parse", "origin/main")
	// Point origin at a path that doesn't exist → fetch fails like offline.
	gitRun(t, mainDir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))

	ws, err := New(ports.New(nilUsed{})).Create(context.Background(),
		fetchTestProject(container),
		backend.CreateIntent{Intent: "say hello", BaseRef: "origin/main"})
	if err != nil {
		t.Fatalf("create should be non-fatal on fetch failure, got: %v", err)
	}
	if _, err := os.Stat(ws.Path); err != nil {
		t.Errorf("worktree not created: %v", err)
	}
	// Branched from the last-known (pre-failure) origin/main.
	if got := gitRun(t, ws.Path, "rev-parse", "HEAD"); got != firstSHA {
		t.Errorf("worktree HEAD = %s, want last-known %s", got, firstSHA)
	}
}

// Create leaves its ports pending: the caller confirms once the workspace row
// is persisted. Confirming inside Create opened a window where a concurrent
// create, reading the store, saw neither the row nor the pending entry.
func TestCreateLeavesPortsPending(t *testing.T) {
	requireGit(t)
	mainDir := mustInitRepo(t, t.TempDir())
	alloc := ports.New(nilUsed{}, ports.WithBindProbe(func(int) bool { return true }))
	p := projectFor(mainDir)
	p.PortRanges = map[string]pigtail.PortRange{"dev": {39300, 39399}}
	ws, err := New(alloc).Create(context.Background(), p,
		backend.CreateIntent{Intent: "pending ports", BaseRef: "main", NoFetch: true})
	if err != nil {
		t.Fatal(err)
	}
	if !alloc.Pending("dev")[ws.Ports["dev"]] {
		t.Fatalf("port %d left the pending set before the row was persisted", ws.Ports["dev"])
	}
	next, err := alloc.Allocate(p.PortRanges, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if next == ws.Ports["dev"] {
		t.Fatalf("second allocation got %d again", next)
	}
}
