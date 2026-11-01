package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/scope"
)

// fakePigtaild writes a script that records its argv in <dir>/argv, writes
// stderrText on stderr (none if empty) and exits code.
func fakePigtaild(t *testing.T, code int, stderrText string) (bin, argvFile string) {
	t.Helper()
	dir := t.TempDir()
	argvFile = filepath.Join(dir, "argv")
	bin = filepath.Join(dir, "pigtaild")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$*\" > %q\nprintf '%%s' %s >&2\nexit %d\n", argvFile, shellQuote(stderrText), code)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argvFile
}

// shellQuote single-quotes s for /bin/sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// outOfAnyCell makes dbView resolve to the default scope: no cell table, no
// CLAUDE_CONFIG_DIR, no PIGTAIL_SCOPE.
func outOfAnyCell(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv(envScope, "")
}

func usePigtaild(t *testing.T, bin string) {
	orig := pigtaildPathFn
	pigtaildPathFn = func() (string, error) { return bin, nil }
	t.Cleanup(func() { pigtaildPathFn = orig })
}

func execute(t *testing.T, cmd *cobra.Command, args ...string) error {
	t.Helper()
	if args == nil { // SetArgs(nil) makes cobra v1.10.2 (command.go:1105) read os.Args[1:] of the test binary
		args = []string{}
	}
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.Execute()
}

func TestDBListForwardsToPigtaild(t *testing.T) {
	outOfAnyCell(t)
	bin, argv := fakePigtaild(t, 0, "")
	usePigtaild(t, bin)
	if err := execute(t, dbListCmd(), "--db", "/x/p.db"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(argv)
	if want := "-db-tool list -view " + string(scope.Default()) + " -db /x/p.db"; string(got) != want {
		t.Fatalf("argv %q, want %q", got, want)
	}
}

func TestDBRestoreForwardsItsFlags(t *testing.T) {
	outOfAnyCell(t)
	origUp := daemonIsUp
	daemonIsUp = func() bool { return false }
	t.Cleanup(func() { daemonIsUp = origUp })
	bin, argv := fakePigtaild(t, 0, "")
	usePigtaild(t, bin)
	if err := execute(t, dbRestoreCmd(), "--generation", "1", "--dry-run", "--yes", "--db", "/x/p.db"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(argv)
	want := "-db-tool restore -view " + string(scope.Default()) + " -db /x/p.db -generation 1 -dry-run -yes"
	if string(got) != want {
		t.Fatalf("argv %q, want %q", got, want)
	}
}

// The db tool printed its own "Error: …"; the CLI adds nothing and keeps the code.
func TestDBForwardPropagatesTheEnginesExitCode(t *testing.T) {
	outOfAnyCell(t)
	bin, _ := fakePigtaild(t, 1, "")
	usePigtaild(t, bin)
	cmd := dbListCmd()
	err := execute(t, cmd)
	if exitCode(err) != 1 || !cmd.SilenceErrors {
		t.Fatalf("exit %d, silenced %v (%v)", exitCode(err), cmd.SilenceErrors, err)
	}
}

// Code 2 and the unknown-flag text: Go's flag package refusing -db-tool, i.e. an
// installed pigtaild older than this CLI (measured 2026-10-28). Say so, and how to fix it.
func TestDBForwardNamesAnOlderPigtaild(t *testing.T) {
	outOfAnyCell(t)
	bin, _ := fakePigtaild(t, 2, "flag provided but not defined: -db-tool\n")
	usePigtaild(t, bin)
	cmd := dbListCmd()
	err := execute(t, cmd)
	if err == nil || cmd.SilenceErrors || !strings.Contains(err.Error(), "make install") || !strings.Contains(err.Error(), bin) {
		t.Fatalf("want a visible reinstall hint naming %s, got %v (silenced %v)", bin, err, cmd.SilenceErrors)
	}
}

// Code 2 without the unknown-flag text is the db tool failing (a panic exits 2
// too): an ordinary error, no reinstall hint.
func TestDBForwardExitTwoWithoutTheFlagTextIsAnEngineFailure(t *testing.T) {
	outOfAnyCell(t)
	bin, _ := fakePigtaild(t, 2, "panic: boom\n")
	usePigtaild(t, bin)
	cmd := dbListCmd()
	err := execute(t, cmd)
	if exitCode(err) != 2 || !cmd.SilenceErrors || (err != nil && strings.Contains(err.Error(), "make install")) {
		t.Fatalf("want exit 2, silenced, no reinstall hint; got exit %d, silenced %v (%v)", exitCode(err), cmd.SilenceErrors, err)
	}
}

func TestPigtaildPathPrefersTheSibling(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "pigtail")
	sib := filepath.Join(dir, "pigtaild")
	for _, p := range []string{self, sib} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(t.TempDir(), "pigtail") // reached through a symlink
	if err := os.Symlink(self, link); err != nil {
		t.Fatal(err)
	}
	// pigtaildPathFrom resolves symlinks, and on macOS t.TempDir() sits under
	// /var -> /private/var: compare against the resolved sibling.
	want, err := filepath.EvalSymlinks(sib)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pigtaildPathFrom(link, func(string) (string, error) { t.Fatal("PATH consulted"); return "", nil })
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestPigtaildPathFallsBackToPATH(t *testing.T) {
	self := filepath.Join(t.TempDir(), "pigtail") // no sibling
	got, err := pigtaildPathFrom(self, func(n string) (string, error) { return "/opt/bin/" + n, nil })
	if err != nil || got != "/opt/bin/pigtaild" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestPigtaildPathSaysWhatIsMissing(t *testing.T) {
	self := filepath.Join(t.TempDir(), "pigtail")
	_, err := pigtaildPathFrom(self, func(string) (string, error) { return "", exec.ErrNotFound })
	if err == nil || !strings.Contains(err.Error(), "pigtaild") || !strings.Contains(err.Error(), "make install") {
		t.Fatalf("got %v", err)
	}
}
