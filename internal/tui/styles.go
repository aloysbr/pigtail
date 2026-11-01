package tui

import "github.com/charmbracelet/lipgloss"

var (
	attentionColor = lipgloss.Color("196")
	readyColor     = lipgloss.Color("220")
	runningColor   = lipgloss.Color("82")
	shippedColor   = lipgloss.Color("245")

	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errStyle   = lipgloss.NewStyle().Foreground(attentionColor)

	selectedStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("62")).
			Foreground(lipgloss.Color("255")).
			Bold(true)

	groupAttentionStyle = lipgloss.NewStyle().Bold(true).Foreground(attentionColor)
	groupReadyStyle     = lipgloss.NewStyle().Bold(true).Foreground(readyColor)
	groupRunningStyle   = lipgloss.NewStyle().Bold(true).Foreground(runningColor)
	groupShippedStyle   = lipgloss.NewStyle().Bold(true).Foreground(shippedColor)

	statusBarStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("252")).
			Background(lipgloss.Color("236"))

	helpBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Background(lipgloss.Color("235"))

	helpKeyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("212")).
			Background(lipgloss.Color("235")).
			Bold(true)

	helpDescStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Background(lipgloss.Color("235"))

	detailHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	detailLabelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Width(10)
	detailValueStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("255"))

	portAliveStyle = lipgloss.NewStyle().Foreground(runningColor)
	portDeadStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("238"))

	verdictSafeStyle     = lipgloss.NewStyle().Bold(true).Foreground(runningColor)
	verdictWarnStyle     = lipgloss.NewStyle().Bold(true).Foreground(readyColor)
	verdictDangerStyle   = lipgloss.NewStyle().Bold(true).Foreground(attentionColor)
	preflightHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
)
