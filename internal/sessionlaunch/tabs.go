package sessionlaunch

import (
	"strings"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/spawnreg"
)

// TabLister is the listing a launch reads around a create (cmux.SidebarCLI
// satisfies it): no new cmux command.
type TabLister interface {
	ListWindows() ([]cmux.Window, error)
	ListWorkspaces(windowID string) ([]cmux.WorkspaceInfo, error)
}

// ListTabs lists the workspaces of every window, IDs in upper case (the
// reservation's before/after diff compares upper-case UUIDs, as Create and
// the launch-ref do).
func ListTabs(l TabLister) ([]spawnreg.Tab, error) {
	wins, err := l.ListWindows()
	if err != nil {
		return nil, err
	}
	var tabs []spawnreg.Tab
	for _, w := range wins {
		ws, err := l.ListWorkspaces(w.ID)
		if err != nil {
			return nil, err
		}
		for _, x := range ws {
			tabs = append(tabs, spawnreg.Tab{ID: strings.ToUpper(x.ID), Ref: x.Ref})
		}
	}
	return tabs, nil
}
