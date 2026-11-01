package cmuxsync

import (
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// D29-A (sub_x000000000000026, Task 18): a session the loop judges gone —
// ended, or not alive WaitGoneConfirm apart — loses its pill, but the wait
// its subject owns stays open; a live session's declared wait judged over
// is cleared as before.

type clearCall struct {
	session string
	sources []string
	gone    bool
}

// subjectWait records an arbitrage of s1 on the harness's subject.
func subjectWait(t *testing.T, st *store.Store, now time.Time) string {
	t.Helper()
	tr, err := st.SetSessionWaitRecord("s1", pigtail.SessionWait{Type: "arbitrage", Reason: "A or B", Source: pigtail.WaitSourceDeclared, Since: now},
		store.WaitMeta{Scope: "perso", SubjectID: subID})
	if err != nil {
		t.Fatal(err)
	}
	if tr.OpenedID != "" {
		return tr.OpenedID
	}
	r, err := st.ListOpenWaitRecords(store.WaitRecordFilter{})
	if err != nil || len(r) != 1 {
		t.Fatalf("open records = %+v, %v", r, err)
	}
	return r[0].ID
}

func recordState(t *testing.T, st *store.Store, id string) string {
	t.Helper()
	r, err := st.GetWaitRecord(id)
	if err != nil {
		t.Fatal(err)
	}
	return r.State
}

// clearThroughStore is the wiring's fallback, recording each call.
func clearThroughStore(st *store.Store, calls *[]clearCall) func(string, []string, bool) {
	return func(session string, sources []string, gone bool) {
		*calls = append(*calls, clearCall{session, sources, gone})
		if gone {
			_, _ = st.ClearGoneSessionWaits(session, sources...)
		} else {
			_, _ = st.ClearSessionWaits(session, sources...)
		}
	}
}

func TestDeadSessionKeepsSubjectWait(t *testing.T) {
	t.Parallel()
	t.Run("ended", func(t *testing.T) {
		h := newHarness(t, on())
		var calls []clearCall
		h.r.deps.ClearWaits = clearThroughStore(h.st, &calls)
		h.world.add(winA, wsA, "a")
		h.bind(wsA)
		h.session("s1", wsA, "idle", true)
		h.step(0)
		id := subjectWait(t, h.st, h.now)
		if err := h.st.EndPeer("s1", h.now); err != nil {
			t.Fatal(err)
		}
		// The row an end would have dropped, still there for the loop (the
		// same wait told again: its record is kept).
		if again := subjectWait(t, h.st, h.now); again != id {
			id = again
		}
		h.step(time.Second)
		if ws, _ := h.st.SessionWaits("s1"); len(ws) != 0 {
			t.Fatalf("rows left: %+v", ws)
		}
		if len(calls) != 1 || !calls[0].gone {
			t.Fatalf("ClearWaits calls = %+v, want one gone", calls)
		}
		if s := recordState(t, h.st, id); s != pigtail.WaitStateOpen {
			t.Fatalf("subject's wait = %s, want open", s)
		}
	})
	t.Run("not alive", func(t *testing.T) {
		h := newHarness(t, on())
		var calls []clearCall
		h.r.deps.ClearWaits = clearThroughStore(h.st, &calls)
		h.world.add(winA, wsA, "a")
		h.bind(wsA)
		h.session("s1", wsA, "idle", true)
		h.step(0)
		id := subjectWait(t, h.st, h.now)
		h.alive["s1"] = false
		h.step(time.Second)
		h.step(WaitGoneConfirm)
		if ws, _ := h.st.SessionWaits("s1"); len(ws) != 0 {
			t.Fatalf("rows left: %+v", ws)
		}
		if len(calls) != 1 || !calls[0].gone {
			t.Fatalf("ClearWaits calls = %+v, want one gone", calls)
		}
		if s := recordState(t, h.st, id); s != pigtail.WaitStateOpen {
			t.Fatalf("subject's wait = %s, want open", s)
		}
	})
	t.Run("alive, declared over", func(t *testing.T) {
		h := newNativeHarness(t, on())
		var calls []clearCall
		h.r.deps.ClearWaits = clearThroughStore(h.st, &calls)
		h.world.add(winA, wsA, "a")
		h.bind(wsA)
		h.native("s1", wsA, "busy", h.now.Add(-time.Minute))
		h.step(0)
		id := subjectWait(t, h.st, h.now)
		h.step(time.Second)
		h.nativeWait("s1", "idle", "", h.now.Add(500*time.Millisecond))
		h.step(time.Second)
		h.nativeWait("s1", "busy", "", h.now.Add(500*time.Millisecond))
		h.step(time.Second)
		if len(calls) != 1 || calls[0].gone {
			t.Fatalf("ClearWaits calls = %+v, want one not gone", calls)
		}
		if s := recordState(t, h.st, id); s != pigtail.WaitStateCleared {
			t.Fatalf("wait = %s, want cleared", s)
		}
	})
}

// Deps.ClearWaits nil: the runner's own store fallback keeps it too.
func TestClearWaitsFallbackKeepsSubjectWait(t *testing.T) {
	t.Parallel()
	h := newHarness(t, on())
	h.world.add(winA, wsA, "a")
	h.bind(wsA)
	h.session("s1", wsA, "idle", true)
	h.step(0)
	id := subjectWait(t, h.st, h.now)
	h.alive["s1"] = false
	h.step(time.Second)
	h.step(WaitGoneConfirm)
	if ws, _ := h.st.SessionWaits("s1"); len(ws) != 0 {
		t.Fatalf("rows left: %+v", ws)
	}
	if s := recordState(t, h.st, id); s != pigtail.WaitStateOpen {
		t.Fatalf("subject's wait = %s, want open", s)
	}
}
