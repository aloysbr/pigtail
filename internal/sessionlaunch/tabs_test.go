package sessionlaunch

import (
	"errors"
	"reflect"
	"testing"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/spawnreg"
)

type fakeLister struct {
	wins map[string][]cmux.WorkspaceInfo
	ids  []string
	err  error
}

func (f fakeLister) ListWindows() ([]cmux.Window, error) {
	var ws []cmux.Window
	for _, id := range f.ids {
		ws = append(ws, cmux.Window{ID: id})
	}
	return ws, nil
}

func (f fakeLister) ListWorkspaces(id string) ([]cmux.WorkspaceInfo, error) {
	if f.err != nil && id == "W2" {
		return nil, f.err
	}
	return f.wins[id], nil
}

// TestListTabsUpperCasesAcrossWindows: every window's tabs, IDs upper case.
func TestListTabsUpperCasesAcrossWindows(t *testing.T) {
	l := fakeLister{ids: []string{"W1", "W2"}, wins: map[string][]cmux.WorkspaceInfo{
		"W1": {{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Ref: "workspace:1"}},
		"W2": {{ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Ref: "workspace:2"}},
	}}
	tabs, err := ListTabs(l)
	if err != nil {
		t.Fatal(err)
	}
	want := []spawnreg.Tab{{ID: wsA, Ref: "workspace:1"}, {ID: wsB, Ref: "workspace:2"}}
	if !reflect.DeepEqual(tabs, want) {
		t.Fatalf("tabs = %v, want %v", tabs, want)
	}
	l.err = errors.New("cmux: timeout")
	if _, err := ListTabs(l); err == nil {
		t.Fatal("a window that cannot be listed must fail the listing")
	}
}
