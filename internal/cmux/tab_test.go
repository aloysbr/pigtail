package cmux

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	tabWS    = "64F9131B-F905-4859-A0DC-8103E1FC3154"
	tabWS2   = "B2FC70AD-8013-4D88-870D-C5F74C70CAA6"
	tabSurf  = "A3506517-1A00-47C7-9D6A-BDA2A748A9E2"
	tabSurf2 = "0C1D2E3F-1A00-47C7-9D6A-BDA2A748A9E2"
)

// twoWorkspaceTree: surface:5 (tabSurf, lower case in the tree) in tabWS,
// surface:70 (tabSurf2) in tabWS2.
func twoWorkspaceTree() []byte {
	return []byte(`{"windows":[{"id":"W1","key":false,"selected_workspace_id":"` + tabWS2 + `","workspaces":[
 {"id":"` + strings.ToLower(tabWS) + `","panes":[{"surfaces":[{"id":"` + strings.ToLower(tabSurf) + `","ref":"surface:5"}]}]},
 {"id":"` + tabWS2 + `","panes":[{"surfaces":[{"id":"` + tabSurf2 + `","ref":"surface:70"}]}]}]}]}`)
}

func TestNewTabArgs(t *testing.T) {
	log := recordingCLI(t, "0", "OK surface:70 "+tabSurf2+" pane:60 workspace:57")
	id, err := NewTab(tabWS, "/w/my dir")
	if err != nil {
		t.Fatal(err)
	}
	if id != tabSurf2 {
		t.Fatalf("id %q", id)
	}
	b, _ := os.ReadFile(log)
	want := "ARGS --id-format both new-surface --workspace " + tabWS + " --working-directory /w/my dir --focus false"
	if !strings.Contains(string(b), want) {
		t.Fatalf("calls %q, want %q", b, want)
	}
	if _, err := newTabArgs(tabWS, " "); err == nil {
		t.Fatal("an empty cwd must be an error")
	}
	if _, err := newTabArgs("workspace:3", "/w"); err == nil {
		t.Fatal("a workspace ref must be refused: UUID only")
	}
}

func TestNewTabReturnsUUID(t *testing.T) {
	noTree := func() ([]byte, error) { t.Fatal("tree read although the output names the UUID"); return nil, nil }
	for _, out := range []string{
		"OK surface:70 " + strings.ToLower(tabSurf2) + " pane:60 workspace:57",
		"OK surface:70 (" + tabSurf2 + ") pane:60",
		`{"surface_id":"` + strings.ToLower(tabSurf2) + `","surface_ref":"surface:70"}`,
	} {
		id, err := newTabUUID(out, tabWS2, noTree)
		if err != nil || id != tabSurf2 {
			t.Fatalf("%q: id %q err %v, want %s (upper case)", out, id, err, tabSurf2)
		}
	}
	tree := func() ([]byte, error) { return twoWorkspaceTree(), nil }
	id, err := newTabUUID("OK surface:70 pane:60 workspace:57", tabWS2, tree)
	if err != nil || id != tabSurf2 {
		t.Fatalf("ref resolved by the tree: id %q err %v", id, err)
	}
	id, err = newTabUUID("OK surface:5 pane:2 workspace:1", strings.ToLower(tabWS), tree)
	if err != nil || id != tabSurf {
		t.Fatalf("lower-case tree: id %q err %v, want %s", id, err, tabSurf)
	}
	for _, out := range []string{"OK surface:99 pane:1", "OK pane:60 " + tabWS} {
		if id, err := newTabUUID(out, tabWS2, tree); err == nil || id != "" {
			t.Fatalf("%q: id %q err %v, want an error, never a ref", out, id, err)
		}
	}
	// a UUID of another object (pane, workspace) is never the surface's
	if id, err := newTabUUID("OK surface:70 pane:60 "+tabWS, tabWS2, tree); err != nil || id != tabSurf2 {
		t.Fatalf("id %q err %v", id, err)
	}
}

func TestCloseSurfaceArgs(t *testing.T) {
	log := recordingCLI(t, "0", "OK")
	if err := CloseSurface(tabSurf, tabWS); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if want := "ARGS close-surface --surface " + tabSurf + " --workspace " + tabWS + "\n"; !strings.Contains(string(b), want) {
		t.Fatalf("calls %q, want %q", b, want)
	}
	if strings.Contains(string(b), "--force") {
		t.Fatal("never --force")
	}
	if err := CloseSurface("surface:5", tabWS); err == nil {
		t.Fatal("a surface ref must be refused")
	}
}

func TestCloseSurfaceLastIsReported(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cmux")
	script := "#!/bin/sh\necho 'Error: invalid_state: Cannot close the last surface' >&2\nexit 1\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvBundledCLI, p)
	err := CloseSurface(tabSurf, tabWS)
	if !errors.Is(err, ErrLastSurface) {
		t.Fatalf("err = %v, want ErrLastSurface", err)
	}
	if err := closeSurfaceErr([]byte("OK"), nil); err != nil {
		t.Fatalf("OK = %v", err)
	}
}

func TestWorkspaceOfSurfaceFromTree(t *testing.T) {
	ws, err := workspaceOfSurface(twoWorkspaceTree(), strings.ToLower(tabSurf2))
	if err != nil || ws != tabWS2 {
		t.Fatalf("ws %q err %v", ws, err)
	}
	ws, err = workspaceOfSurface(twoWorkspaceTree(), tabSurf)
	if err != nil || ws != tabWS {
		t.Fatalf("ws %q err %v, want %s upper case", ws, err, tabWS)
	}
	if ws, err := workspaceOfSurface(twoWorkspaceTree(), "FFFFFFFF-1A00-47C7-9D6A-BDA2A748A9E2"); err == nil || ws != "" {
		t.Fatalf("unknown surface: ws %q err %v, want an error", ws, err)
	}
	if _, err := workspaceOfSurface([]byte(`{}`), tabSurf); err == nil {
		t.Fatal("a tree without windows must be an error")
	}
}
