package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func wsWithAgents(states ...pigtail.AgentState) pigtail.Workspace {
	w := pigtail.Workspace{
		ID:         "ws_x",
		State:      pigtail.StateBuilding,
		DisplayRef: "feat/x",
	}
	for i, st := range states {
		w.AgentSessions = append(w.AgentSessions, pigtail.AgentSession{
			ID:              "agt_" + string(rune('a'+i)),
			AgentKind:       "claude",
			State:           st,
			Intent:          "subject " + string(rune('A'+i)),
			TmuxSessionName: "pigtail-ws_x-agt_" + string(rune('a'+i)),
			StartedAt:       time.Now(),
			LastEventAt:     time.Now(),
		})
	}
	return w
}

func TestRenderListItemSublineCount(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name   string
		states []pigtail.AgentState
		// wantSublines is the number of lines BELOW the workspace line.
		wantSublines int
	}{
		{"zero agents", nil, 1},
		{"one live agent", []pigtail.AgentState{pigtail.AgentStateRunning}, 1},
		{"two live agents", []pigtail.AgentState{pigtail.AgentStateRunning, pigtail.AgentStatePausedInput}, 2},
		{"three live agents", []pigtail.AgentState{
			pigtail.AgentStateRunning, pigtail.AgentStateRunning, pigtail.AgentStatePausedInput}, 3},
		// Terminal sessions don't count as live → single status sub-line.
		{"two agents one done", []pigtail.AgentState{pigtail.AgentStateRunning, pigtail.AgentStateDone}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := wsWithAgents(tc.states...)
			lines := renderListItem(w, now, portInfo{}, "", 80, false)
			gotSub := len(lines) - 1 // first line is the workspace line
			if gotSub != tc.wantSublines {
				t.Fatalf("sublines = %d, want %d (lines=%q)", gotSub, tc.wantSublines, lines)
			}
		})
	}
}

func TestAgentAggregate(t *testing.T) {
	live := []pigtail.AgentSession{
		{State: pigtail.AgentStateRunning},
		{State: pigtail.AgentStatePausedInput},
	}
	got := agentAggregate(live)
	if !strings.Contains(got, "2 agents") {
		t.Errorf("aggregate missing count: %q", got)
	}
	if !strings.Contains(got, "1*") || !strings.Contains(got, "1!") {
		t.Errorf("aggregate missing per-state counts: %q", got)
	}
}

func TestAttachOrSelectOpensSelectorOnlyForMultipleLive(t *testing.T) {
	mk := func(w pigtail.Workspace) model {
		m := model{now: time.Now()}
		m.flat = []flatItem{{ws: &w}}
		m.cursor = 0
		return m
	}

	// One live agent → no selector, a command is returned (attach).
	one := mk(wsWithAgents(pigtail.AgentStateRunning))
	m2, cmd := one.attachOrSelect()
	if m2.(model).selecting != nil {
		t.Errorf("selector should NOT open for a single live agent")
	}
	if cmd == nil {
		t.Errorf("expected an attach command for a single live agent")
	}

	// Two live agents → selector opens with both sessions.
	two := mk(wsWithAgents(pigtail.AgentStateRunning, pigtail.AgentStatePausedInput))
	m3, _ := two.attachOrSelect()
	sel := m3.(model).selecting
	if sel == nil {
		t.Fatalf("selector should open for two live agents")
	}
	if len(sel.sessions) != 2 {
		t.Errorf("selector should list 2 sessions, got %d", len(sel.sessions))
	}

	// Two agents but only one live → no selector.
	mixed := mk(wsWithAgents(pigtail.AgentStateRunning, pigtail.AgentStateDone))
	m4, _ := mixed.attachOrSelect()
	if m4.(model).selecting != nil {
		t.Errorf("selector should NOT open when only one agent is live")
	}
}
