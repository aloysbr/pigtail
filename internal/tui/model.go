package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

type model struct {
	client *pigtail.Client

	workspaces []pigtail.Workspace
	groups     []group
	flat       []flatItem
	err        error

	cursor int
	width  int
	height int
	now    time.Time

	eventCh     <-chan pigtail.Event
	portStatus  map[string]portInfo
	portPending map[string]string // workspace ID -> "dev" or "storybook" (starting/stopping)
	statusMsg   string
	confirming  *cleanPreflight

	dispatching   bool
	dispatchInput textinput.Model
	projectID     string

	// selecting is non-nil while the agent selector overlay is open (a
	// workspace has more than one live agent and the user pressed
	// attach/reply). Mirrors the m.confirming overlay pattern.
	selecting *agentSelector
}

// agentSelector is the state of the multi-agent attach overlay: the live
// sessions to choose from and the highlighted index.
type agentSelector struct {
	sessions []pigtail.AgentSession
	cursor   int
}

func initialModel(c *pigtail.Client, projectID string) model {
	ti := textinput.New()
	ti.Placeholder = "describe what you want to work on…"
	ti.CharLimit = 200
	ti.Width = 50
	return model{
		client:        c,
		now:           time.Now(),
		portStatus:    map[string]portInfo{},
		portPending:   map[string]string{},
		dispatchInput: ti,
		projectID:     projectID,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(loadWorkspaces(m.client), openStream(m.client), tick(), portTick())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		m.now = time.Time(msg)
		return m, tick()
	case portTickMsg:
		return m, tea.Batch(checkPorts(m.workspaces), portTick())
	case workspacesMsg:
		m.workspaces = []pigtail.Workspace(msg)
		m.groups = computeGroups(m.workspaces)
		m.flat = flattenGroups(m.groups)
		m.err = nil
		if m.cursor >= len(m.flat) && len(m.flat) > 0 {
			m.cursor = len(m.flat) - 1
		}
	case portStatusMsg:
		m.portStatus = map[string]portInfo(msg)
		m.portPending = map[string]string{}
	case errMsg:
		m.err = msg
	case actionDoneMsg:
		m.statusMsg = string(msg)
		return m, loadWorkspaces(m.client)
	case streamReadyMsg:
		m.eventCh = msg.ch
		return m, readNext(m.eventCh)
	case streamEventMsg:
		t := string(pigtail.Event(msg).Type)
		var cmds []tea.Cmd
		if strings.HasPrefix(t, "workspace.") || strings.HasPrefix(t, "agent.") {
			cmds = append(cmds, loadWorkspaces(m.client))
		}
		if m.eventCh != nil {
			cmds = append(cmds, readNext(m.eventCh))
		}
		return m, tea.Batch(cmds...)
	case streamClosed:
		m.eventCh = nil
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return openStreamMsg{} })
	case openStreamMsg:
		return m, openStream(m.client)
	case preflightMsg:
		m.confirming = msg.data
		m.statusMsg = ""
	case preflightErrMsg:
		m.err = msg.err
		m.statusMsg = "preflight failed"
	}
	return m, nil
}

func (m model) selectedWorkspace() *pigtail.Workspace {
	if m.cursor >= 0 && m.cursor < len(m.flat) {
		return m.flat[m.cursor].ws
	}
	return nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.dispatching {
		return m.handleDispatchKey(msg)
	}
	if m.confirming != nil {
		return m.handleConfirmKey(msg)
	}
	if m.selecting != nil {
		return m.handleSelectKey(msg)
	}
	switch {
	case key.Matches(msg, keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, keys.Down):
		if m.cursor < len(m.flat)-1 {
			m.cursor++
		}
	case key.Matches(msg, keys.Up):
		if m.cursor > 0 {
			m.cursor--
		}
	case key.Matches(msg, keys.Tab):
		m.cursor = m.nextGroupStart()
	case key.Matches(msg, keys.Enter), key.Matches(msg, keys.Reply):
		return m.attachOrSelect()
	case key.Matches(msg, keys.Dev):
		if ws := m.selectedWorkspace(); ws != nil {
			return m, execToggleServer(m.client, ws.Path, "dev", ws.ID)
		}
	case key.Matches(msg, keys.SB):
		if ws := m.selectedWorkspace(); ws != nil {
			return m, execToggleServer(m.client, ws.Path, "storybook", ws.ID)
		}
	case key.Matches(msg, keys.Diff):
		if ws := m.selectedWorkspace(); ws != nil {
			return m, execGitDiff(ws.Path)
		}
	case key.Matches(msg, keys.Clean):
		if ws := m.selectedWorkspace(); ws != nil {
			m.statusMsg = "loading preflight…"
			return m, fetchPreflight(m.client, ws.ID)
		}
	case key.Matches(msg, keys.Kill):
		if ws := m.selectedWorkspace(); ws != nil {
			return m, execClean(m.client, ws.ID)
		}
	case key.Matches(msg, keys.New):
		m.dispatching = true
		m.dispatchInput.SetValue("")
		m.dispatchInput.Focus()
		return m, m.dispatchInput.Cursor.BlinkCmd()
	case key.Matches(msg, keys.Restart):
		m.statusMsg = "restarting daemon…"
		return m, execRestartDaemon()
	}
	m.statusMsg = ""
	return m, nil
}

func (m model) handleDispatchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.dispatching = false
		m.dispatchInput.Blur()
		m.statusMsg = ""
		return m, nil
	case tea.KeyEnter:
		intent := strings.TrimSpace(m.dispatchInput.Value())
		if intent == "" {
			return m, nil
		}
		m.dispatching = false
		m.dispatchInput.Blur()
		m.statusMsg = "dispatching…"
		return m, execDispatch(m.client, m.projectID, intent)
	}
	var cmd tea.Cmd
	m.dispatchInput, cmd = m.dispatchInput.Update(msg)
	return m, cmd
}

// attachOrSelect attaches to the single live agent of the selected workspace,
// or opens the agent selector overlay when more than one agent is live.
func (m model) attachOrSelect() (tea.Model, tea.Cmd) {
	ws := m.selectedWorkspace()
	if ws == nil {
		return m, nil
	}
	live := liveSessions(*ws)
	switch {
	case len(live) == 0:
		// Fall back to any session (e.g. a finished one) so attach still works.
		if len(ws.AgentSessions) > 0 && ws.AgentSessions[0].TmuxSessionName != "" {
			return m, execTmuxAttach(ws.AgentSessions[0].TmuxSessionName)
		}
		return m, nil
	case len(live) == 1:
		if live[0].TmuxSessionName != "" {
			return m, execTmuxAttach(live[0].TmuxSessionName)
		}
		return m, nil
	default:
		m.selecting = &agentSelector{sessions: live}
		m.statusMsg = ""
		return m, nil
	}
}

// handleSelectKey drives the agent selector overlay: up/down to move, enter to
// attach to the highlighted agent, esc/cancel to close.
func (m model) handleSelectKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	sel := m.selecting
	switch {
	case key.Matches(msg, keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, keys.Cancel):
		m.selecting = nil
		return m, nil
	case key.Matches(msg, keys.Down):
		if sel.cursor < len(sel.sessions)-1 {
			sel.cursor++
		}
	case key.Matches(msg, keys.Up):
		if sel.cursor > 0 {
			sel.cursor--
		}
	case key.Matches(msg, keys.Enter):
		s := sel.sessions[sel.cursor]
		m.selecting = nil
		if s.TmuxSessionName != "" {
			return m, execTmuxAttach(s.TmuxSessionName)
		}
		return m, nil
	}
	return m, nil
}

func (m model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Confirm):
		wsID := m.confirming.wsID
		m.confirming = nil
		return m, execClean(m.client, wsID)
	case key.Matches(msg, keys.Cancel):
		m.confirming = nil
		m.statusMsg = ""
	case key.Matches(msg, keys.Quit):
		return m, tea.Quit
	}
	return m, nil
}

func (m model) nextGroupStart() int {
	if len(m.flat) == 0 {
		return 0
	}
	currentGroup := -1
	if m.cursor >= 0 && m.cursor < len(m.flat) {
		currentGroup = m.flat[m.cursor].groupIdx
	}
	for i, fi := range m.flat {
		if fi.groupIdx > currentGroup {
			return i
		}
	}
	return 0
}
