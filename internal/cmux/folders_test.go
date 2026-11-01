package cmux

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFoldersArgv(t *testing.T) {
	const win, g, ws = "2D7A4724-6273-4254-ABD3-1DA3AB4FAF3C", "B89B4597-2397-45F6-81D9-95F3B0FA83F8", "80914656-BF88-4780-9982-CA1665755494"
	f := &fakeRun{out: map[string]string{
		"--json --id-format both workspace-group create --name Terrace --window " + win: string(readTestdata(t, "workspace-group-create.json")),
	}}
	c := FoldersCLI{Run: f.run}
	if _, err := c.CreateGroup(win, "Terrace"); err != nil {
		t.Fatal(err)
	}
	_ = c.AddToGroup(win, g, ws)
	_ = c.Ungroup(win, g, true)
	_ = c.Collapse(win, g)
	_ = c.Expand(win, g)
	_ = c.SetIcon(win, g, "house.fill")
	_ = c.Reorder(win, []string{ws, g})
	_ = c.MoveGroup(win, g, ws, false)
	_ = c.MoveGroup(win, g, ws, true)
	_ = c.RemoveFromGroup(win, ws)
	_ = c.RenameGroup(win, g, "Inbox")
	want := []string{
		"--json --id-format both workspace-group create --name Terrace --window " + win,
		"workspace-group add --group " + g + " --workspace " + ws + " --window " + win,
		"workspace-group ungroup " + g + " --remove-generated-anchor --window " + win,
		"workspace-group collapse " + g + " --window " + win,
		"workspace-group expand " + g + " --window " + win,
		"workspace-group set-icon " + g + " --symbol house.fill --window " + win,
		"reorder-workspaces --window " + win + " --order " + ws + "," + g,
		"workspace-group move " + g + " --after " + ws + " --window " + win,
		"workspace-group move " + g + " --before " + ws + " --window " + win,
		"workspace-group remove --workspace " + ws + " --window " + win, // P2 (b): the argv is kept tested whatever the verdict
		"workspace-group rename " + g + " --name Inbox --window " + win,
	}
	if got := f.joined(); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv =\n%q\nwant\n%q", got, want)
	}
}

func TestFoldersRefuseBadArgs(t *testing.T) {
	c := FoldersCLI{Run: (&fakeRun{}).run}
	const win = "2D7A4724-6273-4254-ABD3-1DA3AB4FAF3C"
	for name, err := range map[string]error{
		"flag name":   func() error { _, e := c.CreateGroup(win, "--help"); return e }(),
		"bad window":  c.Collapse("window:1", win),
		"bad symbol":  c.SetIcon(win, win, "house fill"),
		"flag symbol": c.SetIcon(win, win, "--help"),
		"dup order":   c.Reorder(win, []string{win, win}),
		"empty order": c.Reorder(win, nil),
		"ref order":   c.Reorder(win, []string{"workspace:1"}),
		"ref move":    c.MoveGroup(win, win, "workspace_group:1", false),
		"self move":   c.MoveGroup(win, win, win, true),
		"empty name":  c.RenameGroup(win, win, ""),
		"flag rename": c.RenameGroup(win, win, "-x"),
	} {
		if !errors.Is(err, ErrInvalidArg) {
			t.Errorf("%s: err = %v, want ErrInvalidArg", name, err)
		}
	}
}

func TestFoldersRunsWithoutCmuxEnv(t *testing.T) {
	// A fake cmux that dumps its environment: the real runners, not an injected Run.
	dir := t.TempDir()
	dump := filepath.Join(dir, "env.txt")
	bin := filepath.Join(dir, "cmux")
	script := "#!/bin/sh\nenv > '" + dump + "'\nprintf '{\"groups\":[]}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvBundledCLI, bin)
	// A shell spawned under a loaded `make check -race` can outlast the
	// production bounds; these tests are about argv, env and error text.
	t.Cleanup(SetCallTimeoutForTest(time.Minute))
	t.Cleanup(SetListingTimeoutForTest(time.Minute))
	t.Setenv("CMUX_SURFACE_ID", "80914656-BF88-4780-9982-CA1665755494")
	t.Setenv("CMUX_WORKSPACE_ID", "B89B4597-2397-45F6-81D9-95F3B0FA83F8")
	const win, g = "2D7A4724-6273-4254-ABD3-1DA3AB4FAF3C", "B89B4597-2397-45F6-81D9-95F3B0FA83F8"
	c := FoldersCLI{} // nil Run and RunListing: the package runners
	for name, call := range map[string]func(){
		"listing": func() { _, _ = c.ListGroups(win) },
		"write":   func() { _ = c.Collapse(win, g) },
	} {
		_ = os.Remove(dump)
		call()
		got, err := os.ReadFile(dump)
		if err != nil {
			t.Fatalf("%s: the fake cmux did not run: %v", name, err)
		}
		for _, kv := range strings.Split(string(got), "\n") {
			if strings.HasPrefix(kv, "CMUX_") {
				t.Errorf("%s: cmux ran with %q in its environment", name, kv)
			}
		}
	}
}

func TestParseGroupsFixture(t *testing.T) {
	gs, err := ParseGroups(readTestdata(t, "workspace-group-list.json"))
	if err != nil || len(gs) == 0 {
		t.Fatalf("ParseGroups = %v, %v", gs, err)
	}
	for _, g := range gs {
		if g.ID == "" || g.AnchorID == "" || len(g.Members) == 0 || g.Members[0] != g.AnchorID {
			t.Errorf("group %+v: id, anchor, members anchor-first expected", g)
		}
	}
	if _, err := ParseGroups([]byte(`{"error":"x"}`)); err == nil {
		t.Fatal("output without groups must be an error, never an empty list")
	}
}

// Web launch, lot 2: a group's workspace_group:N ref is read, so a launch
// can name it in --group.
func TestParseGroupsReadsRef(t *testing.T) {
	gs, err := ParseGroups(readTestdata(t, "p18-a-groups.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gs {
		if g.Name == "H" {
			if g.Ref != "workspace_group:44" {
				t.Fatalf("group H ref = %q, want workspace_group:44", g.Ref)
			}
			return
		}
	}
	t.Fatalf("no group H in %+v", gs)
}

// The anchor-only group as cmux really lists it (P1 fixture, F1 or F2), then both
// expected shapes and an out-of-order list inline: R4 (`len(Members) == 1`) stands on this.
func TestParseGroupsAnchorOnly(t *testing.T) {
	gs, err := ParseGroups(readTestdata(t, "workspace-group-list-anchor-only.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx *Group
	for i := range gs {
		if gs[i].Name == "PT Folder" {
			fx = &gs[i]
		}
	}
	if fx == nil || !fx.AnchorGenerated || !reflect.DeepEqual(fx.Members, []string{fx.AnchorID}) {
		t.Fatalf("fixture group = %+v, want PT Folder, generated anchor, Members = [anchor]", fx)
	}
	const a, b = "80914656-bf88-4780-9982-ca1665755494", "b89b4597-2397-45f6-81d9-95f3b0fa83f8"
	group := func(members string, empty bool, n int) []byte {
		return []byte(fmt.Sprintf(`{"groups":[{"id":"%s","name":"G","anchor_workspace_id":"%s","anchor_workspace_is_generated":true,"member_workspace_ids":%s,"is_empty":%t,"member_count":%d}]}`, b, a, members, empty, n))
	}
	up := strings.ToUpper
	for name, c := range map[string]struct {
		raw  []byte
		want []string
	}{
		"F1 anchor listed alone": {group(`["`+a+`"]`, false, 1), []string{up(a)}},
		"F2 empty list":          {group(`[]`, true, 0), []string{up(a)}},
		"anchor listed second":   {group(`["`+b+`","`+a+`"]`, false, 2), []string{up(a), up(b)}},
	} {
		gs, err := ParseGroups(c.raw)
		if err != nil || len(gs) != 1 || !reflect.DeepEqual(gs[0].Members, c.want) {
			t.Errorf("%s: Members = %+v (err %v), want %v", name, gs, err, c.want)
		}
	}
	if _, err := ParseGroups([]byte(`{"groups":[{"id":"x","name":"G","member_workspace_ids":[]}]}`)); err == nil {
		t.Fatal("a group without anchor_workspace_id must be an error")
	}
}

// D13: a pinned group is read from `pinned` or `is_pinned` (the listing of
// cmux 0.64.24 says `is_pinned`).
func TestParseGroupsPinned(t *testing.T) {
	const a = "80914656-BF88-4780-9982-CA1665755494"
	for key, want := range map[string]bool{`"pinned":true`: true, `"is_pinned":true`: true, `"is_pinned":false`: false, `"x":1`: false} {
		gs, err := ParseGroups([]byte(`{"groups":[{"id":"` + a + `","name":"G","anchor_workspace_id":"` + a + `","member_workspace_ids":[],` + key + `}]}`))
		if err != nil || len(gs) != 1 || gs[0].Pinned != want {
			t.Errorf("%s: groups = %+v (err %v), want Pinned %v", key, gs, err, want)
		}
	}
}

// A cmux failure through the default runners (no injected Run): the error
// names the command, the exit status and cmux's stderr.
func TestFoldersCLIErrorCarriesStderr(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "cmux")
	script := "#!/bin/sh\necho 'Error: invalid_state: Workspace is the anchor of another group' >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvBundledCLI, bin)
	// A shell spawned under a loaded `make check -race` can outlast the
	// production bounds; these tests are about argv, env and error text.
	t.Cleanup(SetCallTimeoutForTest(time.Minute))
	t.Cleanup(SetListingTimeoutForTest(time.Minute))
	const win, g, ws = "2D7A4724-6273-4254-ABD3-1DA3AB4FAF3C", "B89B4597-2397-45F6-81D9-95F3B0FA83F8", "80914656-BF88-4780-9982-CA1665755494"
	err := FoldersCLI{}.AddToGroup(win, g, ws)
	if err == nil {
		t.Fatal("cmux exited 1: an error expected")
	}
	for _, want := range []string{"exit status 1", "Workspace is the anchor of another group"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if _, err := (FoldersCLI{}).ListGroups(win); err == nil || !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("listing: error with the exit status expected, got %v", err)
	}
}

// SetPinned writes cmux's own pin (D3): `workspace-action --action pin|unpin
// --workspace <UUID>`, never a ref, never without a target.
func TestSetPinnedArgs(t *testing.T) {
	f := &fakeRun{}
	c := FoldersCLI{Run: f.run}
	if err := c.SetPinned(testWS, true); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPinned(testWS, false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"workspace-action --action pin --workspace " + testWS,
		"workspace-action --action unpin --workspace " + testWS,
	}
	if got := f.joined(); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv =\n%q\nwant\n%q", got, want)
	}
	f.calls = nil
	for _, bad := range []string{"", "workspace:3", "--help"} {
		if err := c.SetPinned(bad, true); !errors.Is(err, ErrInvalidArg) {
			t.Errorf("SetPinned(%q) = %v, want ErrInvalidArg", bad, err)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("an invalid target reached cmux: %q", f.joined())
	}
}

// D39 b: one workspace moved alone, right after (before) a
// workspace of its own group, by UUID only.
func TestPlaceWorkspaceArgs(t *testing.T) {
	const win, ws, ref = "2D7A4724-6273-4254-ABD3-1DA3AB4FAF3C", "80914656-BF88-4780-9982-CA1665755494", "B89B4597-2397-45F6-81D9-95F3B0FA83F8"
	f := &fakeRun{}
	c := FoldersCLI{Run: f.run}
	if err := c.PlaceWorkspace(win, ws, ref, false); err != nil {
		t.Fatal(err)
	}
	if err := c.PlaceWorkspace(win, ws, ref, true); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"reorder-workspace --workspace " + ws + " --after " + ref + " --window " + win,
		"reorder-workspace --workspace " + ws + " --before " + ref + " --window " + win,
	}
	if got := f.joined(); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv =\n%q\nwant\n%q", got, want)
	}
	for name, args := range map[string][3]string{
		"bad window":   {"window:1", ws, ref},
		"ref target":   {win, "workspace:3", ref},
		"ref ref":      {win, ws, "workspace:3"},
		"flag ref":     {win, ws, "--index"},
		"flag target":  {win, "--index", ref},
		"same ws, ref": {win, ws, strings.ToLower(ws)},
	} {
		g := &fakeRun{}
		if err := (FoldersCLI{Run: g.run}).PlaceWorkspace(args[0], args[1], args[2], false); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if len(g.joined()) != 0 {
			t.Errorf("%s: cmux called: %q", name, g.joined())
		}
	}
}
