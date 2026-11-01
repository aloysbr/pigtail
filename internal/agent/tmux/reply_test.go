package tmux

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestReplyTypesIntoPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	name := "pigtail-reply-test-" + shortID()
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", name, "-c", "/tmp").CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })

	a := New()
	sess := &pigtail.AgentSession{ID: "agt_x", TmuxSessionName: name}

	if err := a.Reply(context.Background(), sess, "echo PIGTAIL-MARKER-XYZ"); err != nil {
		t.Fatalf("reply: %v", err)
	}
	testutil.Eventually(t, 30*time.Second, "marker in the pane", func() bool {
		out, err := exec.Command("tmux", "capture-pane", "-p", "-t", name).Output()
		return err == nil && strings.Contains(string(out), "PIGTAIL-MARKER-XYZ")
	})

	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", name).Output()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if !strings.Contains(string(out), "PIGTAIL-MARKER-XYZ") {
		t.Fatalf("marker not found in pane output:\n%s", out)
	}
}

func TestReplyRefusesEmpty(t *testing.T) {
	a := New()
	sess := &pigtail.AgentSession{ID: "agt_x", TmuxSessionName: "irrelevant"}
	if err := a.Reply(context.Background(), sess, ""); err == nil {
		t.Fatal("expected error on empty content")
	}
}

func TestReplyRefusesWithoutSession(t *testing.T) {
	a := New()
	if err := a.Reply(context.Background(), &pigtail.AgentSession{}, "hi"); err == nil {
		t.Fatal("expected error when session has no tmux_session_name")
	}
}
