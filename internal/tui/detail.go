package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func renderDetail(m model, ws *pigtail.Workspace, width, height int) string {
	if m.dispatching {
		return renderDispatchForm(m, width)
	}
	if m.confirming != nil {
		return renderPreflight(m.confirming, width)
	}
	if m.selecting != nil {
		return renderAgentSelector(m.selecting, m.now, width)
	}
	if ws == nil {
		return dimStyle.Render("\n  Select a workspace to see details")
	}

	var b strings.Builder
	branch := ws.DisplayRef

	b.WriteString(detailHeaderStyle.Render(branch))
	b.WriteString("\n\n")

	// State
	row(&b, "state", string(ws.State))

	// Branch
	base, _ := ws.BackendData["base_branch"].(string)
	if base == "" {
		base = "main"
	}
	row(&b, "branch", fmt.Sprintf("%s (base: %s)", branch, base))

	// Ports
	ports := m.portStatus[ws.ID]
	pending := m.portPending[ws.ID]
	portLine := renderPorts(ws, ports, pending)
	if portLine != "" {
		row(&b, "ports", portLine)
	}

	// Agents — one block per session (several may share the workspace).
	if len(ws.AgentSessions) > 0 {
		for i := range ws.AgentSessions {
			s := ws.AgentSessions[i]
			label := "agent"
			if len(ws.AgentSessions) > 1 {
				label = fmt.Sprintf("agent %d", i+1)
			}
			age := humaneAge(m.now.Sub(s.StartedAt))
			row(&b, label, fmt.Sprintf("%s · %s · %s · started %s ago · %d msgs",
				s.ID, s.AgentKind, s.State, age, s.MessageCount))
			if s.Intent != "" {
				row(&b, "  intent", truncStr(s.Intent, maxIntentWidth(width)))
			}
			if s.TmuxSessionName != "" {
				row(&b, "  tmux", s.TmuxSessionName)
			}
		}
	} else {
		row(&b, "agent", dimStyle.Render("none"))
	}

	// Diff
	if ws.DiffSummary != nil {
		row(&b, "diff", fmt.Sprintf("+%d −%d · %d files",
			ws.DiffSummary.Additions, ws.DiffSummary.Deletions, ws.DiffSummary.Files))
	}

	// Runaway dev-server kill by the watchdog
	if ws.LastRunawayAt != nil {
		row(&b, "runaway", verdictWarnStyle.Render(fmt.Sprintf("⚠ dev server killed %s ago (%s)",
			humaneAge(m.now.Sub(*ws.LastRunawayAt)), ws.LastRunawayReason)))
	}

	// Path (truncate to fit panel)
	maxValW := width - 12
	p := shortPath(ws.Path)
	if maxValW > 0 && len(p) > maxValW {
		p = "…" + p[len(p)-maxValW+1:]
	}
	row(&b, "path", p)

	// Workspace ID
	row(&b, "id", ws.ID)

	// Activity
	b.WriteString("\n")
	b.WriteString(detailLabelStyle.Render("activity"))
	b.WriteString("\n")
	age := m.now.Sub(ws.LastActivityAt)
	b.WriteString(dimStyle.Render(fmt.Sprintf("  last activity %s ago", humaneAge(age))))
	b.WriteString("\n")
	created := m.now.Sub(ws.CreatedAt)
	b.WriteString(dimStyle.Render(fmt.Sprintf("  created %s ago", humaneAge(created))))

	return b.String()
}

// renderAgentSelector renders the multi-agent attach overlay: one row per
// live session (agt_ id · kind · state · intent excerpt), with the cursor row
// highlighted. Attach-only — replying to a specific agent goes through the CLI.
func renderAgentSelector(sel *agentSelector, now time.Time, width int) string {
	var b strings.Builder
	b.WriteString(detailHeaderStyle.Render("🤝 Attach to which agent?"))
	b.WriteString("\n\n")
	for i := range sel.sessions {
		s := sel.sessions[i]
		row := fmt.Sprintf("%s · %s · %s", s.ID, s.AgentKind, stateChar(s.State))
		if s.Intent != "" {
			row += " · " + truncStr(s.Intent, maxIntentWidth(width))
		}
		prefix := "  "
		if i == sel.cursor {
			prefix = "> "
		}
		line := prefix + row
		if i == sel.cursor {
			b.WriteString(selectedStyle.Render(line))
		} else {
			b.WriteString(dimStyle.Render(line))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  ↑/↓ move · ⏎ attach · esc cancel"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  (to reply to one agent: pigtail reply <agt_id>)"))
	return b.String()
}

var pendingStyle = lipgloss.NewStyle().Foreground(readyColor)

func renderPorts(ws *pigtail.Workspace, ports portInfo, pending string) string {
	var parts []string
	if p, ok := ws.Ports["dev"]; ok {
		indicator := portDeadStyle.Render("○")
		if pending == "dev" {
			indicator = pendingStyle.Render("◐")
		} else if ports.DevAlive {
			indicator = portAliveStyle.Render("●")
		}
		parts = append(parts, fmt.Sprintf("dev %s :%d", indicator, p))
	}
	if p, ok := ws.Ports["storybook"]; ok {
		indicator := portDeadStyle.Render("○")
		if pending == "storybook" {
			indicator = pendingStyle.Render("◐")
		} else if ports.SBAlive {
			indicator = portAliveStyle.Render("●")
		}
		parts = append(parts, fmt.Sprintf("sb %s :%d", indicator, p))
	}
	return strings.Join(parts, "  ")
}

func shortPath(p string) string {
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(p, home) {
		p = "~" + p[len(home):]
	}
	return p
}

// maxIntentWidth bounds the intent excerpt shown in the detail panel.
func maxIntentWidth(width int) int {
	w := width - 14
	if w < 10 {
		w = 10
	}
	return w
}

func row(b *strings.Builder, label, value string) {
	b.WriteString(detailLabelStyle.Render(label))
	b.WriteString(detailValueStyle.Render(value))
	b.WriteString("\n")
}

//nolint:unused // helper for the detail view under active development
func humaneAgeDuration(d time.Duration) string {
	return humaneAge(d)
}

func renderDispatchForm(m model, width int) string {
	var b strings.Builder
	b.WriteString(detailHeaderStyle.Render("🚀 New workspace"))
	b.WriteString("\n\n")
	b.WriteString(detailLabelStyle.Render("intent"))
	b.WriteString("\n")
	b.WriteString("  " + m.dispatchInput.View())
	b.WriteString("\n\n")
	b.WriteString(dimStyle.Render("  enter ↵ dispatch  ·  esc cancel"))
	return b.String()
}

func renderPreflight(pf *cleanPreflight, width int) string {
	var b strings.Builder

	b.WriteString(preflightHeaderStyle.Render("🧹 Clean " + pf.branch + "?"))
	b.WriteString("\n\n")

	// Git status
	gitParts := []string{fmt.Sprintf("%d ahead", pf.ahead)}
	if pf.behind > 0 {
		gitParts = append(gitParts, fmt.Sprintf("%d behind", pf.behind))
	}
	if pf.clean {
		gitParts = append(gitParts, "clean")
	} else {
		gitParts = append(gitParts, "dirty")
	}
	row(&b, "git", strings.Join(gitParts, " · "))

	// Merged
	if pf.merged {
		row(&b, "merged", verdictSafeStyle.Render("✓ yes"))
	} else {
		row(&b, "merged", verdictWarnStyle.Render("✗ no"))
	}

	// Dirty files
	if len(pf.dirty) > 0 {
		row(&b, "changed", strings.Join(pf.dirty, ", "))
	}

	// Recent commits
	if len(pf.commits) > 0 {
		b.WriteString("\n")
		row(&b, "commits", "")
		max := 5
		if len(pf.commits) < max {
			max = len(pf.commits)
		}
		for _, c := range pf.commits[:max] {
			b.WriteString(dimStyle.Render("          " + c))
			b.WriteString("\n")
		}
		if len(pf.commits) > max {
			b.WriteString(dimStyle.Render(fmt.Sprintf("          ... +%d more", len(pf.commits)-max)))
			b.WriteString("\n")
		}
	}

	// Delivery / PR
	if pf.delivery != nil {
		prLine := pf.delivery.state
		if pf.delivery.ciStatus != "" && pf.delivery.ciStatus != "none" {
			ciIcon := "?"
			switch pf.delivery.ciStatus {
			case "success":
				ciIcon = verdictSafeStyle.Render("✓")
			case "failure":
				ciIcon = verdictDangerStyle.Render("✗")
			case "pending":
				ciIcon = verdictWarnStyle.Render("…")
			}
			prLine += " · CI " + ciIcon
		}
		row(&b, "PR", prLine)
	} else {
		row(&b, "PR", dimStyle.Render("none"))
	}

	b.WriteString("\n")

	// Verdict
	switch pf.verdict {
	case "safe":
		row(&b, "verdict", verdictSafeStyle.Render("✅ SAFE TO CLEAN"))
	default:
		row(&b, "verdict", verdictWarnStyle.Render("⚠️  HAS UNMERGED WORK"))
	}

	return b.String()
}
