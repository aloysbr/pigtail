package handlers

// Idle wake (lot 2 of the sidebar subject, D3, D26–D38): at each relay sweep,
// a recipient with pending messages, idle and unwatched, gets the wake line
// (relay.WakeLine) typed in its cmux pane — under the screen guard, never twice
// without a turn in between. Success is the claim (via=prompt), not the typing.
// Never a session in Standby (D16, sub_x000000000000041): relay.WakeAllowed.
// The same sweep, through the same calls and guards (typeGuarded), types
// /compact once in a session in Standby (relay_standby.go): no other place
// runs cmux.

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// WakeDeps are the seams of the idle wake. nil RelayConfig.Wake = no wake
// (every handler test, and the delivery stays "none"/"unknown").
type WakeDeps struct {
	ReadNative         func(cfg string, pid int) (claudesession.Native, bool) // claudesession.ReadNative
	SessionAlive       func(cfg string, pid int, session string) bool         // claudesession.SessionAlive
	PidAlive           func(pid int) bool                                     // claudesession.PidAlive (D38: a held wait's holder)
	Frozen             func() map[string]bool                                 // maintenance.FrozenSessions
	CmuxSessions       func() ([]byte, error)                                 // cmux.SessionsJSONListing (listingCallTimeout, 10 s): the sweep
	CmuxSessionsAtSend func() ([]byte, error)                                 // cmux.SessionsJSONListingBounded (relayCallTimeout, 3 s): POST /messages (D37)
	Focused            func() (string, error)                                 // cmux.FocusedSurfaceID
	ReadScreen         func(surfaceID string) (string, error)                 // cmux.ReadScreenBounded
	SendLine           func(surfaceID, line string) error                     // cmux.SendLineBounded
	CheckRunning       func() bool                                            // cmuxcheck.Running: a cmux check types into panes, the wake holds (T-f)

	// The session budget (sessbudget.Controller), asked by the sweep only and
	// never by a send. All three nil = no gate, explicitly fail-open (every
	// handler test, a daemon built without the controller).
	BudgetAcquire  func(kind, sessionID string) (granted bool, reason string) // once per wake, after the last guard, without waiting
	BudgetRelease  func(sessionID string)                                     // a granted wake that was not typed gives its place back
	BudgetDeferred func(n int, oldest time.Time)                              // the display gauge of the last pass (zero values when none)

	// The auto-retry's seams (relay_retry.go); any nil holds the retry.
	APIReachable      func() bool                                                                // netprobe.Reachable, bounded at 3 s
	LastAPIError      func(cfg, session string) (claudesession.APIError, bool)                   // the transcript's last API error
	HumanPromptsSince func(cfg, session string, since time.Time) ([]claudesession.Prompt, error) // the transcript's typed prompts

	// The idle pause's own seams (relay_pause.go); any nil holds the pause.
	Background     func(cfg, session string, pid int, screen string) ([]string, error) // bgwork.Work, no ancestors (T-j)
	ProcessEnviron func(pid int) (string, error)                                       // cmux.ProcessEnviron: the .sid path
	Signal         func(pid int) error                                                 // SIGTERM
	WaitGone       func(pid int, timeout time.Duration) bool                           // the pid exited within timeout
}

// wakeStartupGrace holds every typing in the first seconds after a daemon
// start, while watchers reconnect (B10). The sweep's first tick comes a
// minute in, so in production the re-registered watchers are what holds a
// watched session; the grace only covers a shorter sweep interval.
const wakeStartupGrace = 2 * watchGrace

// inStartupGrace reports whether now falls in the start-up grace; a relay
// that never ran (startedAt zero) has none.
func (rl *Relay) inStartupGrace(now time.Time) bool {
	rl.wakeMu.Lock()
	started := rl.startedAt
	rl.wakeMu.Unlock()
	return now.Sub(started) < wakeStartupGrace
}

type typedAt struct {
	session         string // as the sweep read it from the store, never from a request
	at              time.Time
	statusUpdatedAt int64 // the native status time seen at the typing
}

// cmuxListing is `cmux sessions list`, fetched at most once per sweep or per
// request, on first need.
type cmuxListing struct {
	fetch func() ([]byte, error)
	done  bool
	ls    []peers.CmuxSession
	err   error
}

func (l *cmuxListing) get() ([]peers.CmuxSession, error) {
	if !l.done {
		l.done = true
		var b []byte
		if b, l.err = l.fetch(); l.err == nil {
			l.ls, l.err = peers.ParseCmuxSessions(b)
		}
	}
	return l.ls, l.err
}

// newListing: the sweep passes Wake.CmuxSessions (listingCallTimeout, D36),
// deliver passes Wake.CmuxSessionsAtSend (relayCallTimeout, D37).
func newListing(fetch func() ([]byte, error)) *cmuxListing { return &cmuxListing{fetch: fetch} }

// wakeShort is the session id as the log shows it: never a label (D23).
func wakeShort(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// candidate gathers what §1 needs, without cmux.
func (rl *Relay) candidate(p pigtail.PeerSession) (relay.Candidate, error) {
	w := rl.cfg.Wake
	c := relay.Candidate{
		Watched:   rl.watched(p.SessionID),
		SurfaceID: strings.ToUpper(strings.TrimSpace(p.CmuxSurfaceID)),
		Now:       rl.cfg.Now(),
		PidAlive:  w.PidAlive,
	}
	if p.PID > 0 && p.ConfigDir != "" {
		n, ok := w.ReadNative(p.ConfigDir, p.PID)
		c.Native, c.NativeOK = n, ok && n.SessionID == p.SessionID
		c.Alive = w.SessionAlive(p.ConfigDir, p.PID, p.SessionID)
	}
	if w.Frozen != nil {
		c.Frozen = w.Frozen()[p.SessionID]
	}
	if in, why := rl.inStandby(p.SessionID); in {
		c.Standby = true // an unreadable Standby too: never read as free
		if why != standbyReason {
			c.StandbyReason = why
		}
	}
	waits, err := rl.svc.Store.SessionWaits(p.SessionID)
	if err != nil {
		return c, err
	}
	c.Waits = waits
	return c, nil
}

// wakeable is §1 in full: Eligible, then the surface checked against cmux.
// failure is set when a read failed (store or cmux) rather than a condition
// refusing: the caller logs it, never drops it.
func (rl *Relay) wakeable(p pigtail.PeerSession, ls *cmuxListing) (ok bool, reason string, failure error) {
	if rl.cfg.Wake == nil || ls == nil {
		return false, "wake off", nil
	}
	c, err := rl.candidate(p)
	if err != nil {
		return false, "waits unreadable", fmt.Errorf("waits unreadable: %w", err)
	}
	if ok, why := relay.WakeAllowed(c); !ok {
		return false, why, nil
	}
	all, err := ls.get()
	if err != nil {
		return false, "cmux sessions list", fmt.Errorf("cmux sessions list: %w", err)
	}
	ok, why := relay.SurfaceVerified(all, p.SessionID, c.SurfaceID, p.PID)
	return ok, why, nil
}

// sendWake is the wake check of a send (deliver): true when the recipient
// is wakeable now. A failed read is logged (same shape as the sweep's line),
// and the answer stays "none"/"unknown" (D29, D37). In the start-up grace no
// sweep types, so the send promises nothing and calls no cmux (B10).
func (rl *Relay) sendWake(session string, ls *cmuxListing) bool {
	if rl.inStartupGrace(rl.cfg.Now()) {
		return false
	}
	p, err := rl.svc.Store.GetPeer(session)
	if err != nil {
		rl.wakeLog("wake: %s: peer unreadable at send: %v", wakeShort(session), err)
		return false
	}
	if p == nil {
		return false
	}
	ok, _, failure := rl.wakeable(*p, ls)
	if failure != nil {
		rl.wakeLog("wake: %s: %v", wakeShort(session), failure)
	}
	return ok
}

// wakeLog writes one wake line flattened to a single line: a send or a claim
// runs on a request, whose values must not split a log line (gosec G706).
func (rl *Relay) wakeLog(format string, args ...any) {
	rl.svc.logf("%s", strings.ReplaceAll(fmt.Sprintf(format, args...), "\n", " "))
}

// sendListing is the listing of a send (POST /messages, /messages/broadcast):
// Wake.CmuxSessionsAtSend, 3 s at most, never the sweep's 10 s listing — it
// runs inside msg send's 10 s CLI context (D37). nil when the wake is off.
func (rl *Relay) sendListing() *cmuxListing {
	if rl.cfg.Wake == nil || rl.cfg.Wake.CmuxSessionsAtSend == nil {
		return nil
	}
	return newListing(rl.cfg.Wake.CmuxSessionsAtSend)
}

// WakeDepsForTest returns the wake seams the relay was built with: read
// only, for the daemon's wiring test (TestServerWiresRelayWake).
func (rl *Relay) WakeDepsForTest() *WakeDeps { return rl.cfg.Wake }

// wakeSweep is what one sweep fetches from cmux once for every recipient.
type wakeSweep struct {
	ls        *cmuxListing
	focusDone bool
	focused   string
	focusErr  error
	// typed: the sessions typeGuarded typed into during this sweep (the
	// wake line, /compact). Distinct from rl.typed, which outlives the
	// sweep: the idle pause never stops a session typed into this sweep.
	typed map[string]bool
	// deferred: the recipients the session budget refused in this pass, and
	// the oldest message among them (the display gauge, wakeIdle).
	deferred       int
	oldestDeferred time.Time
}

// wakeIdle is the wake's part of a sweep (sweepIdle, relay_standby.go): each
// pending recipient, cheapest checks first, no cmux call while a local one
// fails. False when a cmux answer every recipient needs failed (the listing,
// identify): the compaction needs it too, it waits for the next sweep. An
// unreadable pending list skips the wake only (true): the compaction reads
// the Standby rows, not the messages.
func (rl *Relay) wakeIdle(now time.Time, sw *wakeSweep) bool {
	pending, err := rl.svc.Store.PendingRecipientsByAge(now)
	if err != nil {
		rl.svc.logf("wake: pending recipients: %v", err)
		return true
	}
	names := make([]string, len(pending))
	for i, r := range pending {
		names[i] = r.Session
	}
	defer rl.forgetWakes(names)
	// The gauge is replaced at every pass, also when a cmux answer stops it.
	defer func() {
		if b := rl.cfg.Wake.BudgetDeferred; b != nil {
			b(sw.deferred, sw.oldestDeferred)
		}
	}()
	for _, r := range pending { // the one waiting longest first
		if !rl.wakeOne(r, now, sw) {
			return false // a cmux answer every recipient needs failed: next sweep
		}
	}
	return true
}

// wakeOne tries one recipient; false stops the sweep.
func (rl *Relay) wakeOne(r store.PendingRecipient, now time.Time, sw *wakeSweep) bool {
	s := r.Session
	p, err := rl.svc.Store.GetPeer(s)
	if err != nil || p == nil || p.EndedAt != nil {
		rl.wakeNote(s, "unknown or ended")
		return true
	}
	c, err := rl.candidate(*p)
	if err != nil {
		rl.wakeNote(s, "waits unreadable: "+err.Error())
		return true
	}
	if ok, why := relay.WakeAllowed(c); !ok {
		rl.wakeNote(s, why)
		return true
	}
	rl.wakeMu.Lock()
	t, typed := rl.typed[s]
	rl.wakeMu.Unlock()
	if typed && c.Native.StatusUpdatedAt <= t.statusUpdatedAt {
		rl.wakeNote(s, "typed at "+t.at.Format(time.RFC3339)+", never left idle")
		return true
	}
	// The session budget, asked once and last (typeGuarded). the user's own
	// answer skips it: the store says whether one is among the pending.
	var admit func() (bool, string)
	var release func()
	if b := rl.cfg.Wake.BudgetAcquire; b != nil && !r.HumanPending {
		admit = func() (bool, string) { return b(string(sessbudget.KindWake), s) }
		if rel := rl.cfg.Wake.BudgetRelease; rel != nil {
			release = func() { rel(s) }
		}
	}
	n, out := rl.typeGuarded(s, relay.WakeLine, c, *p, now, sw, wakeNoter{rl}, nil, admit, release, nil)
	switch out {
	case typeStop:
		return false
	case typeRefused:
		return true
	case typeBudget:
		sw.deferred++
		if sw.oldestDeferred.IsZero() || r.OldestAt.Before(sw.oldestDeferred) {
			sw.oldestDeferred = r.OldestAt
		}
		return true // the message stays in the inbox; the next pass asks again
	}
	rl.wakeMu.Lock()
	rl.typed[s] = typedAt{session: s, at: now, statusUpdatedAt: n.StatusUpdatedAt}
	delete(rl.lastReason, s)
	rl.wakeMu.Unlock()
	count, _ := rl.svc.Store.CountPending(s, now)
	rl.svc.logf("wake: typed in %s for %s (%d pending)", c.SurfaceID, wakeShort(s), count)
	return true
}

// typeOutcome is how typeGuarded ended.
type typeOutcome int

const (
	typeRefused typeOutcome = iota // a guard refused, or the send failed: nothing typed
	typeSent                       // line typed
	typeStop                       // a cmux answer every session needs failed: stop the sweep
	typeBudget                     // every guard passed, the session budget refused: nothing typed, nothing stored
)

// typeNoter is where typeGuarded writes its refusals: the wake's memory
// (lastReason) or the compaction's (compactReason, relay_standby.go), never
// both, so neither erases the other's line. watcherHolds is the line's own
// D26 re-read of the Stop watcher right before typing.
type typeNoter interface {
	held(s, reason string)
	readScreen(s string, err error)
	watcherHolds(s string) (bool, string)
}

// screenJudge is the optional second face of a typeNoter: its own screen rule
// in place of relay.ScreenAllows. The wake, the compaction and the wrap keep
// the default.
type screenJudge interface {
	screenAllows(screen string, focused bool) (bool, string)
}

type wakeNoter struct{ rl *Relay }

// watcherHolds: a watcher that arrived during the cmux calls wins (D26).
func (n wakeNoter) watcherHolds(s string) (bool, string) {
	if n.rl.watched(s) {
		return true, "watched"
	}
	return false, ""
}

func (n wakeNoter) held(s, reason string) { n.rl.wakeNote(s, reason) }

// readScreen: once per change, like a refusal — a pane that cannot be read
// stays so for many sweeps.
func (n wakeNoter) readScreen(s string, err error) {
	n.rl.noteOnce(s, "read-screen: "+err.Error(), fmt.Sprintf("wake: %s: cmux read-screen: %v", wakeShort(s), err))
}

// errTypeStop is the beforeSend error that stops the whole sweep.
var errTypeStop = errors.New("stop the sweep")

// typeGuarded is the guard sequence of every text pigtaild types in a pane
// (the wake line, the Standby's /compact): paneGuard, the native status
// still idle or shell, then the send. c has passed its own eligibility.
// onScreen, when set, sees the screen read before the input-box guard and
// may end the attempt (false: nothing typed, nothing noted). It returns the
// native state read right before the send; a send marks sw.typed.
//
// admit, when set, is the session budget's one non-waiting ask, made after
// every guard and right before the send (so a refused wake never touches the
// budget): refused, the reason is noted and the outcome is typeBudget;
// granted and the send fails, release gives the place back. nil admit =
// no gate, explicitly (the Standby compaction, a daemon without a budget).
// beforeSend, when set, runs after every other guard and before admit: an
// error aborts the send (typeRefused, noted), errTypeStop stops the sweep
// (typeStop, the callee logged it).
func (rl *Relay) typeGuarded(s, line string, c relay.Candidate, p pigtail.PeerSession, now time.Time, sw *wakeSweep, note typeNoter, onScreen func(screen string) bool, admit func() (bool, string), release func(), beforeSend func() error) (claudesession.Native, typeOutcome) {
	n, out := rl.paneGuard(s, c, p, now, sw, note, onScreen)
	if out != typeSent {
		return n, out
	}
	if n.Status != "idle" && n.Status != "shell" {
		note.held(s, "left idle during the checks")
		return claudesession.Native{}, typeRefused
	}
	if beforeSend != nil {
		if err := beforeSend(); err != nil {
			if errors.Is(err, errTypeStop) {
				return claudesession.Native{}, typeStop
			}
			note.held(s, err.Error())
			return claudesession.Native{}, typeRefused
		}
	}
	if admit != nil {
		if ok, why := admit(); !ok {
			note.held(s, why)
			return claudesession.Native{}, typeBudget
		}
	}
	if err := rl.cfg.Wake.SendLine(c.SurfaceID, line); err != nil {
		if admit != nil && release != nil {
			release()
		}
		rl.svc.logf("wake: %s: cmux send: %v", wakeShort(s), err)
		return claudesession.Native{}, typeRefused
	}
	if sw.typed != nil {
		sw.typed[s] = true
	}
	return n, typeSent
}

// paneGuard is the guard sequence before pigtaild acts on a pane, with no
// send: start-up grace, the surface checked against cmux (D29), the focus and
// the input box (§3, D33), then the re-reads (D26, T-f) — the note's own
// re-read, the native state still this session's, no cmux check typing.
// typeSent means every guard passed (nothing was sent); the native state read
// last is returned for the caller's own status rule (the wake and the
// compaction accept idle or shell, the pause idle only).
func (rl *Relay) paneGuard(s string, c relay.Candidate, p pigtail.PeerSession, now time.Time, sw *wakeSweep, note typeNoter, onScreen func(screen string) bool) (claudesession.Native, typeOutcome) {
	w := rl.cfg.Wake
	if rl.inStartupGrace(now) {
		note.held(s, "daemon start-up grace")
		return claudesession.Native{}, typeRefused
	}
	all, err := sw.ls.get()
	if err != nil {
		rl.svc.logf("wake: %s: cmux sessions list: %v", wakeShort(s), err)
		return claudesession.Native{}, typeStop
	}
	if ok, why := relay.SurfaceVerified(all, s, c.SurfaceID, p.PID); !ok {
		note.held(s, why)
		return claudesession.Native{}, typeRefused
	}
	if !sw.focusDone {
		sw.focusDone = true
		sw.focused, sw.focusErr = w.Focused()
	}
	if sw.focusErr != nil {
		rl.svc.logf("wake: %s: cmux identify: %v", wakeShort(s), sw.focusErr)
		return claudesession.Native{}, typeStop
	}
	screen, err := w.ReadScreen(c.SurfaceID)
	if err != nil {
		note.readScreen(s, err)
		return claudesession.Native{}, typeRefused
	}
	if onScreen != nil && !onScreen(screen) {
		return claudesession.Native{}, typeRefused
	}
	rule := relay.ScreenAllows
	if j, ok := note.(screenJudge); ok {
		rule = j.screenAllows // a caller with its own screen rule (the post-reset retry)
	}
	if ok, why := rule(screen, strings.EqualFold(sw.focused, c.SurfaceID)); !ok {
		note.held(s, why)
		return claudesession.Native{}, typeRefused
	}
	// D26: re-check what may have moved during the cmux calls.
	if held, why := note.watcherHolds(s); held {
		note.held(s, why)
		return claudesession.Native{}, typeRefused
	}
	n, ok := w.ReadNative(p.ConfigDir, p.PID)
	if !ok || n.SessionID != s {
		note.held(s, "left idle during the checks")
		return claudesession.Native{}, typeRefused
	}
	// T-f: a cmux check that started during the sweep — read again right
	// before acting.
	if w.CheckRunning != nil && w.CheckRunning() {
		note.held(s, "pigtail cmux check running")
		return claudesession.Native{}, typeRefused
	}
	return n, typeSent
}

// noteCheckHeld logs the hold on each transition (T-f) and reports it.
func (rl *Relay) noteCheckHeld(held bool) bool {
	rl.wakeMu.Lock()
	changed := rl.checkHeld != held
	rl.checkHeld = held
	rl.wakeMu.Unlock()
	if changed && held {
		rl.svc.logf("wake: held — pigtail cmux check is typing into panes")
	} else if changed {
		rl.svc.logf("wake: resumed — pigtail cmux check done")
	}
	return held
}

// wakeNote logs a refusal once per change of reason and session (Review
// Focus 5): a refusal that holds for hours is one line, not one a minute.
func (rl *Relay) wakeNote(s, reason string) {
	rl.noteOnce(s, "held: "+reason, fmt.Sprintf("wake: %s held: %s", wakeShort(s), reason))
}

// noteOnce logs line when key differs from the session's last noted key.
func (rl *Relay) noteOnce(s, key, line string) {
	rl.wakeMu.Lock()
	same := rl.lastReason[s] == key
	rl.lastReason[s] = key
	rl.wakeMu.Unlock()
	if !same {
		rl.svc.logf("%s", line)
	}
}

// forgetWakes drops the memory of sessions with nothing pending any more.
func (rl *Relay) forgetWakes(pending []string) {
	keep := make(map[string]bool, len(pending))
	for _, s := range pending {
		keep[s] = true
	}
	rl.wakeMu.Lock()
	defer rl.wakeMu.Unlock()
	for s := range rl.typed {
		if !keep[s] {
			delete(rl.typed, s)
		}
	}
	for s := range rl.lastReason {
		if !keep[s] {
			delete(rl.lastReason, s)
		}
	}
}

// wakeClaimed is the success line of a wake (D35): the session claimed its
// messages from a prompt after pigtaild typed in it.
func (rl *Relay) wakeClaimed(session string, n int) {
	rl.wakeMu.Lock()
	t, ok := rl.typed[session]
	delete(rl.typed, session)
	rl.wakeMu.Unlock()
	if ok {
		rl.wakeLog("wake: %s claimed %d via prompt, %s after typing", wakeShort(t.session), n, rl.cfg.Now().Sub(t.at).Round(time.Second))
	}
}
