package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ClassifyOffsiteFallback and RenderOffsiteFallbackDetail are the pure state
// and classification this exercise's probe (a later task) reports through.
// See offsite_fallback.go's package comment for why this exists: an `ok`
// primary backup only proves the route it actually took, never the
// redundant one held in reserve.

var fbNow = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

const fbPrimaryDest = "/Volumes/nas/pigtail"

// A primary route that is itself healthy — the ordinary case, where the
// fallback's own verdict is free to show through unsuppressed.
var fbHealthyPrimary = OffsiteHealth{Severity: SeverityOK}

// --- Contract constants ---------------------------------------------------

// Pins the plan's "valeurs exactes" table. OffsiteFallbackInterval is read
// directly, across the package boundary, by
// internal/daemon/offsite_fallback.go's offsiteFallbackDueInterval — grep
// that file for `store.OffsiteFallbackInterval` to confirm; this test is
// what stops that one real reader's value drifting silently. The other
// three stay unexported and unread outside this package; pinning them here
// is what keeps golangci-lint's unused check quiet about constants this
// file's own classification logic does not all reach on every path.
func TestOffsiteFallbackContractConstants(t *testing.T) {
	if OffsiteFallbackInterval != 6*time.Hour {
		t.Errorf("OffsiteFallbackInterval = %v, want 6h", OffsiteFallbackInterval)
	}
	if offsiteFallbackProbeTimeout != 30*time.Second {
		t.Errorf("offsiteFallbackProbeTimeout = %v, want 30s", offsiteFallbackProbeTimeout)
	}
	if offsiteFallbackWarnStreak != 2 {
		t.Errorf("offsiteFallbackWarnStreak = %d, want 2", offsiteFallbackWarnStreak)
	}
	if offsiteFallbackProbeFile != ".pigtail-fallback-probe" {
		t.Errorf("offsiteFallbackProbeFile = %q, want %q", offsiteFallbackProbeFile, ".pigtail-fallback-probe")
	}
}

// --- OffsiteState round trip ------------------------------------------------

func TestOffsiteFallbackStateRoundTrips(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	okAt := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	tryAt := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)

	want := OffsiteState{
		DestDir:                     fbPrimaryDest,
		FallbackDest:                "backup-b:/srv/backups",
		FallbackLastOKAt:            okAt,
		FallbackLastTryAt:           tryAt,
		FallbackLastErr:             "ssh: connection refused",
		FallbackConsecutiveFailures: 2,
		FallbackVia:                 "127.0.0.1",
	}
	if err := WriteOffsiteState(db, want); err != nil {
		t.Fatalf("WriteOffsiteState: %v", err)
	}

	got := ReadOffsiteState(db)
	if got.FallbackDest != want.FallbackDest {
		t.Errorf("FallbackDest = %q, want %q", got.FallbackDest, want.FallbackDest)
	}
	if !got.FallbackLastOKAt.Equal(okAt) {
		t.Errorf("FallbackLastOKAt = %v, want %v", got.FallbackLastOKAt, okAt)
	}
	if !got.FallbackLastTryAt.Equal(tryAt) {
		t.Errorf("FallbackLastTryAt = %v, want %v", got.FallbackLastTryAt, tryAt)
	}
	if got.FallbackLastErr != want.FallbackLastErr {
		t.Errorf("FallbackLastErr = %q, want %q", got.FallbackLastErr, want.FallbackLastErr)
	}
	if got.FallbackConsecutiveFailures != 2 {
		t.Errorf("FallbackConsecutiveFailures = %d, want 2", got.FallbackConsecutiveFailures)
	}
	if got.FallbackVia != "127.0.0.1" {
		t.Errorf("FallbackVia = %q, want %q", got.FallbackVia, "127.0.0.1")
	}
}

// A state file written before this task existed — Task 3's shape, still
// version 1, with no fallback_* key at all — must read back as a fallback
// that is simply not configured, never as an error and never discarded.
// This is the whole point of adding the fields without bumping
// offsiteStateVersion.
func TestOffsiteFallbackStateWithoutFieldsReadsAsUnconfigured(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "pigtail.db")
	body := `{"version":1,"dest_dir":"/Volumes/nas/pigtail","last_try_at":"2026-09-10T09:00:00Z"}`
	if err := os.WriteFile(OffsiteStatePath(db), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got := ReadOffsiteState(db)
	if !got.Configured() {
		t.Fatal("a valid version-1 file without any fallback_* field was discarded")
	}
	if got.FallbackDest != "" {
		t.Errorf("FallbackDest = %q, want empty for a file predating the fallback fields", got.FallbackDest)
	}

	fb := ClassifyOffsiteFallback(got, ClassifyOffsite(got, fbNow), fbNow)
	if fb.Configured {
		t.Error("Configured = true for a state with no fallback_dest at all")
	}
}

func TestOffsiteFallbackStateVersionStaysAtOne(t *testing.T) {
	if offsiteStateVersion != 1 {
		t.Fatalf("offsiteStateVersion = %d, want 1; the six fallback_* fields must not require a version bump",
			offsiteStateVersion)
	}
}

// --- ClassifyOffsiteFallback: status grid -----------------------------------

func TestClassifyOffsiteFallbackStatus(t *testing.T) {
	tests := []struct {
		name    string
		state   OffsiteState
		primary OffsiteHealth
		status  OffsiteFallbackStatus
		// reason is only checked for the two misconfigured cases below;
		// left at its zero value (offsiteFallbackMisconfiguredReasonNone)
		// for every other case, which is also what ClassifyOffsiteFallback
		// leaves it at outside the misconfigured branch.
		reason offsiteFallbackMisconfiguredReason
	}{
		{
			name:  "unconfigured: empty fallback_dest",
			state: OffsiteState{DestDir: fbPrimaryDest},
		},
		{
			name: "misconfigured: not an [user@]host:/path destination",
			state: OffsiteState{
				DestDir:      fbPrimaryDest,
				FallbackDest: "backup-b",
			},
			primary: fbHealthyPrimary,
			status:  OffsiteFallbackMisconfigured,
			reason:  offsiteFallbackMisconfiguredReasonMalformed,
		},
		{
			// DestDir must itself be remote-shaped here: a local-path
			// DestDir would also fail ParseRemoteDest on its own, and the
			// test would pass even if the "== DestDir" half of the
			// condition were deleted entirely.
			name: "misconfigured: fallback_dest equals dest_dir",
			state: OffsiteState{
				DestDir:      "nas:/srv/backups",
				FallbackDest: "nas:/srv/backups",
			},
			primary: fbHealthyPrimary,
			status:  OffsiteFallbackMisconfigured,
			reason:  offsiteFallbackMisconfiguredReasonSameAsPrimary,
		},
		{
			name: "not_yet_exercised: never tried, just declared",
			state: OffsiteState{
				DestDir:      fbPrimaryDest,
				FallbackDest: "backup-b:/srv/backups",
			},
			primary: OffsiteHealth{Severity: SeverityOK, HasDeclared: true, DeclaredAge: 10 * time.Minute},
			status:  OffsiteFallbackNotYetExercised,
		},
		{
			name: "not_yet_exercised: never tried, no declared_at on record",
			state: OffsiteState{
				DestDir:      fbPrimaryDest,
				FallbackDest: "backup-b:/srv/backups",
			},
			primary: fbHealthyPrimary,
			status:  OffsiteFallbackNotYetExercised,
		},
		{
			name: "failing: 1 consecutive failure",
			state: OffsiteState{
				DestDir:                     fbPrimaryDest,
				FallbackDest:                "backup-b:/srv/backups",
				FallbackLastTryAt:           fbNow.Add(-time.Hour),
				FallbackLastErr:             "ssh: connection refused",
				FallbackConsecutiveFailures: 1,
			},
			primary: fbHealthyPrimary,
			status:  OffsiteFallbackFailing,
		},
		{
			name: "failing: 2 consecutive failures",
			state: OffsiteState{
				DestDir:                     fbPrimaryDest,
				FallbackDest:                "backup-b:/srv/backups",
				FallbackLastTryAt:           fbNow.Add(-time.Hour),
				FallbackLastErr:             "ssh: connection refused",
				FallbackConsecutiveFailures: 2,
			},
			primary: fbHealthyPrimary,
			status:  OffsiteFallbackFailing,
		},
		{
			name: "ok: exercised and healthy",
			state: OffsiteState{
				DestDir:           fbPrimaryDest,
				FallbackDest:      "backup-b:/srv/backups",
				FallbackLastOKAt:  fbNow.Add(-2 * time.Hour),
				FallbackLastTryAt: fbNow.Add(-2 * time.Hour),
				FallbackVia:       "127.0.0.1",
			},
			primary: fbHealthyPrimary,
			status:  OffsiteFallbackOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyOffsiteFallback(tt.state, tt.primary, fbNow)
			if tt.state.FallbackDest == "" {
				if got.Configured {
					t.Fatalf("Configured = true, want false for an empty fallback_dest")
				}
				return
			}
			if !got.Configured {
				t.Fatalf("Configured = false, want true")
			}
			if got.Status != tt.status {
				t.Errorf("Status = %q, want %q", got.Status, tt.status)
			}
			if got.misconfiguredReason != tt.reason {
				t.Errorf("misconfiguredReason = %v, want %v", got.misconfiguredReason, tt.reason)
			}
		})
	}
}

// --- ClassifyOffsiteFallback: severity grid, before suppression ------------

func TestClassifyOffsiteFallbackSeverity(t *testing.T) {
	tests := []struct {
		name     string
		state    OffsiteState
		primary  OffsiteHealth
		severity OffsiteSeverity
	}{
		{
			name: "misconfigured is warn",
			state: OffsiteState{
				DestDir:      fbPrimaryDest,
				FallbackDest: "not-a-remote-dest",
			},
			primary:  fbHealthyPrimary,
			severity: SeverityWarn,
		},
		{
			name: "not_yet_exercised under 26h is ok",
			state: OffsiteState{
				DestDir:      fbPrimaryDest,
				FallbackDest: "backup-b:/srv/backups",
			},
			primary:  OffsiteHealth{Severity: SeverityOK, HasDeclared: true, DeclaredAge: 10 * time.Hour},
			severity: SeverityOK,
		},
		{
			name: "not_yet_exercised at or past 26h is warn",
			state: OffsiteState{
				DestDir:      fbPrimaryDest,
				FallbackDest: "backup-b:/srv/backups",
			},
			primary:  OffsiteHealth{Severity: SeverityOK, HasDeclared: true, DeclaredAge: 30 * time.Hour},
			severity: SeverityWarn,
		},
		{
			name: "1 consecutive failure is ok",
			state: OffsiteState{
				DestDir:                     fbPrimaryDest,
				FallbackDest:                "backup-b:/srv/backups",
				FallbackLastTryAt:           fbNow.Add(-time.Hour),
				FallbackConsecutiveFailures: 1,
			},
			primary:  fbHealthyPrimary,
			severity: SeverityOK,
		},
		{
			name: "2 consecutive failures is warn",
			state: OffsiteState{
				DestDir:                     fbPrimaryDest,
				FallbackDest:                "backup-b:/srv/backups",
				FallbackLastTryAt:           fbNow.Add(-time.Hour),
				FallbackConsecutiveFailures: 2,
			},
			primary:  fbHealthyPrimary,
			severity: SeverityWarn,
		},
		{
			name: "healthy and exercised is ok",
			state: OffsiteState{
				DestDir:           fbPrimaryDest,
				FallbackDest:      "backup-b:/srv/backups",
				FallbackLastOKAt:  fbNow.Add(-2 * time.Hour),
				FallbackLastTryAt: fbNow.Add(-2 * time.Hour),
			},
			primary:  fbHealthyPrimary,
			severity: SeverityOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyOffsiteFallback(tt.state, tt.primary, fbNow)
			if got.Severity != tt.severity {
				t.Errorf("Severity = %q, want %q (status=%q)", got.Severity, tt.severity, got.Status)
			}
		})
	}
}

// --- Boundaries: both sides of every threshold, plus the threshold itself ---

func TestClassifyOffsiteFallbackThresholdBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		state    OffsiteState
		primary  OffsiteHealth
		status   OffsiteFallbackStatus
		severity OffsiteSeverity
	}{
		{
			name: "failure-streak boundary: exactly 1 is failing/ok",
			state: OffsiteState{
				DestDir:                     fbPrimaryDest,
				FallbackDest:                "backup-b:/srv/backups",
				FallbackLastTryAt:           fbNow.Add(-time.Hour),
				FallbackConsecutiveFailures: 1,
			},
			primary:  fbHealthyPrimary,
			status:   OffsiteFallbackFailing,
			severity: SeverityOK,
		},
		{
			// The threshold itself: >= means exactly 2 must already be warn.
			name: "failure-streak boundary: exactly 2 is failing/warn",
			state: OffsiteState{
				DestDir:                     fbPrimaryDest,
				FallbackDest:                "backup-b:/srv/backups",
				FallbackLastTryAt:           fbNow.Add(-time.Hour),
				FallbackConsecutiveFailures: 2,
			},
			primary:  fbHealthyPrimary,
			status:   OffsiteFallbackFailing,
			severity: SeverityWarn,
		},
		{
			name: "declared-age boundary: 25h59 is not_yet_exercised/ok",
			state: OffsiteState{
				DestDir:      fbPrimaryDest,
				FallbackDest: "backup-b:/srv/backups",
			},
			primary: OffsiteHealth{
				Severity: SeverityOK, HasDeclared: true,
				DeclaredAge: 25*time.Hour + 59*time.Minute,
			},
			status:   OffsiteFallbackNotYetExercised,
			severity: SeverityOK,
		},
		{
			name: "declared-age boundary: 26h01 is not_yet_exercised/warn",
			state: OffsiteState{
				DestDir:      fbPrimaryDest,
				FallbackDest: "backup-b:/srv/backups",
			},
			primary: OffsiteHealth{
				Severity: SeverityOK, HasDeclared: true,
				DeclaredAge: 26*time.Hour + time.Minute,
			},
			status:   OffsiteFallbackNotYetExercised,
			severity: SeverityWarn,
		},
		{
			// The threshold itself: >= means exactly 26h00m00s must already
			// be warn, not just the minute past it.
			name: "declared-age boundary: exactly 26h00m00s is not_yet_exercised/warn",
			state: OffsiteState{
				DestDir:      fbPrimaryDest,
				FallbackDest: "backup-b:/srv/backups",
			},
			primary: OffsiteHealth{
				Severity: SeverityOK, HasDeclared: true,
				DeclaredAge: 26 * time.Hour,
			},
			status:   OffsiteFallbackNotYetExercised,
			severity: SeverityWarn,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyOffsiteFallback(tt.state, tt.primary, fbNow)
			if got.Status != tt.status {
				t.Errorf("Status = %q, want %q", got.Status, tt.status)
			}
			if got.Severity != tt.severity {
				t.Errorf("Severity = %q, want %q", got.Severity, tt.severity)
			}
		})
	}
}

// declared_age escalates not_yet_exercised only when declared_at is
// actually on record. A state with no declared_at at all — the shape any
// file predating Task 3 has — must not invent an age; it stays capped at ok
// no matter how implausibly large a caller-supplied DeclaredAge would be if
// it were trusted, because HasDeclared gates it out first.
func TestClassifyOffsiteFallbackWithoutDeclaredAtHasNoSubstituteAge(t *testing.T) {
	state := OffsiteState{
		DestDir:      fbPrimaryDest,
		FallbackDest: "backup-b:/srv/backups",
	}
	primary := OffsiteHealth{Severity: SeverityOK, HasDeclared: false, DeclaredAge: 100 * time.Hour}
	got := ClassifyOffsiteFallback(state, primary, fbNow)
	if got.Status != OffsiteFallbackNotYetExercised {
		t.Fatalf("test setup: Status = %q, want not_yet_exercised", got.Status)
	}
	if got.Severity != SeverityOK {
		t.Errorf("Severity = %q, want ok: HasDeclared=false must not let a stray DeclaredAge escalate", got.Severity)
	}
}

// --- The suppression rule ---------------------------------------------------

// This is the rule the whole file exists for: a primary genuinely failing
// silences the fallback's own severity to ok, even though the fallback's
// counter is genuinely unhealthy on its own — a laptop away from its NAS
// with the VPN down must not double-alarm on the same outage.
//
// Status is set to OffsiteFailing explicitly, not left at its zero value:
// ruling 2 (final whole-branch review) narrowed suppression from "primary
// severity is at least warn" to "the primary itself needs attention"
// (primaryOffsiteNeedsAttention) precisely because the coarser check also
// swallowed a merely-stale, no_attempt, or never_succeeded primary — see
// TestClassifyOffsiteFallbackNotSuppressedWhenPrimaryIsOnlyRoutinelyUnwell
// below. A primary fixture with Severity alone and a zero-value Status would
// no longer exercise this rule at all under the new, narrower gate.
func TestClassifyOffsiteFallbackSuppressedWhenPrimaryIsUnhealthy(t *testing.T) {
	state := OffsiteState{
		DestDir:                     fbPrimaryDest,
		FallbackDest:                "backup-b:/srv/backups",
		FallbackLastTryAt:           fbNow.Add(-time.Hour),
		FallbackLastErr:             "ssh: connection refused",
		FallbackConsecutiveFailures: 5,
	}
	primary := OffsiteHealth{Severity: SeverityWarn, Status: OffsiteFailing}

	got := ClassifyOffsiteFallback(state, primary, fbNow)
	if got.Status != OffsiteFallbackFailing {
		t.Fatalf("test setup: Status = %q, want failing (the counter must keep telling the truth)", got.Status)
	}
	if got.ConsecutiveFailures != 5 {
		t.Errorf("ConsecutiveFailures = %d, want 5: the count is not suppressed, only the severity", got.ConsecutiveFailures)
	}
	if got.Severity != SeverityOK {
		t.Errorf("Severity = %q, want ok: a genuinely failing primary must mute the fallback's own severity", got.Severity)
	}
}

// A primary stuck mid-copy is just as loud a problem as an outright failure
// — it must suppress the fallback too, on the same "the primary is already
// the louder problem" rationale, without needing OffsiteFailing specifically.
func TestClassifyOffsiteFallbackSuppressedWhenPrimaryIsStuck(t *testing.T) {
	state := OffsiteState{
		DestDir:                     fbPrimaryDest,
		FallbackDest:                "backup-b:/srv/backups",
		FallbackLastTryAt:           fbNow.Add(-time.Hour),
		FallbackConsecutiveFailures: 5,
	}
	primary := OffsiteHealth{Severity: SeverityWarn, Status: OffsiteStuck}

	got := ClassifyOffsiteFallback(state, primary, fbNow)
	if got.Severity != SeverityOK {
		t.Errorf("Severity = %q, want ok: a stuck primary must mute the fallback's own severity too", got.Severity)
	}
}

// A primary sharing the database's own filesystem (same_device) is, per
// ruling 1 above, actionable in its own right even though its Status stays
// the plain "ok" a healthy route also reads — this must suppress the
// fallback too: the primary is already the thing needing a human, whatever
// its Status string says.
func TestClassifyOffsiteFallbackSuppressedWhenPrimaryIsSameDevice(t *testing.T) {
	state := OffsiteState{
		DestDir:                     fbPrimaryDest,
		FallbackDest:                "backup-b:/srv/backups",
		FallbackLastTryAt:           fbNow.Add(-time.Hour),
		FallbackConsecutiveFailures: 5,
	}
	primary := OffsiteHealth{Severity: SeverityWarn, Status: OffsiteOK, SameDevice: true}

	got := ClassifyOffsiteFallback(state, primary, fbNow)
	if got.Severity != SeverityOK {
		t.Errorf("Severity = %q, want ok: a same-device primary must mute the fallback's own severity too", got.Severity)
	}
}

// The composition bug ruling 2 exists to close: a primary at exactly warn
// for a routine, passage-of-time reason (stale, no_attempt, never_succeeded
// — none of which ever light the client's own marker on their own, see
// the client-side backup-health fold, routineStatusesAtWarn)
// must NOT mute a fallback that is genuinely dead. Before ruling 2, this
// suppressed unconditionally on primary.Severity alone, which meant a laptop
// closed for an afternoon (primary goes stale) silenced a fallback dead for
// two days at the exact moment the primary's own routine warn already left
// the marker dark — both routes silent at once, worse than either alone.
//
// Mutation: reverting the suppression condition to
// `pigtail.OffsiteSeverityAtLeast(primary.Severity, SeverityWarn)` (the
// pre-ruling-2 form) makes every case in this table fail, since Severity is
// SeverityWarn in each one.
func TestClassifyOffsiteFallbackNotSuppressedWhenPrimaryIsOnlyRoutinelyUnwell(t *testing.T) {
	for _, status := range []OffsiteStatus{OffsiteStale, OffsiteNoAttempt, OffsiteNeverSucceeded} {
		t.Run(string(status), func(t *testing.T) {
			state := OffsiteState{
				DestDir:                     fbPrimaryDest,
				FallbackDest:                "backup-b:/srv/backups",
				FallbackLastTryAt:           fbNow.Add(-time.Hour),
				FallbackConsecutiveFailures: 5,
			}
			primary := OffsiteHealth{Severity: SeverityWarn, Status: status}

			got := ClassifyOffsiteFallback(state, primary, fbNow)
			if got.Severity != SeverityWarn {
				t.Errorf("Severity = %q, want warn: a primary that is merely %q must not mute "+
					"a genuinely dead fallback", got.Severity, status)
			}
		})
	}
}

// The full sequence the composition bug played out as: a fallback already
// dead, then a primary that goes stale — not the other way around — must
// still leave the fallback's own trouble visible throughout, never masked
// by the primary's later, routine warn.
func TestClassifyOffsiteFallbackSequenceStaysVisibleWhenPrimaryGoesStaleAfterTheFallbackDies(t *testing.T) {
	state := OffsiteState{
		DestDir:                     fbPrimaryDest,
		FallbackDest:                "backup-b:/srv/backups",
		FallbackLastTryAt:           fbNow.Add(-48 * time.Hour),
		FallbackConsecutiveFailures: 20, // dead for two days' worth of exercises
	}

	// Moment 1: primary healthy, fallback already dead — visible on its own.
	healthyPrimary := OffsiteHealth{Severity: SeverityOK, Status: OffsiteOK}
	step1 := ClassifyOffsiteFallback(state, healthyPrimary, fbNow)
	if step1.Severity != SeverityWarn {
		t.Fatalf("step 1: Severity = %q, want warn (fallback dead, primary healthy)", step1.Severity)
	}

	// Moment 2: the laptop is closed for an afternoon — the primary goes
	// stale (routine, over three hours with no successful copy). The
	// fallback's own trouble must stay visible: staleness alone is not
	// grounds to mute it.
	stalePrimary := OffsiteHealth{Severity: SeverityWarn, Status: OffsiteStale}
	step2 := ClassifyOffsiteFallback(state, stalePrimary, fbNow)
	if step2.Severity != SeverityWarn {
		t.Fatalf("step 2: Severity = %q, want warn: a stale primary must not mute the still-dead fallback", step2.Severity)
	}
	if step2.Status != OffsiteFallbackFailing {
		t.Errorf("step 2: Status = %q, want failing: the fallback's own status keeps telling the truth", step2.Status)
	}
	if step2.ConsecutiveFailures != 20 {
		t.Errorf("step 2: ConsecutiveFailures = %d, want 20", step2.ConsecutiveFailures)
	}
}

// Suppression fires on alarm too, not just warn — the primary's severity is
// compared with OffsiteSeverityAtLeast(primary.Severity, SeverityAlarm), so
// alarm (which ranks at least as urgent as itself) must suppress as well,
// unconditionally, regardless of Status.
//
// Mutation: replacing OffsiteSeverityAtLeast(primary.Severity, SeverityAlarm)
// with primary.Severity == SeverityAlarm (an equality check instead of a
// floor) would still pass this particular case; what it would break is the
// "unknown severity" test below, which is why both live in this file.
func TestClassifyOffsiteFallbackSuppressedWhenPrimaryIsInAlarm(t *testing.T) {
	state := OffsiteState{
		DestDir:                     fbPrimaryDest,
		FallbackDest:                "backup-b:/srv/backups",
		FallbackLastTryAt:           fbNow.Add(-time.Hour),
		FallbackConsecutiveFailures: 2,
	}
	primary := OffsiteHealth{Severity: SeverityAlarm}

	got := ClassifyOffsiteFallback(state, primary, fbNow)
	if got.Severity != SeverityOK {
		t.Errorf("Severity = %q, want ok: an alarming primary must suppress the fallback's severity too", got.Severity)
	}
}

// A healthy primary must not suppress anything: this is what stops the
// suppression rule from silently swallowing every case above it if it were
// applied unconditionally.
func TestClassifyOffsiteFallbackNotSuppressedWhenPrimaryIsHealthy(t *testing.T) {
	state := OffsiteState{
		DestDir:                     fbPrimaryDest,
		FallbackDest:                "backup-b:/srv/backups",
		FallbackLastTryAt:           fbNow.Add(-time.Hour),
		FallbackConsecutiveFailures: 2,
	}
	got := ClassifyOffsiteFallback(state, fbHealthyPrimary, fbNow)
	if got.Severity != SeverityWarn {
		t.Errorf("Severity = %q, want warn: a healthy primary must not mute a genuine fallback problem", got.Severity)
	}
}

// An unrecognized primary severity — the wire form of a value a newer
// daemon sends — must rank at least as urgent as alarm (see
// OffsiteSeverityAtLeast) and therefore suppress the fallback exactly like
// alarm does. A display that trusted string equality against "warn"/"alarm"
// would double-alarm on precisely the value it cannot interpret.
func TestClassifyOffsiteFallbackSuppressedWhenPrimarySeverityIsUnknown(t *testing.T) {
	state := OffsiteState{
		DestDir:                     fbPrimaryDest,
		FallbackDest:                "backup-b:/srv/backups",
		FallbackLastTryAt:           fbNow.Add(-time.Hour),
		FallbackConsecutiveFailures: 2,
	}
	primary := OffsiteHealth{Severity: OffsiteSeverity("critical")}
	got := ClassifyOffsiteFallback(state, primary, fbNow)
	if got.Severity != SeverityOK {
		t.Errorf("Severity = %q, want ok: an unrecognized primary severity must still suppress", got.Severity)
	}
}

// --- Invariant: the fallback severity never reaches alarm -------------------

// Swept across the whole grid and every primary severity, known and
// unknown. This is the property task 6 exists to guarantee: whatever the
// fallback's own counters say, and whatever the primary's severity is, a
// caller gating an alert on "alarm" must never see one from the fallback.
//
// Compared by rank (OffsiteSeverityAtLeast), never by string equality
// against SeverityAlarm: the wire contract this function backs
// (docs/wire-protocol.md's fallback section, "fallback.severity never
// reaches alarm", and the "take the maximum rank" contract it documents for
// a client folding offsite.severity and offsite.fallback.severity into one
// glyph — verified by grep, no Go client in this repo implements that fold
// yet; it is a documented wire contract, not yet a consumer here) reads
// this as a rank claim, not a three-way label match. String equality is
// blind to the one value the invariant most needs to catch: an
// unconfigured fallback used to leave Severity at its zero value (""),
// which ranks *above* alarm under offsiteSeverityRank's unknown-value
// policy — "" != "alarm" let that slip straight through an equality check
// while still outranking a genuine alarm under the real contract. The
// "unconfigured" entry in states below is what exercises exactly that case.
//
// Mutation 1: changing either `case s.FallbackConsecutiveFailures >=
// offsiteFallbackWarnStreak` branch to set SeverityAlarm instead of
// SeverityWarn makes this fail immediately.
// Mutation 2: removing ClassifyOffsiteFallback's explicit `h.Severity =
// SeverityOK` on the unconfigured early return (offsite_fallback.go) makes
// this fail on the "unconfigured" state alone — verified: with that one
// line removed, this test goes red on `state=... primarySeverity="ok"
// declaredAge=0s: Severity ranks at least alarm ("")`.
func TestClassifyOffsiteFallbackSeverityNeverReachesAlarm(t *testing.T) {
	states := []OffsiteState{
		{DestDir: fbPrimaryDest},                                                        // unconfigured
		{DestDir: fbPrimaryDest, FallbackDest: "not-a-remote-dest"},                     // misconfigured (malformed)
		{DestDir: fbPrimaryDest, FallbackDest: fbPrimaryDest},                           // misconfigured (same as primary)
		{DestDir: fbPrimaryDest, FallbackDest: "backup-b:/srv/backups"}, // not_yet_exercised
		{
			DestDir: fbPrimaryDest, FallbackDest: "backup-b:/srv/backups",
			FallbackLastTryAt: fbNow.Add(-time.Hour), FallbackConsecutiveFailures: 1,
		}, // failing/ok
		{
			DestDir: fbPrimaryDest, FallbackDest: "backup-b:/srv/backups",
			FallbackLastTryAt: fbNow.Add(-time.Hour), FallbackConsecutiveFailures: 2,
		}, // failing/warn
		{
			DestDir: fbPrimaryDest, FallbackDest: "backup-b:/srv/backups",
			FallbackLastTryAt: fbNow.Add(-time.Hour), FallbackConsecutiveFailures: 50,
		}, // failing, way past the streak
		{
			DestDir: fbPrimaryDest, FallbackDest: "backup-b:/srv/backups",
			FallbackLastOKAt: fbNow.Add(-time.Hour), FallbackLastTryAt: fbNow.Add(-time.Hour),
		}, // ok
	}
	declaredAges := []time.Duration{0, time.Hour, 25*time.Hour + 59*time.Minute, 26 * time.Hour, 1000 * time.Hour}
	primarySeverities := []OffsiteSeverity{SeverityOK, SeverityWarn, SeverityAlarm, OffsiteSeverity("unknown-to-this-build")}

	for _, s := range states {
		for _, age := range declaredAges {
			for _, sev := range primarySeverities {
				primary := OffsiteHealth{Severity: sev, HasDeclared: true, DeclaredAge: age}
				got := ClassifyOffsiteFallback(s, primary, fbNow)
				if OffsiteSeverityAtLeast(got.Severity, SeverityAlarm) {
					t.Errorf("state=%+v primarySeverity=%q declaredAge=%v: Severity ranks at least alarm (%q)",
						s, sev, age, got.Severity)
				}
			}
		}
	}
}

// --- Purity ------------------------------------------------------------------

func TestClassifyOffsiteFallbackIsPure(t *testing.T) {
	state := OffsiteState{
		DestDir:                     fbPrimaryDest,
		FallbackDest:                "backup-b:/srv/backups",
		FallbackLastTryAt:           fbNow.Add(-time.Hour),
		FallbackConsecutiveFailures: 2,
	}
	primary := fbHealthyPrimary

	a := ClassifyOffsiteFallback(state, primary, fbNow)
	b := ClassifyOffsiteFallback(state, primary, fbNow)
	if a != b {
		t.Errorf("ClassifyOffsiteFallback is not deterministic: %+v != %+v", a, b)
	}
	if state.FallbackDest != "backup-b:/srv/backups" || state.FallbackConsecutiveFailures != 2 {
		t.Error("ClassifyOffsiteFallback mutated its input state")
	}
}

// --- RenderOffsiteFallbackDetail: the six exact formulations ----------------

func TestRenderOffsiteFallbackDetail(t *testing.T) {
	tests := []struct {
		name string
		h    OffsiteFallbackHealth
		want string
	}{
		{
			name: "unconfigured renders nothing",
			h:    OffsiteFallbackHealth{Configured: false},
			want: "",
		},
		{
			name: "ok with a known via",
			h: OffsiteFallbackHealth{
				Configured: true, Status: OffsiteFallbackOK,
				HasLastOK: true, LastOKAge: 2*time.Hour + 10*time.Minute, Via: "127.0.0.1",
			},
			want: "ok — exercised 2h10m ago via 127.0.0.1",
		},
		{
			name: "ok with an unknown via",
			h: OffsiteFallbackHealth{
				Configured: true, Status: OffsiteFallbackOK,
				HasLastOK: true, LastOKAge: 2*time.Hour + 10*time.Minute, Via: "",
			},
			want: "ok — exercised 2h10m ago",
		},
		{
			name: "failing names the count and the error",
			h: OffsiteFallbackHealth{
				Configured: true, Status: OffsiteFallbackFailing,
				ConsecutiveFailures: 2, LastErr: "ssh: connection refused",
			},
			want: "FALLBACK FAILING — 2 in a row: ssh: connection refused",
		},
		{
			name: "not yet exercised, under 26h",
			h: OffsiteFallbackHealth{
				Configured: true, Status: OffsiteFallbackNotYetExercised,
				LastTryAge: 10 * time.Minute,
			},
			want: "not yet exercised",
		},
		{
			name: "not exercised, at or past 26h names the configured age",
			h: OffsiteFallbackHealth{
				Configured: true, Status: OffsiteFallbackNotYetExercised,
				LastTryAge: 30 * time.Hour,
			},
			want: "NOT EXERCISED — configured 30h00m ago and not once tried",
		},
		{
			name: "misconfigured: malformed alias",
			h: OffsiteFallbackHealth{
				Configured: true, Status: OffsiteFallbackMisconfigured,
				Dest: "backup-b", misconfiguredReason: offsiteFallbackMisconfiguredReasonMalformed,
			},
			want: `MISCONFIGURED — "backup-b" is not an [user@]host:/path destination`,
		},
		{
			// Dest here (fbPrimaryDest, a local path) happens to also fail
			// ParseRemoteDest, but the render must not be checking that: it
			// is misconfiguredReason alone that selects this branch, which
			// is exactly what TestRenderOffsiteFallbackDetailReasonWinsOver­
			// AParsableDest below proves by using a Dest that WOULD parse.
			name: "misconfigured: malformed, reason carried explicitly",
			h: OffsiteFallbackHealth{
				Configured: true, Status: OffsiteFallbackMisconfigured,
				Dest: fbPrimaryDest, misconfiguredReason: offsiteFallbackMisconfiguredReasonMalformed,
			},
			want: `MISCONFIGURED — "/Volumes/nas/pigtail" is not an [user@]host:/path destination`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RenderOffsiteFallbackDetail(tt.h); got != tt.want {
				t.Errorf("RenderOffsiteFallbackDetail = %q, want %q", got, tt.want)
			}
		})
	}
}

// The "same fallback destination as the primary" render branch, selected by
// misconfiguredReason — not by re-parsing Dest.
func TestRenderOffsiteFallbackDetailMisconfiguredSameAsPrimary(t *testing.T) {
	h := OffsiteFallbackHealth{
		Configured: true, Status: OffsiteFallbackMisconfigured,
		Dest: "nas:/srv/backups", misconfiguredReason: offsiteFallbackMisconfiguredReasonSameAsPrimary,
	}
	want := "MISCONFIGURED — the fallback destination is the primary one; it exercises nothing"
	if got := RenderOffsiteFallbackDetail(h); got != want {
		t.Errorf("RenderOffsiteFallbackDetail = %q, want %q", got, want)
	}
}

// The property fix round 1 exists for: RenderOffsiteFallbackDetail must
// trust misconfiguredReason, never re-derive it from Dest. This health is
// built by hand — the same shape a test fixture, a future migration, or a
// decoded wire payload would produce — with a Dest that parses perfectly
// well as [user@]host:/path (so a renderer that re-parsed Dest would
// conclude "same as primary", the opposite of the truth) but a reason that
// explicitly says "malformed". The rendered sentence must follow the field.
//
// This test would have failed against the version of RenderOffsiteFallback­
// Detail that called ParseRemoteDest(h.Dest) itself: that code had no field
// to disagree with and would have printed the "same as primary" sentence.
func TestRenderOffsiteFallbackDetailReasonWinsOverAParsableDest(t *testing.T) {
	h := OffsiteFallbackHealth{
		Configured: true, Status: OffsiteFallbackMisconfigured,
		Dest:                "nas:/srv/backups", // perfectly well-formed
		misconfiguredReason: offsiteFallbackMisconfiguredReasonMalformed,
	}
	want := `MISCONFIGURED — "nas:/srv/backups" is not an [user@]host:/path destination`
	if got := RenderOffsiteFallbackDetail(h); got != want {
		t.Errorf("RenderOffsiteFallbackDetail = %q, want %q (the reason field must win over Dest's actual shape)",
			got, want)
	}
}

// RenderOffsiteFallbackDetail's default case must never read as healthy for
// a status this build has never heard of — the wire form of a status a
// newer daemon adds. Mirrors TestRenderOffsiteDetailIsNoisyAboutAnUnknownStatus.
func TestRenderOffsiteFallbackDetailIsNoisyAboutAnUnknownStatus(t *testing.T) {
	got := RenderOffsiteFallbackDetail(OffsiteFallbackHealth{
		Configured: true, Status: OffsiteFallbackStatus("degraded"),
	})
	if got == "ok" || got == "" {
		t.Fatalf("RenderOffsiteFallbackDetail for an unknown status = %q, want a loud, non-ok, non-empty message", got)
	}
	if !strings.Contains(got, "degraded") {
		t.Errorf("RenderOffsiteFallbackDetail for an unknown status = %q, want it to name the unrecognized value", got)
	}
}

// A caller-built health with Status==ok but HasLastOK==false (unreachable
// from ClassifyOffsiteFallback, reachable from a hand-built fixture) must
// still render something safe rather than format a zero-value duration.
func TestRenderOffsiteFallbackDetailOKWithoutALastOKIsSafe(t *testing.T) {
	got := RenderOffsiteFallbackDetail(OffsiteFallbackHealth{Configured: true, Status: OffsiteFallbackOK})
	if got != "ok" {
		t.Errorf("RenderOffsiteFallbackDetail(ok, HasLastOK=false) = %q, want %q", got, "ok")
	}
}
