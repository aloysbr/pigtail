package setup

import (
	"strings"
	"testing"
	"time"
)

func TestCheckPrereqs_findsGitAndTmux(t *testing.T) {
	results := CheckPrereqs()
	if len(results) == 0 {
		t.Fatal("expected at least one prereq result")
	}
	var foundGit bool
	for _, r := range results {
		if r.Name == "git" {
			foundGit = true
			if !r.Found {
				t.Skip("git not in PATH on this machine")
			}
			if r.Path == "" {
				t.Error("git found but path is empty")
			}
		}
	}
	if !foundGit {
		t.Error("git not in prereq results")
	}
}

func TestWaitForDaemon_failsOnBadSocket(t *testing.T) {
	err := WaitForDaemon("/tmp/pigtail-nonexistent-test.sock", 2, 50*time.Millisecond)
	if err == nil {
		t.Error("expected error for non-existent socket")
	}
}

func TestResolvePigtaildPath_fallsBackToLocalBin(t *testing.T) {
	path := ResolvePigtaildPath()
	if path == "" {
		t.Fatal("empty path")
	}
	if !strings.HasSuffix(path, "pigtaild") {
		t.Errorf("path %q doesn't end with pigtaild", path)
	}
}
