package relay

import (
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// WakeLine is what pigtaild types in an idle, unwatched recipient's pane (D3).
const WakeLine = "lis ta boîte pigtail"

// CompactLine is what pigtaild types in a session in Standby, once, to
// compact it (sub_x000000000000041, Tâche 4): the only text typed there (D16).
const CompactLine = "/compact"

// Candidate is what the daemon knows of a recipient at the instant of a wake
// attempt (D26: re-read then, never the sender's view).
type Candidate struct {
	Native    claudesession.Native // sessions/<pid>.json, read now
	NativeOK  bool
	Alive     bool // claudesession.SessionAlive(cfg, pid, session)
	Watched   bool // Relay.watched: a waiter open, or gone < 15 s
	Frozen    bool // maintenance.FrozenSessions (D32)
	Waits     []pigtail.SessionWait
	SurfaceID string         // the registry's cmux surface (UUID); "" = launched outside cmux
	Now       time.Time      // D38: judges a wait's own end
	PidAlive  func(int) bool // D38: a held wait's holder; nil = claudesession.PidAlive
	// Standby: the session is in Standby (sub_x000000000000041, D16), or its
	// Standby could not be read; StandbyReason then names the read error.
	Standby bool
	// DoneCommand: a done command is configured (settings.yaml
	// session.done_command); a declared done wait is then left to it, else the
	// idle pause reclaims the finished session like any other idle one.
	DoneCommand   bool
	StandbyReason string
	// LimitResumePending: the session's limit wait reached its end and the
	// resume at reset (relay_retry_limit.go) has not decided yet — no
	// limit-resume marker for this pid and this reset. Filled by the idle
	// pause only; never set for a session in Standby.
	LimitResumePending bool
}

// blockingWait reports a wait that forbids typing — one the sidebar would
// still show (D38): every declared wait (D30: working included), a held wait
// (D30: mac) whose holder runs and whose Until has not passed, and the
// automatic failures Limit, Login, API error (D32) before their own end.
// The end is the sidebar's own rule (waittype.HeldOver, waittype.FailureReset).
// Other automatic waits come with a native status other than idle and are
// refused there.
func blockingWait(now time.Time, w pigtail.SessionWait, alive func(int) bool) bool {
	var until time.Time
	if w.Until != nil {
		until = *w.Until
	}
	switch w.Source {
	case pigtail.WaitSourceDeclared:
		return true
	case pigtail.WaitSourceHeld:
		return !waittype.HeldOver(now, until, w.HolderPID, alive)
	case pigtail.WaitSourceAuto:
		return waittype.Type(w.Type).Failure() && !waittype.FailureReset(now, until)
	}
	return false
}

// Eligible applies spec §1 except the cmux surface check (SurfaceVerified).
func Eligible(c Candidate) (bool, string) {
	switch {
	case c.Watched:
		return false, "watched"
	case c.SurfaceID == "":
		return false, "no cmux surface"
	case !c.NativeOK:
		return false, "native state unreadable"
	case !c.Alive:
		return false, "pid not alive"
	case c.Native.Kind != "" && c.Native.Kind != "interactive":
		return false, "not interactive (" + c.Native.Kind + ")"
	case c.Native.Status != "idle" && c.Native.Status != "shell":
		return false, "status " + c.Native.Status
	case c.Frozen:
		return false, "frozen"
	}
	alive := c.PidAlive
	if alive == nil {
		alive = claudesession.PidAlive
	}
	for _, w := range c.Waits {
		if blockingWait(c.Now, w, alive) {
			return false, "wait " + w.Type + " (" + w.Source + ")"
		}
	}
	return true, ""
}

// WakeAllowed is Eligible for the wake: a session in Standby is never woken
// (D16 — its message waits in its inbox for the user's prompt). The compaction's
// rule is CompactAllowed (relay_standby.go).
func WakeAllowed(c Candidate) (bool, string) {
	if c.Standby {
		if c.StandbyReason != "" {
			return false, c.StandbyReason
		}
		return false, "standby (D16)"
	}
	return Eligible(c)
}

// CompactAllowed is Eligible for the Standby's /compact (Tâche 4). Only a
// session read in Standby, readably, is compacted; its Stop watcher is then
// not a refusal: its long wait stays open during the Standby but claims
// nothing (WaitMessages, the same predicate, Tâche 10), so it cannot wake the
// session against the /compact. Every other reason of Eligible holds as is;
// the wake keeps "watched" (WakeAllowed).
func CompactAllowed(c Candidate) (bool, string) {
	if !c.Standby {
		return false, "standby ended"
	}
	if c.StandbyReason != "" {
		return false, c.StandbyReason
	}
	c.Watched = false // held by this Standby: it delivers nothing
	return Eligible(c)
}

// SurfaceVerified is D29: cmux sees this session, with this pid, active on
// this surface.
func SurfaceVerified(ls []peers.CmuxSession, sessionID, surfaceID string, pid int) (bool, string) {
	for _, s := range ls {
		if s.SessionID != sessionID {
			continue
		}
		switch {
		case !strings.EqualFold(s.SurfaceID, surfaceID):
			return false, "cmux places it on another surface"
		case !s.ActiveForSurface || s.ActiveSurfaceSessionID != sessionID:
			return false, "another session is active on its surface"
		case s.PID != pid:
			return false, "pid differs from cmux's"
		}
		return true, ""
	}
	return false, "cmux does not list it"
}

// usageLimitMarkers are strings Claude Code shows during a usage-limit wait:
// the status reads idle and the input is empty, but any keystroke cancels the
// auto-continue and the process exits (verify round 3, 2026-10-21). A marker
// counts ANYWHERE in the status area (substring, D10): a false negative types
// into the wait and the session exits (work lost), while a false positive only
// holds the session until the user's next prompt or the message's expiry. The
// broad match is the safe side; a reply quoting a marker in the last block is
// an accepted false positive.
var usageLimitMarkers = []string{"Usage limit reached", "continuing automatically", "You've hit your", "You’ve hit your", "/usage-credits"}

var usageLimitMarkersLower = func() []string {
	out := make([]string, len(usageLimitMarkers))
	for i, m := range usageLimitMarkers {
		out[i] = strings.ToLower(m)
	}
	return out
}()

// usageLimitLine reports whether a status-area line contains a marker.
// Case-insensitive: the line and the markers are lowered once.
func usageLimitLine(line string) bool {
	line = strings.ToLower(line)
	for _, m := range usageLimitMarkersLower {
		if strings.Contains(line, m) {
			return true
		}
	}
	return false
}

// ScreenAllows is spec §3: no usage-limit wait in the status area, the input box visible and empty (no draft,
// multi-line or recalled history; a dialog hides the box and fails closed),
// and the pane without keyboard focus (D33). A fresh session's dimmed
// suggestion (claudesession.InputPlaceholder) counts as empty.
func ScreenAllows(screen string, focused bool) (bool, string) {
	return screenAllows(screen, focused, usageLimitLine)
}

// ScreenAllowsIgnoringUsageLimit is ScreenAllows without the usage-limit
// refusal, for account rotation only: it starts from a StopFailure rate_limit
// and /exits exactly a session at its limit, so cancelling Claude Code's
// auto-continue costs nothing there. The box, draft and focus checks stay.
func ScreenAllowsIgnoringUsageLimit(screen string, focused bool) (bool, string) {
	return screenAllows(screen, focused, nil)
}

// screenAllows is the screen rule; refuseLine, when not nil, names the
// status-area lines that refuse as a usage-limit wait.
func screenAllows(screen string, focused bool, refuseLine func(string) bool) (bool, string) {
	if focused {
		return false, "pane has keyboard focus"
	}
	if refuseLine != nil {
		// Only the status area counts (the last message block above the box,
		// from its "⏺" line, plus the footer below it): a marker quoted in the
		// conversation is history and would hold the typing forever. A marker
		// anywhere in that area refuses (D10, see usageLimitMarkers). No
		// recognisable box: the whole screen, as before.
		area, boxed := claudesession.StatusArea(screen)
		if !boxed {
			area = screen
		}
		for _, line := range strings.Split(area, "\n") {
			if refuseLine(line) {
				return false, "usage limit wait (not idle)"
			}
		}
	}
	draft, found := claudesession.InputDraft(screen)
	if !found {
		return false, waittype.RotationNoInputBox
	}
	if draft != "" && !claudesession.InputPlaceholder(draft) {
		return false, "draft in the input"
	}
	return true, ""
}

// PauseAllowed is Eligible for the idle pause (sub_x000000000000036,
// D11-D16): a threshold above zero; a Standby read readably is accepted (D16:
// the pause keeps the session in Standby), an unreadable one refused; not
// parked as a background job; the native status shell refused (D12: a
// background task would die with the process); then Eligible with two differences
// — the Stop watcher is no refusal and the declared waits
// done and wrapped are removed first (D1). Finally the session must have
// been idle for the threshold.
func PauseAllowed(c Candidate, threshold time.Duration) (bool, string) {
	switch {
	case threshold <= 0:
		return false, "idle pause off"
	case c.Standby && c.StandbyReason != "":
		return false, c.StandbyReason
	case c.Native.ParkedJobID != "":
		return false, "parked as a background job"
	case c.Native.Status == "shell":
		return false, "background task running (status shell)"
	}
	if c.DoneCommand && HasDeclaredWait(c.Waits, "done") {
		return false, "done: left to the done command"
	}
	// A limit past its reset is owed a "continue" (LimitResumeGrace later):
	// the pause must not stop the process before. The hold is bounded.
	if c.LimitResumePending && limitResumeHeld(c) {
		return false, "limit reset: resume pending"
	}
	c.Watched = false
	return idleEligible(c, threshold)
}

// WrapAllowed is Eligible for the idle wrap: a declared done wait, idle for
// the threshold. A session in Standby is never typed in (D16, D3), readable
// or not; the Stop watcher stays a refusal (the wrap waits for it, like the
// wake). Same refusals as PauseAllowed otherwise.
func WrapAllowed(c Candidate, threshold time.Duration) (bool, string) {
	switch {
	case threshold <= 0:
		return false, "idle pause off"
	case c.Standby && c.StandbyReason != "":
		return false, c.StandbyReason
	case c.Standby:
		return false, "standby (D16)"
	case !HasDeclaredWait(c.Waits, "done"):
		return false, "no declared done wait"
	case c.Native.ParkedJobID != "":
		return false, "parked as a background job"
	case c.Native.Status == "shell":
		return false, "background task running (status shell)"
	}
	return idleEligible(c, threshold)
}

// HasDeclaredWait reports whether a wait of this type was declared by the
// session itself (not derived).
func HasDeclaredWait(ws []pigtail.SessionWait, typ string) bool {
	for _, w := range ws {
		if w.Source == pigtail.WaitSourceDeclared && w.Type == typ {
			return true
		}
	}
	return false
}

// idleEligible drops the declared done and wrapped waits (D1), runs Eligible
// and checks the idle time against the threshold. It never alters the
// caller's waits.
func idleEligible(c Candidate, threshold time.Duration) (bool, string) {
	waits := make([]pigtail.SessionWait, 0, len(c.Waits))
	for _, w := range c.Waits {
		if w.Source == pigtail.WaitSourceDeclared && (w.Type == "done" || w.Type == "wrapped") {
			continue
		}
		waits = append(waits, w)
	}
	c.Waits = waits
	if ok, why := Eligible(c); !ok {
		return false, why
	}
	if c.Native.StatusUpdatedAt <= 0 {
		return false, "no status time"
	}
	idle := c.Now.Sub(time.UnixMilli(c.Native.StatusUpdatedAt))
	if idle < threshold {
		return false, "idle " + idle.Round(time.Second).String() + " < " + threshold.Round(time.Second).String()
	}
	return true, ""
}
