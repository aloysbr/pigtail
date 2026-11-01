package handlers

// Auto-retry of a session stopped on an API error (sub_x000000000000013):
// at each relay sweep (sweepIdle, between the compaction and the wrap), a
// session whose automatic wait is api_error gets the bare line "continue"
// typed in its cmux pane — through typeGuarded, so the wake's guards hold
// (focus, draft, Standby, cmux check), with no new cmux call and no new
// place that runs cmux. A network failure waits for the API to be reachable
// again (netprobe, at most one probe per sweep); anything else follows the
// back-off relay.RetryDelays, five tries. The state of a series is the
// journal: one api-retry-sent row per try (its id carries the anchor, the
// rank and the class), api-retry-held per refusal reason, api-retry-exhausted.

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	retryKindSent      = "api-retry-sent"
	retryKindHeld      = "api-retry-held"
	retryKindExhausted = "api-retry-exhausted"

	retryClassUnknown = "unknown"
	retryErrorTextMax = 120
	retrySendFailed   = "send failed"
)

func retryIDPrefix(s string, pid int) string { return "api-retry:" + s + ":" + strconv.Itoa(pid) + ":" }

func retrySentID(s string, pid int, anchor time.Time, try int, class string, other int) string {
	return fmt.Sprintf("%s%d:%d:%s:%d", retryIDPrefix(s, pid), anchor.UnixMilli(), try, class, other)
}

func retryHeldID(s string, pid int, anchor time.Time, try int, reason string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(reason))
	return fmt.Sprintf("%s%d:%d:held:%x", retryIDPrefix(s, pid), anchor.UnixMilli(), try, h.Sum32())
}

func retryExhaustedID(s string, pid int, anchor time.Time) string {
	return fmt.Sprintf("%s%d:exhausted", retryIDPrefix(s, pid), anchor.UnixMilli())
}

// retrySent is the last api-retry-sent row of a session and pid.
type retrySent struct {
	at     time.Time
	anchor time.Time
	try    int
	other  int
	class  string
}

func (r retrySent) series() relay.Series {
	return relay.Series{Anchor: r.anchor, Tries: r.try, OtherTries: r.other, LastTry: r.at}
}

// parseRetrySent reads <anchorMs>:<try>:<class>:<otherTry> from the end of an id.
func parseRetrySent(e pigtail.JournalEntry) (retrySent, bool) {
	f := strings.Split(e.ID, ":")
	if len(f) < 4 {
		return retrySent{}, false
	}
	f = f[len(f)-4:]
	ms, err1 := strconv.ParseInt(f[0], 10, 64)
	try, err2 := strconv.Atoi(f[1])
	other, err3 := strconv.Atoi(f[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return retrySent{}, false
	}
	return retrySent{at: e.Timestamp, anchor: time.UnixMilli(ms).UTC(), try: try, class: f[2], other: other}, true
}

// retryLast reads the newest api-retry-sent of this session and pid.
func (rl *Relay) retryLast(p pigtail.PeerSession) (*retrySent, error) {
	rows, err := rl.listJournalRows(store.JournalFilter{Scope: sessionScope(p), Kind: retryKindSent, IDPrefix: retryIDPrefix(p.SessionID, p.PID), Limit: 1})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	r, ok := parseRetrySent(rows[0])
	if !ok {
		return nil, fmt.Errorf("unreadable retry marker %s", rows[0].ID)
	}
	return &r, nil
}

// retryJournal writes one row of the auto-retry; created is false when it
// already exists (first writer wins).
func (rl *Relay) retryJournal(p pigtail.PeerSession, kind, id, summary string, now time.Time) (bool, error) {
	e := pigtail.JournalEntry{ID: id, Timestamp: now, Kind: kind, Scope: string(sessionScope(p)), SubjectID: p.SubjectID,
		Summary: summary, Source: "pigtail:session/" + p.SessionID}
	if err := e.Validate(); err != nil {
		return false, err
	}
	return rl.addJournalRow(&e)
}

// retryFlatten makes the error text one line (the journal refuses CR and LF)
// then cuts it.
func retryFlatten(s string) string {
	s = strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(s)), " ")
	if r := []rune(s); len(r) > retryErrorTextMax {
		s = string(r[:retryErrorTextMax]) + "…"
	}
	return s
}

// retryNeeds: the seams the retry reads and types through; any nil keeps it off.
func (rl *Relay) retryWired() bool {
	w := rl.cfg.Wake
	return w != nil && w.ReadNative != nil && w.SessionAlive != nil && w.Focused != nil && w.SendLine != nil &&
		w.CmuxSessions != nil && w.ReadScreen != nil && w.LastAPIError != nil && w.HumanPromptsSince != nil
}

// retrySweep is the per-sweep memory of the retry: the API probe is made at
// most once.
type retrySweep struct {
	probed, reachable bool
}

func (rl *Relay) retryReachable(rs *retrySweep) bool {
	if !rs.probed {
		rs.probed = true
		rs.reachable = rl.cfg.Wake.APIReachable != nil && rl.cfg.Wake.APIReachable()
	}
	return rs.reachable
}

// retryStopped is the auto-retry's part of a sweep: the API errors, then the
// limits whose reset passed (resumeLimit). False when a cmux answer
// every session needs failed (typeStop): the wrap and the pause wait for the
// next sweep.
func (rl *Relay) retryStopped(now time.Time, sw *wakeSweep) bool {
	if !rl.retryWired() {
		return true
	}
	ps, err := rl.svc.Store.ListPeers(false)
	if err != nil {
		rl.svc.logf("retry: peers unreadable: %v", err)
		return true
	}
	waits, err := rl.svc.Store.ListSessionWaits()
	if err != nil {
		rl.svc.logf("retry: waits unreadable: %v", err)
		return true
	}
	rs := &retrySweep{}
	for _, p := range ps {
		// Cheap first: a session with no api_error wait costs no further read
		// (candidate reads the Standby, the native state, the pid).
		if lw, ok := autoWait(waits[p.SessionID], "limit"); ok && lw.Until != nil && !now.Before(lw.Until.Add(relay.LimitResumeGrace)) {
			if !rl.resumeLimit(p, lw, now, sw) {
				return false
			}
			continue
		}
		w, ok := autoWait(waits[p.SessionID], "api_error")
		if !ok {
			rl.retryClearNote(p.SessionID)
			continue
		}
		c, err := rl.candidate(p)
		if err != nil {
			rl.retryNote(p.SessionID, "waits unreadable: "+err.Error())
			continue
		}
		if !rl.retryOne(p, c, w, now, sw, rs) {
			return false
		}
	}
	return true
}

// autoWait is the session's automatic wait of this type.
func autoWait(ws []pigtail.SessionWait, typ string) (pigtail.SessionWait, bool) {
	for _, w := range ws {
		if w.Source == pigtail.WaitSourceAuto && w.Type == typ {
			return w, true
		}
	}
	return pigtail.SessionWait{}, false
}

// retryOne handles one session stopped on an API error. False stops the sweep.
func (rl *Relay) retryOne(p pigtail.PeerSession, c relay.Candidate, w pigtail.SessionWait, now time.Time, sw *wakeSweep, rs *retrySweep) bool {
	s := p.SessionID
	// Out of the retry's perimeter: only the daemon log says why.
	switch {
	case c.SurfaceID == "":
		rl.retryNote(s, "no cmux surface")
		return true
	case !c.NativeOK:
		rl.retryNote(s, "native state unreadable")
		return true
	case !c.Alive:
		rl.retryNote(s, "pid not alive")
		return true
	case c.Native.Kind != "" && c.Native.Kind != "interactive":
		rl.retryNote(s, "not interactive ("+c.Native.Kind+")")
		return true
	}
	if sw.typed[s] {
		rl.retryNote(s, "typed into during this sweep")
		return true
	}

	// 1. The error: its class comes from the transcript.
	class := retryClassUnknown
	errText := ""
	if e, ok := rl.cfg.Wake.LastAPIError(p.ConfigDir, s); ok && !e.At.Before(w.Since.Add(-time.Minute)) {
		class, errText = e.Class(), e.Text
	}

	// 2. The series: the journal, then the transcript.
	last, err := rl.retryLast(p)
	if err != nil {
		rl.retryNote(s, "journal unreadable: "+err.Error())
		return true
	}
	series := relay.Series{Anchor: w.Since}
	stop := w.Since
	sendFailed := false
	var heldWait bool // the wait is the one from before our last try
	if last != nil {
		switch {
		case w.Since.After(last.at):
			cont, err := rl.retryContinues(p, *last)
			if err != nil {
				class = retryClassUnknown // transcript or store unreadable
			} else if cont {
				series = last.series()
			}
		default:
			heldWait = true
			series = last.series()
			stop = last.at
			if rows, err := rl.listJournalRows(store.JournalFilter{Scope: sessionScope(p), Kind: retryKindHeld,
				IDs: []string{retryHeldID(s, p.PID, last.anchor, last.try, retrySendFailed)}, Limit: 1}); err == nil && len(rows) > 0 {
				sendFailed = true
			}
		}
	}
	if class == retryClassUnknown {
		rl.retryHeld(p, series.Anchor, series.Tries+1, class, series.OtherTries, "transcript unreadable", now)
		return true
	}
	if heldWait && !sendFailed {
		// Our "continue" was typed and the wait stays: the Enter was swallowed,
		// or a guard refused after the marker. Never retyped.
		rl.retryHeld(p, series.Anchor, series.Tries, class, series.OtherTries, "wait unchanged since the last try", now)
		rl.retryPill(p, w, relay.ReasonHeld)
		return true
	}

	step := relay.PlanRetry(series, class, stop, now, nil)
	anchor := series.Anchor
	if step.Exhausted {
		why := fmt.Sprintf("gave up after %d tries", relay.MaxOtherTries)
		if class == claudesession.ClassNetwork {
			why = "gave up 12 h after the stop"
		}
		summary := fmt.Sprintf("%s: %s · %s · %d/%d", wakeShort(s), why, class, series.OtherTries, relay.MaxOtherTries)
		if created, err := rl.retryJournal(p, retryKindExhausted, retryExhaustedID(s, p.PID, anchor), summary, now); err != nil {
			rl.retryNote(s, "exhausted marker not written: "+err.Error())
		} else if created {
			rl.svc.logf("retry: %s: %s", wakeShort(s), why)
		}
		rl.retryPill(p, w, step.Reason)
		return true
	}
	due := !now.Before(step.Due)
	if !due {
		rl.retryPill(p, w, step.Reason)
		return true
	}
	if ok, why := relay.RetryAllowed(c); !ok {
		rl.retryHeld(p, anchor, step.Try, class, step.OtherTry, why, now)
		rl.retryPill(p, w, relay.ReasonHeld)
		return true
	}
	if step.NeedNetwork && !rl.retryReachable(rs) {
		rl.retryPill(p, w, step.Reason)
		return true
	}
	return rl.retryType(p, c, w, step, class, errText, anchor, now, sw)
}

// retryContinues tells whether the new stop belongs to the series of last:
// no turn succeeded since its try, and the only prompts typed are our own
// "continue" (the user or another text resumed the session otherwise).
func (rl *Relay) retryContinues(p pigtail.PeerSession, last retrySent) (bool, error) {
	ok, err := rl.svc.Store.TurnSucceededSince(p.SessionID, last.at)
	if err != nil {
		return false, err
	}
	if ok {
		return false, nil
	}
	prompts, err := rl.cfg.Wake.HumanPromptsSince(p.ConfigDir, p.SessionID, last.at)
	if err != nil {
		return false, err
	}
	switch len(prompts) {
	case 0:
		return true, nil
	case 1:
		return strings.TrimSpace(prompts[0].Text) == relay.ContinueLine && !prompts[0].Image, nil
	}
	return false, nil
}

// retryPill puts the retry's reason on the API error pill when it changed.
func (rl *Relay) retryPill(p pigtail.PeerSession, w pigtail.SessionWait, reason string) {
	if w.Reason == reason {
		return
	}
	changed, err := rl.svc.Store.SetAutoWaitReason(p.SessionID, "api_error", w.Since, reason)
	if err != nil {
		rl.retryNote(p.SessionID, "pill reason not written: "+err.Error())
		return
	}
	if changed {
		rl.publishWait(p, w.Type, w.Source)
	}
}

// retryHeld journals a refusal once per try and reason, and logs it once.
func (rl *Relay) retryHeld(p pigtail.PeerSession, anchor time.Time, try int, class string, other int, reason string, now time.Time) {
	s := p.SessionID
	rl.retryNote(s, reason)
	summary := fmt.Sprintf("%s: held: %s · try %d · %s · %d/%d", wakeShort(s), reason, try, class, other, relay.MaxOtherTries)
	if _, err := rl.retryJournal(p, retryKindHeld, retryHeldID(s, p.PID, anchor, try, reason), summary, now); err != nil {
		rl.retryNote(s, "held marker not written: "+err.Error())
	}
}

// retryType types "continue" under the wake's guards. The marker is written in
// beforeSend, after the last guard and the session budget, right before the
// send (the wrap's rule): a guard or the budget refusing consumes no try.
func (rl *Relay) retryType(p pigtail.PeerSession, c relay.Candidate, w pigtail.SessionWait, step relay.RetryStep, class, errText string, anchor, now time.Time, sw *wakeSweep) bool {
	s := p.SessionID
	wk := rl.cfg.Wake
	marked, budgeted := false, false
	noter := &retryNoter{rl: rl, onHeld: func(reason string) {
		rl.retryHeld(p, anchor, step.Try, class, step.OtherTry, reason, now)
	}}
	beforeSend := func() error {
		sw.focusDone = true
		sw.focused, sw.focusErr = wk.Focused()
		if sw.focusErr != nil {
			rl.svc.logf("retry: %s: cmux identify: %v", wakeShort(s), sw.focusErr)
			return errTypeStop
		}
		if strings.EqualFold(sw.focused, c.SurfaceID) {
			return fmt.Errorf("pane has keyboard focus")
		}
		n, ok := wk.ReadNative(p.ConfigDir, p.PID)
		if !ok || n.SessionID != s || n.Status != "idle" || n.StatusUpdatedAt != c.Native.StatusUpdatedAt {
			return fmt.Errorf("left idle during the checks")
		}
		if b := wk.BudgetAcquire; b != nil {
			if ok, why := b(string(sessbudget.KindWake), s); !ok {
				return fmt.Errorf("session budget: %s", why)
			}
			budgeted = true
		}
		summary := fmt.Sprintf("%s: continue typed · try %d · %s · %d/%d", wakeShort(s), step.Try, class, step.OtherTry, relay.MaxOtherTries)
		if errText != "" {
			summary += " · " + retryFlatten(errText)
		}
		created, err := rl.retryJournal(p, retryKindSent, retrySentID(s, p.PID, anchor, step.Try, class, step.OtherTry), summary, now)
		if err != nil {
			return fmt.Errorf("journal write failed: %w", err)
		}
		if !created {
			return fmt.Errorf("marker already written")
		}
		marked = true
		return nil
	}
	_, out := rl.typeGuarded(s, relay.ContinueLine, c, p, now, sw, noter, nil, nil, nil, beforeSend)
	if out != typeSent && budgeted && wk.BudgetRelease != nil {
		wk.BudgetRelease(s)
	}
	switch out {
	case typeStop:
		return false
	case typeSent:
		rl.retryClearNote(s)
		rl.svc.logf("retry: typed %s in %s for %s (try %d, %s)", relay.ContinueLine, c.SurfaceID, wakeShort(s), step.Try, class)
		return true
	}
	if marked {
		// The send failed after the marker: the try is spent, bounded; the
		// next one comes at its own due time.
		rl.retryHeld(p, anchor, step.Try, class, step.OtherTry, retrySendFailed, now)
		next := relay.PlanRetry(relay.Series{Anchor: anchor, Tries: step.Try, OtherTries: step.OtherTry, LastTry: now}, class, now, now, nil)
		rl.retryPill(p, w, next.Reason)
		return true
	}
	rl.retryPill(p, w, relay.ReasonHeld)
	return true
}

// retryNote logs a refusal once per change of reason and session: its own
// memory, never the wake's lastReason.
func (rl *Relay) retryNote(s, reason string) {
	rl.retryOnce(s, "held: "+reason, fmt.Sprintf("retry: %s held: %s", wakeShort(s), reason))
}

func (rl *Relay) retryOnce(s, key, line string) {
	rl.wakeMu.Lock()
	same := rl.retryReason[s] == key
	rl.retryReason[s] = key
	rl.wakeMu.Unlock()
	if !same {
		rl.svc.logf("%s", line)
	}
}

func (rl *Relay) retryClearNote(s string) {
	rl.wakeMu.Lock()
	delete(rl.retryReason, s)
	rl.wakeMu.Unlock()
}

// retryNoter is typeGuarded's noter for the retry: every refusal is logged
// once and journalled as a held row of the try in progress, except the two
// global holds below (start-up grace, cmux check running), which are logged
// only. A per-session cmux read-screen error is journalled too.
type retryNoter struct {
	rl     *Relay
	onHeld func(reason string)
	screen func(screen string, focused bool) (bool, string)
}

// held: a transient hold (start-up grace, a cmux check typing) is logged
// only; any other refusal is journalled for the try in progress.
func (n *retryNoter) held(s, reason string) {
	switch reason {
	case "daemon start-up grace", "pigtail cmux check running":
		n.rl.retryNote(s, reason)
	default:
		n.onHeld(reason)
	}
}

func (n *retryNoter) screenAllows(screen string, focused bool) (bool, string) {
	if n.screen != nil {
		return n.screen(screen, focused)
	}
	return relay.ScreenAllows(screen, focused)
}

func (n *retryNoter) readScreen(s string, err error) {
	// onHeld logs through retryNote; a retryOnce here too would flip the
	// once-per-change key every sweep and log twice a minute.
	n.onHeld("cmux read-screen failed: " + err.Error())
}

// watcherHolds is the last re-check before typing: Standby, readable or not,
// holds (D16, fail closed). The Stop watcher is no refusal: it does not
// resume a failed turn.
func (n *retryNoter) watcherHolds(s string) (bool, string) {
	if in, why := n.rl.inStandby(s); in {
		if why != standbyReason {
			return true, why
		}
		return true, "standby (D16)"
	}
	return false, ""
}
