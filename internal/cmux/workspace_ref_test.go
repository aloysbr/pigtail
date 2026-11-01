package cmux

import (
	"strings"
	"testing"
)

// Recorded `cmux workspace list --id-format both` (cmux 0.64.x).
const workspaceListBoth = `  workspace:1 B4773701-DCB2-4B51-A17A-47A181B369AF  Group 2
  workspace:4 F8290864-F5B5-4B3F-9B93-0B027295DECC  ✳ Garden create-tenant button
* workspace:105 29664df9-67b9-4aaf-9419-b1fd8e3a8994  ◑ Agents avec playwright  [selected]
`

func TestParseWorkspaceRefs(t *testing.T) {
	got := ParseWorkspaceRefs(workspaceListBoth)
	want := map[string]string{
		"workspace:1":   "B4773701-DCB2-4B51-A17A-47A181B369AF",
		"workspace:4":   "F8290864-F5B5-4B3F-9B93-0B027295DECC",
		"workspace:105": "29664DF9-67B9-4AAF-9419-B1FD8E3A8994", // upper-cased
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for ref, id := range want {
		if got[ref] != id {
			t.Errorf("%s = %q, want %q", ref, got[ref], id)
		}
	}
	if len(ParseWorkspaceRefs("garbage\nworkspace:9 not-a-uuid x\n")) != 0 {
		t.Error("garbage parsed")
	}
}

func TestResolveWorkspaceRefFrom(t *testing.T) {
	id, err := resolveWorkspaceRef("workspace:4", func() (string, error) { return workspaceListBoth, nil })
	if err != nil || id != "F8290864-F5B5-4B3F-9B93-0B027295DECC" {
		t.Fatalf("resolve = %q %v", id, err)
	}
	if _, err := resolveWorkspaceRef("workspace:7", func() (string, error) { return workspaceListBoth, nil }); err == nil || !strings.Contains(err.Error(), "workspace:7") {
		t.Fatalf("unknown ref = %v", err)
	}
}
