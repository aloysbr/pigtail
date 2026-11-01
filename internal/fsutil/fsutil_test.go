package fsutil

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readonlyTree(t *testing.T, root string) {
	t.Helper()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "f"), []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "a"), 0o555); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveTreeReadOnly(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "tree")
	readonlyTree(t, root)
	// Positive control: plain RemoveAll fails on the same shape.
	ctl := filepath.Join(base, "ctl")
	readonlyTree(t, ctl)
	if err := os.RemoveAll(ctl); err == nil {
		t.Fatal("control: os.RemoveAll should fail on a read-only tree")
	}
	_ = RemoveTree(ctl)

	if err := RemoveTree(root); err != nil {
		t.Fatalf("RemoveTree: %v", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("tree still there: %v", err)
	}
}

func TestRemoveTreeNeverFollowsLinks(t *testing.T) {
	base := t.TempDir()
	outFile := filepath.Join(base, "out.txt")
	outDir := filepath.Join(base, "outdir")
	if err := os.WriteFile(outFile, []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(outDir, 0o755) })
	root := filepath.Join(base, "tree")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outFile, filepath.Join(root, "lf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outDir, filepath.Join(root, "ld")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveTree(root); err != nil {
		t.Fatalf("RemoveTree: %v", err)
	}
	for p, want := range map[string]os.FileMode{outFile: 0o444, outDir: 0o555} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("target %s gone: %v", p, err)
		}
		if st.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", p, st.Mode().Perm(), want)
		}
	}
}

func TestNewestMTime(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-5 * time.Hour).Truncate(time.Second)
	recent := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	f1 := filepath.Join(root, "a")
	sub := filepath.Join(root, "d")
	f2 := filepath.Join(sub, "b")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f1, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte("123"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A link to a far newer file outside must not count.
	outside := filepath.Join(t.TempDir(), "new")
	if err := os.WriteFile(outside, []byte("zzzzzzzzzz"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "ln")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{f1, sub, root} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(f2, recent, recent); err != nil {
		t.Fatal(err)
	}
	// Lchtimes is not portable; the link's own mtime is "now" and is a
	// lstat'd entry, so only the target's content must stay out of the size.
	m, size, err := NewestMTime(root)
	if err != nil {
		t.Fatal(err)
	}
	if size != 8 {
		t.Errorf("size = %d, want 8 (links not followed)", size)
	}
	if m.Before(recent) {
		t.Errorf("newest = %v, want >= %v", m, recent)
	}
}
