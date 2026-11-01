package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

func (m model) View() string {
	if m.width < 10 || m.height < 5 {
		return "terminal too small"
	}

	statusBar := m.renderStatusBar()
	helpContent := helpBar(helpContext{
		hasWorkspace: m.selectedWorkspace() != nil,
		confirming:   m.confirming != nil,
		dispatching:  m.dispatching,
		selecting:    m.selecting != nil,
	})
	help := helpBarStyle.Width(m.width).Render(" " + helpContent)

	// 2 lines reserved: status bar + help bar. Border adds 2 more (top+bottom).
	panelOuterH := m.height - 2
	panelInnerH := panelOuterH - 2
	if panelInnerH < 1 {
		panelInnerH = 1
	}

	var main string
	if m.width < 80 {
		main = m.renderSinglePanel(m.width, panelInnerH)
	} else {
		main = m.renderSplitPanels(m.width, panelInnerH)
	}

	return lipgloss.JoinVertical(lipgloss.Left, statusBar, main, help)
}

func (m model) renderStatusBar() string {
	total := len(m.workspaces)
	var running int
	for _, w := range m.workspaces {
		if isActive(w) {
			running++
		}
	}

	left := titleStyle.Render("pigtail")
	info := dimStyle.Render(fmt.Sprintf(" · %d items · %d running", total, running))

	var extra string
	if m.statusMsg != "" {
		extra = " · " + lipgloss.NewStyle().Foreground(runningColor).Bold(true).Render(m.statusMsg)
	}
	if m.err != nil {
		extra = " · " + errStyle.Render(fmt.Sprintf("error: %v", m.err))
	}

	bar := left + info + extra
	return statusBarStyle.Width(m.width).Render(bar)
}

func (m model) renderSinglePanel(width, innerH int) string {
	innerW := width - 2
	if innerW < 1 {
		innerW = 1
	}
	var content string
	switch {
	case m.dispatching:
		content = renderDispatchForm(m, innerW)
	case m.selecting != nil:
		content = renderAgentSelector(m.selecting, m.now, innerW)
	case m.confirming != nil:
		content = renderPreflight(m.confirming, innerW)
	default:
		content = renderList(m, innerW, innerH)
	}
	placed := lipgloss.Place(innerW, innerH, lipgloss.Left, lipgloss.Top, content)
	return borderStyle.Render(placed)
}

func (m model) renderSplitPanels(width, innerH int) string {
	leftOuterW := width * 42 / 100
	if leftOuterW < 40 {
		leftOuterW = 40
	}
	if leftOuterW > width-30 {
		leftOuterW = width - 30
	}
	rightOuterW := width - leftOuterW

	leftInnerW := leftOuterW - 2
	rightInnerW := rightOuterW - 2
	if leftInnerW < 1 {
		leftInnerW = 1
	}
	if rightInnerW < 1 {
		rightInnerW = 1
	}

	leftContent := renderList(m, leftInnerW, innerH)
	leftPlaced := lipgloss.Place(leftInnerW, innerH, lipgloss.Left, lipgloss.Top, leftContent)
	left := borderStyle.Render(leftPlaced)

	ws := m.selectedWorkspace()
	rightContent := renderDetail(m, ws, rightInnerW, innerH)
	rightPlaced := lipgloss.Place(rightInnerW, innerH, lipgloss.Left, lipgloss.Top, rightContent)
	right := borderStyle.Render(rightPlaced)

	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}
