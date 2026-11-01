package events

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestPublishFanout(t *testing.T) {
	b := New()
	sub1 := b.Subscribe()
	sub2 := b.Subscribe()
	defer sub1.Close()
	defer sub2.Close()

	const n = 100
	for i := 0; i < n; i++ {
		b.Publish(pigtail.Event{Type: pigtail.EventWorkspaceActivity})
	}

	for i, s := range []*Subscription{sub1, sub2} {
		count := 0
		deadline := time.After(2 * time.Second)
	loop:
		for count < n {
			select {
			case _, ok := <-s.C:
				if !ok {
					t.Fatalf("sub%d closed early at %d", i+1, count)
				}
				count++
			case <-deadline:
				break loop
			}
		}
		if count != n {
			t.Fatalf("sub%d: got %d events, want %d", i+1, count, n)
		}
	}
}

func TestFilterByPattern(t *testing.T) {
	b := New()
	sub := b.Subscribe("workspace.*")
	defer sub.Close()

	b.Publish(pigtail.Event{Type: pigtail.EventWorkspaceCreated})
	b.Publish(pigtail.Event{Type: pigtail.EventAgentStarted})
	b.Publish(pigtail.Event{Type: pigtail.EventWorkspaceActivity})

	got := drain(t, sub.C, 100*time.Millisecond)
	if len(got) != 2 {
		t.Fatalf("expected 2 workspace.* events, got %d (%v)", len(got), got)
	}
}

func TestReplayAfterID(t *testing.T) {
	b := New()
	ids := make([]string, 5)
	for i := range ids {
		ids[i] = b.Publish(pigtail.Event{Type: pigtail.EventWorkspaceCreated})
	}
	after := b.Replay(ids[1], nil)
	if len(after) != 3 {
		t.Fatalf("expected 3 replayed events after %s, got %d", ids[1], len(after))
	}
}

func drain(t *testing.T, ch <-chan pigtail.Event, timeout time.Duration) []pigtail.EventType {
	t.Helper()
	var out []pigtail.EventType
	deadline := time.After(timeout)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e.Type)
		case <-deadline:
			return out
		}
	}
}
