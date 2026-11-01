package handlers

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Auto-retry of a session stopped on an API error (relay_retry.go): every
// test goes through sweepIdle, the one call site.

const (
	text529  = "API Error: 529 Overloaded\r\nPlease try again\r\n"
	textDNS  = "API Error: Can't reach the API server (ENOTFOUND)"
	retrySid = "s-alpha"
)

// retryFixture: s-alpha (pid 7, surf) idle, unfocused, its input empty, and no
// stop yet; the retry's fakes are on (the API reachable).
func retryFixture(t *testing.T) (*relayFixture, *fakeWake, *wakeLog) {
	t.Helper()
	f, fw, lg := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	fw.apiErrors = map[string]claudesession.APIError{}
	fw.prompts = map[string][]claudesession.Prompt{}
	fw.reachable = true
	fw.resetCounts()
	return f, fw, lg
}

// stop records a StopFailure at the fixture's now: the automatic api_error
// wait and the transcript's error entry.
func (f *relayFixture) stop(t *testing.T, fw *fakeWake, text string) time.Time {
	t.Helper()
	at := f.clock()
	if _, err := f.st.ClearSessionWaits(retrySid, pigtail.WaitSourceAuto); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetSessionWait(retrySid, pigtail.SessionWait{Type: "api_error", Source: pigtail.WaitSourceAuto, Reason: "server_error", Since: at}); err != nil {
		t.Fatal(err)
	}
	fw.apiErrors[retrySid] = claudesession.APIError{Error: "server_error", Status: 529, At: at, Text: text}
	return at
}

func (f *relayFixture) retryRows(t *testing.T, kind string) []pigtail.JournalEntry {
	t.Helper()
	rows, err := f.st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func (f *relayFixture) autoReason(t *testing.T) string {
	t.Helper()
	ws, err := f.st.SessionWaits(retrySid)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		if w.Source == pigtail.WaitSourceAuto {
			return w.Reason
		}
	}
	return "<no auto wait>"
}

// afterContinue plays what Claude Code does with our "continue": the wait is
// cleared by the prompt, the turn fails again, a new stop is recorded.
func (f *relayFixture) afterContinue(t *testing.T, fw *fakeWake, text string) time.Time {
	t.Helper()
	f.advance(time.Second)
	fw.prompts[retrySid] = append(fw.prompts[retrySid], claudesession.Prompt{At: f.clock(), Text: "continue"})
	f.advance(time.Second)
	return f.stop(t, fw, text)
}

// runToNextTry advances past the next due time of a non-network series and
// sweeps; it fails unless a "continue" was typed.
func (f *relayFixture) runToNextTry(t *testing.T, fw *fakeWake, delay time.Duration) {
	t.Helper()
	n := len(fw.continues)
	f.advance(delay + time.Second)
	f.rl.sweepIdle()
	if len(fw.continues) != n+1 {
		t.Fatalf("continues = %d, want %d", len(fw.continues), n+1)
	}
}

func lastSentID(t *testing.T, f *relayFixture) string {
	t.Helper()
	rows := f.retryRows(t, retryKindSent)
	if len(rows) == 0 {
		t.Fatal("no api-retry-sent row")
	}
	return rows[0].ID
}

// A1, A8: a 529 stop, one minute without a prompt: "continue" once.
func TestRetryTypesContinueAfterOneMinute(t *testing.T) {
	f, fw, lg := retryFixture(t)
	stop := f.stop(t, fw, text529)
	f.advance(30 * time.Second)
	f.rl.sweepIdle()
	if len(fw.continues) != 0 {
		t.Fatalf("typed before the first minute: %v", fw.continues)
	}
	if got, want := f.autoReason(t), relay.ReasonAt(stop.Add(time.Minute), nil, 1); got != want {
		t.Fatalf("pill reason = %q, want %q", got, want)
	}
	f.advance(31 * time.Second)
	f.rl.sweepIdle()
	if len(fw.continues) != 1 || fw.continues[0] != surf {
		t.Fatalf("continues = %v; log:\n%s", fw.continues, lg)
	}
	rows := f.retryRows(t, retryKindSent)
	if len(rows) != 1 {
		t.Fatalf("sent rows = %+v", rows)
	}
	for _, want := range []string{"s-alpha", "other", "continue typed", "try 1", "1/5", "API Error: 529 Overloaded Please try again"} {
		if !strings.Contains(rows[0].Summary, want) {
			t.Errorf("summary %q lacks %q", rows[0].Summary, want)
		}
	}
	if strings.ContainsAny(rows[0].Summary, "\r\n") {
		t.Errorf("summary is not one line: %q", rows[0].Summary)
	}
	f.rl.sweepIdle()
	if len(fw.continues) != 1 {
		t.Fatalf("typed twice: %v", fw.continues)
	}
}

// A2, A8: five tries without a completed turn, the sixth due time types nothing.
func TestRetryExhaustedAfterFiveTries(t *testing.T) {
	f, fw, lg := retryFixture(t)
	f.stop(t, fw, text529)
	delays := []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 20 * time.Minute}
	for i, d := range delays {
		f.runToNextTry(t, fw, d)
		f.afterContinue(t, fw, text529)
		if i == len(delays)-1 {
			break
		}
	}
	f.advance(time.Hour)
	f.rl.sweepIdle()
	if len(fw.continues) != 5 {
		t.Fatalf("continues = %d, want 5; log:\n%s", len(fw.continues), lg)
	}
	rows := f.retryRows(t, retryKindExhausted)
	if len(rows) != 1 {
		t.Fatalf("exhausted rows = %+v", rows)
	}
	for _, want := range []string{"s-alpha", "other", "5/5", "gave up after 5 tries"} {
		if !strings.Contains(rows[0].Summary, want) {
			t.Errorf("summary %q lacks %q", rows[0].Summary, want)
		}
	}
	if got := f.autoReason(t); got != relay.ReasonExhausted {
		t.Fatalf("pill reason = %q", got)
	}
	f.advance(time.Hour)
	f.rl.sweepIdle()
	if len(fw.continues) != 5 || len(f.retryRows(t, retryKindExhausted)) != 1 {
		t.Fatalf("a 7th sweep wrote or typed: continues %d exhausted %d", len(fw.continues), len(f.retryRows(t, retryKindExhausted)))
	}
}

// A3: a network failure waits for the API; the probe is made once per sweep,
// however many sessions are due in it.
func TestRetryNetworkWaitsForReachable(t *testing.T) {
	f, fw, lg := retryFixture(t)
	fw.natives[8] = claudesession.Native{SessionID: "s-gamma", Status: "idle", Kind: "interactive", StatusUpdatedAt: 1000}
	fw.listing = cmuxListingOf(cmuxEntry("s-alpha", surf, 7, "s-alpha"), cmuxEntry("s-gamma", surf2, 8, "s-gamma"))
	f.wakePeers(t, alphaPeer(), gammaPeer())
	fw.resetCounts()
	f.stop(t, fw, textDNS)
	at := f.clock()
	if err := f.st.SetSessionWait("s-gamma", pigtail.SessionWait{Type: "api_error", Source: pigtail.WaitSourceAuto, Reason: "server_error", Since: at}); err != nil {
		t.Fatal(err)
	}
	fw.apiErrors["s-gamma"] = claudesession.APIError{Error: "server_error", At: at, Text: textDNS}
	fw.reachable = false
	for i := 0; i < 3; i++ {
		f.advance(time.Minute)
		before := fw.reachCalls
		f.rl.sweepIdle()
		if got := fw.reachCalls - before; got != 1 {
			t.Fatalf("sweep %d probed %d times with two sessions due, want exactly 1", i, got)
		}
	}
	if len(fw.continues) != 0 || fw.reachCalls != 3 {
		t.Fatalf("continues %v probes %d; log:\n%s", fw.continues, fw.reachCalls, lg)
	}
	if got := f.autoReason(t); got != relay.ReasonNetwork {
		t.Fatalf("pill reason = %q", got)
	}
	fw.reachable = true
	f.advance(time.Minute)
	f.rl.sweepIdle()
	f.rl.sweepIdle()
	if len(fw.continues) != 2 {
		t.Fatalf("continues = %v after the API came back", fw.continues)
	}
	if id := lastSentID(t, f); !strings.HasSuffix(id, ":1:network:0") {
		t.Fatalf("a network try counts for none of the 5: %s", id)
	}
}

// A3b: 12 h after the first stop of a network series, give up.
func TestRetryNetworkGivesUpAfter12h(t *testing.T) {
	f, fw, _ := retryFixture(t)
	f.stop(t, fw, textDNS)
	fw.reachable = false
	f.advance(12 * time.Hour)
	f.rl.sweepIdle()
	if len(fw.continues) != 0 {
		t.Fatalf("typed: %v", fw.continues)
	}
	rows := f.retryRows(t, retryKindExhausted)
	if len(rows) != 1 || !strings.Contains(rows[0].Summary, "gave up 12 h after the stop") || !strings.Contains(rows[0].Summary, "network") {
		t.Fatalf("exhausted rows = %+v", rows)
	}
	if got := f.autoReason(t); got != relay.ReasonExhausted {
		t.Fatalf("pill reason = %q", got)
	}
}

// A4: the user types a prompt of his own: the series ends, the next stop is 1/5.
func TestRetrySeriesEndsOnHumanPrompt(t *testing.T) {
	f, fw, _ := retryFixture(t)
	f.stop(t, fw, text529)
	f.runToNextTry(t, fw, time.Minute)
	f.afterContinue(t, fw, text529)
	f.runToNextTry(t, fw, 2*time.Minute)
	f.afterContinue(t, fw, text529)
	f.runToNextTry(t, fw, 5*time.Minute)
	oldAnchor := strings.Split(lastSentID(t, f), ":")[3]
	f.advance(time.Second)
	fw.prompts[retrySid] = append(fw.prompts[retrySid], claudesession.Prompt{At: f.clock(), Text: "fais X"})
	f.advance(time.Second)
	f.stop(t, fw, text529)
	f.runToNextTry(t, fw, time.Minute)
	id := lastSentID(t, f)
	f2 := strings.Split(id, ":")
	if !strings.HasSuffix(id, ":1:other:1") || f2[3] == oldAnchor {
		t.Fatalf("after a human prompt the series must restart at 1/5 on a new anchor: %s (old anchor %s)", id, oldAnchor)
	}
}

// A4b: a completed turn resets the counter.
func TestRetryCounterResetsOnCompletedTurn(t *testing.T) {
	f, fw, _ := retryFixture(t)
	f.stop(t, fw, text529)
	f.runToNextTry(t, fw, time.Minute)
	f.afterContinue(t, fw, text529)
	f.runToNextTry(t, fw, 2*time.Minute)
	f.advance(time.Second)
	if err := f.st.RecordTurnEnd(retrySid, "gx", "turn-ok", f.clock().Add(-time.Second), f.clock(), pigtail.TurnEventStop); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Second)
	fw.prompts[retrySid] = append(fw.prompts[retrySid], claudesession.Prompt{At: f.clock(), Text: "continue"})
	f.advance(time.Second)
	f.stop(t, fw, text529)
	f.runToNextTry(t, fw, time.Minute)
	if id := lastSentID(t, f); !strings.HasSuffix(id, ":1:other:1") {
		t.Fatalf("a completed turn must restart the count: %s", id)
	}
}

// A5, A8: Standby, focus, a draft, busy: nothing typed, one journal line per
// reason however many sweeps.
func TestRetryHeldIsJournalledOnce(t *testing.T) {
	f, fw, lg := retryFixture(t)
	f.stop(t, fw, text529)
	steps := []struct {
		name  string
		on    func()
		off   func()
		given string
	}{
		{"standby", func() {
			f.rl.getStandby = func(string) (*pigtail.SessionStandby, error) {
				return &pigtail.SessionStandby{SessionID: retrySid}, nil
			}
		}, func() { f.rl.getStandby = nil }, "standby"},
		{"focus", func() { fw.focused = surf }, func() { fw.focused = "" }, "keyboard focus"},
		{"draft", func() { fw.screen = screenFixture(t, "screen-draft.txt") }, func() { fw.screen = screenFixture(t, "screen-idle-empty.txt") }, "draft in the input"},
		{"busy", func() { n := fw.natives[7]; n.Status = "busy"; fw.natives[7] = n }, func() { n := fw.natives[7]; n.Status = "idle"; fw.natives[7] = n }, "status busy"},
	}
	for i, st := range steps {
		st.on()
		for k := 0; k < 3; k++ {
			f.advance(time.Minute)
			f.rl.sweepIdle()
		}
		st.off()
		rows := f.retryRows(t, retryKindHeld)
		if len(rows) != i+1 {
			t.Fatalf("%s: held rows = %d, want %d; rows %+v; log:\n%s", st.name, len(rows), i+1, rows, lg)
		}
		if s := rows[0].Summary; !strings.Contains(s, "s-alpha") || !strings.Contains(s, "held: ") || !strings.Contains(s, st.given) ||
			!strings.Contains(s, "other") || !strings.Contains(s, "1/5") {
			t.Errorf("%s: summary %q", st.name, s)
		}
	}
	if len(fw.continues) != 0 || len(f.retryRows(t, retryKindSent)) != 0 {
		t.Fatalf("typed while held: %v", fw.continues)
	}
}

func (f *relayFixture) useBudget(fw *fakeWake) {
	f.rl.cfg.Wake.BudgetAcquire = func(_, _ string) (bool, string) {
		fw.budgetCalls++
		if fw.budgetNo != "" {
			return false, fw.budgetNo
		}
		return true, ""
	}
	f.rl.cfg.Wake.BudgetRelease = func(string) { fw.released++ }
}

// A refused budget consumes no try; a failed send spends it, frees the budget
// and does not reopen a 1/5 series.
func TestRetryBudgetRefusalKeepsTry(t *testing.T) {
	f, fw, lg := retryFixture(t)
	f.useBudget(fw)
	f.stop(t, fw, text529)
	fw.budgetNo = "session budget full"
	f.advance(61 * time.Second)
	f.rl.sweepIdle()
	if len(fw.continues) != 0 || len(f.retryRows(t, retryKindSent)) != 0 {
		t.Fatalf("typed or marked on a refused budget; log:\n%s", lg)
	}
	if h := f.retryRows(t, retryKindHeld); len(h) != 1 || !strings.Contains(h[0].Summary, "session budget") {
		t.Fatalf("held rows = %+v", h)
	}
	fw.budgetNo = ""
	f.advance(time.Minute)
	f.rl.sweepIdle()
	if len(fw.continues) != 1 || !strings.HasSuffix(lastSentID(t, f), ":1:other:1") {
		t.Fatalf("continues %v id %v", fw.continues, f.retryRows(t, retryKindSent))
	}

	// A send that fails.
	f2, fw2, _ := retryFixture(t)
	f2.useBudget(fw2)
	f2.stop(t, fw2, text529)
	fw2.sendErr = errors.New("cmux gone")
	f2.advance(61 * time.Second)
	f2.rl.sweepIdle()
	if fw2.released != 1 {
		t.Fatalf("budget released %d times, want 1", fw2.released)
	}
	if len(f2.retryRows(t, retryKindSent)) != 1 {
		t.Fatalf("the try is spent: sent rows %+v", f2.retryRows(t, retryKindSent))
	}
	if h := f2.retryRows(t, retryKindHeld); len(h) != 1 || !strings.Contains(h[0].Summary, "send failed") {
		t.Fatalf("held rows = %+v", h)
	}
	fw2.sendErr = nil
	f2.advance(30 * time.Second)
	f2.rl.sweepIdle()
	if len(f2.retryRows(t, retryKindSent)) != 1 {
		t.Fatalf("the unchanged wait reopened a series: %+v", f2.retryRows(t, retryKindSent))
	}
}

// m15: after "send failed" of try 1, try 2 goes at LastTry + 2 min, not before.
func TestRetrySendFailedAllowsNextTry(t *testing.T) {
	f, fw, _ := retryFixture(t)
	f.stop(t, fw, text529)
	fw.sendErr = errors.New("cmux gone")
	f.advance(61 * time.Second)
	f.rl.sweepIdle()
	fw.sendErr = nil
	if len(f.retryRows(t, retryKindSent)) != 1 || len(fw.continues) != 1 {
		t.Fatalf("setup: sent %d continues %d", len(f.retryRows(t, retryKindSent)), len(fw.continues))
	}
	f.advance(time.Minute + 59*time.Second)
	f.rl.sweepIdle()
	if len(f.retryRows(t, retryKindSent)) != 1 {
		t.Fatalf("try 2 went before LastTry + 2 min")
	}
	// A "continue" left in the box: refused, no duplicate.
	fw.screen = withContinueDraft(t)
	f.advance(2 * time.Second)
	f.rl.sweepIdle()
	if len(f.retryRows(t, retryKindSent)) != 1 || len(fw.continues) != 1 {
		t.Fatalf("a duplicate went over a draft")
	}
	fw.screen = screenFixture(t, "screen-idle-empty.txt")
	f.advance(time.Second)
	f.rl.sweepIdle()
	if rows := f.retryRows(t, retryKindSent); len(rows) != 2 || !strings.HasSuffix(rows[0].ID, ":2:other:2") {
		t.Fatalf("sent rows %+v", rows)
	}
}

func withContinueDraft(t *testing.T) string {
	t.Helper()
	s := screenFixture(t, "screen-idle-empty.txt")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "❯" {
			lines[i] = "❯ continue"
			return strings.Join(lines, "\n")
		}
	}
	t.Fatal("fixture has no empty input line")
	return ""
}

// m20: a held series shows "relance en attente", never a past time.
func TestRetryHeldShowsPendingReason(t *testing.T) {
	f, fw, _ := retryFixture(t)
	f.useBudget(fw)
	f.stop(t, fw, text529)
	check := func(name string) {
		t.Helper()
		if got := f.autoReason(t); got != relay.ReasonHeld {
			t.Fatalf("%s: pill reason = %q, want %q", name, got, relay.ReasonHeld)
		}
	}
	fw.screen = withContinueDraft(t)
	f.advance(61 * time.Second)
	f.rl.sweepIdle()
	check("draft")
	fw.screen = screenFixture(t, "screen-idle-empty.txt")
	fw.screen = strings.Replace(fw.screen, "❯", "⏺ Usage limit reached · continuing automatically\n❯", 1)
	f.advance(time.Minute)
	f.rl.sweepIdle()
	check("screen refused")
	fw.screen = screenFixture(t, "screen-idle-empty.txt")
	fw.budgetNo = "full"
	f.advance(time.Minute)
	f.rl.sweepIdle()
	check("budget refused")
}

func TestRetrySkipsSessionTypedThisSweep(t *testing.T) {
	f, fw, _ := retryFixture(t)
	f.stop(t, fw, text529)
	f.advance(2 * time.Minute)
	sw := &wakeSweep{ls: newListing(fw.deps().CmuxSessions), typed: map[string]bool{retrySid: true}}
	f.rl.retryStopped(f.clock(), sw)
	if len(fw.continues) != 0 {
		t.Fatalf("typed into a session already typed this sweep: %v", fw.continues)
	}
}

// A9: the reason follows the try and an event goes out.
func TestRetryWaitReasonShown(t *testing.T) {
	f, fw, _ := retryFixture(t)
	f.rl.svc.Bus = events.New()
	sub := f.rl.svc.Bus.Subscribe(string(pigtail.EventSessionWaitChanged))
	stop := f.stop(t, fw, text529)
	f.advance(10 * time.Second)
	f.rl.sweepIdle()
	if got, want := f.autoReason(t), relay.ReasonAt(stop.Add(time.Minute), nil, 1); got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
	if evs := drainEvents(sub); len(evs) == 0 {
		t.Fatal("no session.wait_changed after the reason changed")
	}
	f.advance(time.Minute)
	f.rl.sweepIdle()
	f.afterContinue(t, fw, text529)
	f.advance(10 * time.Second)
	f.rl.sweepIdle()
	if got := f.autoReason(t); !strings.HasSuffix(got, "(2/5)") {
		t.Fatalf("second stop: reason = %q", got)
	}
}

// A respawn (a new pid) starts a new series; the old pid's markers block nothing.
func TestRetrySeriesIsPerPid(t *testing.T) {
	f, fw, lg := retryFixture(t)
	f.stop(t, fw, text529)
	f.runToNextTry(t, fw, time.Minute)
	oldID := lastSentID(t, f)
	p := alphaPeer()
	p.PID = 9
	fw.natives[9] = claudesession.Native{SessionID: retrySid, Status: "idle", Kind: "interactive", StatusUpdatedAt: 1000}
	fw.listing = cmuxListingOf(cmuxEntry(retrySid, surf, 9, retrySid))
	f.peers(t, p)
	f.advance(time.Minute)
	f.stop(t, fw, text529)
	f.runToNextTry(t, fw, time.Minute)
	id := lastSentID(t, f)
	if id == oldID || !strings.HasPrefix(id, "api-retry:s-alpha:9:") || !strings.HasSuffix(id, ":1:other:1") {
		t.Fatalf("new pid, new series: old %s new %s; log:\n%s", oldID, id, lg)
	}
}

// The state is the journal: a new Relay on the same store resumes at the right rank.
func TestRetrySurvivesRelayRestart(t *testing.T) {
	f, fw, _ := retryFixture(t)
	f.stop(t, fw, text529)
	f.runToNextTry(t, fw, time.Minute)
	f.afterContinue(t, fw, text529)
	rl2 := NewRelay(f.rl.svc, f.rl.cfg)
	rl2.startedAt = f.rl.startedAt
	f.rl = rl2
	f.runToNextTry(t, fw, 2*time.Minute)
	if id := lastSentID(t, f); !strings.HasSuffix(id, ":2:other:2") {
		t.Fatalf("second try after a restart: %s", id)
	}
}

type screenOnlyNoter struct {
	wakeNoter
	allow bool
}

func (n screenOnlyNoter) screenAllows(string, bool) (bool, string) {
	if n.allow {
		return true, ""
	}
	return false, "custom refusal"
}

// paneGuard uses the noter's own screen rule when it has one.
func TestPaneGuardUsesTheNoterScreenRule(t *testing.T) {
	f, fw, _ := newWakeFixture(t)
	f.wakePeers(t, alphaPeer())
	p, err := f.st.GetPeer(retrySid)
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.rl.candidate(*p)
	if err != nil {
		t.Fatal(err)
	}
	sw := &wakeSweep{ls: newListing(fw.deps().CmuxSessions), typed: map[string]bool{}}
	// A draft the default rule refuses, a custom rule allows: typed.
	fw.screen = screenFixture(t, "screen-draft.txt")
	_, out := f.rl.typeGuarded(retrySid, relay.ContinueLine, c, *p, f.clock(), sw, screenOnlyNoter{wakeNoter{f.rl}, true}, nil, nil, nil, nil)
	if out != typeSent || len(fw.continues) != 1 {
		t.Fatalf("custom rule allowed, outcome %v continues %v", out, fw.continues)
	}
	// A free screen the custom rule refuses: held.
	fw.screen = screenFixture(t, "screen-idle-empty.txt")
	sw = &wakeSweep{ls: newListing(fw.deps().CmuxSessions), typed: map[string]bool{}}
	_, out = f.rl.typeGuarded(retrySid, relay.ContinueLine, c, *p, f.clock(), sw, screenOnlyNoter{wakeNoter{f.rl}, false}, nil, nil, nil, nil)
	if out != typeRefused || len(fw.continues) != 1 {
		t.Fatalf("custom rule refused, outcome %v continues %v", out, fw.continues)
	}
	// The wake's noter keeps ScreenAllows (a draft refuses).
	fw.screen = screenFixture(t, "screen-draft.txt")
	sw = &wakeSweep{ls: newListing(fw.deps().CmuxSessions), typed: map[string]bool{}}
	_, out = f.rl.typeGuarded(retrySid, relay.WakeLine, c, *p, f.clock(), sw, wakeNoter{f.rl}, nil, nil, nil, nil)
	if out != typeRefused || len(fw.sent) != 0 {
		t.Fatalf("default rule: outcome %v sent %v", out, fw.sent)
	}
	_ = fmt.Sprint
}
