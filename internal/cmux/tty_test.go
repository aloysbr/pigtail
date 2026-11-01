package cmux

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// A trimmed real `cmux tree --all --json --id-format both` (cmux 0.64.24,
// 2026-10-09): two surfaces claim ttys034 (F9), one has no tty (null).
func TestParseSurfaceTTYs(t *testing.T) {
	b, err := os.ReadFile("testdata/tree-all-ttys.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSurfaceTTYs(b)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"05153769-6402-4B24-A818-FB21C6EE07A6": "ttys027",
		"A3506517-1A00-47C7-9D6A-BDA2A748A9E2": "ttys034",
		"5F788D65-E302-4FE2-97B8-4B3340B81EB1": "ttys034",
		"09E98A30-FCEB-4788-A0CE-E61905388572": "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseSurfaceTTYs = %v, want %v", got, want)
	}
	for _, bad := range []string{"", "Error: socket", `{"active":{}}`, `{"windows":[{"workspaces":[{"id":"W"}]}]}`} {
		if _, err := ParseSurfaceTTYs([]byte(bad)); err == nil {
			t.Errorf("%q: want an error, never an empty map", bad)
		}
	}
}

// The listing is read-only and fixed: no caller value reaches cmux's argv.
func TestSurfaceTTYsArgs(t *testing.T) {
	want := []string{"tree", "--all", "--json", "--id-format", "both"}
	if got := treeAllArgs(); !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

// D22 (sub_x000000000000041): a surface cmux never rendered is the one with
// both a null tty and render_health "not_started" (the fixture's
// 09E98A30…); every other combination is a doubt, read as rendered.
func TestParseNeverRendered(t *testing.T) {
	b, err := os.ReadFile("testdata/tree-all-ttys.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseNeverRendered(b)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{ // the three others carry a tty, though not_started
		"05153769-6402-4B24-A818-FB21C6EE07A6": false,
		"A3506517-1A00-47C7-9D6A-BDA2A748A9E2": false,
		"5F788D65-E302-4FE2-97B8-4B3340B81EB1": false,
		"09E98A30-FCEB-4788-A0CE-E61905388572": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseNeverRendered = %v, want %v", got, want)
	}
	tree := func(surface string) string {
		return `{"windows":[{"workspaces":[{"id":"W","panes":[{"surfaces":[` + surface + `]}]}]}]}`
	}
	for _, tc := range []struct {
		surface string
		never   bool
	}{
		{`{"id":"s","tty":null,"render_health":"not_started"}`, true},
		{`{"id":"s","tty":null,"render_health":"rendering"}`, false},
		{`{"id":"s","tty":null,"render_health":"stalled"}`, false},
		{`{"id":"s","tty":null,"render_health":""}`, false},
		{`{"id":"s","tty":null,"render_health":null}`, false},
		{`{"id":"s","tty":null}`, false},
		{`{"id":"s","render_health":"not_started"}`, false},
		{`{"id":"s","tty":"","render_health":"not_started"}`, false},
		{`{"id":"s","tty":"ttys003","render_health":"not_started"}`, false},
		{`{"id":"s","tty":"ttys003","render_health":"rendering"}`, false},
	} {
		got, err := ParseNeverRendered([]byte(tree(tc.surface)))
		if err != nil {
			t.Fatalf("%s: %v", tc.surface, err)
		}
		if got["S"] != tc.never {
			t.Errorf("%s: never rendered = %v, want %v", tc.surface, got["S"], tc.never)
		}
	}
	for _, bad := range []string{"", "Error: socket", `{"active":{}}`, `{"windows":[{"workspaces":[{"id":"W"}]}]}`} {
		if _, err := ParseNeverRendered([]byte(bad)); err == nil {
			t.Errorf("%q: want an error, never an empty map", bad)
		}
	}
}

// The rotation worker (sub_x000000000000008) maps the hook's surface UUID to
// its surface:N ref from the same tree SurfaceTTYs reads.
func TestParseSurfaceRefsByID(t *testing.T) {
	b, err := os.ReadFile("testdata/tree-all-ttys.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSurfaceRefsByID(b)
	if err != nil {
		t.Fatal(err)
	}
	if got["A3506517-1A00-47C7-9D6A-BDA2A748A9E2"] != "surface:5" {
		t.Fatalf("refs = %v", got)
	}
	for id, ref := range got {
		if id != strings.ToUpper(id) || !strings.HasPrefix(ref, "surface:") {
			t.Fatalf("entry %q → %q", id, ref)
		}
	}
	if _, err := ParseSurfaceRefsByID([]byte(`{}`)); err == nil {
		t.Fatal("no windows array must be an error")
	}
}
