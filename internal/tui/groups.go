package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

type group struct {
	label string
	icon  string
	items []pigtail.Workspace
	style lipgloss.Style
}

type flatItem struct {
	groupIdx int
	itemIdx  int
	ws       *pigtail.Workspace
}

func computeGroups(ws []pigtail.Workspace) []group {
	var attention, ready, running, shipped []pigtail.Workspace

	for _, w := range ws {
		switch {
		case needsAttention(w):
			attention = append(attention, w)
		case isReady(w):
			ready = append(ready, w)
		case isActive(w):
			running = append(running, w)
		default:
			shipped = append(shipped, w)
		}
	}

	return []group{
		{label: "ATTENTION", icon: "🔴", items: attention, style: groupAttentionStyle},
		{label: "READY TO REVIEW", icon: "🟡", items: ready, style: groupReadyStyle},
		{label: "RUNNING", icon: "🟢", items: running, style: groupRunningStyle},
		{label: "SHIPPED", icon: "⚪", items: shipped, style: groupShippedStyle},
	}
}

func flattenGroups(groups []group) []flatItem {
	var out []flatItem
	for gi, g := range groups {
		for ii := range g.items {
			out = append(out, flatItem{groupIdx: gi, itemIdx: ii, ws: &groups[gi].items[ii]})
		}
	}
	return out
}

func needsAttention(w pigtail.Workspace) bool {
	for _, s := range w.AgentSessions {
		switch s.State {
		case pigtail.AgentStatePausedInput, pigtail.AgentStatePausedPlan, pigtail.AgentStateCrashed:
			return true
		}
	}
	return false
}

func isReady(w pigtail.Workspace) bool {
	return w.State == pigtail.StateReadyToReview || w.State == pigtail.StateReadyToShip
}

func isActive(w pigtail.Workspace) bool {
	switch w.State {
	case pigtail.StatePlanning, pigtail.StateBuilding, pigtail.StateReviewing:
		return true
	}
	return false
}
