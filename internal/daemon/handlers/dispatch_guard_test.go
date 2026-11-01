package handlers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestResolvePlanFirst(t *testing.T) {
	t.Parallel()
	tt := true
	ff := false
	cases := []struct {
		name string
		req  pigtail.DispatchRequest
		want bool
	}{
		{"no plan, unset -> plan mode", pigtail.DispatchRequest{}, true},
		{"plan provided, unset -> autonomous", pigtail.DispatchRequest{PlanContent: "x"}, false},
		{"explicit true wins over plan", pigtail.DispatchRequest{PlanContent: "x", PlanFirst: &tt}, true},
		{"explicit false wins over no plan", pigtail.DispatchRequest{PlanFirst: &ff}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolvePlanFirst(&tc.req); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestWritePlanFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rel, err := writePlanFile(dir, "../../etc/2026-05-28-plan.md", "# plan\nstep 1")
	if err != nil {
		t.Fatal(err)
	}
	if rel != filepath.Join(".pigtail", "2026-05-28-plan.md") {
		t.Fatalf("unexpected rel path %q", rel)
	}
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil || string(b) != "# plan\nstep 1" {
		t.Fatalf("content mismatch: %q err=%v", b, err)
	}
}

func TestIsLiveAgentState(t *testing.T) {
	t.Parallel()
	live := []pigtail.AgentState{pigtail.AgentStateRunning, pigtail.AgentStatePausedInput, pigtail.AgentStatePausedPlan}
	dead := []pigtail.AgentState{pigtail.AgentStateDone, pigtail.AgentStateKilled, pigtail.AgentStateCrashed}
	for _, s := range live {
		if !isLiveAgentState(s) {
			t.Errorf("%s should be live", s)
		}
	}
	for _, s := range dead {
		if isLiveAgentState(s) {
			t.Errorf("%s should be terminal", s)
		}
	}
}
