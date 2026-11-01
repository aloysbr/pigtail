package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSharedMainCheckout(t *testing.T) {
	plain := t.TempDir()
	if shared, err := SharedMainCheckout(plain); err != nil || shared {
		t.Fatalf("non-git dir = %v %v, want false, nil", shared, err)
	}
	repo := t.TempDir()
	for _, a := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x"},
	} {
		if out, err := exec.Command("git", a...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	if shared, err := SharedMainCheckout(repo); err != nil || shared {
		t.Fatalf("lone main checkout = %v %v, want false (no linked worktree)", shared, err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", "feat", wt).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v %s", err, out)
	}
	if shared, err := SharedMainCheckout(repo); err != nil || !shared {
		t.Fatalf("main checkout with a linked worktree = %v %v, want true", shared, err)
	}
	if shared, err := SharedMainCheckout(wt); err != nil || shared {
		t.Fatalf("linked worktree = %v %v, want false", shared, err)
	}
}

func TestMainCheckoutRoot(t *testing.T) {
	plain := t.TempDir()
	if root, main, linked, err := MainCheckoutRoot(plain); err != nil || main || linked || root != "" {
		t.Fatalf("non-git = %q %v %v %v", root, main, linked, err)
	}
	repo := t.TempDir()
	for _, a := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x"},
	} {
		if out, err := exec.Command("git", a...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	sub := filepath.Join(repo, "pkg")
	_ = os.MkdirAll(sub, 0o700)
	want, _ := filepath.EvalSymlinks(repo)
	for _, d := range []string{repo, sub} {
		root, main, linked, err := MainCheckoutRoot(d)
		if err != nil || !main || linked {
			t.Fatalf("%s = %q %v %v %v", d, root, main, linked, err)
		}
		if got, _ := filepath.EvalSymlinks(root); got != want {
			t.Fatalf("%s root = %q, want %q", d, root, want)
		}
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", "feat", wt).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v %s", err, out)
	}
	if root, main, linked, err := MainCheckoutRoot(wt); err != nil || main || !linked || root != "" {
		t.Fatalf("linked worktree = %q %v %v %v", root, main, linked, err)
	}
}

// D3: R19's list. Main checkout excluded, sorted, a removed (prunable) worktree dropped.
func TestLinkedWorktrees(t *testing.T) {
	repo := t.TempDir()
	for _, a := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x"},
	} {
		if out, err := exec.Command("git", a...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	if got, err := LinkedWorktrees(repo); err != nil || len(got) != 0 {
		t.Fatalf("no linked worktree = %v %v", got, err)
	}
	base := t.TempDir()
	b, a, gone := filepath.Join(base, "b"), filepath.Join(base, "a"), filepath.Join(base, "gone")
	for i, wt := range []string{b, a, gone} {
		if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", "w"+strconv.Itoa(i), wt).CombinedOutput(); err != nil {
			t.Fatalf("worktree add %s: %v %s", wt, err, out)
		}
	}
	_ = os.RemoveAll(gone)
	ra, _ := filepath.EvalSymlinks(a)
	rb, _ := filepath.EvalSymlinks(b)
	got, err := LinkedWorktrees(repo)
	if err != nil || strings.Join(got, ",") != ra+","+rb {
		t.Fatalf("linked = %v %v, want [%s %s]", got, err, ra, rb)
	}
}
