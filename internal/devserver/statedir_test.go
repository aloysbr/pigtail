package devserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/testutil"
)

// stateMarker tells a re-executed test binary that the state dir is already set.
const stateMarker = "PIGTAIL_DEVSERVER_TEST_STATE"

// TestMain isolates the whole package's runtime state under a throwaway
// XDG_STATE_HOME. Without it, every test that starts a server or writes a
// marker would litter the developer's real ~/.local/state/pigtail — and worse,
// two packages' tests running concurrently would share it.
//
// A helper process (the test binary re-executed by a test, and killed by it)
// inherits the parent's XDG_STATE_HOME and the marker: it makes no dir of its
// own, which nobody would remove (it dies before any cleanup).
func TestMain(m *testing.M) {
	// The stop tests use fake pids: never read the real process table.
	processParentsFn = func() (map[int]int, error) { return map[int]int{}, nil }
	if os.Getenv(stateMarker) != "" {
		os.Exit(m.Run())
	}
	root, cleanRoot := testutil.MkdirTempMain("pigtail-state-test-")
	_ = os.Setenv("XDG_STATE_HOME", root)
	_ = os.Setenv(stateMarker, root)
	code := m.Run()
	cleanRoot()
	os.Exit(code)
}

// TestStateDirIsOutsideTheWorktree is the whole point of this file: nothing
// pigtail writes for its own bookkeeping may live under the user's worktree,
// where a `git add -A` can commit it (it did, twice) and a `pigtail stop --all`
// deleting it shows up as a deleted TRACKED file in someone else's session.
func TestStateDirIsOutsideTheWorktree(t *testing.T) {
	wt := t.TempDir()
	dir := StateDir(wt)
	if dir == "" {
		t.Fatal("StateDir returned an empty path")
	}
	rel, err := filepath.Rel(canonicalPath(wt), canonicalPath(dir))
	if err == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("StateDir(%q) = %q lives INSIDE the worktree", wt, dir)
	}
	for _, p := range []string{OwnerPath(wt, "dev"), LogPath(wt, "dev")} {
		if strings.HasPrefix(canonicalPath(p), canonicalPath(wt)+string(os.PathSeparator)) {
			t.Fatalf("%q lives inside the worktree %q", p, wt)
		}
	}
}

// TestStateDirIdenticalAcrossEquivalentPaths covers constraint 2: the CLI
// writes markers, the daemon reads them, and they do not necessarily hold the
// same *spelling* of the worktree path — the daemon has whatever ws.Path was
// registered, the CLI has os.Getwd(). Every spelling of one directory must
// resolve to one state dir, symlinks included (on macOS /tmp and /var are
// themselves symlinks, so this is the common case, not an exotic one).
func TestStateDirIdenticalAcrossEquivalentPaths(t *testing.T) {
	base := t.TempDir()
	wt := filepath.Join(base, "feat-x")
	if err := os.MkdirAll(filepath.Join(wt, "src"), 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link-to-feat-x")
	if err := os.Symlink(wt, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	want := StateDir(wt)
	for _, spelling := range []string{
		wt + string(os.PathSeparator),
		filepath.Join(wt, "."),
		filepath.Join(wt, "src", ".."),
		link,
	} {
		if got := StateDir(spelling); got != want {
			t.Fatalf("StateDir(%q) = %q, want %q — the CLI and the daemon would disagree", spelling, got, want)
		}
	}
}

// TestStateDirDistinguishesWorktreesWithTheSameName: worktree basenames repeat
// across projects (two `main`s, two `feat-login`s). The key is the full path,
// so the basename in the directory name is legibility only — never identity.
func TestStateDirDistinguishesWorktreesWithTheSameName(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "projA", "main")
	b := filepath.Join(base, "projB", "main")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if StateDir(a) == StateDir(b) {
		t.Fatalf("two worktrees named main collide on %q", StateDir(a))
	}
	if !strings.Contains(filepath.Base(StateDir(a)), "main") {
		t.Fatalf("state dir %q does not carry the worktree name — undebuggable", StateDir(a))
	}
}

func TestStateRootHonoursXDGStateHome(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	if got, want := stateRoot(), filepath.Join(xdg, "pigtail"); got != want {
		t.Fatalf("stateRoot = %q, want %q", got, want)
	}
}

func TestStateRootFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := stateRoot(), filepath.Join(home, ".local", "state", "pigtail"); got != want {
		t.Fatalf("stateRoot = %q, want %q", got, want)
	}
}

// TestStateFileSanitisesSegments: server names come from a project's
// pigtail.yaml, i.e. from a file pigtail does not control. A name carrying a
// separator must not be able to place a file outside the state dir.
func TestStateFileSanitisesSegments(t *testing.T) {
	wt := t.TempDir()
	p := stateFile(wt, "../../evil", ".owner")
	if filepath.Dir(p) != StateDir(wt) {
		t.Fatalf("stateFile escaped the state dir: %q", p)
	}
}

func TestEnsureStateDirCreatesLazily(t *testing.T) {
	wt := t.TempDir()
	if _, err := os.Stat(StateDir(wt)); !os.IsNotExist(err) {
		t.Fatalf("state dir exists before any write (err=%v)", err)
	}
	dir, err := EnsureStateDir(wt)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("%q is not a directory", dir)
	}
	if _, err := EnsureStateDir(wt); err != nil {
		t.Fatalf("EnsureStateDir is not idempotent: %v", err)
	}
}
