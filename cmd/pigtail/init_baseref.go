package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// initIsTTY reports whether stdin is a terminal. A variable so tests script it.
var initIsTTY = func() bool { return isTerminal(os.Stdin) }

// initStdin is where the confirmation answer is read from.
var initStdin io.Reader = os.Stdin

// detectBaseRef picks the ref new branches start from: the remote's default
// branch (origin/HEAD) when there is one, else the current branch.
func detectBaseRef(repoDir string) (string, error) {
	if out, err := exec.Command("git", "-C", repoDir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output(); err == nil { //nolint:gosec // fixed git argv; repoDir is the repo being initialised
		if ref := strings.TrimSpace(string(out)); ref != "" {
			return ref, nil
		}
	}
	if out, err := exec.Command("git", "-C", repoDir, "branch", "--show-current").Output(); err == nil { //nolint:gosec // fixed git argv; repoDir is the repo being initialised
		if br := strings.TrimSpace(string(out)); br != "" {
			return br, nil
		}
	}
	return "", fmt.Errorf("cannot detect the base branch (detached HEAD?) — run pigtail init --base <ref>")
}

var baseRefLine = regexp.MustCompile(`(?m)^(\s*base_ref:\s*)\S+`)

// setBaseRef rewrites the base_ref line of dir's pigtail.yaml.
func setBaseRef(dir, ref string) error {
	p := filepath.Join(dir, pigtail.ProjectConfigFilename)
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if !baseRefLine.Match(b) {
		return fmt.Errorf("%s has no base_ref line", p)
	}
	return os.WriteFile(p, baseRefLine.ReplaceAll(b, []byte("${1}"+ref)), 0o600)
}
