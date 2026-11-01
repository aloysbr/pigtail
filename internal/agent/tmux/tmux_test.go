package tmux

import (
	"context"
	"os/exec"
	"testing"

	"github.com/aloysbr/pigtail/internal/agent"
)

func TestTmuxLifecycle(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	ctx := context.Background()
	name := "pigtail-test-" + shortID()

	// Should not exist
	alive, err := tmuxHasSession(ctx, name)
	if err != nil {
		t.Fatalf("has-session pre-create: %v", err)
	}
	if alive {
		t.Fatalf("session %s already exists", name)
	}

	if err := tmuxNewSession(ctx, name, "/tmp", nil); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	t.Cleanup(func() { _ = tmuxKillSession(context.Background(), name) })

	alive, err = tmuxHasSession(ctx, name)
	if err != nil {
		t.Fatalf("has-session post-create: %v", err)
	}
	if !alive {
		t.Fatal("session should exist")
	}

	if err := tmuxKillSession(ctx, name); err != nil {
		t.Fatalf("kill: %v", err)
	}

	// Idempotent kill
	if err := tmuxKillSession(ctx, name); err != nil {
		t.Fatalf("kill (second): %v", err)
	}

	alive, _ = tmuxHasSession(ctx, name)
	if alive {
		t.Fatal("session should be gone")
	}
}

// A second kill can race the server's own exit after its last session:
// every way tmux says "that session is gone" is a success, nothing else is.
func TestSessionAlreadyGone(t *testing.T) {
	t.Parallel()
	for out, want := range map[string]bool{
		"can't find session: pigtail-test-x":                 true,
		"no server running on /private/tmp/tmux-501/default": true,
		"no current target":                                  true,
		"lost server":                                        false,
		"":                                                   false,
	} {
		if got := sessionAlreadyGone(out); got != want {
			t.Errorf("sessionAlreadyGone(%q) = %v, want %v", out, got, want)
		}
	}
}

func TestBuildClaudeCommand(t *testing.T) {
	cases := []struct {
		name   string
		intent agent.SpawnIntent
		want   string
	}{
		{
			name:   "plain",
			intent: agent.SpawnIntent{Intent: "fix the bug"},
			want:   `claude 'fix the bug'`,
		},
		{
			name:   "plan first",
			intent: agent.SpawnIntent{Intent: "fix the bug", PlanFirst: true},
			want:   `claude --permission-mode plan 'fix the bug'`,
		},
		{
			name:   "with plan path takes precedence over plan first",
			intent: agent.SpawnIntent{Intent: "fix the bug", PlanFirst: true, PlanPath: ".pigtail/plan.md"},
			want:   `claude 'Read .pigtail/plan.md in full first, then execute it exactly. Do not rescope or reinterpret. Intent: fix the bug'`,
		},
		{
			name:   "quote escaping",
			intent: agent.SpawnIntent{Intent: "it's broken"},
			want:   `claude 'it'\''s broken'`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildClaudeCommand(tc.intent)
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}
