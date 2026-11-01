package pigtail

// This file is the canonical home of the offsite-backup wire vocabulary.
// pkg/pigtail is the public package the protocol documentation designates as
// the reference (see docs/wire-protocol.md), so the types and constants live
// here rather than in internal/daemon/store, which used to declare them
// itself. internal/daemon/store/offsite_status.go now aliases these
// declarations instead of redeclaring them — a type alias and delegated
// constants, not a second definition that could drift from this one.

// OffsiteStatus names which case of the offsite health decision produced a
// given classification: the "status" field of GET /backup, and what
// internal/daemon/store's ClassifyOffsite returns as OffsiteHealth.Status.
type OffsiteStatus string

const (
	OffsiteOK             OffsiteStatus = "ok"
	OffsiteFailing        OffsiteStatus = "failing"
	OffsiteStuck          OffsiteStatus = "stuck"
	OffsiteInProgress     OffsiteStatus = "in_progress"
	OffsiteStale          OffsiteStatus = "stale"
	OffsiteNeverSucceeded OffsiteStatus = "never_succeeded"
	OffsiteNoAttempt      OffsiteStatus = "no_attempt"
	OffsiteUnconfigured   OffsiteStatus = "unconfigured"
)

// OffsiteSeverity is how urgently an OffsiteStatus needs a human to look:
// the "severity" field of GET /backup, and the value a client compares by
// rank — never by string equality — via OffsiteSeverityAtLeast below.
type OffsiteSeverity string

const (
	OffsiteSeverityOK    OffsiteSeverity = "ok"
	OffsiteSeverityWarn  OffsiteSeverity = "warn"
	OffsiteSeverityAlarm OffsiteSeverity = "alarm"
)

// OffsiteFallbackStatus names which case of the offsite *fallback* decision
// produced a given classification — the secondary destination a later task
// (ClassifyOffsiteFallback, internal/daemon/store) exercises independently
// of the primary. Declared here, alongside OffsiteStatus, so the fallback
// vocabulary starts typed rather than repeating the bare-string mistake this
// task exists to fix.
type OffsiteFallbackStatus string

const (
	OffsiteFallbackOK              OffsiteFallbackStatus = "ok"
	OffsiteFallbackFailing         OffsiteFallbackStatus = "failing"
	OffsiteFallbackNotYetExercised OffsiteFallbackStatus = "not_yet_exercised"
	OffsiteFallbackMisconfigured   OffsiteFallbackStatus = "misconfigured"
)

// offsiteSeverityRank orders the scale ok < warn < alarm. A value that is
// none of the three known constants — the wire form of a severity a newer
// daemon added and this build predates — ranks *above* alarm rather than
// falling into the same bucket as ok: the one place "unknown" gets a
// number, and it must never be the safest one.
func offsiteSeverityRank(sev OffsiteSeverity) int {
	switch sev {
	case OffsiteSeverityOK:
		return 0
	case OffsiteSeverityWarn:
		return 1
	case OffsiteSeverityAlarm:
		return 2
	default:
		return 3
	}
}

// OffsiteSeverityAtLeast reports whether sev is at least as urgent as floor
// on the ok < warn < alarm scale, comparing by rank rather than by string
// equality. It exists for callers like `pigtail servers`'s alarm line, which
// used to gate on `severity != "alarm"` — a comparison a value this package
// has never heard of always fails, so a next tranche adding a severity this
// build does not know about would have gone silent instead of showing up.
// An unrecognized value ranks above alarm (see offsiteSeverityRank), so it
// passes every floor from "at least ok" through "at least alarm": what an
// unknown severity means is not visible here, but it must never render as
// quieter than the loudest thing this build can already express. See the
// unknown-value policy in docs/wire-protocol.md's GET /backup section.
func OffsiteSeverityAtLeast(sev, floor OffsiteSeverity) bool {
	return offsiteSeverityRank(sev) >= offsiteSeverityRank(floor)
}
