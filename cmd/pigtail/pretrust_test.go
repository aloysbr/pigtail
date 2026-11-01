package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/internal/claudeconfig"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestPreTrustForNeedsAWorkspaceRecord(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", base)
	dev := filepath.Join(base, "Developer")
	repo := filepath.Join(dev, "home", "repo")
	wt := filepath.Join(dev, "home", "repo-wt", "feat")
	cfg := filepath.Join(base, "cell")
	_ = os.MkdirAll(cfg, 0o755)
	for _, a := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x"},
		{"-C", repo, "worktree", "add", "-q", "-b", "feat", wt},
	} {
		if out, err := exec.Command("git", a...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	body := `{"projects":{"` + dev + `":{"hasTrustDialogAccepted":true}}}`
	_ = os.WriteFile(filepath.Join(cfg, claudeconfig.FileName), []byte(body), 0o600)

	if r := preTrustFor(nil, cfg, wt); r.Outcome != claudeconfig.NotEligible {
		t.Fatalf("unknown dir: %+v", r)
	}
	// A cwd outside any git worktree, even one listed as a workspace: not eligible, nothing written.
	plain := filepath.Join(dev, "plain")
	_ = os.MkdirAll(plain, 0o755)
	if r := preTrustFor([]pigtail.Workspace{{ID: "w0", Path: plain}}, cfg, plain); r.Outcome != claudeconfig.NotEligible {
		t.Fatalf("cwd outside git: %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(cfg, claudeconfig.FileName)); string(b) != body {
		t.Fatal("wrote for a directory pigtail does not know (D2)")
	}
	wss := []pigtail.Workspace{{ID: "w1", ProjectID: "p1", Path: wt}}
	sub := filepath.Join(wt, "pkg")
	_ = os.MkdirAll(sub, 0o755)
	if r := preTrustFor(wss, cfg, sub); r.Outcome != claudeconfig.Written || r.Key != repo {
		t.Fatalf("cwd inside a workspace: %+v", r)
	}
}
