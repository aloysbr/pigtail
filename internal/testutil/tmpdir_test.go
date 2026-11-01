package testutil

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestMkdirTempRemovesReadOnlyTree(t *testing.T) {
	var dir string
	t.Run("inner", func(t *testing.T) {
		dir = MkdirTemp(t, "tmpdirtest-")
		if !regexp.MustCompile(`^pt-[a-z0-9-]*[0-9]+$`).MatchString(filepath.Base(dir)) {
			t.Fatalf("name %q does not match the sweeper's rule", filepath.Base(dir))
		}
		sub := filepath.Join(dir, "mod", "x")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "f"), []byte("x"), 0o444); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(sub, 0o555); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("dir survived its test: %v", err)
	}
}

func TestMkdirTempMainCleanup(t *testing.T) {
	dir, cleanup := MkdirTempMain("tmpdirmain-")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("dir survived cleanup: %v", err)
	}
}

func TestRemoveLoggedReportsFailure(t *testing.T) {
	var logged string
	removeLogged(func(f string, a ...any) { logged = f + "|" + a[0].(string) },
		"/x/y", func(string) error { return errors.New("boom") })
	if !strings.Contains(logged, "could not remove") || !strings.Contains(logged, "/x/y") {
		t.Fatalf("failure not logged: %q", logged)
	}
	logged = ""
	removeLogged(func(string, ...any) { logged = "called" }, "/x", func(string) error { return nil })
	if logged != "" {
		t.Fatal("logged on success")
	}
}

// TestMkdirTempMainSharedChildMakesNoDir: a helper process (the test binary
// re-executed by a test, killed before any cleanup) runs TestMain again; with
// the parent's marker it reuses the parent's dir and creates none.
func TestMkdirTempMainSharedChildMakesNoDir(t *testing.T) {
	const name = "sharedtest-"
	key := sharedMarker(name)
	t.Setenv(key, "")
	_ = os.Unsetenv(key)

	parent, cleanParent := MkdirTempMainShared(name)
	if got := os.Getenv(key); got != parent {
		t.Fatalf("marker = %q, want the parent's dir %q", got, parent)
	}
	before, _ := filepath.Glob(filepath.Join(tmpRoot, TempPrefix+name+"*"))

	child, cleanChild := MkdirTempMainShared(name) // the re-executed binary's TestMain
	cleanChild()
	after, _ := filepath.Glob(filepath.Join(tmpRoot, TempPrefix+name+"*"))
	if child != parent {
		t.Fatalf("child dir = %q, want the parent's %q", child, parent)
	}
	if len(after) != len(before) {
		t.Fatalf("child created a dir: before %v, after %v", before, after)
	}
	if _, err := os.Lstat(parent); err != nil {
		t.Fatalf("child cleanup removed the parent's dir: %v", err)
	}
	cleanParent()
	if _, err := os.Lstat(parent); !os.IsNotExist(err) {
		t.Fatalf("parent cleanup left %s: %v", parent, err)
	}
}
