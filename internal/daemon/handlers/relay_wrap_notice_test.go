package handlers

import (
	"net/http"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// sendNoticeTo queues one one-way notice from s-beta, then zeroes the fake's
// counters (like sendTo).
func (f *relayFixture) sendNoticeTo(t *testing.T, fw *fakeWake, to string) {
	t.Helper()
	rec, _ := f.send(t, pigtail.SendMessageRequest{From: "s-beta", To: to, Body: "disk low", Kind: pigtail.MessageKindNotice})
	if rec.Code != http.StatusCreated {
		t.Fatalf("notice to %s = %d %s", to, rec.Code, rec.Body.String())
	}
	fw.resetCounts()
}

func assertWrapTypedNoticeKept(t *testing.T, f *relayFixture, fw *fakeWake, lg *wakeLog) {
	t.Helper()
	f.rl.sweepIdle()
	if len(fw.wraps) != 1 || fw.wraps[0] != surf || wrapRows(t, f, "sent") != 1 {
		t.Fatalf("wraps %v sent %d; log:\n%s", fw.wraps, wrapRows(t, f, "sent"), lg)
	}
	// Neither acked nor claimed by the daemon: still pending...
	if n, err := f.st.CountPending("s-alpha", f.clock()); err != nil || n != 1 {
		t.Fatalf("pending after wrap = %d, %v; want 1", n, err)
	}
	// ... and the inbox hook delivers it at the done command's prompt.
	if ms := claimVia(t, f, "s-alpha", "prompt"); len(ms) != 1 || ms[0].Kind != pigtail.MessageKindNotice {
		t.Fatalf("claim = %+v", ms)
	}
}

// A pending one-way notice no longer holds the idle wrap of a Done session.
func TestWrapIdleNoticePendingTypesWrap(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	f.sendNoticeTo(t, fw, "s-alpha")
	assertWrapTypedNoticeKept(t, f, fw, lg)
}

// Same through the real path: a broadcast creates a notice.
func TestWrapIdleNoticeViaBroadcastTypesWrap(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	rec, out := f.broadcastAs(t, work, pigtail.BroadcastRequest{From: "s-beta", Body: "disk low"})
	if rec.Code != http.StatusOK || out.Sent < 1 {
		t.Fatalf("broadcast = %d %s", rec.Code, rec.Body.String())
	}
	fw.resetCounts()
	assertWrapTypedNoticeKept(t, f, fw, lg)
}

// A pending info still holds the wrap.
func TestWrapIdleRequestPendingStillHolds(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	f.sendTo(t, fw, "s-alpha")
	sweeps(f, 3)
	assertNoWrap(t, f, fw, lg)
	if lg.count("wrap: s-alpha held: messages pending") != 1 {
		t.Fatalf("log:\n%s", lg)
	}
}

func TestWrapIdleNoticePlusRequestHolds(t *testing.T) {
	t.Parallel()
	f, fw, lg := wrapFixture(t)
	f.sendNoticeTo(t, fw, "s-alpha")
	f.sendTo(t, fw, "s-alpha")
	sweeps(f, 3)
	assertNoWrap(t, f, fw, lg)
}

func TestWrapNoterNoticeOnly(t *testing.T) {
	t.Parallel()
	f, fw, _ := wrapFixture(t)
	f.sendNoticeTo(t, fw, "s-alpha")
	if held, why := (wrapNoter{f.rl}).watcherHolds("s-alpha"); held {
		t.Fatalf("notice alone holds: %q", why)
	}
	f.sendTo(t, fw, "s-alpha")
	if held, why := (wrapNoter{f.rl}).watcherHolds("s-alpha"); !held || why != "messages pending" {
		t.Fatalf("with an info: held=%v why=%q", held, why)
	}
}
