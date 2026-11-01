package cmux

import (
	"strings"
	"testing"
)

// D1 (sub_x000000000000040, overturns D8 of sub_x000000000000030): a spawned
// session is born right under its caller in the caller's group
// (--group-placement afterCurrent --group-reference <caller>).
func TestNewWorkspaceArgs(t *testing.T) {
	s := WorkspaceSpec{
		Name: "Sessions fraîches", Cwd: "/w/x", Command: "/bin/zsh -i -c 'x'",
		Env:       []string{"CLAUDE_CONFIG_DIR=/c/jp", "CMUX_PRESERVE_CLAUDE_AUTH_SELECTION_ENV=1"},
		GroupRef:  "workspace_group:11",
		CallerRef: "workspace:207",
	}
	got := strings.Join(newWorkspaceArgs(s), "|")
	want := "new-workspace|--name|Sessions fraîches|--cwd|/w/x|--env|CLAUDE_CONFIG_DIR=/c/jp|--env|CMUX_PRESERVE_CLAUDE_AUTH_SELECTION_ENV=1|--command|/bin/zsh -i -c 'x'|--focus|false|--group|workspace_group:11|--group-placement|afterCurrent|--group-reference|workspace:207"
	if got != want {
		t.Fatalf("args\n got %s\nwant %s", got, want)
	}
	s.CallerRef = "" // no caller known: never afterCurrent, which would follow the selection
	if got := strings.Join(newWorkspaceArgs(s), "|"); !strings.HasSuffix(got, "|--group|workspace_group:11|--group-placement|end") {
		t.Fatalf("grouped without a caller ref: %s", got)
	}
	s.GroupRef, s.CallerRef = "", "workspace:207" // caller in no group (pinned, young): no --group* at all
	if got := strings.Join(newWorkspaceArgs(s), "|"); strings.Contains(got, "--group") {
		t.Fatalf("ungrouped caller must not pass --group*: %s", got)
	}
	if got := strings.Join(newWorkspaceArgs(s), "|"); !strings.Contains(got, "--focus|false") {
		t.Fatalf("focus must be false: %s", got)
	}
}

func TestParseNewWorkspaceRef(t *testing.T) {
	if ref, err := parseNewWorkspaceRef("OK workspace:42\n"); err != nil || ref != "workspace:42" {
		t.Fatalf("ref = %q %v", ref, err)
	}
	if _, err := parseNewWorkspaceRef("Error: no window"); err == nil {
		t.Fatal("output without a ref must be an error")
	}
}

const identifyFixture = `{"caller":{"pane_ref":"pane:212","surface_ref":"surface:268","workspace_ref":"workspace:207","window_ref":"window:1"},"focused":{"workspace_ref":"workspace:209"}}`

func TestParseCallerWorkspaceRef(t *testing.T) {
	if ref, err := parseCallerWorkspaceRef([]byte(identifyFixture)); err != nil || ref != "workspace:207" {
		t.Fatalf("ref = %q %v (must be the caller's, not the focused one)", ref, err)
	}
	for _, bad := range []string{`{}`, `{"caller":{"workspace_ref":null}}`, `not json`, `{"caller":{"workspace_ref":"surface:1"}}`} {
		if _, err := parseCallerWorkspaceRef([]byte(bad)); err == nil {
			t.Fatalf("%s: want an error", bad)
		}
	}
}

const groupsFixture = `{"groups":[{"ref":"workspace_group:1","member_workspace_refs":["workspace:1","workspace:2"]},{"ref":"workspace_group:11","member_workspace_refs":["workspace:193","workspace:207"]}]}`

func TestParseGroupOf(t *testing.T) {
	if g, err := parseGroupOf([]byte(groupsFixture), "workspace:207"); err != nil || g != "workspace_group:11" {
		t.Fatalf("group = %q %v", g, err)
	}
	if g, err := parseGroupOf([]byte(groupsFixture), "workspace:999"); err != nil || g != "" {
		t.Fatalf("ungrouped = %q %v, want empty", g, err)
	}
	if _, err := parseGroupOf([]byte("nope"), "workspace:1"); err == nil {
		t.Fatal("bad JSON must be an error")
	}
}

// Web launch, lot 2: a daemon launch is born at the bottom of its
// subject's folder in a named window (--window before --group);
// without a group, no --window (cmux's default place, like ⌘N).
func TestNewWorkspaceArgsWindowBeforeGroup(t *testing.T) {
	const w = "6F0C1A2B-3C4D-4E5F-8A9B-0C1D2E3F4A5B"
	s := WorkspaceSpec{Name: "n", Cwd: "/w", Command: "c", WindowID: w, GroupRef: "workspace_group:44"}
	got := strings.Join(newWorkspaceArgs(s), "|")
	if !strings.HasSuffix(got, "|--focus|false|--window|"+w+"|--group|workspace_group:44|--group-placement|end") {
		t.Fatalf("args = %s", got)
	}
	if err := checkSpec(s); err != nil {
		t.Fatalf("a UUID window refused: %v", err)
	}
	for _, bad := range []string{"--group", "window:1", "6f0c1a2b"} {
		if err := checkSpec(WorkspaceSpec{WindowID: bad, GroupRef: "workspace_group:44"}); err == nil {
			t.Errorf("window %q accepted", bad)
		}
	}
	t.Run("no_group", func(t *testing.T) {
		s := WorkspaceSpec{Name: "n", Cwd: "/w", Command: "c", WindowID: w}
		if got := strings.Join(newWorkspaceArgs(s), "|"); strings.Contains(got, "--window") || strings.Contains(got, "--group") {
			t.Fatalf("no group: args = %s", got)
		}
	})
}
