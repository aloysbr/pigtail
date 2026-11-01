package store

import (
	"fmt"
	"time"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The fallback exercise answers a question ClassifyOffsite cannot: not "did
// the backup run", but "does the *redundant* route still work". On
// one time the host's VPN node key had expired for days and the
// backup kept reading `ok`, because ~/.ssh/config routes the LAN first when
// it answers — the daemon never had a reason to try the other path. An `ok`
// only proves the path that was actually taken, never the one held in
// reserve.
//
// This file is pure: state in, health out, no I/O, no mutation. The probe
// that actually exercises the pinned route lives in
// offsite_fallback_probe.go (a separate file on purpose, so two tasks never
// edit the same new file in sequence), and the loop that decides when to run
// it lives in internal/daemon/offsite_fallback.go. Both write their result
// back through offsiteBackupper.updateState — see offsite_state.go's
// FallbackDest through FallbackVia fields — never directly.

const (
	// OffsiteFallbackInterval is how long a fallback exercise's own last
	// attempt stays "recent enough": internal/daemon/offsite_fallback.go's
	// exerciseFallbackIfDue reads this constant directly (as
	// offsiteFallbackDueInterval, a package-local alias for the exact same
	// value, not a second copy of it) and treats a probe younger than this
	// as not yet due. Exported, unlike every other constant in this block,
	// for that one cross-package reader — it used to be copied there as an
	// unexported literal of its own, pinned equal "by hand" in a comment
	// that had no way to notice the day the two drifted apart. A happy side
	// effect (see the package's plan doc): two consecutive failures are
	// then always at least this far apart, so offsiteFallbackWarnStreak
	// needs no separate age condition the way no_attempt/never_succeeded
	// need declared_at above.
	OffsiteFallbackInterval = 6 * time.Hour

	// offsiteFallbackProbeTimeout bounds a single exercise. Far tighter than
	// RemoteTimeout (offsite_remote.go): the probe writes and removes one
	// empty marker file over ssh, not an rsync transfer, so anything past a
	// handshake plus two round trips is a route that is not really up.
	offsiteFallbackProbeTimeout = 30 * time.Second

	// offsiteFallbackWarnStreak is how many consecutive failed exercises
	// turn the verdict from ok to warn. Two, not one: a single failure is
	// what a laptop asleep at the moment the probe fired produces on its
	// own, and OffsiteFallbackInterval already spaces two failures at least
	// six hours apart — reaching this streak is a fact about the route, not
	// about asking too often.
	offsiteFallbackWarnStreak = 2

	// offsiteFallbackProbeFile is the marker the exercise writes, then
	// removes, on the far side. Never a generation, never a name rsync
	// would touch.
	offsiteFallbackProbeFile = ".pigtail-fallback-probe"
)

// OffsiteFallbackStatus and its constants are aliases onto pkg/pigtail's
// declarations, exactly like OffsiteStatus above: pkg/pigtail is the wire
// vocabulary's one home (see that package's offsite.go), and store already
// imports it.
type OffsiteFallbackStatus = pigtail.OffsiteFallbackStatus

const (
	OffsiteFallbackOK              = pigtail.OffsiteFallbackOK
	OffsiteFallbackFailing         = pigtail.OffsiteFallbackFailing
	OffsiteFallbackNotYetExercised = pigtail.OffsiteFallbackNotYetExercised
	OffsiteFallbackMisconfigured   = pigtail.OffsiteFallbackMisconfigured
)

// offsiteFallbackMisconfiguredReason is why ClassifyOffsiteFallback reached
// OffsiteFallbackMisconfigured, carried on OffsiteFallbackHealth purely so
// RenderOffsiteFallbackDetail can pick its sentence from the struct itself
// rather than re-deriving the reason by re-parsing Dest.
//
// Re-deriving would trust Dest to still describe the same world Status was
// computed from — exactly the inference this whole feature exists to
// forbid one layer up (ClassifyOffsite reads a report, not a live probe).
// An OffsiteFallbackHealth built or reconstructed by something other than
// ClassifyOffsiteFallback — a test fixture, a future migration, a decoded
// wire payload — can carry a Status without Dest still matching whatever
// produced it, and a renderer that re-parses Dest would then confidently
// print the wrong reason instead of the one the classification actually
// found.
//
// Unexported: it is not part of the wire vocabulary (OffsiteFallbackStatus
// keeps its four fixed values — this is not a fifth) and no package outside
// this one needs it; callers render through RenderOffsiteFallbackDetail,
// never by inspecting why.
type offsiteFallbackMisconfiguredReason int

const (
	// offsiteFallbackMisconfiguredReasonNone is the zero value: not
	// (or not yet known to be) misconfigured. ClassifyOffsiteFallback
	// always sets one of the other two constants before it ever reaches
	// OffsiteFallbackMisconfigured, so this value is unreachable from that
	// function in practice — it only exists for a caller that builds an
	// OffsiteFallbackHealth by hand (a test fixture, a future migration, a
	// decoded wire payload) and leaves the reason unset.
	// RenderOffsiteFallbackDetail's misconfigured branch treats it the same
	// as "malformed" (see that function, below), which is NOT accurate
	// either way: a hand-built health with Status==misconfigured, this zero
	// reason, and a Dest that happens to be a well-formed
	// [user@]host:/path equal to the primary destination gets told it "is
	// not an [user@]host:/path destination" — false, for exactly the
	// destination that render branch is naming. Defaulting to "same as
	// primary" instead would be no better: that would assert something
	// concrete a zero value cannot actually back up either, for the
	// opposite case (a genuinely malformed Dest). There is no reason value
	// here that renders correctly for every Dest a caller could pair with
	// it — the honest fix is to always go through ClassifyOffsiteFallback,
	// which never leaves this at its zero value on this status.
	offsiteFallbackMisconfiguredReasonNone offsiteFallbackMisconfiguredReason = iota
	offsiteFallbackMisconfiguredReasonMalformed
	offsiteFallbackMisconfiguredReasonSameAsPrimary
)

// OffsiteFallbackHealth is what ClassifyOffsiteFallback decides about the
// redundant route at a point in time — the fallback's own status, capped
// severity, and the details RenderOffsiteFallbackDetail needs without
// recomputing them from the raw state.
type OffsiteFallbackHealth struct {
	// Configured reports whether a fallback destination is set at all.
	// Callers use it to decide whether to show a row: no destination,
	// nothing to report — the same rule OffsiteState.Configured() applies
	// to the primary route.
	Configured bool
	// Dest is the raw fallback_dest string, carried through unparsed so
	// RenderOffsiteFallbackDetail can name it verbatim in a misconfigured
	// message.
	Dest string

	Status   OffsiteFallbackStatus
	Severity OffsiteSeverity

	// HasLastOK reports whether an exercise has ever succeeded. LastOKAge is
	// only meaningful when this is true, on the same model as
	// OffsiteHealth.HasLastOK.
	HasLastOK bool
	LastOKAge time.Duration

	// HasLastTry reports whether the fallback has ever been exercised at
	// all, successful or not. See LastTryAge below for the one case where
	// HasLastTry is false but the duration it gates is still meaningful.
	HasLastTry bool
	// LastTryAge is the duration since the fallback was last exercised, and
	// is only meaningful when HasLastTry is true — with one deliberate
	// exception: in the not_yet_exercised status, HasLastTry is false
	// (there has truly never been a try) but LastTryAge still carries a
	// duration, borrowed from how long the fallback has been declared (see
	// ClassifyOffsiteFallback). That is what lets RenderOffsiteFallbackDetail
	// print "configured 3h00m ago and not once tried" without a seventh
	// field on this struct — there is no "last try" to measure yet, but
	// there is still a "how long has this sat idle" to report, and declared
	// age is the only fact on record for it.
	LastTryAge time.Duration

	// ConsecutiveFailures is carried straight from
	// OffsiteState.FallbackConsecutiveFailures: a fact about the route, not
	// about the moment this is classified.
	ConsecutiveFailures int
	// LastErr is the fallback's own most recent failure, independent of the
	// primary route's LastErr.
	LastErr string
	// Via is the source address the destination last reported seeing on a
	// successful exercise — see OffsiteState.FallbackVia's doc comment for
	// why "127.0.0.1" is the healthy reading on a userspace-VPN host,
	// not a bug.
	Via string

	// misconfiguredReason is set only when Status ==
	// OffsiteFallbackMisconfigured; see its type's doc comment for why
	// RenderOffsiteFallbackDetail switches on it instead of re-parsing Dest.
	misconfiguredReason offsiteFallbackMisconfiguredReason
}

// ClassifyOffsiteFallback decides what the fallback route's last known state
// means, once, on the same model as ClassifyOffsite. It is a pure function:
// same inputs, same output, no I/O, no mutation of s.
//
// primary is the *already classified* primary route, at the same instant —
// callers pass whatever ClassifyOffsite(s, now) just returned. Two of its
// fields are load-bearing here, not incidental:
//
//   - primary.HasDeclared / primary.DeclaredAge feed the not_yet_exercised
//     escalation. The fallback is declared in the same declare() call as
//     the primary destination (task 8), so "how long has the daemon known
//     it should be exercising this route" is the same clock as the
//     primary's own declared_at — there is no separate fallback_declared_at
//     to invent.
//   - primary.Severity feeds the suppression rule below, the single most
//     important thing this function does.
func ClassifyOffsiteFallback(s OffsiteState, primary OffsiteHealth, now time.Time) OffsiteFallbackHealth {
	h := OffsiteFallbackHealth{
		Dest:                s.FallbackDest,
		ConsecutiveFailures: s.FallbackConsecutiveFailures,
		LastErr:             s.FallbackLastErr,
		Via:                 s.FallbackVia,
	}
	if s.FallbackDest == "" {
		// Nothing configured: every other field stays at its zero value —
		// except Severity, posed explicitly to SeverityOK rather than left
		// unset. OffsiteSeverity's zero value is "", and an empty string
		// ranks *above* alarm under pigtail.OffsiteSeverityAtLeast's
		// unknown-value policy (see offsiteSeverityRank): a caller that
		// compares by rank, exactly as this package's own severity
		// invariant test does, would read an unconfigured fallback as
		// outranking a genuine alarm — the reverse of "never reaches
		// alarm", the one guarantee this whole function exists to hold.
		// RenderOffsiteFallbackDetail treats Configured==false as "nothing
		// to render" regardless, the same contract OffsiteUnconfigured has
		// on the primary side, but Severity must still be a real, ranked
		// value for any caller that reads it before checking Configured.
		h.Severity = SeverityOK
		return h
	}
	h.Configured = true

	if !s.FallbackLastOKAt.IsZero() {
		h.HasLastOK = true
		h.LastOKAge = now.Sub(s.FallbackLastOKAt)
	}
	if !s.FallbackLastTryAt.IsZero() {
		h.HasLastTry = true
		h.LastTryAge = now.Sub(s.FallbackLastTryAt)
	}

	_, _, parses := ParseRemoteDest(s.FallbackDest)

	switch {
	case !parses || s.FallbackDest == s.DestDir:
		// Immediate, and ahead of every other case: a malformed alias or one
		// that resolves to the primary destination exercises nothing,
		// regardless of what any counter on record says.
		h.Status = OffsiteFallbackMisconfigured
		h.Severity = SeverityWarn
		if !parses {
			h.misconfiguredReason = offsiteFallbackMisconfiguredReasonMalformed
		} else {
			h.misconfiguredReason = offsiteFallbackMisconfiguredReasonSameAsPrimary
		}

	case !h.HasLastTry:
		h.Status = OffsiteFallbackNotYetExercised
		if primary.HasDeclared {
			// Borrowed for rendering only — see HasLastTry's doc comment
			// above. HasLastTry itself stays false: there has truly been no
			// exercise.
			h.LastTryAge = primary.DeclaredAge
		}
		if h.LastTryAge >= offsiteLastOKAlarmAfter {
			h.Severity = SeverityWarn
		} else {
			h.Severity = SeverityOK
		}

	case s.FallbackConsecutiveFailures >= offsiteFallbackWarnStreak:
		h.Status = OffsiteFallbackFailing
		h.Severity = SeverityWarn

	case s.FallbackConsecutiveFailures == 1:
		h.Status = OffsiteFallbackFailing
		h.Severity = SeverityOK

	default:
		h.Status = OffsiteFallbackOK
		h.Severity = SeverityOK
	}

	// The rule the whole file exists for. Without it, a laptop away from
	// its NAS with the VPN down reads the primary as failing (or worse) and
	// the fallback as warn *at the same time*, for the same underlying
	// cause — two alarms for one outage, which is how alarms stop getting
	// read. The fallback's entire reason to exist is to answer "the primary
	// looks fine, but is the redundancy actually there" — a question that
	// only makes sense while the primary does look fine. So the moment the
	// primary itself needs attention, the fallback's own verdict is muted
	// to ok, unconditionally, regardless of which case above produced it.
	//
	// Muted, not erased: Status, ConsecutiveFailures and LastErr above are
	// untouched by this rule and keep telling the truth — RenderOffsiteFall-
	// backDetail still renders "FALLBACK FAILING — 5 in a row" even when
	// Severity reads ok. Only the number a caller alerts on is silenced.
	//
	// "Needs attention", not "at least warn": the final whole-branch review
	// (ruling 2) found that gating this on primary.Severity alone — every
	// severity at least warn, no narrower — silences a genuinely dead
	// fallback the moment the primary merely goes stale, no_attempt, or
	// never_succeeded: all three are routine on the primary's own side (see
	// the client-side backup-health fold's
	// routineStatusesAtWarn, computed independently over the wire form of
	// Status) and never light the marker by themselves. A dead fallback
	// muted at exactly that moment leaves *both* routes silent — the
	// marker goes dark mid-outage the instant a laptop is closed for an
	// afternoon, the two-silences-compounding-into-a-blind-spot failure
	// this whole subject exists to close. primaryOffsiteNeedsAttention
	// below narrows the gate to what the file's own rationale above already
	// asks for — "the primary itself needs attention" — instead of the
	// coarser proxy severity>=warn happened to be. An unrecognized (or
	// alarm) severity still suppresses unconditionally, ahead of and
	// regardless of Status: an unknown value must never be read as calmer
	// than alarm (see OffsiteSeverityAtLeast), and by the time a route is
	// genuinely at alarm, Status is never one of the routine values in the
	// first place.
	primaryNeedsAttention := pigtail.OffsiteSeverityAtLeast(primary.Severity, SeverityAlarm) ||
		(primary.Severity == SeverityWarn && primaryOffsiteNeedsAttention(primary))
	if primaryNeedsAttention {
		h.Severity = SeverityOK
	}

	return h
}

// primaryOffsiteNeedsAttention reports whether the primary route's own
// severity being (exactly) warn reflects something actually wrong — a live
// failure, a wedged copy, or a destination silently sharing the database's
// own filesystem — as opposed to a passage-of-time artifact (stale,
// no_attempt, never_succeeded) that resolves on its own the moment a
// sleeping or traveling machine wakes up. Scoped to exactly one caller,
// ClassifyOffsiteFallback's suppression rule above: it decides whether the
// primary route's own trouble is loud enough to justify muting a redundant
// route's independent problem, not whether the primary's own marker should
// light — that is a separate decision the Swift client makes on its own,
// off the wire form of Status (routineStatusesAtWarn), deliberately not
// shared across the process boundary. The two happen to agree on which
// statuses are routine today because they describe the same underlying
// grid, not because one calls the other.
func primaryOffsiteNeedsAttention(h OffsiteHealth) bool {
	return h.Status == OffsiteFailing || h.Status == OffsiteStuck || h.SameDevice
}

// RenderOffsiteFallbackDetail renders the fallback status phrase for an
// OffsiteFallbackHealth, on the same model as RenderOffsiteDetail.
//
// "" for an unconfigured fallback: nothing is set up, so there is nothing to
// say, matching RenderOffsiteDetail's rule for OffsiteUnconfigured.
func RenderOffsiteFallbackDetail(h OffsiteFallbackHealth) string {
	if !h.Configured {
		return ""
	}

	switch h.Status {
	case OffsiteFallbackOK:
		if !h.HasLastOK {
			// Unreachable from ClassifyOffsiteFallback in practice — the ok
			// status is only ever reached after an exercise has landed —
			// but a caller building an OffsiteFallbackHealth by hand (a
			// test fixture, a future migration) gets a safe, honest
			// fallback rather than a duration computed from a zero time.
			return "ok"
		}
		if h.Via == "" {
			return fmt.Sprintf("ok — exercised %s ago", OffsiteShortDuration(h.LastOKAge))
		}
		return fmt.Sprintf("ok — exercised %s ago via %s", OffsiteShortDuration(h.LastOKAge), h.Via)

	case OffsiteFallbackFailing:
		return fmt.Sprintf("FALLBACK FAILING — %d in a row: %s", h.ConsecutiveFailures, h.LastErr)

	case OffsiteFallbackNotYetExercised:
		if h.LastTryAge >= offsiteLastOKAlarmAfter {
			return fmt.Sprintf("NOT EXERCISED — configured %s ago and not once tried",
				OffsiteShortDuration(h.LastTryAge))
		}
		return "not yet exercised"

	case OffsiteFallbackMisconfigured:
		// Switches on the reason ClassifyOffsiteFallback recorded, never on
		// a fresh parse of h.Dest — see offsiteFallbackMisconfiguredReason's
		// doc comment for why re-deriving it here would be exactly the kind
		// of inference from a partial view this whole feature exists to
		// forbid.
		if h.misconfiguredReason == offsiteFallbackMisconfiguredReasonSameAsPrimary {
			return "MISCONFIGURED — the fallback destination is the primary one; it exercises nothing"
		}
		return fmt.Sprintf("MISCONFIGURED — %q is not an [user@]host:/path destination", h.Dest)

	default:
		// Every OffsiteFallbackStatus this build knows about is handled
		// above. Landing here means h.Status is a value this build has
		// never heard of — the wire form of a status a newer daemon
		// produces. Loud and unrecognizable-as-healthy on purpose, on the
		// same model as RenderOffsiteDetail's default case
		// (offsite_status.go): an unknown value must never read as "ok".
		return fmt.Sprintf("UNKNOWN FALLBACK STATUS %q — treat as unhealthy until this build is updated", h.Status)
	}
}
