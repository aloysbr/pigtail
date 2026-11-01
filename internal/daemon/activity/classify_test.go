package activity

import (
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestClassifyPane(t *testing.T) {
	cases := []struct {
		name string
		pane string
		want pigtail.AgentState
	}{
		{
			name: "permission prompt -> paused input",
			pane: "Use skill \"check\"?\n❯ 1. Yes\n  2. No",
			want: pigtail.AgentStatePausedInput,
		},
		{
			name: "proceed prompt -> paused input",
			pane: "Do you want to proceed?\n❯ 1. Yes",
			want: pigtail.AgentStatePausedInput,
		},
		{
			name: "plan approval -> paused plan",
			pane: "Ready to code?\n❯ 1. Yes, and auto-accept edits\n  2. Yes, and manually approve",
			want: pigtail.AgentStatePausedPlan,
		},
		{
			name: "normal output -> running",
			pane: "Editing file foo.go...\nDone.",
			want: pigtail.AgentStateRunning,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPane(tc.pane); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}
