package main

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// freePort returns a port nothing listens on (bound, then released).
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// captureStdout runs fn and returns what it wrote to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	testutil.SerialOnly(t) // swaps os.Stdout
	stdout := os.Stdout
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = wr
	fn()
	_ = wr.Close()
	os.Stdout = stdout
	b, _ := io.ReadAll(rd)
	return string(b)
}

func TestIsWorkspaceID(t *testing.T) {
	t.Parallel()
	for arg, want := range map[string]bool{
		"ws_cf8935":           true,
		"ws_cf8935:storybook": true,
		"ws_docs":             false, // not hex: a static server may carry that name
		"ws_":                 false,
		"wsx1":                false,
		"ws_cf8935x":          false,
		"feat-auth":           false,
		"3044":                false,
		"":                    false,
	} {
		if got := isWorkspaceID(arg); got != want {
			t.Errorf("isWorkspaceID(%q) = %v, want %v", arg, got, want)
		}
	}
}

func TestStopAcceptsAWorkspaceID(t *testing.T) {
	container := writeProject(t, map[string]int{"fix-clean-stops": freePort(t)})
	s := newStubDaemon(t)
	s.workspaces = map[string]pigtail.Workspace{"ws_cf8935": {ID: "ws_cf8935", Path: filepath.Join(container, "fix-clean-stops")}}
	withStubDaemon(t, s, func() {
		var err error
		out := captureStdout(t, func() { err = runStaticCmd(t, stopServerCmd, "ws_cf8935") })
		if err != nil {
			t.Fatalf("stop ws_cf8935: %v", err)
		}
		if !strings.Contains(out, "→ testproj/fix-clean-stops\n") {
			t.Fatalf("stdout = %q, want the line \"→ testproj/fix-clean-stops\"", out)
		}
		if s.stoppedName != "" {
			t.Fatalf("a static server %q was stopped", s.stoppedName)
		}
	})
}

func TestResolveTargetContextWorkspaceIDWithServer(t *testing.T) {
	container := writeProject(t, map[string]int{"fix-clean-stops": freePort(t)})
	s := newStubDaemon(t)
	s.workspaces = map[string]pigtail.Workspace{"ws_cf8935": {ID: "ws_cf8935", Path: filepath.Join(container, "fix-clean-stops")}}
	withStubDaemon(t, s, func() {
		ctx, wt, srv, err := resolveTargetContext([]string{"ws_cf8935:storybook"})
		if err != nil {
			t.Fatal(err)
		}
		if wt != "fix-clean-stops" || srv != "storybook" {
			t.Fatalf("wt=%q srv=%q", wt, srv)
		}
		if ctx.ContainerDir != container {
			t.Fatalf("ContainerDir = %q, want %q", ctx.ContainerDir, container)
		}
	})
}

func TestWorkspaceIDUnknownNeverFallsToStatic(t *testing.T) {
	writeProject(t, map[string]int{"fix-clean-stops": freePort(t)})
	s := newStubDaemon(t)
	s.servers = staticViews("ws_docs")
	withStubDaemon(t, s, func() {
		// "ws_docs" is not hex, so it is still a static server's name.
		if err := runStaticCmd(t, stopServerCmd, "ws_docs"); err != nil {
			t.Fatalf("stop ws_docs: %v", err)
		}
		if s.stoppedName != "ws_docs" {
			t.Fatalf("a non-id name must keep its static meaning; stoppedName = %q", s.stoppedName)
		}
	})
	s2 := newStubDaemon(t)
	s2.servers = staticViews("ws_dead01")
	withStubDaemon(t, s2, func() {
		err := runStaticCmd(t, stopServerCmd, "ws_dead01")
		if err == nil || !strings.Contains(err.Error(), "no workspace ws_dead01") {
			t.Fatalf("err = %v, want \"no workspace ws_dead01\"", err)
		}
		if s2.stoppedName != "" {
			t.Fatalf("an id fell through to the static server %q", s2.stoppedName)
		}
	})
}

func TestWorkspaceIDFromOutsideAnyProject(t *testing.T) {
	container := writeProject(t, map[string]int{"fix-clean-stops": freePort(t)})
	s := newStubDaemon(t)
	s.workspaces = map[string]pigtail.Workspace{"ws_cf8935": {ID: "ws_cf8935", Path: filepath.Join(container, "fix-clean-stops")}}
	t.Chdir(t.TempDir())
	withStubDaemon(t, s, func() {
		var err error
		out := captureStdout(t, func() { err = runStaticCmd(t, stopServerCmd, "ws_cf8935") })
		if err != nil {
			t.Fatalf("stop ws_cf8935 from outside a project: %v", err)
		}
		if !strings.Contains(out, "→ testproj/fix-clean-stops\n") {
			t.Fatalf("stdout = %q", out)
		}
	})
}

func TestLookupWorkspaceBuildsTheClientAtCallTime(t *testing.T) {
	container := writeProject(t, map[string]int{"fix-clean-stops": freePort(t)})
	s := newStubDaemon(t)
	s.workspaces = map[string]pigtail.Workspace{"ws_cf8935": {ID: "ws_cf8935", Path: filepath.Join(container, "fix-clean-stops")}}
	// lookupWorkspaceFn is read at package init; the stub is wired only now.
	withStubDaemon(t, s, func() {
		ws, err := lookupWorkspaceFn("ws_cf8935")
		if err != nil || ws == nil || ws.ID != "ws_cf8935" {
			t.Fatalf("ws=%v err=%v", ws, err)
		}
	})
}
