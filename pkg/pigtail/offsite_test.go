package pigtail

import (
	"encoding/json"
	"testing"
)

// pkg/pigtail is the protocol's reference package: internal/daemon/store
// aliases these declarations rather than keeping its own, so a test here
// that pins a wire value pins it for every consumer. See offsite.go.

func TestOffsiteStatusWireValues(t *testing.T) {
	cases := []struct {
		status OffsiteStatus
		want   string
	}{
		{OffsiteOK, "ok"},
		{OffsiteFailing, "failing"},
		{OffsiteStuck, "stuck"},
		{OffsiteInProgress, "in_progress"},
		{OffsiteStale, "stale"},
		{OffsiteNeverSucceeded, "never_succeeded"},
		{OffsiteNoAttempt, "no_attempt"},
		{OffsiteUnconfigured, "unconfigured"},
	}
	for _, tt := range cases {
		if string(tt.status) != tt.want {
			t.Errorf("status = %q, want %q", string(tt.status), tt.want)
		}
	}
}

func TestOffsiteSeverityWireValues(t *testing.T) {
	cases := []struct {
		sev  OffsiteSeverity
		want string
	}{
		{OffsiteSeverityOK, "ok"},
		{OffsiteSeverityWarn, "warn"},
		{OffsiteSeverityAlarm, "alarm"},
	}
	for _, tt := range cases {
		if string(tt.sev) != tt.want {
			t.Errorf("severity = %q, want %q", string(tt.sev), tt.want)
		}
	}
}

func TestOffsiteFallbackStatusWireValues(t *testing.T) {
	cases := []struct {
		status OffsiteFallbackStatus
		want   string
	}{
		{OffsiteFallbackOK, "ok"},
		{OffsiteFallbackFailing, "failing"},
		{OffsiteFallbackNotYetExercised, "not_yet_exercised"},
		{OffsiteFallbackMisconfigured, "misconfigured"},
	}
	for _, tt := range cases {
		if string(tt.status) != tt.want {
			t.Errorf("fallback status = %q, want %q", string(tt.status), tt.want)
		}
	}
}

// OffsiteSeverityAtLeast: the known ok < warn < alarm scale.
func TestOffsiteSeverityAtLeastKnownScale(t *testing.T) {
	cases := []struct {
		name  string
		sev   OffsiteSeverity
		floor OffsiteSeverity
		want  bool
	}{
		{"ok at least ok", OffsiteSeverityOK, OffsiteSeverityOK, true},
		{"ok at least warn", OffsiteSeverityOK, OffsiteSeverityWarn, false},
		{"ok at least alarm", OffsiteSeverityOK, OffsiteSeverityAlarm, false},
		{"warn at least ok", OffsiteSeverityWarn, OffsiteSeverityOK, true},
		{"warn at least warn", OffsiteSeverityWarn, OffsiteSeverityWarn, true},
		{"warn at least alarm", OffsiteSeverityWarn, OffsiteSeverityAlarm, false},
		{"alarm at least warn", OffsiteSeverityAlarm, OffsiteSeverityWarn, true},
		{"alarm at least alarm", OffsiteSeverityAlarm, OffsiteSeverityAlarm, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := OffsiteSeverityAtLeast(tt.sev, tt.floor); got != tt.want {
				t.Errorf("OffsiteSeverityAtLeast(%q, %q) = %v, want %v", tt.sev, tt.floor, got, tt.want)
			}
		})
	}
}

// The rank that matters most: a severity this build has never heard of
// (the wire form of a value a newer daemon sends) must never be swallowed
// by a floor check — it must rank at or above alarm, the loudest floor this
// build can express. See internal/daemon/store's identical guarantee, which
// this function now backs directly.
func TestOffsiteSeverityAtLeastUnknownRanksAboveAlarm(t *testing.T) {
	unknown := OffsiteSeverity("critical")
	for _, floor := range []OffsiteSeverity{OffsiteSeverityOK, OffsiteSeverityWarn, OffsiteSeverityAlarm} {
		if !OffsiteSeverityAtLeast(unknown, floor) {
			t.Errorf("OffsiteSeverityAtLeast(%q, %q) = false, want true: an unknown severity must never be swallowed",
				unknown, floor)
		}
	}
}

// OffsiteView's wire form must not move a byte: Status/Severity became named
// types in this task, but json.Marshal renders them exactly as it rendered
// the plain strings before, and untyped string literals still assign to
// the fields without a conversion (see cmd/pigtail/servers_offsite_test.go,
// which this task must not touch).
func TestOffsiteViewJSONRoundTrip(t *testing.T) {
	lastOK := 42
	view := OffsiteView{
		DestDir:             "/Volumes/nas/pigtail",
		Status:              "failing",
		Severity:            "alarm",
		Detail:              "FAILING — offsite destination unreachable",
		LastOKAgeSeconds:    &lastOK,
		ConsecutiveFailures: 3,
		SameDevice:          false,
		LastErr:             "offsite destination unreachable",
	}
	b, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if raw["status"] != "failing" {
		t.Errorf("wire status = %v, want %q (string, not an object)", raw["status"], "failing")
	}
	if raw["severity"] != "alarm" {
		t.Errorf("wire severity = %v, want %q (string, not an object)", raw["severity"], "alarm")
	}

	var back OffsiteView
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.DestDir != view.DestDir || back.Status != view.Status || back.Severity != view.Severity ||
		back.Detail != view.Detail || back.ConsecutiveFailures != view.ConsecutiveFailures ||
		back.SameDevice != view.SameDevice || back.LastErr != view.LastErr ||
		back.LastTryAgeSeconds != nil ||
		back.LastOKAgeSeconds == nil || *back.LastOKAgeSeconds != *view.LastOKAgeSeconds {
		t.Fatalf("round trip mismatch: got %+v, want %+v", back, view)
	}
}
