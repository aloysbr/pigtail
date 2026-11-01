package main

import (
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/sessionlaunch"
	"github.com/aloysbr/pigtail/internal/spawnreg"
)

// cmuxTabLister is the listing spawn reads around a create (sidebar.go): no
// new cmux command.
type cmuxTabLister = sessionlaunch.TabLister

var (
	// spawnListTabs lists every cmux tab, for the reservation's before/after diff.
	spawnListTabs = func() ([]spawnreg.Tab, error) { return listCmuxTabsWith(cmux.SidebarCLI{}) }
	// spawnRegistryLock takes the registry lock for the short check before the create (T16).
	spawnRegistryLock = lockWithNotice
)

// listCmuxTabsWith lists the workspaces of every window, IDs in upper case
// (sessionlaunch.ListTabs).
func listCmuxTabsWith(l cmuxTabLister) ([]spawnreg.Tab, error) { return sessionlaunch.ListTabs(l) }
