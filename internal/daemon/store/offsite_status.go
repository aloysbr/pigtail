package store

import (
	"fmt"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// offsiteStuckAfter is when a copy still in flight stops being normal. The
// rotation is hourly and a healthy copy of a sub-megabyte file takes under a
// second, so anything past a few minutes is a mount that is not answering.
const offsiteStuckAfter = 5 * time.Minute

// offsiteStaleAfter is how long a destination may go without a successful copy
// before the row says so on its own. Three missed hourly rotations: long
// enough that a laptop closed over lunch is not an alarm, short enough that a
// day of silence is impossible to miss.
const offsiteStaleAfter = 3 * time.Hour

// offsiteStuckAlarmAfter is when a stuck copy stops being merely worth a
// look and becomes something to act on: an hour past due, not a few minutes.
const offsiteStuckAlarmAfter = time.Hour

// offsiteLastOKAlarmAfter is when the absence of a successful copy stops
// being a warning and becomes an alarm: about a day, past the point a closed
// laptop or a slow network explains it.
const offsiteLastOKAlarmAfter = 26 * time.Hour

// offsiteFailureStreakAlarmAfter is how many failures in a row stop reading
// as a blip a retry will quietly fix and become a fact to act on. On the
// 2026-09-06 outage this is what would have raised the alarm after about
// three hours of the destination actually being *tried*, not three calendar
// hours: an attempt only happens while the machine is awake and only after a
// completed local rotation, so three failed attempts is not the same clock as
// 3am — instead of the 9am the 26h rule produced only by coincidence. A
// repeated failure is a hard fact, not an artefact of the machine being
// asleep.
const offsiteFailureStreakAlarmAfter = 3

// OffsiteStatus and OffsiteSeverity are aliases onto pkg/pigtail's
// declarations, not a second definition of them: pkg/pigtail is the public
// package the protocol documentation designates as the reference (see that
// package's offsite.go), and this package already imports it (store.go).
// An alias (`=`), not a defined type, so store.OffsiteStatus and
// pigtail.OffsiteStatus are the identical type — a value classified here
// needs no conversion to cross into pkg/pigtail or back, and there is
// nothing here for a parity test to compare: it is one declaration, not two
// that happen to agree.
type OffsiteStatus = pigtail.OffsiteStatus

const (
	OffsiteOK             = pigtail.OffsiteOK
	OffsiteFailing        = pigtail.OffsiteFailing
	OffsiteStuck          = pigtail.OffsiteStuck
	OffsiteInProgress     = pigtail.OffsiteInProgress
	OffsiteStale          = pigtail.OffsiteStale
	OffsiteNeverSucceeded = pigtail.OffsiteNeverSucceeded
	OffsiteNoAttempt      = pigtail.OffsiteNoAttempt
	OffsiteUnconfigured   = pigtail.OffsiteUnconfigured
)

// OffsiteSeverity is how urgently an OffsiteStatus needs a human to look.
type OffsiteSeverity = pigtail.OffsiteSeverity

const (
	SeverityOK    = pigtail.OffsiteSeverityOK
	SeverityWarn  = pigtail.OffsiteSeverityWarn
	SeverityAlarm = pigtail.OffsiteSeverityAlarm
)

// OffsiteHealth is what ClassifyOffsite decides about an OffsiteState at a
// point in time: which status it is in, how urgently that matters, and the
// details a renderer needs without recomputing them from the raw state.
type OffsiteHealth struct {
	Status OffsiteStatus
	// Severity is computed here but not yet read by any caller: the CLI still
	// renders Status directly, and consuming Severity is a later task.
	Severity OffsiteSeverity

	// HasLastOK reports whether a copy has ever landed. LastOKAge is only
	// meaningful when this is true — a zero-value age would otherwise be
	// indistinguishable from "a copy landed just now".
	HasLastOK bool
	LastOKAge time.Duration

	// Attempting reports whether a copy was in flight. AttemptAge is only
	// meaningful when this is true.
	Attempting bool
	AttemptAge time.Duration

	SameDevice bool
	LastErr    string

	// ConsecutiveFailures is carried straight from OffsiteState: a repeated
	// failure is a fact about the destination, not about the moment this is
	// classified, so there is nothing to derive from `now` here.
	ConsecutiveFailures int

	// HasDeclared reports whether the state carries a declared_at at all.
	// DeclaredAge is only meaningful when this is true: it is computed even
	// when declared_at is zero (see ClassifyOffsite), which reads as an
	// enormous age rather than a small or zero one, so a caller that reads
	// DeclaredAge without checking HasDeclared first gets an obviously wrong
	// answer rather than a plausible-looking one. A state file written
	// before declared_at existed has HasDeclared false forever, which is
	// exactly the point: no_attempt and never_succeeded must not invent a
	// substitute age they were never actually given.
	HasDeclared bool
	DeclaredAge time.Duration
}

// ClassifyOffsite decides what an OffsiteState means, once, so that the CLI
// and any future HTTP client read the same answer instead of two renderers
// quietly drifting apart. It used to live inline in cmd/pigtail's
// `formatOffsite`, keyed off the same thresholds declared above.
//
// It decides the status and severity of an offsite destination from its last
// known state, as of now. It is a pure function: same inputs, same output,
// no I/O, no mutation of s.
func ClassifyOffsite(s OffsiteState, now time.Time) OffsiteHealth {
	h := OffsiteHealth{
		SameDevice:          s.SameDevice,
		LastErr:             s.LastErr,
		ConsecutiveFailures: s.ConsecutiveFailures,
	}

	if !s.LastOKAt.IsZero() {
		h.HasLastOK = true
		h.LastOKAge = now.Sub(s.LastOKAt)
	}
	if s.Attempting() {
		h.Attempting = true
		h.AttemptAge = now.Sub(s.AttemptStartedAt)
	}
	// Computed unconditionally, unlike HasLastOK/LastOKAge and
	// Attempting/AttemptAge above: s.DeclaredAt's zero value is year 1, so
	// now.Sub of it is on the order of two millennia — comfortably past
	// offsiteLastOKAlarmAfter on its own. That is deliberate: it is what
	// makes the HasDeclared gate below load-bearing rather than redundant.
	// A gate that only ever guards a value already at its harmless zero
	// (the pattern used elsewhere in this struct) would still pass every
	// test even if a later edit dropped the gate — this shape does not.
	h.HasDeclared = !s.DeclaredAt.IsZero()
	h.DeclaredAge = now.Sub(s.DeclaredAt)

	switch {
	case !s.Configured():
		h.Status = OffsiteUnconfigured

	case s.LastErr != "":
		h.Status = OffsiteFailing

	case s.Attempting():
		// A copy in flight is normal for seconds and a symptom for hours.
		if h.AttemptAge >= offsiteStuckAfter {
			h.Status = OffsiteStuck
		} else {
			h.Status = OffsiteInProgress
		}

	case s.LastTryAt.IsZero():
		// Declared at startup and never acted on: no copy, no error, nothing
		// in the log.
		h.Status = OffsiteNoAttempt

	case s.LastOKAt.IsZero():
		h.Status = OffsiteNeverSucceeded

	case now.Sub(s.LastOKAt) >= offsiteStaleAfter:
		// No error on record, nothing in flight, and yet the copies stopped.
		h.Status = OffsiteStale

	default:
		h.Status = OffsiteOK
	}

	h.Severity = classifyOffsiteSeverity(h)
	return h
}

// classifyOffsiteSeverity applies the severity grid.
func classifyOffsiteSeverity(h OffsiteHealth) OffsiteSeverity {
	sev := SeverityOK

	switch h.Status {
	case OffsiteFailing, OffsiteStale, OffsiteNoAttempt, OffsiteNeverSucceeded:
		sev = SeverityWarn

	case OffsiteStuck:
		if h.AttemptAge >= offsiteStuckAlarmAfter {
			sev = SeverityAlarm
		} else {
			sev = SeverityWarn
		}

	case OffsiteOK, OffsiteInProgress, OffsiteUnconfigured:
		// No escalation from status alone.
	}

	// no_attempt and never_succeeded carry no last_ok_at at all — HasLastOK
	// is false for them, so this rule has nothing to measure and correctly
	// does not fire (arbitrage: don't invent a substitute age).
	if h.HasLastOK && h.LastOKAge >= offsiteLastOKAlarmAfter {
		sev = maxOffsiteSeverity(sev, SeverityAlarm)
	}

	if h.SameDevice {
		sev = maxOffsiteSeverity(sev, SeverityWarn)
	}

	// no_attempt and never_succeeded carry no last_ok_at at all — HasLastOK
	// is false for them, so the rule above has nothing to measure and
	// correctly does not fire. declared_at is their substitute: how long the
	// daemon has known it should be backing up here, which is a fact both
	// statuses do have, unlike a successful copy. Scoped to exactly these
	// two statuses so a stale declared_at on an otherwise-healthy or already
	// -failing destination cannot escalate it through a rule aimed at a
	// different problem. HasDeclared gates it the same way HasLastOK gates
	// the rule above: a state file written before declared_at existed must
	// not have an age invented for it.
	if (h.Status == OffsiteNoAttempt || h.Status == OffsiteNeverSucceeded) &&
		h.HasDeclared && h.DeclaredAge >= offsiteLastOKAlarmAfter {
		sev = maxOffsiteSeverity(sev, SeverityAlarm)
	}

	// This rule reads across every status on paper, no_attempt and
	// never_succeeded included. In the state this package actually receives it
	// does not: offsiteBackupper.attempt sets LastErr and increments
	// ConsecutiveFailures together on every failure, and clears both together
	// on every success (internal/daemon/offsite.go), so a nonzero count is
	// never on record without a non-empty LastErr — and a non-empty LastErr
	// classifies as OffsiteFailing before either no_attempt or
	// never_succeeded is ever reached below. That leaves the declared_at rule
	// above as the only way either status reaches alarm in production.
	//
	// A copy currently in flight is excluded outright: the state written
	// before an attempt runs carries the *previous* attempt's failure count
	// forward without yet knowing whether this one will fail too (same
	// function, same file) — counting it as one more failure would turn an
	// ordinary retry in progress into a false alarm, which is exactly what
	// produced an alarm-severity row reading "copy in progress" before this
	// exclusion existed.
	if h.Status != OffsiteInProgress && h.ConsecutiveFailures >= offsiteFailureStreakAlarmAfter {
		sev = maxOffsiteSeverity(sev, SeverityAlarm)
	}

	return sev
}

// maxOffsiteSeverity picks the more urgent of two severities. It is written
// in terms of pigtail.OffsiteSeverityAtLeast rather than a rank function of
// its own: the "an unknown severity never ranks below alarm" property this
// whole scale exists to protect must be decided in exactly one place — the
// public package's — not re-derived here, where a second copy could agree
// today and silently drift the day the public one changes. A tie (b's rank
// equal to a's, the one case where >= and the original > would differ)
// returns b instead of a; every call site below only ever passes a known
// constant as b, so a tie means a and b are the same value and which one
// comes back is unobservable.
func maxOffsiteSeverity(a, b OffsiteSeverity) OffsiteSeverity {
	if pigtail.OffsiteSeverityAtLeast(b, a) {
		return b
	}
	return a
}

// OffsiteSeverityAtLeast delegates to pkg/pigtail.OffsiteSeverityAtLeast —
// see that function's doc for the ok < warn < alarm < unknown contract.
// Kept here, rather than dropped, so this package's own tests
// (offsite_status_test.go) keep compiling and passing unchanged; external
// callers should prefer pigtail.OffsiteSeverityAtLeast directly, as
// cmd/pigtail/servers.go now does.
func OffsiteSeverityAtLeast(sev, floor OffsiteSeverity) bool {
	return pigtail.OffsiteSeverityAtLeast(sev, floor)
}

// RenderOffsiteDetail renders the status phrase for an OffsiteHealth — the
// exact wording `pigtail db list` has always used for its "status:" line.
// It is the shared render arbitrage 3 asked for: cmd/pigtail's formatOffsite
// composes its output around it, and a future HTTP client's "detail" field
// reuses it verbatim so the two surfaces cannot say different things about
// the same state.
//
// "" for OffsiteUnconfigured: an unconfigured destination has nothing to
// report, matching formatOffsite's own rule of printing nothing at all.
func RenderOffsiteDetail(h OffsiteHealth) string {
	switch h.Status {
	case OffsiteUnconfigured:
		return ""

	case OffsiteFailing:
		return fmt.Sprintf("FAILING — %s", h.LastErr)

	case OffsiteStuck:
		return fmt.Sprintf("STUCK — copy in progress for %s, far past the %s it should take",
			OffsiteShortDuration(h.AttemptAge), OffsiteShortDuration(offsiteStuckAfter))

	case OffsiteInProgress:
		return fmt.Sprintf("copy in progress (%s)", OffsiteShortDuration(h.AttemptAge))

	case OffsiteNoAttempt:
		return "NO ATTEMPT YET — the daemon has not tried since it started"

	case OffsiteNeverSucceeded:
		return "NEVER SUCCEEDED — attempted, but no copy has ever landed"

	case OffsiteStale:
		return fmt.Sprintf("STALE — no copy for %s, with nothing reported as failing",
			OffsiteShortDuration(h.LastOKAge))

	case OffsiteOK:
		return "ok"

	default:
		// Every OffsiteStatus this build knows about is handled above. Landing
		// here means h.Status is a value this build has never heard of — the
		// wire form of a status a newer daemon produces and this binary
		// predates. The old `default: return "ok"` read that as health; that
		// is precisely the silence-as-health failure this feature exists to
		// close, so an unrecognized status must say so loudly instead of
		// disappearing into the one word that means everything is fine.
		return fmt.Sprintf("UNKNOWN STATUS %q — treat as unhealthy until this build is updated", h.Status)
	}
}

// OffsiteShortDuration mirrors, exactly, the shortDuration the CLI prints
// ages with. There are three copies, kept apart on purpose: cmd/pigtail's
// (servers.go), internal/dbtool's (pigtaild's backup tool for `pigtail db`) and this one,
// so that the wording this package renders stays byte-for-byte identical to
// what the CLI has always printed and neither the CLI nor dbtool has to import
// this package for a formatter. Exported only so the parity tests
// (TestShortDurationParity in cmd/pigtail and in internal/dbtool) can pin the
// copies to this one — nothing else outside this package should call it;
// formatting durations is store's callers' job via RenderOffsiteDetail.
func OffsiteShortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
