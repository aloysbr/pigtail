package pigtail

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestStaffingWireNames(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	cand := StaffingCandidate{SubjectID: "s", ShortTitle: "t", Scope: "x", Status: "building", Rank: 1, Skip: "held", Detail: "d"}
	dec := StaffingDecision{TS: now, LastAt: now, Repeats: 2, Mode: StaffingAdvise, PlacesUsed: 1, Ceiling: 3,
		Outcome: StaffingOutcomeObservedLaunch, Gate: "g", Reason: "r", SubjectID: "s", ShortTitle: "t", Scope: "x",
		LaunchID: "l", Origin: "api", Cause: "c", Skipped: []StaffingCandidate{cand}}
	st := StaffingState{Mode: StaffingLaunch, Ceiling: 3, PlacesUsed: 1,
		Places:    []StaffingPlace{{SessionID: "p", SubjectID: "s", ShortTitle: "t", Scope: "x", Label: "l", Wait: "review", LaunchID: "ln", Pending: true, Phantom: true}},
		Queues:    map[string][]StaffingCandidate{"x": {cand}},
		Last:      &dec,
		CheckedAt: &now, Launcher: StaffingLauncherScheduler, SettingsError: "e"}

	for _, tc := range []struct {
		name string
		v    any
		want string
	}{
		{"state", st, "ceiling checked_at last launcher mode places places_used queues settings_error"},
		{"decision", dec, "cause ceiling gate last_at launch_id mode origin outcome places_used reason repeats scope short_title skipped subject_id ts"},
		{"candidate", cand, "detail rank scope short_title skip status subject_id"},
		{"place", st.Places[0], "label launch_id pending phantom scope session_id short_title subject_id wait"},
	} {
		if got := strings.Join(jsonKeys(t, tc.v), " "); got != tc.want {
			t.Errorf("%s keys = %q\nwant %q", tc.name, got, tc.want)
		}
	}
	c := 2
	if got := strings.Join(jsonKeys(t, StaffingSettings{Mode: StaffingOff, Ceiling: &c}), " "); got != "ceiling mode" {
		t.Errorf("settings keys = %q", got)
	}

	for _, c := range []struct{ got, want string }{
		{string(StaffingOff), "off"},
		{string(StaffingAdvise), "advise"},
		{string(StaffingLaunch), "launch"},
		{string(EventStaffingDecision), "staffing.decision"},
		{StaffingOutcomeIdle, "idle"},
		{StaffingOutcomeWouldLaunch, "would_launch"},
		{StaffingOutcomeLaunchStarted, "launch_started"},
		{StaffingOutcomeLaunchRefused, "launch_refused"},
		{StaffingOutcomeLaunched, "launched"},
		{StaffingOutcomeLaunchFailed, "launch_failed"},
		{StaffingOutcomeSettingsChanged, "settings_changed"},
		{StaffingOutcomeObservedLaunch, "observed_launch"},
		{StaffingGateNoFreePlace, "no_free_place"},
		{StaffingGateNoReadySubject, "no_ready_subject"},
		{StaffingGateOverMargin, "over_margin"},
		{StaffingGateMaintenance, "maintenance"},
		{StaffingGateSessionBudgetOff, "session_budget_off"},
		{StaffingGateSessionBudgetFull, "session_budget_full"},
		{StaffingGateMachineBusy, "machine_busy"},
		{StaffingGateBurst, "burst"},
		{StaffingGateUnreadable, "unreadable"},
		{StaffingSkipHeld, "held"},
		{StaffingSkipPhantom, "phantom"},
		{StaffingSkipPaused, "paused"},
		{StaffingSkipPending, "pending"},
		{StaffingSkipWaitsHuman, "waits_human"},
		{StaffingSkipDeps, "deps"},
		{StaffingSkipCooldown, "cooldown"},
		{StaffingSkipOverMargin, "over_margin"},
		{StaffingLauncherScheduler, "scheduler"},
		{StaffingLauncherPilot, "pilot"},
	} {
		got, want := c.got, c.want
		if got != want {
			t.Errorf("constant = %q, want %q", got, want)
		}
	}
}

func TestStaffingClient(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+strings.TrimSpace(string(b))+" cross="+r.Header.Get(HeaderCrossScope))
		if strings.HasPrefix(r.URL.Path, "/staffing/decisions") {
			_, _ = w.Write([]byte(`{"data":[{"outcome":"idle","gate":"no_ready_subject"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"mode":"advise","ceiling":2,"launcher":"pilot"}}`))
	}))
	defer srv.Close()
	c := NewHTTPClient(strings.TrimPrefix(srv.URL, "http://"), "")
	ctx := context.Background()
	st, err := c.Staffing(ctx)
	if err != nil || st.Mode != StaffingAdvise || st.Ceiling != 2 {
		t.Fatalf("Staffing = %+v, %v", st, err)
	}
	two := 2
	if _, err := c.WithCrossScopeWrite().SetStaffing(ctx, StaffingSettings{Ceiling: &two}); err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	ds, err := c.StaffingDecisions(ctx, since, 7)
	if err != nil || len(ds) != 1 || ds[0].Gate != StaffingGateNoReadySubject {
		t.Fatalf("StaffingDecisions = %+v, %v", ds, err)
	}
	if _, err := c.StaffingDecisions(ctx, time.Time{}, 0); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /staffing  cross=",
		`POST /staffing {"ceiling":2} cross=write`,
		"GET /staffing/decisions?limit=7&since=2026-10-30T08%3A00%3A00Z  cross=",
		"GET /staffing/decisions  cross=",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
