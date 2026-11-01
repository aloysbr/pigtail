package devserver

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseCwds(t *testing.T) {
	out := []byte("p100\nfcwd\nn/Users/a/monorepo/feat-x\np200\nfcwd\nn/Users/a/monorepo/main/apps/web\n")
	got := parseCwds(out)
	want := map[int]string{100: "/Users/a/monorepo/feat-x", 200: "/Users/a/monorepo/main/apps/web"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// lsof exits 1 when one of the pids is gone, and still prints the others.
func TestProcessCwdsResultPartialExit1(t *testing.T) {
	exit1 := exitErr(t, 1)
	got, err := cwdsResult([]byte("p100\nfcwd\nn/x\n"), exit1)
	if err != nil || got[100] != "/x" || len(got) != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := cwdsResult(nil, exit1); err != nil || len(got) != 0 {
		t.Fatalf("no pid found is an answer, not an error: %v, %v", got, err)
	}
	if _, err := cwdsResult(nil, errors.New("exec: lsof not found")); err == nil {
		t.Fatal("a probe that did not run must be an error")
	}
}

func TestWithinIsSeparatorAware(t *testing.T) {
	root := t.TempDir()
	a, ab := filepath.Join(root, "feat-a"), filepath.Join(root, "feat-ab")
	for _, d := range []string{a, ab, filepath.Join(a, "apps", "web")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if !Within(a, a) || !Within(filepath.Join(a, "apps", "web"), a) {
		t.Fatal("the dir and its subdirs are within")
	}
	if Within(ab, a) {
		t.Fatal("feat-ab is not within feat-a")
	}
}

// On macOS t.TempDir() is under /var, which lsof reports as /private/var.
func TestWithinResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if !Within(real, dir) || !Within(real, link) || !Within(dir, real) {
		t.Fatal("symlinked spellings of one dir must match")
	}
}

// A sub-path that does not exist still resolves through its existing ancestor:
// /var/…/gone/apps and /private/var/…/gone match.
func TestWithinNonExistentSubPath(t *testing.T) {
	dir := t.TempDir() // /var/folders/… on macOS
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "gone")
	if !Within(filepath.Join(real, "gone", "apps"), gone) || !Within(filepath.Join(gone, "apps"), filepath.Join(real, "gone")) {
		t.Fatal("non-existent sub-paths must match across symlinked spellings")
	}
	if Within(filepath.Join(real, "gone-ab"), gone) {
		t.Fatal("separator-aware on non-existent paths too")
	}
}

func TestSplitByCwd(t *testing.T) {
	dir := t.TempDir()
	cwds := map[int]string{1: dir, 2: filepath.Join(t.TempDir(), "other")}
	owned, foreign := SplitByCwd([]int{1, 2, 3}, cwds, dir)
	if !reflect.DeepEqual(owned, []int{1}) {
		t.Fatalf("owned %v", owned)
	}
	if foreign[2] != cwds[2] || foreign[3] != "" || len(foreign) != 2 {
		t.Fatalf("foreign %v (pid 3 has no known cwd)", foreign)
	}
}

func TestHolderWorktree(t *testing.T) {
	if got := HolderWorktree("/c/monorepo/feat-x/apps/web", "/c/monorepo"); got != "/c/monorepo/feat-x" {
		t.Fatalf("got %q", got)
	}
	if got := HolderWorktree("/elsewhere/app", "/c/monorepo"); got != "/elsewhere/app" {
		t.Fatalf("got %q", got)
	}
}

// Real lsof on our own pid: our cwd comes back.
func TestProcessCwdsSelf(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not installed")
	}
	got, err := ProcessCwds([]int{os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	if !Within(got[os.Getpid()], wd) || !Within(wd, got[os.Getpid()]) {
		t.Fatalf("cwd %q, want %q", got[os.Getpid()], wd)
	}
}
