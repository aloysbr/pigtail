package activity

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestWatcherEmitsOnPaneChange(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	name := "pigtail-test-watcher-" + randID()
	cmd := exec.Command("tmux", "new-session", "-d", "-s", name, "-c", "/tmp")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })

	bus := events.New()
	w := New(bus)
	w.SetInterval(150 * time.Millisecond)
	defer w.StopAll()

	sub := bus.Subscribe(string(pigtail.EventWorkspaceActivity))
	defer sub.Close()

	w.Start(pigtail.AgentSession{
		ID:              "agt_test",
		WorkspaceID:     "ws_test",
		TmuxSessionName: name,
	})

	// Drain the initial "first capture" event (state-change vs nothing)
	// then write something to the pane and assert we get another event.
	_, _ = drainOne(sub, 500*time.Millisecond)

	if out, err := exec.Command("tmux", "send-keys", "-t", name, "echo hello-from-test", "Enter").CombinedOutput(); err != nil {
		t.Fatalf("send-keys: %v: %s", err, out)
	}

	ev, ok := drainOne(sub, 2*time.Second)
	if !ok {
		t.Fatal("expected an activity event after send-keys")
	}
	if ev.Data["agent_session_id"] != "agt_test" {
		t.Fatalf("wrong agent_session_id in event: %v", ev.Data)
	}
	if summary, _ := ev.Data["summary"].(string); summary == "" {
		t.Errorf("summary should not be empty: %+v", ev.Data)
	}
}

func TestWatcherStopIsIdempotent(t *testing.T) {
	bus := events.New()
	w := New(bus)
	w.Stop("does-not-exist") // no panic
	w.StopAll()              // no panic
}

func drainOne(sub *events.Subscription, timeout time.Duration) (pigtail.Event, bool) {
	select {
	case e, ok := <-sub.C:
		return e, ok
	case <-time.After(timeout):
		return pigtail.Event{}, false
	}
}

// randID is unique across concurrent test binaries (two go-test runs at a
// time share one tmux server): the old seconds-only stamp collided.
func randID() string {
	return strconv.Itoa(os.Getpid()) + "x" + strconv.FormatInt(time.Now().UnixNano(), 36)
}
