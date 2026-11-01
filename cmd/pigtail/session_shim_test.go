package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
)

// TestMoveOneRelaunchesThroughCmuxWrapper: the relaunch must not depend on
// the pane's `claude` shim. In a pane whose cmux-cli-shims/<SURFACE_ID>/ is
// empty, a plain `claude` resolves to the bare binary and cmux never
// registers the relaunched session. Typing the wrapper's absolute path is
// what the shim would have done, and works whatever state the shim is in.
func TestMoveOneRelaunchesThroughCmuxWrapper(t *testing.T) {
	stubMoveOps(t)
	tbl, _, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")

	claudeWrapper = func() (string, error) {
		return "/Applications/cmux.app/Contents/Resources/bin/cmux-claude-wrapper", nil
	}
	var lines []string
	sendLine = func(_, line string) error { lines = append(lines, line); return nil }

	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err != nil {
		t.Fatalf("moveOne: %v", err)
	}

	want := "CLAUDE_CONFIG_DIR='" + dst.ConfigDir + "' '/Applications/cmux.app/Contents/Resources/bin/cmux-claude-wrapper' --resume " +
		row.SessionID + " --permission-mode auto"
	if len(lines) != 2 || lines[0] != "/exit" || lines[1] != want {
		t.Fatalf("lines sent = %q, want [/exit %q]", lines, want)
	}
	if warn.Len() != 0 {
		t.Fatalf("unexpected warning: %q", warn.String())
	}
}

// TestMoveOneFallsBackToClaudeWithoutWrapper: a cmux without the wrapper
// still gets relaunched — with plain `claude`, and a warning saying what that
// costs, rather than a silent downgrade.
func TestMoveOneFallsBackToClaudeWithoutWrapper(t *testing.T) {
	stubMoveOps(t)
	tbl, _, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")

	claudeWrapper = func() (string, error) { return "", errors.New("cmux-claude-wrapper not found") }
	var relaunch string
	sendLine = func(_, line string) error { relaunch = line; return nil }

	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err != nil {
		t.Fatalf("moveOne: %v", err)
	}
	want := "CLAUDE_CONFIG_DIR='" + dst.ConfigDir + "' claude --resume " + row.SessionID + " --permission-mode auto"
	if relaunch != want {
		t.Fatalf("relaunch = %q, want %q", relaunch, want)
	}
	if !strings.Contains(warn.String(), "cmux-claude-wrapper not found") {
		t.Fatalf("warn = %q, want the reason the wrapper was not used", warn.String())
	}
}

// TestUnboundSessions pins which running sessions `session ls` reports as
// invisible to cmux: in a cmux surface, not launched through the wrapper,
// and not already known through a resume binding.
func TestUnboundSessions(t *testing.T) {
	t.Parallel()
	tbl, _, _, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	srcDir := row.ConfigDir

	emptyShim := t.TempDir() // the observed failure: the directory exists, empty
	goodShim := t.TempDir()
	if err := os.WriteFile(filepath.Join(goodShim, "claude"), []byte("#!/usr/bin/env bash\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	const (
		bound     = "1bb0480e-a291-4dab-bb69-8190b97b6824"
		bareEmpty = "22222222-0000-0000-0000-000000000002"
		bareGood  = "33333333-0000-0000-0000-000000000003"
		outside   = "44444444-0000-0000-0000-000000000004"
		wrapped   = "55555555-0000-0000-0000-000000000005"
		noEnv     = "66666666-0000-0000-0000-000000000006"
	)
	live := []claudesession.Live{
		{PID: 1, SessionID: bound, ConfigDir: srcDir, Args: "/bin/claude"},
		{PID: 2, SessionID: bareEmpty, ConfigDir: srcDir, Cwd: "/w/2", Args: "/bin/claude"},
		{PID: 3, SessionID: bareGood, ConfigDir: srcDir, Cwd: "/w/3", Args: "/bin/claude --resume " + bareGood},
		{PID: 4, SessionID: outside, ConfigDir: srcDir, Args: "/bin/claude"},
		{PID: 5, SessionID: wrapped, ConfigDir: srcDir, Args: "/bin/claude --settings /tmp/cmux-claude-settings.AbC123"},
		{PID: 6, SessionID: noEnv, ConfigDir: srcDir, Args: "/bin/claude"},
	}
	surfaces := map[int]cmux.Surface{
		1: {ID: "S1", ShimRoot: emptyShim},
		2: {ID: "S2", ShimRoot: emptyShim},
		3: {ID: "S3", ShimRoot: goodShim},
		4: {},
		5: {ID: "S5", ShimRoot: emptyShim},
	}
	surfaceOf := func(pid int) (cmux.Surface, error) {
		s, ok := surfaces[pid]
		if !ok {
			return cmux.Surface{}, errors.New("no such process")
		}
		return s, nil
	}

	got := unboundSessions([]sessionRow{row}, live, surfaceOf, tbl)
	if len(got) != 2 {
		t.Fatalf("unboundSessions = %+v, want exactly the two bare sessions in a cmux surface", got)
	}
	if got[0].SessionID != bareEmpty || got[0].SurfaceID != "S2" || !got[0].ShimEmpty || got[0].CellID != "t" || got[0].Cwd != "/w/2" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].SessionID != bareGood || got[1].SurfaceID != "S3" || got[1].ShimEmpty {
		t.Errorf("got[1] = %+v", got[1])
	}
}

// TestPrintUnboundGivesTheRelaunchLine: listing an invisible session is only
// useful with the way out — the exact line to relaunch it through the
// wrapper, on the account it already runs on.
func TestPrintUnboundGivesTheRelaunchLine(t *testing.T) {
	t.Parallel()
	var w bytes.Buffer
	printUnbound(&w, []unboundSession{{
		SessionID: "22222222-0000-0000-0000-000000000002",
		SurfaceID: "S2",
		CellID:    "t",
		ConfigDir: "/cfg/t",
		Cwd:       "/w/2",
		ShimEmpty: true,
	}}, "/x/cmux-claude-wrapper")

	s := w.String()
	for _, want := range []string{
		"22222222-0000-0000-0000-000000000002",
		"S2",
		"shim",
		"/exit",
		"CLAUDE_CONFIG_DIR='/cfg/t' '/x/cmux-claude-wrapper' --resume 22222222-0000-0000-0000-000000000002 --permission-mode auto",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}

	w.Reset()
	printUnbound(&w, nil, "/x/cmux-claude-wrapper")
	if w.Len() != 0 {
		t.Errorf("nothing to report must print nothing, got %q", w.String())
	}
}

// TestReportUnboundSaysWhenItCannotLook: a failed process listing must not
// read as "every session is visible to cmux".
func TestReportUnboundSaysWhenItCannotLook(t *testing.T) {
	tbl, _, _, _, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")
	saved := liveSessions
	t.Cleanup(func() { liveSessions = saved })
	liveSessions = func([]string) ([]claudesession.Live, error) { return nil, errors.New("ps: boom") }

	var warn bytes.Buffer
	reportUnbound(&warn, nil, tbl)
	if !strings.Contains(warn.String(), "ps: boom") {
		t.Fatalf("warn = %q, want the listing failure reported", warn.String())
	}
}

// TestMoveOneRebindsThroughCmuxWrapper: the rewritten resume binding goes
// through the same wrapper as the live relaunch — otherwise the next cmux
// restore would replay a bare `claude` into a pane whose shim may be empty.
func TestMoveOneRebindsThroughCmuxWrapper(t *testing.T) {
	stubMoveOps(t)
	tbl, _, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")

	const w = "/Applications/cmux.app/Contents/Resources/bin/cmux-claude-wrapper"
	claudeWrapper = func() (string, error) { return w, nil }
	var gotWrapper, gotConfigDir string
	setResumeBinding = func(_, _, _, configDir, wrapper string) error {
		gotConfigDir, gotWrapper = configDir, wrapper
		return nil
	}

	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err != nil {
		t.Fatalf("moveOne: %v", err)
	}
	if gotWrapper != w || gotConfigDir != dst.ConfigDir {
		t.Fatalf("setResumeBinding got wrapper %q config dir %q, want %q %q", gotWrapper, gotConfigDir, w, dst.ConfigDir)
	}
}

// TestMoveOneRebindFallsBackToClaudeWithoutWrapper: no wrapper means a plain
// `claude` binding, with the warning printed once for the whole move — not
// once per place the launcher is used.
func TestMoveOneRebindFallsBackToClaudeWithoutWrapper(t *testing.T) {
	stubMoveOps(t)
	tbl, _, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")

	claudeWrapper = func() (string, error) { return "", errors.New("cmux-claude-wrapper not found") }
	gotWrapper := "unset"
	setResumeBinding = func(_, _, _, _, wrapper string) error { gotWrapper = wrapper; return nil }

	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err != nil {
		t.Fatalf("moveOne: %v", err)
	}
	if gotWrapper != "" {
		t.Fatalf("setResumeBinding wrapper = %q, want empty (plain claude)", gotWrapper)
	}
	if n := strings.Count(warn.String(), "cmux-claude-wrapper not found"); n != 1 {
		t.Fatalf("warning printed %d times, want once:\n%s", n, warn.String())
	}
}

// TestMoveOneRebindFailureFixLineUsesWrapper: the hand-fix printed when the
// rebind fails must set the same binding the move would have.
func TestMoveOneRebindFailureFixLineUsesWrapper(t *testing.T) {
	stubMoveOps(t)
	tbl, _, dst, row, _ := newMoveFixture(t, "1bb0480e-a291-4dab-bb69-8190b97b6824")

	claudeWrapper = func() (string, error) { return "/x/cmux-claude-wrapper", nil }
	setResumeBinding = func(string, string, string, string, string) error { return errors.New("boom") }

	var out, warn bytes.Buffer
	if err := moveOne(&out, &warn, row, dst, tbl, false, false); err == nil {
		t.Fatal("expected the rebind failure to fail the move")
	}
	want := "CLAUDE_CONFIG_DIR=" + dst.ConfigDir + " /x/cmux-claude-wrapper --resume " + row.SessionID
	if !strings.Contains(warn.String(), want) {
		t.Fatalf("fix line lacks %q:\n%s", want, warn.String())
	}
}
