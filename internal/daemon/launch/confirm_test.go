package launch

import (
	"context"
	"errors"
	"github.com/aloysbr/pigtail/internal/usersettings"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// fakeClock drives the confirmation's polling without a real sleep: After
// moves the clock by d and fires at once; with hold set it never fires.
type fakeClock struct {
	mu   sync.Mutex
	now  time.Time
	hold bool
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hold {
		return nil
	}
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

// confirmHarness is a harness with a fake clock and a started launcher.
func confirmHarness(t *testing.T) (*harness, *fakeClock) {
	t.Helper()
	h := newHarness(t)
	clk := &fakeClock{now: time.Now().UTC()}
	h.d.Now, h.d.After = clk.Now, clk.After
	return h, clk
}

// peerOnTab answers ListPeers with one live peer on the n-th created tab,
// updated at `at`, visible once the clock reaches it.
func (h *harness) peerOnTab(clk *fakeClock, n int, at time.Time, pid int) {
	h.st.peersFn = func() []pigtail.PeerSession {
		id := h.f.createdID(n)
		if id == "" || clk.Now().Before(at) {
			return nil
		}
		return []pigtail.PeerSession{{SessionID: "s1", PID: pid, CmuxWorkspaceID: strings.ToLower(id), UpdatedAt: at}}
	}
}

func (h *harness) eventsCopy() []pigtail.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]pigtail.Event(nil), h.events...)
}

func startLauncher(t *testing.T, h *harness) (*Launcher, context.CancelFunc) {
	t.Helper()
	l := New(h.d)
	ctx, cancel := context.WithCancel(context.Background())
	l.Start(ctx)
	t.Cleanup(func() { cancel(); l.Wait() })
	return l, cancel
}

func TestConfirmLaunchedOnPeer(t *testing.T) {
	h, clk := confirmHarness(t)
	h.peerOnTab(clk, 0, clk.Now().Add(40*time.Second), 4242)
	l, _ := startLauncher(t, h)
	got, err := l.Launch(context.Background(), h.sub)
	if err != nil {
		t.Fatal(err)
	}
	l.Wait()
	st, ok, err := l.Get(got.LaunchID)
	if err != nil || !ok {
		t.Fatalf("Get = %v, %v", ok, err)
	}
	if st.State != pigtail.LaunchLaunched || st.Cause != "" || st.Cell != "mp" || st.PermissionMode != "auto" {
		t.Fatalf("state = %+v", st)
	}
	evs := h.eventsCopy()
	if len(evs) != 2 {
		t.Fatalf("events = %+v", evs)
	}
	for i, want := range []pigtail.LaunchState{pigtail.LaunchPending, pigtail.LaunchLaunched} {
		e := evs[i]
		if e.Type != pigtail.EventSessionLaunchChanged || e.Scope != "perso" {
			t.Fatalf("event %d = %+v", i, e)
		}
		if e.Data["state"] != string(want) || e.Data["launch_id"] != got.LaunchID {
			t.Fatalf("event %d data = %v", i, e.Data)
		}
		if _, ok := e.Data["deduplicated"]; ok {
			t.Fatalf("event %d carries deduplicated: %v", i, e.Data)
		}
	}
	entries, _ := h.reg.Entries()
	last := entries[len(entries)-1]
	if last.Kind != "" || last.Result != spawnreg.ResultLaunched || last.PID != 4242 || last.Cell != "mp" || last.Prompt != usersettings.Defaults().LaunchPrompt("sub_x", "Web launch") {
		t.Fatalf("launched entry = %+v", last)
	}
}

func TestConfirmIgnoresOldPeer(t *testing.T) {
	h, clk := confirmHarness(t)
	h.peerOnTab(clk, 0, clk.Now().Add(-time.Second), 4242)
	l, _ := startLauncher(t, h)
	got, err := l.Launch(context.Background(), h.sub)
	if err != nil {
		t.Fatal(err)
	}
	l.Wait()
	st, _, _ := l.Get(got.LaunchID)
	if st.State != pigtail.LaunchFailed {
		t.Fatalf("an older peer confirmed the launch: %+v", st)
	}
}

func TestConfirmFailsAtDeadline(t *testing.T) {
	h, _ := confirmHarness(t)
	l, _ := startLauncher(t, h)
	got, err := l.Launch(context.Background(), h.sub)
	if err != nil {
		t.Fatal(err)
	}
	l.Wait()
	st, ok, err := l.Get(got.LaunchID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if st.State != pigtail.LaunchFailed || !strings.Contains(st.Cause, "no session registered within 5 min") {
		t.Fatalf("state = %+v", st)
	}
	evs := h.eventsCopy()
	if len(evs) != 2 || evs[1].Data["state"] != "failed" || evs[1].Data["cause"] == "" {
		t.Fatalf("events = %+v", evs)
	}
	again, err := l.Launch(context.Background(), h.sub)
	if err != nil {
		t.Fatal(err)
	}
	if again.LaunchID == got.LaunchID || again.Deduplicated || h.f.creates() != 2 {
		t.Fatalf("relaunch = %+v, creates = %d", again, h.f.creates())
	}
}

func TestConfirmListPeersErrorKeepsPolling(t *testing.T) {
	h, clk := confirmHarness(t)
	h.st.peersErr = []error{errors.New("database is locked")}
	h.peerOnTab(clk, 0, clk.Now().Add(time.Second), 77)
	l, _ := startLauncher(t, h)
	got, err := l.Launch(context.Background(), h.sub)
	if err != nil {
		t.Fatal(err)
	}
	l.Wait()
	st, _, _ := l.Get(got.LaunchID)
	if st.State != pigtail.LaunchLaunched {
		t.Fatalf("a ListPeers error ended the poll: %+v", st)
	}
}

func TestWaitDrainsConfirm(t *testing.T) {
	h, clk := confirmHarness(t)
	clk.hold = true
	pub := h.publishTo()
	l, cancel := startLauncher(t, h)
	got, err := l.Launch(context.Background(), h.sub)
	if err != nil {
		t.Fatal(err)
	}
	awaitEvent(t, pub, pigtail.LaunchPending)
	cancel()
	done := make(chan struct{})
	go func() { l.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after the context was cancelled")
	}
	entries, _ := h.reg.Entries()
	if last := entries[len(entries)-1]; last.Kind != spawnreg.KindLaunchArmed {
		t.Fatalf("a stop wrote %+v; nothing but the armed record is expected", last)
	}
	if st, _, _ := l.Get(got.LaunchID); st.State != pigtail.LaunchPending {
		t.Fatalf("state = %+v", st)
	}
}

// publishTo routes the launcher's events to a channel.
func (h *harness) publishTo() chan pigtail.Event {
	ch := make(chan pigtail.Event, 16)
	h.d.Publish = func(e pigtail.Event) { ch <- e }
	return ch
}

// awaitEvent waits for the next event, bounded so a bug fails rather than hangs.
func awaitEvent(t *testing.T, ch chan pigtail.Event, want pigtail.LaunchState) pigtail.Event {
	t.Helper()
	select {
	case e := <-ch:
		if e.Data["state"] != string(want) {
			t.Fatalf("event = %v, want state %s", e.Data, want)
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s event", want)
	}
	return pigtail.Event{}
}
