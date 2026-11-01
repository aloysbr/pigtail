package handlers

import (
	"testing"

	"github.com/aloysbr/pigtail/internal/relay"
)

// paneGuard is typeGuarded without the send (shared with the idle pause):
// every guard runs, the screen is read once, nothing is typed.
func TestPaneGuardSendsNothing(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	fw.resetCounts()
	p, err := f.st.GetPeer("s-alpha")
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.rl.candidate(*p)
	if err != nil {
		t.Fatal(err)
	}
	sw := &wakeSweep{ls: newListing(fw.deps().CmuxSessions), typed: map[string]bool{}}
	var seen string
	n, out := f.rl.paneGuard("s-alpha", c, *p, f.now, sw, wakeNoter{f.rl}, func(s string) bool { seen = s; return true })
	if out != typeSent {
		t.Fatalf("outcome = %v, want typeSent (all guards passed)", out)
	}
	if n.SessionID != "s-alpha" {
		t.Fatalf("native = %+v, want the re-read native", n)
	}
	if len(fw.sent) != 0 || len(fw.compacts) != 0 || fw.screenCalls != 1 || len(sw.typed) != 0 {
		t.Fatalf("sent %v compacts %v screenCalls %d typed %v; want nothing typed, one read-screen", fw.sent, fw.compacts, fw.screenCalls, sw.typed)
	}
	if seen != fw.screen {
		t.Fatal("onScreen did not see the screen read")
	}
}

// A real send through typeGuarded marks the session typed in this sweep
// (read by the pause, T-k).
func TestTypeGuardedMarksTyped(t *testing.T) {
	t.Parallel()
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	fw.resetCounts()
	p, _ := f.st.GetPeer("s-alpha")
	c, err := f.rl.candidate(*p)
	if err != nil {
		t.Fatal(err)
	}
	sw := &wakeSweep{ls: newListing(fw.deps().CmuxSessions), typed: map[string]bool{}}
	if _, out := f.rl.typeGuarded("s-alpha", relay.WakeLine, c, *p, f.now, sw, wakeNoter{f.rl}, nil, nil, nil, nil); out != typeSent {
		t.Fatalf("outcome = %v", out)
	}
	if !sw.typed["s-alpha"] || len(fw.sent) != 1 {
		t.Fatalf("typed %v sent %v; want s-alpha marked and one send", sw.typed, fw.sent)
	}
}
