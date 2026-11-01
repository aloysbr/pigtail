// Package tui implements the pigtail terminal UI using bubbletea + lipgloss.
//
// The triage view is the main surface: a split-panel layout with a navigable
// workspace list on the left and workspace details on the right, with
// keyboard-driven actions (attach, reply, dev toggle, clean, diff).
package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Run starts the TUI event loop bound to the given client. It blocks until
// the user quits.
func Run(client *pigtail.Client, projectID string) error {
	m := initialModel(client, projectID)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
