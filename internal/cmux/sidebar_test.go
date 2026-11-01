package cmux

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const testWS = "21138669-40DD-479A-BA89-B2D91A9AABB3"

// fakeRun records every argv and answers from a table keyed on the first
// argument(s), so the primitives are exercised without a cmux binary.
type fakeRun struct {
	calls [][]string
	out   map[string]string
	err   error
}

func (f *fakeRun) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if f.err != nil {
		return nil, f.err
	}
	return []byte(f.out[strings.Join(args, " ")]), nil
}

// joined returns every recorded call, its arguments joined by a space.
func (f *fakeRun) joined() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

func TestSidebarCLIArgs(t *testing.T) {
	f := &fakeRun{}
	sb := SidebarCLI{Run: f.run}
	steps := []struct {
		do   func() error
		want []string
	}{
		{func() error { return sb.Rename(testWS, "Sidebar pigtail") },
			[]string{"workspace-action", "--action", "rename", "--workspace", testWS, "--title", "Sidebar pigtail"}},
		{func() error { return sb.ClearName(testWS) },
			[]string{"workspace-action", "--action", "clear-name", "--workspace", testWS}},
		{func() error { return sb.SetDescription(testWS, "Method · implement · #3") },
			[]string{"workspace-action", "--action", "set-description", "--workspace", testWS, "--description", "Method · implement · #3"}},
		{func() error { return sb.ClearDescription(testWS) },
			[]string{"workspace-action", "--action", "clear-description", "--workspace", testWS}},
		{func() error {
			return sb.SetStatus(testWS, StatusEntry{Key: "pigtail-attn", Value: "⏳ en attente de toi", Icon: "bell.fill", Color: "#FF9500", Priority: 90})
		},
			[]string{"set-status", "pigtail-attn", "⏳ en attente de toi", "--icon", "bell.fill", "--color", "#FF9500", "--priority", "90", "--workspace", testWS}},
		{func() error { return sb.SetStatus(testWS, StatusEntry{Key: "pigtail-srv", Value: "▶ dev :5173 up"}) },
			[]string{"set-status", "pigtail-srv", "▶ dev :5173 up", "--priority", "0", "--workspace", testWS}},
		{func() error { return sb.ClearStatus(testWS, "pigtail-attn") },
			[]string{"clear-status", "pigtail-attn", "--workspace", testWS}},
	}
	for i, s := range steps {
		if err := s.do(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if got := f.calls[len(f.calls)-1]; !reflect.DeepEqual(got, s.want) {
			t.Errorf("step %d argv = %q, want %q", i, got, s.want)
		}
	}
}

// Every primitive targets an explicit workspace UUID: without --workspace,
// cmux falls back to the "current" workspace, which under launchd is
// whatever is selected — never what the loop meant (spec C4).
func TestSidebarCLIRejectsBadArguments(t *testing.T) {
	f := &fakeRun{}
	sb := SidebarCLI{Run: f.run}
	bad := map[string]func() error{
		"empty workspace":           func() error { return sb.Rename("", "x") },
		"ref, not uuid":             func() error { return sb.ClearName("workspace:3") },
		"flag-like workspace":       func() error { return sb.ClearDescription("--window") },
		"title as a flag":           func() error { return sb.Rename(testWS, "--help") },
		"empty title":               func() error { return sb.Rename(testWS, "") },
		"multi-line title":          func() error { return sb.Rename(testWS, "a\nb") },
		"empty description":         func() error { return sb.SetDescription(testWS, " ") },
		"value as a flag":           func() error { return sb.SetStatus(testWS, StatusEntry{Key: "pigtail-attn", Value: "--icon"}) },
		"key as a flag":             func() error { return sb.SetStatus(testWS, StatusEntry{Key: "--workspace", Value: "x"}) },
		"key with space":            func() error { return sb.ClearStatus(testWS, "pigtail attn") },
		"icon with space":           func() error { return sb.SetStatus(testWS, StatusEntry{Key: "k", Value: "v", Icon: "a b"}) },
		"color not hex":             func() error { return sb.SetStatus(testWS, StatusEntry{Key: "k", Value: "v", Color: "red"}) },
		"list-status ref":           func() error { _, err := sb.ListStatus("workspace:1"); return err },
		"workspace list w/o window": func() error { _, err := sb.ListWorkspaces(""); return err },
		"tree w/o window":           func() error { _, err := sb.ListSurfaces(""); return err },
		"tree with a window ref":    func() error { _, err := sb.ListSurfaces("window:1"); return err },
		"tree with a flag":          func() error { _, err := sb.ListSurfaces("--all"); return err },
	}
	for name, do := range bad {
		if err := do(); !errors.Is(err, ErrInvalidArg) {
			t.Errorf("%s: err = %v, want ErrInvalidArg", name, err)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("invalid arguments reached cmux: %q", f.calls)
	}
}

func TestParseWindowsRecorded(t *testing.T) {
	b := readTestdata(t, "list-windows.json")
	ws, err := ParseWindows(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 || ws[1].ID != "2D7A4724-6273-4254-ABD3-1DA3AB4FAF3C" || ws[1].Index != 1 || ws[1].WorkspaceCount != 2 || !ws[0].Key {
		t.Fatalf("windows = %+v", ws)
	}
	if _, err := ParseWindows([]byte("Error: socket not found")); err == nil {
		t.Fatal("non-JSON output must be an error, never an empty window list")
	}
}

func TestParseWorkspacesRecorded(t *testing.T) {
	b := readTestdata(t, "workspace-list.json")
	ws, err := ParseWorkspaces(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 {
		t.Fatalf("workspaces = %+v", ws)
	}
	auto, named := ws[0], ws[1]
	if auto.ID != "FA706FC3-D5E3-49FB-A623-5AF8229F4D74" || auto.Ref != "workspace:159" || auto.HasCustomTitle || auto.CustomTitle != "" || auto.Description != "" || !auto.Selected {
		t.Errorf("auto-titled workspace = %+v", auto)
	}
	if named.ID != testWS || !named.HasCustomTitle || named.CustomTitle != "Sidebar pigtail" || named.Title != "Sidebar pigtail" || named.Description != "perso · implement · #3" {
		t.Errorf("renamed workspace = %+v", named)
	}
	if _, err := ParseWorkspaces([]byte(`{"window_ref":"window:1"}`)); err == nil {
		t.Fatal("output without a workspaces array must be an error")
	}
}

func TestParseWorkspacesNormalisesIDs(t *testing.T) {
	ws, err := ParseWorkspaces([]byte(`{"workspaces":[{"id":"fa706fc3-d5e3-49fb-a623-5af8229f4d74","title":"x"}]}`))
	if err != nil || ws[0].ID != "FA706FC3-D5E3-49FB-A623-5AF8229F4D74" {
		t.Fatalf("ws = %+v err = %v", ws, err)
	}
}

// Recorded on cmux 0.64.24 with a throwaway window: one workspace split in
// two panes (two surfaces), one fresh workspace (one surface).
func TestParseTreeRecorded(t *testing.T) {
	got, err := ParseTree(readTestdata(t, "tree-window.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"038F8191-1FB7-460C-8A6C-694537CBCD15": {"D88F7C3A-D946-4B07-95D1-5386FD868CDC", "ECC3DA84-E0A3-48D5-8A25-5DD8DFF93815"},
		"C3561DB5-8BAD-4884-BA28-1E13AADDAB76": {"EE057537-E422-4E17-8A6C-A96369A747BA"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

func TestParseTreeNormalisesAndRejects(t *testing.T) {
	got, err := ParseTree([]byte(`{"windows":[{"workspaces":[{"id":"fa706fc3-d5e3-49fb-a623-5af8229f4d74","panes":[{"surfaces":[{"id":"1c678000-6490-4937-b5c3-9fa4144f124f"}]}]}]}]}`))
	want := map[string][]string{"FA706FC3-D5E3-49FB-A623-5AF8229F4D74": {"1C678000-6490-4937-B5C3-9FA4144F124F"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, %v", got, err)
	}
	// An error must never read as "this window has no surface": the loop
	// would then trust an empty set.
	for _, bad := range []string{
		"Error: not_found: Window not found.",
		`{"caller":{}}`,
		`{"windows":[{"workspaces":[{"id":"","panes":[]}]}]}`,
		`{"windows":[{"workspaces":[{"id":"FA706FC3-D5E3-49FB-A623-5AF8229F4D74"}]}]}`,
	} {
		if got, err := ParseTree([]byte(bad)); err == nil {
			t.Errorf("ParseTree(%q) = %v, want an error", bad, got)
		}
	}
}

func TestParseStatusListRecorded(t *testing.T) {
	got := ParseStatusList(string(readTestdata(t, "list-status.txt")))
	want := []StatusEntry{
		{Key: "pigtail-attn", Value: "⏳ en attente de toi", Icon: "bell.fill", Color: "#FF9500", Priority: 90},
		{Key: "pigtail-srv", Value: "▶ dev :5173 up", Icon: "play.fill", Color: "#34C759", Priority: 60},
		{Key: "other_tool", Value: "Running"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if got := ParseStatusList(string(readTestdata(t, "list-status-empty.txt"))); len(got) != 0 {
		t.Fatalf("empty list = %+v", got)
	}
}

func TestParseStatusListValueWithEquals(t *testing.T) {
	got := ParseStatusList("k=a=b c priority=5\n")
	if len(got) != 1 || got[0].Value != "a=b c" || got[0].Priority != 5 {
		t.Fatalf("got %+v", got)
	}
}

func TestSidebarCLIReads(t *testing.T) {
	const win = "2D7A4724-6273-4254-ABD3-1DA3AB4FAF3C"
	f := &fakeRun{out: map[string]string{
		"list-windows --json":                               string(readTestdata(t, "list-windows.json")),
		"workspace list --window " + win + " --json":        string(readTestdata(t, "workspace-list.json")),
		"list-status --workspace " + testWS:                 string(readTestdata(t, "list-status.txt")),
		"tree --window " + win + " --json --id-format both": string(readTestdata(t, "tree-window.json")),
	}}
	sb := SidebarCLI{Run: f.run}
	wins, err := sb.ListWindows()
	if err != nil || len(wins) != 2 {
		t.Fatalf("ListWindows = %+v, %v", wins, err)
	}
	wss, err := sb.ListWorkspaces(win)
	if err != nil || len(wss) != 2 {
		t.Fatalf("ListWorkspaces = %+v, %v", wss, err)
	}
	st, err := sb.ListStatus(testWS)
	if err != nil || len(st) != 3 {
		t.Fatalf("ListStatus = %+v, %v", st, err)
	}
	sf, err := sb.ListSurfaces(win)
	if err != nil || len(sf) != 2 {
		t.Fatalf("ListSurfaces = %+v, %v", sf, err)
	}

	f.err = errors.New("boom")
	if _, err := sb.ListWindows(); err == nil {
		t.Fatal("a failed call must surface as an error, never an empty list")
	}
	if _, err := sb.ListSurfaces(win); err == nil {
		t.Fatal("a failed tree must surface as an error, never an empty set")
	}
}

// D2 (sub_x000000000000041, 2026-10-09) lifts « pigtail never reorders »
// (2026-09-30) for the folder arrangement only: only folders.go spells a
// reorder or a group write, spawn.go may list groups, nothing closes a
// workspace, and the sidebar primitives still emit none of it.
func TestOnlyFoldersReorderOrGroup(t *testing.T) {
	allowed := map[string][]string{
		"internal/cmux/folders.go": {"reorder-workspace", "workspace-group"},
		"internal/cmux/spawn.go":   {"workspace-group"}, // GroupOf: `workspace-group list`
	}
	root := moduleRoot(t)
	walked := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "bin", "macapp", "clients":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		walked++
		b, _ := os.ReadFile(path)
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, word := range []string{"reorder-workspace", "workspace-group", "close-workspace", "--close-workspaces"} {
			if !strings.Contains(string(b), word) || slices.Contains(allowed[rel], word) {
				continue
			}
			t.Errorf("%s spells %q: only internal/cmux/folders.go may (D2)", rel, word)
		}
		return nil
	})
	if walked == 0 {
		t.Fatal("walked no Go file")
	}
	f := &fakeRun{out: map[string]string{}}
	sb := SidebarCLI{Run: f.run}
	_ = sb.Rename(testWS, "t")
	_ = sb.ClearName(testWS)
	_ = sb.SetDescription(testWS, "d")
	_ = sb.ClearDescription(testWS)
	_ = sb.SetStatus(testWS, StatusEntry{Key: "pigtail-attn", Value: "v"})
	_ = sb.ClearStatus(testWS, "pigtail-attn")
	for _, c := range f.calls {
		for _, a := range c {
			if strings.Contains(a, "reorder") || strings.Contains(a, "group") || strings.Contains(a, "move-") {
				t.Errorf("sidebar primitive emitted %q", c)
			}
		}
	}
}

// Only internal/cmux/folders.go writes a pin (D3): no other Go file spells
// "pin" or "unpin" as a workspace action.
func TestOnlyFoldersPin(t *testing.T) {
	re := regexp.MustCompile(`"(un)?pin"`)
	root := moduleRoot(t)
	walked := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "bin", "macapp", "clients":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		walked++
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if rel == "internal/cmux/folders.go" {
			return nil
		}
		b, _ := os.ReadFile(path)
		if re.Match(b) {
			t.Errorf("%s spells a pin action: only internal/cmux/folders.go may (D3)", rel)
		}
		return nil
	})
	if walked == 0 {
		t.Fatal("walked no Go file")
	}
	b, err := os.ReadFile(filepath.Join(root, "internal/cmux/folders.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !re.Match(b) {
		t.Fatal("internal/cmux/folders.go spells no pin action: the guard reads nothing")
	}
}

func TestCliPathFallsBackToAppBundle(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "cmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := bundledCLIFallback
	t.Cleanup(func() { bundledCLIFallback = old })
	bundledCLIFallback = bin

	// launchd: neither the cmux variable nor a PATH entry.
	t.Setenv(EnvBundledCLI, "restored-on-cleanup")
	_ = os.Unsetenv(EnvBundledCLI)
	t.Setenv("PATH", t.TempDir())
	if got, err := cliPath(); err != nil || got != bin {
		t.Fatalf("cliPath = %q, %v; want the app bundle %q", got, err, bin)
	}

	// Not executable: no fallback.
	if err := os.Chmod(bin, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := cliPath(); err == nil {
		t.Fatal("a non-executable bundle path must not be used")
	}

	// The variable set but empty means "no cmux here": hermetic tests rely
	// on it, so it disables the bundle fallback too.
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvBundledCLI, "")
	if _, err := cliPath(); err == nil {
		t.Fatal("CMUX_BUNDLED_CLI_PATH=\"\" must disable the bundle fallback")
	}

	// The variable wins over everything.
	t.Setenv(EnvBundledCLI, "/opt/cmux")
	if got, _ := cliPath(); got != "/opt/cmux" {
		t.Fatalf("cliPath = %q, want the variable", got)
	}
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// pigtail posts no cmux notification (D2, D4): the sidebar is the only
// channel. No production file may spell the notify command.
func TestNeverNotifies(t *testing.T) {
	root := moduleRoot(t)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), `"notify"`) {
			t.Errorf("%s spells the cmux notify command: pigtail posts no notification", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// D13 (sub_x000000000000041): a pinned workspace is never filed. `pinned` is
// read on its row only; every other field reads as before (the pills and
// titles of this page are a regressed zone).
func TestListWorkspacesReadsPinned(t *testing.T) {
	const raw = `{"workspaces":[` +
		`{"id":"fa706fc3-d5e3-49fb-a623-5af8229f4d74","title":"a","selected":true,"description":"d","pinned":true},` +
		`{"id":"` + testWS + `","title":"Sidebar pigtail","custom_title":"Sidebar pigtail","has_custom_title":true,"description":"perso · implement · #3"}]}`
	ws, err := ParseWorkspaces([]byte(raw))
	if err != nil || len(ws) != 2 {
		t.Fatalf("ParseWorkspaces = %+v, %v", ws, err)
	}
	if !ws[0].Pinned || ws[1].Pinned {
		t.Fatalf("pinned = %v, %v; want true, false", ws[0].Pinned, ws[1].Pinned)
	}
	a := ws[0]
	if a.ID != "FA706FC3-D5E3-49FB-A623-5AF8229F4D74" || a.Title != "a" || !a.Selected || a.Description != "d" {
		t.Errorf("pinned row, other fields = %+v", a)
	}
	b := ws[1]
	if b.ID != testWS || b.Title != "Sidebar pigtail" || !b.HasCustomTitle || b.CustomTitle != "Sidebar pigtail" || b.Selected || b.Description != "perso · implement · #3" {
		t.Errorf("unpinned row = %+v", b)
	}
}
