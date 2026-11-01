package store

import (
	"strings"
	"testing"
	"time"
)

// ClassifyOffsite is the one place the offsite status/severity grid is
// decided. cmd/pigtail's `formatOffsite` used to carry this logic inline; it
// now only renders what this function returns. A future HTTP client gets the
// same answer for free, which is the whole point of pulling it out.

var offsiteNow = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

func TestClassifyOffsiteStatus(t *testing.T) {
	tests := []struct {
		name   string
		state  OffsiteState
		status OffsiteStatus
	}{
		{
			name:   "unconfigured",
			state:  OffsiteState{},
			status: OffsiteUnconfigured,
		},
		{
			name: "failing",
			state: OffsiteState{
				DestDir:   "/Volumes/nas/pigtail",
				LastTryAt: offsiteNow,
				LastOKAt:  offsiteNow.Add(-time.Hour),
				LastErr:   "offsite destination unreachable",
			},
			status: OffsiteFailing,
		},
		{
			name: "stuck",
			state: OffsiteState{
				DestDir:          "/Volumes/nas/pigtail",
				AttemptStartedAt: offsiteNow.Add(-10 * time.Minute),
				LastOKAt:         offsiteNow.Add(-time.Hour),
			},
			status: OffsiteStuck,
		},
		{
			name: "in_progress",
			state: OffsiteState{
				DestDir:          "/Volumes/nas/pigtail",
				AttemptStartedAt: offsiteNow.Add(-2 * time.Second),
				LastOKAt:         offsiteNow.Add(-time.Hour),
			},
			status: OffsiteInProgress,
		},
		{
			name: "no_attempt",
			state: OffsiteState{
				DestDir: "/Volumes/nas/pigtail",
			},
			status: OffsiteNoAttempt,
		},
		{
			name: "never_succeeded",
			state: OffsiteState{
				DestDir:   "/Volumes/nas/pigtail",
				LastTryAt: offsiteNow.Add(-30 * time.Minute),
			},
			status: OffsiteNeverSucceeded,
		},
		{
			name: "stale",
			state: OffsiteState{
				DestDir:   "/Volumes/nas/pigtail",
				LastTryAt: offsiteNow.Add(-4 * time.Hour),
				LastOKAt:  offsiteNow.Add(-4 * time.Hour),
			},
			status: OffsiteStale,
		},
		{
			name: "ok",
			state: OffsiteState{
				DestDir:   "/Volumes/nas/pigtail",
				LastTryAt: offsiteNow.Add(-20 * time.Minute),
				LastOKAt:  offsiteNow.Add(-20 * time.Minute),
			},
			status: OffsiteOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyOffsite(tt.state, offsiteNow)
			if got.Status != tt.status {
				t.Errorf("Status = %q, want %q", got.Status, tt.status)
			}
		})
	}
}

func TestClassifyOffsiteSeverity(t *testing.T) {
	tests := []struct {
		name     string
		state    OffsiteState
		severity OffsiteSeverity
	}{
		{
			name: "ok is ok",
			state: OffsiteState{
				DestDir:   "/Volumes/nas/pigtail",
				LastTryAt: offsiteNow.Add(-20 * time.Minute),
				LastOKAt:  offsiteNow.Add(-20 * time.Minute),
			},
			severity: SeverityOK,
		},
		{
			name: "failing is warn",
			state: OffsiteState{
				DestDir:   "/Volumes/nas/pigtail",
				LastTryAt: offsiteNow,
				LastOKAt:  offsiteNow.Add(-time.Hour),
				LastErr:   "offsite destination unreachable",
			},
			severity: SeverityWarn,
		},
		{
			name: "no_attempt caps at warn",
			state: OffsiteState{
				DestDir: "/Volumes/nas/pigtail",
			},
			severity: SeverityWarn,
		},
		{
			name: "never_succeeded caps at warn",
			state: OffsiteState{
				DestDir:   "/Volumes/nas/pigtail",
				LastTryAt: offsiteNow.Add(-30 * time.Minute),
			},
			severity: SeverityWarn,
		},
		{
			name: "same_device is warn even when otherwise ok",
			state: OffsiteState{
				DestDir:    "/Users/me/backups",
				LastTryAt:  offsiteNow.Add(-20 * time.Minute),
				LastOKAt:   offsiteNow.Add(-20 * time.Minute),
				SameDevice: true,
			},
			severity: SeverityWarn,
		},
		{
			name: "stale under 26h is warn",
			state: OffsiteState{
				DestDir:   "/Volumes/nas/pigtail",
				LastTryAt: offsiteNow.Add(-4 * time.Hour),
				LastOKAt:  offsiteNow.Add(-4 * time.Hour),
			},
			severity: SeverityWarn,
		},
		{
			name: "last_ok_age >= 26h is alarm",
			state: OffsiteState{
				DestDir:   "/Volumes/nas/pigtail",
				LastTryAt: offsiteNow.Add(-26 * time.Hour),
				LastOKAt:  offsiteNow.Add(-26 * time.Hour),
			},
			severity: SeverityAlarm,
		},
		{
			name: "stuck under 1h is warn",
			state: OffsiteState{
				DestDir:          "/Volumes/nas/pigtail",
				AttemptStartedAt: offsiteNow.Add(-10 * time.Minute),
				LastOKAt:         offsiteNow.Add(-time.Hour),
			},
			severity: SeverityWarn,
		},
		{
			name: "stuck >= 1h is alarm",
			state: OffsiteState{
				DestDir:          "/Volumes/nas/pigtail",
				AttemptStartedAt: offsiteNow.Add(-time.Hour),
				LastOKAt:         offsiteNow.Add(-2 * time.Hour),
			},
			severity: SeverityAlarm,
		},
		{
			name: "in_progress under 5m is ok",
			state: OffsiteState{
				DestDir:          "/Volumes/nas/pigtail",
				AttemptStartedAt: offsiteNow.Add(-2 * time.Second),
				LastOKAt:         offsiteNow.Add(-time.Hour),
			},
			severity: SeverityOK,
		},
		{
			name:     "unconfigured is ok",
			state:    OffsiteState{},
			severity: SeverityOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyOffsite(tt.state, offsiteNow)
			if got.Severity != tt.severity {
				t.Errorf("Severity = %q, want %q (status=%q)", got.Severity, tt.severity, got.Status)
			}
		})
	}
}

// Boundaries: the moment a threshold is crossed is exactly what these rules
// exist to get right, so both sides of every one are tested.
func TestClassifyOffsiteThresholdBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		state    OffsiteState
		status   OffsiteStatus
		severity OffsiteSeverity
	}{
		{
			name: "stale boundary: 2h59 is still ok",
			state: OffsiteState{
				DestDir:   "/Volumes/nas/pigtail",
				LastTryAt: offsiteNow.Add(-(2*time.Hour + 59*time.Minute)),
				LastOKAt:  offsiteNow.Add(-(2*time.Hour + 59*time.Minute)),
			},
			status:   OffsiteOK,
			severity: SeverityOK,
		},
		{
			name: "stale boundary: 3h01 is stale/warn",
			state: OffsiteState{
				DestDir:   "/Volumes/nas/pigtail",
				LastTryAt: offsiteNow.Add(-(3*time.Hour + time.Minute)),
				LastOKAt:  offsiteNow.Add(-(3*time.Hour + time.Minute)),
			},
			status:   OffsiteStale,
			severity: SeverityWarn,
		},
		{
			// The threshold itself: >= means 3h00m00s exactly must already
			// be stale, not just the minute past it.
			name: "stale boundary: exactly 3h00m00s is stale/warn",
			state: OffsiteState{
				DestDir:   "/Volumes/nas/pigtail",
				LastTryAt: offsiteNow.Add(-3 * time.Hour),
				LastOKAt:  offsiteNow.Add(-3 * time.Hour),
			},
			status:   OffsiteStale,
			severity: SeverityWarn,
		},
		{
			name: "stuck boundary: 4m59 is still in_progress/ok",
			state: OffsiteState{
				DestDir:          "/Volumes/nas/pigtail",
				AttemptStartedAt: offsiteNow.Add(-(4*time.Minute + 59*time.Second)),
				LastOKAt:         offsiteNow.Add(-time.Hour),
			},
			status:   OffsiteInProgress,
			severity: SeverityOK,
		},
		{
			name: "stuck boundary: 5m01 is stuck/warn",
			state: OffsiteState{
				DestDir:          "/Volumes/nas/pigtail",
				AttemptStartedAt: offsiteNow.Add(-(5*time.Minute + time.Second)),
				LastOKAt:         offsiteNow.Add(-time.Hour),
			},
			status:   OffsiteStuck,
			severity: SeverityWarn,
		},
		{
			// The threshold itself: >= means 5m00s exactly must already be
			// stuck, not just the second past it.
			name: "stuck boundary: exactly 5m00s is stuck/warn",
			state: OffsiteState{
				DestDir:          "/Volumes/nas/pigtail",
				AttemptStartedAt: offsiteNow.Add(-5 * time.Minute),
				LastOKAt:         offsiteNow.Add(-time.Hour),
			},
			status:   OffsiteStuck,
			severity: SeverityWarn,
		},
		{
			name: "stuck-alarm boundary: 59m is stuck/warn",
			state: OffsiteState{
				DestDir:          "/Volumes/nas/pigtail",
				AttemptStartedAt: offsiteNow.Add(-59 * time.Minute),
				LastOKAt:         offsiteNow.Add(-2 * time.Hour),
			},
			status:   OffsiteStuck,
			severity: SeverityWarn,
		},
		{
			name: "stuck-alarm boundary: 1h01 is stuck/alarm",
			state: OffsiteState{
				DestDir:          "/Volumes/nas/pigtail",
				AttemptStartedAt: offsiteNow.Add(-(time.Hour + time.Minute)),
				LastOKAt:         offsiteNow.Add(-2 * time.Hour),
			},
			status:   OffsiteStuck,
			severity: SeverityAlarm,
		},
		{
			// The threshold itself: >= means 1h00m00s exactly must already
			// be alarm, not just the minute past it.
			name: "stuck-alarm boundary: exactly 1h00m00s is stuck/alarm",
			state: OffsiteState{
				DestDir:          "/Volumes/nas/pigtail",
				AttemptStartedAt: offsiteNow.Add(-time.Hour),
				LastOKAt:         offsiteNow.Add(-2 * time.Hour),
			},
			status:   OffsiteStuck,
			severity: SeverityAlarm,
		},
		{
			name: "last-ok-alarm boundary: 25h59 is stale/warn",
			state: OffsiteState{
				DestDir:   "/Volumes/nas/pigtail",
				LastTryAt: offsiteNow.Add(-(25*time.Hour + 59*time.Minute)),
				LastOKAt:  offsiteNow.Add(-(25*time.Hour + 59*time.Minute)),
			},
			status:   OffsiteStale,
			severity: SeverityWarn,
		},
		{
			name: "last-ok-alarm boundary: 26h01 is stale/alarm",
			state: OffsiteState{
				DestDir:   "/Volumes/nas/pigtail",
				LastTryAt: offsiteNow.Add(-(26*time.Hour + time.Minute)),
				LastOKAt:  offsiteNow.Add(-(26*time.Hour + time.Minute)),
			},
			status:   OffsiteStale,
			severity: SeverityAlarm,
		},
		{
			// The threshold itself: >= means 26h00m00s exactly must already
			// be alarm, not just the minute past it.
			name: "last-ok-alarm boundary: exactly 26h00m00s is stale/alarm",
			state: OffsiteState{
				DestDir:   "/Volumes/nas/pigtail",
				LastTryAt: offsiteNow.Add(-26 * time.Hour),
				LastOKAt:  offsiteNow.Add(-26 * time.Hour),
			},
			status:   OffsiteStale,
			severity: SeverityAlarm,
		},
		{
			name: "failure-streak boundary: 1 consecutive failure is failing/warn",
			state: OffsiteState{
				DestDir:             "/Volumes/nas/pigtail",
				LastTryAt:           offsiteNow,
				LastOKAt:            offsiteNow.Add(-time.Hour),
				LastErr:             "offsite destination unreachable",
				ConsecutiveFailures: 1,
			},
			status:   OffsiteFailing,
			severity: SeverityWarn,
		},
		{
			name: "failure-streak boundary: 2 consecutive failures is still failing/warn",
			state: OffsiteState{
				DestDir:             "/Volumes/nas/pigtail",
				LastTryAt:           offsiteNow,
				LastOKAt:            offsiteNow.Add(-time.Hour),
				LastErr:             "offsite destination unreachable",
				ConsecutiveFailures: 2,
			},
			status:   OffsiteFailing,
			severity: SeverityWarn,
		},
		{
			// The threshold itself: >= means exactly 3 must already be alarm,
			// not just the failure past it.
			name: "failure-streak boundary: exactly 3 consecutive failures is failing/alarm",
			state: OffsiteState{
				DestDir:             "/Volumes/nas/pigtail",
				LastTryAt:           offsiteNow,
				LastOKAt:            offsiteNow.Add(-time.Hour),
				LastErr:             "offsite destination unreachable",
				ConsecutiveFailures: 3,
			},
			status:   OffsiteFailing,
			severity: SeverityAlarm,
		},
		{
			name: "failure-streak boundary: 4 consecutive failures stays failing/alarm",
			state: OffsiteState{
				DestDir:             "/Volumes/nas/pigtail",
				LastTryAt:           offsiteNow,
				LastOKAt:            offsiteNow.Add(-time.Hour),
				LastErr:             "offsite destination unreachable",
				ConsecutiveFailures: 4,
			},
			status:   OffsiteFailing,
			severity: SeverityAlarm,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyOffsite(tt.state, offsiteNow)
			if got.Status != tt.status {
				t.Errorf("Status = %q, want %q", got.Status, tt.status)
			}
			if got.Severity != tt.severity {
				t.Errorf("Severity = %q, want %q", got.Severity, tt.severity)
			}
		})
	}
}

// no_attempt and never_succeeded carry no last_ok_at at all, so the 26h
// alarm rule has nothing to measure against. declared_at is the substitute
// age for exactly these two statuses (see the boundary tests below), but
// only when it is actually on record: a state with no declared_at at all —
// the shape a file written by a previous version has — must not invent one,
// and stays capped at warn.
func TestClassifyOffsiteWithoutDeclaredAtHasNoSubstituteAge(t *testing.T) {
	tests := []struct {
		name  string
		state OffsiteState
	}{
		{
			name:  "no_attempt, daemon started long ago",
			state: OffsiteState{DestDir: "/Volumes/nas/pigtail"},
		},
		{
			name: "never_succeeded, tried a very long time ago",
			state: OffsiteState{
				DestDir:   "/Volumes/nas/pigtail",
				LastTryAt: offsiteNow.Add(-100 * time.Hour),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyOffsite(tt.state, offsiteNow)
			if got.Severity == SeverityAlarm {
				t.Errorf("Severity = alarm, want at most warn (status=%q)", got.Status)
			}
		})
	}
}

// declared_at is the substitute age no_attempt and never_succeeded lack: it
// measures how long the daemon has known it should back up here, not how
// long since a copy landed (there has never been one). Both sides of the 26h
// threshold, plus the threshold itself, exactly like every other boundary in
// this file.
func TestClassifyOffsiteDeclaredAtEscalatesNoAttemptAndNeverSucceeded(t *testing.T) {
	tests := []struct {
		name     string
		state    OffsiteState
		severity OffsiteSeverity
	}{
		{
			name: "no_attempt, declared 25h59 ago is still warn",
			state: OffsiteState{
				DestDir:    "/Volumes/nas/pigtail",
				DeclaredAt: offsiteNow.Add(-(25*time.Hour + 59*time.Minute)),
			},
			severity: SeverityWarn,
		},
		{
			name: "no_attempt, declared 26h01 ago is alarm",
			state: OffsiteState{
				DestDir:    "/Volumes/nas/pigtail",
				DeclaredAt: offsiteNow.Add(-(26*time.Hour + time.Minute)),
			},
			severity: SeverityAlarm,
		},
		{
			// The threshold itself: >= means 26h00m00s exactly must already
			// be alarm, not just the minute past it.
			name: "no_attempt, declared exactly 26h00m00s ago is alarm",
			state: OffsiteState{
				DestDir:    "/Volumes/nas/pigtail",
				DeclaredAt: offsiteNow.Add(-26 * time.Hour),
			},
			severity: SeverityAlarm,
		},
		{
			name: "never_succeeded, declared 25h59 ago is still warn",
			state: OffsiteState{
				DestDir:    "/Volumes/nas/pigtail",
				LastTryAt:  offsiteNow.Add(-30 * time.Minute),
				DeclaredAt: offsiteNow.Add(-(25*time.Hour + 59*time.Minute)),
			},
			severity: SeverityWarn,
		},
		{
			name: "never_succeeded, declared 26h01 ago is alarm",
			state: OffsiteState{
				DestDir:    "/Volumes/nas/pigtail",
				LastTryAt:  offsiteNow.Add(-30 * time.Minute),
				DeclaredAt: offsiteNow.Add(-(26*time.Hour + time.Minute)),
			},
			severity: SeverityAlarm,
		},
		{
			// The threshold itself, on never_succeeded too.
			name: "never_succeeded, declared exactly 26h00m00s ago is alarm",
			state: OffsiteState{
				DestDir:    "/Volumes/nas/pigtail",
				LastTryAt:  offsiteNow.Add(-30 * time.Minute),
				DeclaredAt: offsiteNow.Add(-26 * time.Hour),
			},
			severity: SeverityAlarm,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyOffsite(tt.state, offsiteNow)
			if got.Severity != tt.severity {
				t.Errorf("Severity = %q, want %q (status=%q)", got.Severity, tt.severity, got.Status)
			}
		})
	}
}

// The rule is scoped to no_attempt and never_succeeded only: an old
// declared_at must not push an otherwise-healthy destination into alarm
// through a rule that was never meant to read it.
//
// Mutation: dropping the Status == OffsiteNoAttempt || Status ==
// OffsiteNeverSucceeded gate makes this fail, since DeclaredAge here is
// 100h, well past the threshold.
func TestClassifyOffsiteDeclaredAtRuleDoesNotReachOtherStatuses(t *testing.T) {
	state := OffsiteState{
		DestDir:    "/Volumes/nas/pigtail",
		LastTryAt:  offsiteNow.Add(-20 * time.Minute),
		LastOKAt:   offsiteNow.Add(-20 * time.Minute),
		DeclaredAt: offsiteNow.Add(-100 * time.Hour),
	}
	got := ClassifyOffsite(state, offsiteNow)
	if got.Status != OffsiteOK {
		t.Fatalf("test setup: Status = %q, want ok", got.Status)
	}
	if got.Severity != SeverityOK {
		t.Errorf("Severity = %q, want ok: a stale declared_at must not escalate a healthy destination", got.Severity)
	}
}

// The two tests below feed ClassifyOffsite states it is never actually
// handed in production: a nonzero ConsecutiveFailures with status
// never_succeeded or no_attempt. offsiteBackupper.attempt (offsite.go) always
// sets LastErr and ConsecutiveFailures together on a failure and clears both
// together on a success, so a real ConsecutiveFailures > 0 is always paired
// with a non-empty LastErr — which classifies as OffsiteFailing before either
// of these two statuses is ever reached. In production, then,
// never_succeeded and no_attempt are capped at warn unconditionally: this
// rule cannot escalate them, because the state that would make it fire never
// occurs. That gap is accepted for this slice; see the comment on the streak
// rule in offsite_status.go.
//
// What these tests actually pin down is that ClassifyOffsite is pure and does
// not special-case status when applying the streak rule: fed a state the real
// pipeline would never produce, it still applies the rule mechanically rather
// than silently ignoring counts it "shouldn't" see. That is a property worth
// keeping — a future caller synthesizing OffsiteState directly (a test
// fixture, a migration script) gets a consistent answer — but it is a
// statement about the function's mechanics, not about what `pigtail servers`
// can ever actually show.
func TestClassifyOffsiteFailureStreakRuleFiresOnASyntheticNeverSucceededState(t *testing.T) {
	tests := []struct {
		name  string
		state OffsiteState
	}{
		{
			name: "never_succeeded carrying 3 consecutive failures (unreachable in production)",
			state: OffsiteState{
				DestDir:             "/Volumes/nas/pigtail",
				LastTryAt:           offsiteNow.Add(-30 * time.Minute),
				ConsecutiveFailures: 3,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyOffsite(tt.state, offsiteNow)
			if got.Severity != SeverityAlarm {
				t.Errorf("Severity = %q, want alarm (status=%q)", got.Severity, got.Status)
			}
		})
	}
}

// See the comment above TestClassifyOffsiteFailureStreakRuleFiresOnASyntheticNeverSucceededState:
// no_attempt cannot itself carry a nonzero ConsecutiveFailures in production —
// declare() writes it before any attempt runs — but ClassifyOffsite is pure
// and must not special-case that: given the count, it applies the rule
// mechanically regardless of which status produced it. This is a statement
// about the function, not a claim that `pigtail servers` can ever actually
// show an alarm for a destination with status no_attempt.
func TestClassifyOffsiteFailureStreakRuleFiresOnASyntheticNoAttemptState(t *testing.T) {
	state := OffsiteState{
		DestDir:             "/Volumes/nas/pigtail",
		ConsecutiveFailures: 3,
	}
	got := ClassifyOffsite(state, offsiteNow)
	if got.Status != OffsiteNoAttempt {
		t.Fatalf("test setup: Status = %q, want no_attempt", got.Status)
	}
	if got.Severity != SeverityAlarm {
		t.Errorf("Severity = %q, want alarm even for a synthetic no_attempt state", got.Severity)
	}
}

// The count itself must reach the renderer, not just the severity it feeds.
func TestClassifyOffsiteCarriesConsecutiveFailures(t *testing.T) {
	state := OffsiteState{
		DestDir:             "/Volumes/nas/pigtail",
		LastTryAt:           offsiteNow,
		LastOKAt:            offsiteNow.Add(-time.Hour),
		LastErr:             "offsite destination unreachable",
		ConsecutiveFailures: 2,
	}
	got := ClassifyOffsite(state, offsiteNow)
	if got.ConsecutiveFailures != 2 {
		t.Errorf("ConsecutiveFailures = %d, want 2", got.ConsecutiveFailures)
	}
}

func TestClassifyOffsiteIsPure(t *testing.T) {
	state := OffsiteState{
		DestDir:   "/Volumes/nas/pigtail",
		LastTryAt: offsiteNow.Add(-20 * time.Minute),
		LastOKAt:  offsiteNow.Add(-20 * time.Minute),
	}
	a := ClassifyOffsite(state, offsiteNow)
	b := ClassifyOffsite(state, offsiteNow)
	if a != b {
		t.Errorf("ClassifyOffsite is not deterministic: %+v != %+v", a, b)
	}
	if state.DestDir != "/Volumes/nas/pigtail" {
		t.Error("ClassifyOffsite mutated its input state")
	}
}

// The state offsiteBackupper.attempt writes before a copy runs carries the
// *previous* attempt's ConsecutiveFailures forward without yet knowing
// whether this attempt will fail too (see offsite.go's attempt). Without this
// exclusion, starting a 4th attempt right after 3 consecutive failures
// classified as alarm severity while the rendered detail still read "copy in
// progress" — an alarm-severity row carrying a reassuring sentence. A copy in
// flight must never be counted as one more failure on top of a streak it has
// not yet joined.
func TestClassifyOffsiteInProgressIsExcludedFromTheFailureStreakRule(t *testing.T) {
	state := OffsiteState{
		DestDir:             "/Volumes/nas/pigtail",
		AttemptStartedAt:    offsiteNow.Add(-2 * time.Second),
		LastOKAt:            offsiteNow.Add(-time.Hour),
		ConsecutiveFailures: 5,
	}
	got := ClassifyOffsite(state, offsiteNow)
	if got.Status != OffsiteInProgress {
		t.Fatalf("test setup: Status = %q, want in_progress", got.Status)
	}
	if got.Severity == SeverityAlarm {
		t.Errorf("Severity = alarm for a fresh in-flight attempt carrying a stale failure streak, "+
			"want at most warn; detail would read: %q", RenderOffsiteDetail(got))
	}
}

// The exclusion above is scoped to in_progress specifically, not to every
// status with a copy in flight. A stuck copy (past offsiteStuckAfter, still
// short of its own offsiteStuckAlarmAfter escalation) carrying a stale
// failure streak from a *previous* attempt must still reach alarm through the
// streak rule — unlike in_progress, "stuck" already means something is wrong
// with the copy in flight, not merely "too soon to tell", so there is no
// reason to shield it from a count that turns out to matter. AttemptStartedAt
// is set 10 minutes ago here — well past stuck (5m) but nowhere near the
// stuck-alarm threshold (1h) — so, without the streak rule reaching it, this
// state classifies only as stuck/warn on its own.
func TestClassifyOffsiteStuckStillEscalatesFromAStaleFailureStreak(t *testing.T) {
	state := OffsiteState{
		DestDir:             "/Volumes/nas/pigtail",
		AttemptStartedAt:    offsiteNow.Add(-10 * time.Minute),
		LastOKAt:            offsiteNow.Add(-2 * time.Hour),
		ConsecutiveFailures: 5,
	}
	got := ClassifyOffsite(state, offsiteNow)
	if got.Status != OffsiteStuck {
		t.Fatalf("test setup: Status = %q, want stuck", got.Status)
	}
	if got.Severity != SeverityAlarm {
		t.Errorf("Severity = %q, want alarm (the streak rule must still reach a merely-stuck, "+
			"not yet stuck-alarm, attempt)", got.Severity)
	}
}

// offsiteSeverityRank/OffsiteSeverityAtLeast are what keeps a display gated
// on severity from going silent the moment it sees a value it does not
// recognize — see printOffsiteAlarm in cmd/pigtail/servers.go, which used to
// compare by equality against "alarm" alone.
func TestOffsiteSeverityAtLeastOrdersTheKnownScale(t *testing.T) {
	tests := []struct {
		sev, floor OffsiteSeverity
		want       bool
	}{
		{SeverityOK, SeverityOK, true},
		{SeverityOK, SeverityWarn, false},
		{SeverityWarn, SeverityOK, true},
		{SeverityWarn, SeverityWarn, true},
		{SeverityWarn, SeverityAlarm, false},
		{SeverityAlarm, SeverityWarn, true},
		{SeverityAlarm, SeverityAlarm, true},
	}
	for _, tt := range tests {
		if got := OffsiteSeverityAtLeast(tt.sev, tt.floor); got != tt.want {
			t.Errorf("OffsiteSeverityAtLeast(%q, %q) = %v, want %v", tt.sev, tt.floor, got, tt.want)
		}
	}
}

// The property the fix exists for: an unrecognized severity — the wire form
// of a value a newer daemon sends and this build predates — must never read
// as quieter than the loudest known severity. It must pass every floor,
// "at least alarm" included, or a display gated on "at least alarm" would go
// silent on exactly the value it cannot interpret.
func TestOffsiteSeverityAtLeastNeverSwallowsAnUnknownValue(t *testing.T) {
	unknown := OffsiteSeverity("critical")
	for _, floor := range []OffsiteSeverity{SeverityOK, SeverityWarn, SeverityAlarm} {
		if !OffsiteSeverityAtLeast(unknown, floor) {
			t.Errorf("OffsiteSeverityAtLeast(%q, %q) = false, want true: an unknown severity must never be swallowed",
				unknown, floor)
		}
	}
}

// RenderOffsiteDetail's old `default: return "ok"` read any status it did not
// enumerate as health. A status this build has never heard of — the wire form
// of a status a newer daemon adds — must instead say plainly that it is not
// understood, never fall back to the one word that means everything is fine.
func TestRenderOffsiteDetailIsNoisyAboutAnUnknownStatus(t *testing.T) {
	got := RenderOffsiteDetail(OffsiteHealth{Status: OffsiteStatus("degraded")})
	if got == "ok" || got == "" {
		t.Fatalf("RenderOffsiteDetail for an unknown status = %q, want a loud, non-ok, non-empty message", got)
	}
	if !strings.Contains(got, "degraded") {
		t.Errorf("RenderOffsiteDetail for an unknown status = %q, want it to name the unrecognized value", got)
	}
}

// The one status that genuinely means "ok" must still render "ok" verbatim —
// internal/dbtool/offsite_visibility_test.go pins this exact string and must
// not be touched.
func TestRenderOffsiteDetailStillRendersOKForOK(t *testing.T) {
	if got := RenderOffsiteDetail(OffsiteHealth{Status: OffsiteOK}); got != "ok" {
		t.Errorf("RenderOffsiteDetail(OffsiteOK) = %q, want %q", got, "ok")
	}
}
