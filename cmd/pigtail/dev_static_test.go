package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// writeProject lays down a minimal pigtail.yaml container with the given
// worktrees (each gets a .env.local with PORT/STORYBOOK_PORT) and chdirs into
// it for the duration of the test. Returns the container dir.
func writeProject(t *testing.T, worktrees map[string]int) string {
	t.Helper()
	// Keep start/stop routing deterministic regardless of the ambient shell:
	// when run inside a cmux session `pigtail start` would otherwise route
	// through the cmux sidecar path.
	t.Setenv("CMUX_WORKSPACE_ID", "")
	dir := t.TempDir()
	yaml := "version: 1\nname: testproj\nmain_worktree: main\n" +
		"ports:\n  dev:\n    range: [3000, 3099]\n    env: PORT\n" +
		"  storybook:\n    range: [6006, 6099]\n    env: STORYBOOK_PORT\n"
	if err := os.WriteFile(filepath.Join(dir, "pigtail.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	for wt, port := range worktrees {
		wtDir := filepath.Join(dir, wt)
		if err := os.MkdirAll(wtDir, 0o755); err != nil {
			t.Fatal(err)
		}
		env := fmt.Sprintf("PORT=%d\nSTORYBOOK_PORT=%d\n", port, port+3000)
		if err := os.WriteFile(filepath.Join(wtDir, ".env.local"), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	return dir
}

func runStaticCmd(t *testing.T, build func() *cobra.Command, args ...string) error {
	t.Helper()
	cmd := build()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.Execute()
}

func staticViews(names ...string) []pigtail.StaticServerView {
	out := make([]pigtail.StaticServerView, 0, len(names))
	for _, n := range names {
		out = append(out, pigtail.StaticServerView{
			StaticServer: pigtail.StaticServer{Name: n, Port: 18080},
		})
	}
	return out
}

// TestStartPrefersWorktreeOverStaticServer proves worktree-first precedence:
// a name that is BOTH a worktree and a static server resolves to the worktree,
// and the static-server start endpoint is never touched.
func TestStartPrefersWorktreeOverStaticServer(t *testing.T) {
	// Occupy the worktree's dev port so devserver.Start sees "already
	// running" and returns early without spawning a process.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	dir := writeProject(t, map[string]int{"main": 3000, "shared": port})
	// The listener is this test process: run it from inside the worktree, so
	// Start reads it as the worktree's own server ("already running") rather
	// than another worktree's (a refusal).
	t.Chdir(filepath.Join(dir, "shared"))

	s := newStubDaemon(t)
	s.servers = staticViews("shared")
	withStubDaemon(t, s, func() {
		if err := runStaticCmd(t, startCmd, "shared"); err != nil {
			t.Fatalf("start shared: %v", err)
		}
	})
	if s.startedName != "" {
		t.Errorf("static start was called with %q; worktree should have won", s.startedName)
	}
}

// TestStartFallsThroughToStaticServer proves that a bare name matching no
// worktree routes to the static-server start path.
func TestStartFallsThroughToStaticServer(t *testing.T) {
	// chdir somewhere with no pigtail.yaml so worktree resolution fails and
	// the daemon registry (empty) confirms "no worktree".
	t.Chdir(t.TempDir())

	s := newStubDaemon(t)
	s.servers = staticViews("onlystatic")
	withStubDaemon(t, s, func() {
		if err := runStaticCmd(t, startCmd, "onlystatic"); err != nil {
			t.Fatalf("start onlystatic: %v", err)
		}
	})
	if s.startedName != "onlystatic" {
		t.Errorf("static start name = %q, want onlystatic", s.startedName)
	}
}

// TestStopFallsThroughToStaticServer is the symmetric stop case.
func TestStopFallsThroughToStaticServer(t *testing.T) {
	t.Chdir(t.TempDir())

	s := newStubDaemon(t)
	s.servers = staticViews("onlystatic")
	withStubDaemon(t, s, func() {
		if err := runStaticCmd(t, stopServerCmd, "onlystatic"); err != nil {
			t.Fatalf("stop onlystatic: %v", err)
		}
	})
	if s.stoppedName != "onlystatic" {
		t.Errorf("static stop name = %q, want onlystatic", s.stoppedName)
	}
}

// TestStartUnknownNameMentionsBoth checks the combined error when a bare name
// is neither a worktree nor a static server.
func TestStartUnknownNameMentionsBoth(t *testing.T) {
	t.Chdir(t.TempDir())

	s := newStubDaemon(t) // no servers registered
	var gotErr error
	withStubDaemon(t, s, func() {
		gotErr = runStaticCmd(t, startCmd, "ghost")
	})
	if gotErr == nil {
		t.Fatal("expected an error for an unknown name")
	}
	msg := gotErr.Error()
	if !strings.Contains(msg, "no worktree or static server") || !strings.Contains(msg, "ghost") {
		t.Errorf("error %q should mention both worktree and static server", msg)
	}
}

// TestStopAllIncludesStaticServers verifies stop --all folds in the static
// stop-all call after the worktree servers.
func TestStopAllIncludesStaticServers(t *testing.T) {
	writeProject(t, map[string]int{"main": 3000})

	s := newStubDaemon(t)
	withStubDaemon(t, s, func() {
		if err := runStaticCmd(t, stopServerCmd, "--all"); err != nil {
			t.Fatalf("stop --all: %v", err)
		}
	})
	if !s.stopAllHit {
		t.Error("stop --all did not call the static stop-all endpoint")
	}
}

// TestStopAllOutsideProjectStillStopsStatics is a regression test: static
// servers are daemon-global, so `stop --all` run from a directory that is not
// inside any pigtail project must skip the worktree-local sweep (instead of
// erroring) and still stop every registered static server.
func TestStopAllOutsideProjectStillStopsStatics(t *testing.T) {
	t.Chdir(t.TempDir()) // no pigtail.yaml here

	s := newStubDaemon(t)
	withStubDaemon(t, s, func() {
		if err := runStaticCmd(t, stopServerCmd, "--all"); err != nil {
			t.Fatalf("stop --all outside a project: %v", err)
		}
	})
	if !s.stopAllHit {
		t.Error("stop --all outside a project did not call the static stop-all endpoint")
	}
}
