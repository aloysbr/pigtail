package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func renderList(m model, width, height int) string {
	var lines []string

	flatIdx := 0
	for _, g := range m.groups {
		header := g.style.Render(fmt.Sprintf("%s %s (%d)", g.icon, g.label, len(g.items)))
		lines = append(lines, header)

		if len(g.items) == 0 {
			lines = append(lines, dimStyle.Render("  (none)"))
		}

		for _, w := range g.items {
			selected := flatIdx == m.cursor
			pending := m.portPending[w.ID]
			lines = append(lines, renderListItem(w, m.now, m.portStatus[w.ID], pending, width, selected)...)
			flatIdx++
		}
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// renderListItem renders one workspace: a workspace line plus, when two or
// more agents are live, one indented sub-line per live session. With 0 or 1
// live agent it falls back to a single status sub-line. Only the workspace
// line participates in selection; sub-lines are not separately selectable.
func renderListItem(w pigtail.Workspace, now time.Time, ports portInfo, pending string, maxW int, selected bool) []string {
	branch := w.DisplayRef
	age := humaneAge(now.Sub(w.LastActivityAt))

	portChar := ""
	if ports.DevPort > 0 {
		if pending == "dev" || pending == "storybook" {
			portChar = "~"
		} else if ports.DevAlive {
			portChar = "+"
		} else {
			portChar = "o"
		}
	}

	live := liveSessions(w)

	// Right side: a single agent shows "kind state"; several show an
	// aggregate like "2 agents · 1* 1!".
	agentPlain := ""
	switch {
	case len(live) == 1:
		agentPlain = live[0].AgentKind + " " + stateChar(live[0].State)
	case len(live) > 1:
		agentPlain = agentAggregate(live)
	}

	// Build right side as plain text for correct padding.
	var rightParts []string
	if portChar != "" {
		rightParts = append(rightParts, portChar)
	}
	if agentPlain != "" {
		rightParts = append(rightParts, agentPlain)
	}
	rightParts = append(rightParts, age)
	rightPlain := strings.Join(rightParts, "  ")

	prefix := "  "
	if selected {
		prefix = "> "
	}

	branchW := maxW - len(rightPlain) - len(prefix) - 2
	if branchW < 10 {
		branchW = 10
	}
	branchStr := truncStr(branch, branchW)

	line1 := fmt.Sprintf("%s%-*s  %s", prefix, branchW, branchStr, rightPlain)

	if selected {
		line1 = selectedStyle.Width(maxW).Render(line1)
	} else {
		// Colorize port indicator and dim the age.
		switch portChar {
		case "~":
			pendingStyle := lipgloss.NewStyle().Foreground(readyColor)
			line1 = strings.Replace(line1, "~  "+age, pendingStyle.Render("◐")+"  "+dimStyle.Render(age), 1)
		case "+":
			line1 = strings.Replace(line1, "+  "+age, portAliveStyle.Render("●")+"  "+dimStyle.Render(age), 1)
		case "o":
			line1 = strings.Replace(line1, "o  "+age, portDeadStyle.Render("○")+"  "+dimStyle.Render(age), 1)
		default:
			line1 = strings.Replace(line1, age, dimStyle.Render(age), 1)
		}
	}

	out := []string{line1}

	if len(live) > 1 {
		// One indented sub-line per live session.
		for i := range live {
			sub := renderAgentSubline(live[i], now, maxW-4)
			line := fmt.Sprintf("    %-*s", maxW-4, truncStr(sub, maxW-4))
			out = append(out, dimStyle.Render(line))
		}
	} else {
		// 0 or 1 agent: single status sub-line (legacy behavior).
		sub := renderSubline(w, now)
		line2 := fmt.Sprintf("  %-*s", maxW-2, truncStr(sub, maxW-2))
		if selected {
			line2 = selectedStyle.Width(maxW).Render(line2)
		} else {
			line2 = dimStyle.Render(line2)
		}
		out = append(out, line2)
	}

	return out
}

// liveSessions returns the workspace's agent sessions that still occupy it
// (running or paused waiting on the user).
func liveSessions(w pigtail.Workspace) []pigtail.AgentSession {
	var out []pigtail.AgentSession
	for _, s := range w.AgentSessions {
		switch s.State {
		case pigtail.AgentStateRunning, pigtail.AgentStatePausedInput, pigtail.AgentStatePausedPlan:
			out = append(out, s)
		}
	}
	return out
}

// agentAggregate summarizes several live agents, e.g. "2 agents · 1* 1!".
func agentAggregate(live []pigtail.AgentSession) string {
	counts := map[string]int{}
	order := []string{}
	for _, s := range live {
		c := stateChar(s.State)
		if _, ok := counts[c]; !ok {
			order = append(order, c)
		}
		counts[c]++
	}
	parts := make([]string, 0, len(order))
	for _, c := range order {
		parts = append(parts, fmt.Sprintf("%d%s", counts[c], c))
	}
	return fmt.Sprintf("%d agents · %s", len(live), strings.Join(parts, " "))
}

// renderAgentSubline renders one live session's sub-line: kind · state ·
// last-event age · intent excerpt.
func renderAgentSubline(s pigtail.AgentSession, now time.Time, width int) string {
	parts := []string{s.AgentKind, stateChar(s.State), humaneAge(now.Sub(s.LastEventAt))}
	head := strings.Join(parts, " · ")
	if s.Intent != "" {
		excerptW := width - len(head) - 3
		if excerptW > 4 {
			head += " · " + truncStr(s.Intent, excerptW)
		}
	}
	return head
}

func stateChar(s pigtail.AgentState) string {
	switch s {
	case pigtail.AgentStateRunning:
		return "*"
	case pigtail.AgentStatePausedInput, pigtail.AgentStatePausedPlan:
		return "!"
	case pigtail.AgentStateDone:
		return "+"
	case pigtail.AgentStateCrashed:
		return "x"
	default:
		return "·"
	}
}

func renderSubline(w pigtail.Workspace, now time.Time) string {
	for _, s := range w.AgentSessions {
		switch s.State {
		case pigtail.AgentStatePausedInput:
			return "waiting for input"
		case pigtail.AgentStatePausedPlan:
			return "waiting for plan approval"
		case pigtail.AgentStateCrashed:
			return "agent crashed"
		case pigtail.AgentStateDone:
			return agentDoneSummary(w)
		case pigtail.AgentStateRunning:
			return "agent working…"
		}
	}
	if w.LastRunawayAt != nil && now.Sub(*w.LastRunawayAt) < time.Hour {
		return "⚠ runaway dev server killed " + humaneAge(now.Sub(*w.LastRunawayAt)) + " ago"
	}
	return string(w.State)
}

func agentDoneSummary(w pigtail.Workspace) string {
	if w.DiffSummary != nil {
		return fmt.Sprintf("done · +%d −%d · %d files", w.DiffSummary.Additions, w.DiffSummary.Deletions, w.DiffSummary.Files)
	}
	return "done"
}

//nolint:unused // retained for the triage list view under active development
func stateIndicator(s pigtail.AgentState) string {
	switch s {
	case pigtail.AgentStateRunning:
		return lipgloss.NewStyle().Foreground(runningColor).Render("⠋")
	case pigtail.AgentStatePausedInput, pigtail.AgentStatePausedPlan:
		return lipgloss.NewStyle().Foreground(readyColor).Render("⏸")
	case pigtail.AgentStateDone:
		return lipgloss.NewStyle().Foreground(runningColor).Render("✓")
	case pigtail.AgentStateCrashed:
		return lipgloss.NewStyle().Foreground(attentionColor).Render("✗")
	default:
		return "·"
	}
}

func humaneAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func truncStr(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
