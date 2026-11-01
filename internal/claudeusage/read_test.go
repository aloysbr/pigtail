package claudeusage

import (
	"encoding/json"
	"errors"
	"io/fs"
	"testing"
	"time"
)

func TestReadCellParsesWindowsWithMicrosecondsAndOffset(t *testing.T) {
	c := cellAt("jp", "perso", "j")
	s := ReadCell(c, (&fakeFS{files: map[string][]byte{fileOf(c): maxCell(t, cellFx{
		UUID: uuidJ, Fetched: now0.Add(-time.Minute), P7: 91, R7: "2026-10-06T02:00:00.177378+00:00",
		P5: 8, R5: "2026-10-05T11:50:00.177348+00:00"})}}).read)
	if s.Err != nil || !s.HasReading || s.AccountUUID != uuidJ {
		t.Fatalf("got %+v", s)
	}
	if s.SevenDay == nil || s.SevenDay.Percent != 91 {
		t.Fatalf("SevenDay = %+v", s.SevenDay)
	}
	want := time.Date(2026, 10, 6, 2, 0, 0, 177378000, time.UTC)
	if !s.SevenDay.ResetsAt.Equal(want) || s.SevenDay.ResetsAt.Location() != time.UTC {
		t.Fatalf("SevenDay.ResetsAt = %v, want %v in UTC", s.SevenDay.ResetsAt, want)
	}
	if s.FiveHour == nil || s.FiveHour.Percent != 8 {
		t.Fatalf("FiveHour = %+v", s.FiveHour)
	}
	if !s.FetchedAt.Equal(now0.Add(-time.Minute)) {
		t.Fatalf("FetchedAt = %v", s.FetchedAt)
	}
	if len(s.WeeklyScoped) != 1 || s.WeeklyScoped[0].Model != "Fable" {
		t.Fatalf("WeeklyScoped = %+v", s.WeeklyScoped)
	}
	if s.CapReached {
		t.Fatal("org_level_disabled is not a spend cap")
	}
}

func TestReadCellTeamSeatCapReached(t *testing.T) {
	c := cellAt("t", "gx", "t")
	s := ReadCell(c, (&fakeFS{files: map[string][]byte{fileOf(c): teamCell(t)}}).read)
	if !s.CapReached {
		t.Fatal("org_spend_cap_reached + spend.severity critical must read as cap reached")
	}
	if n, tier, ok := Capacity(s.UserTier, s.OrgTier); !ok || n != 5 || tier != "default_claude_max_5x" {
		t.Fatalf("Capacity = %d %q %v, want 5 from userRateLimitTier", n, tier, ok)
	}
	if string(s.ExtraUsage) == "" || string(s.Spend) == "" {
		t.Fatal("extra_usage and spend must be kept raw")
	}
}

func TestCapacity(t *testing.T) {
	for _, tc := range []struct {
		user, org string
		n         int
		ok        bool
	}{
		{"", "default_claude_max_20x", 20, true},
		{"default_claude_max_5x", "default_raven", 5, true},
		{"", "default_raven", 0, false},
		{"default_claude_pro", "default_claude_max_20x", 0, false}, // user tier decides: never guessed from org
		{"", "", 0, false},
	} {
		n, _, ok := Capacity(tc.user, tc.org)
		if n != tc.n || ok != tc.ok {
			t.Errorf("Capacity(%q, %q) = %d %v, want %d %v", tc.user, tc.org, n, ok, tc.n, tc.ok)
		}
	}
}

// Each cap signal on its own (Spec 12): removing any one branch of capReached
// turns its row red.
func TestCapReachedEachSignalAlone(t *testing.T) {
	for _, tc := range []struct {
		name, reason, extra, spend string
		want                       bool
	}{
		{"disabled reason alone", "org_spend_cap_reached", `{"spend_limit_reached": false}`, `{"enabled": false, "severity": "normal"}`, true},
		{"extra_usage.spend_limit_reached alone", "org_level_disabled", `{"spend_limit_reached": true}`, `{"enabled": false, "severity": "normal"}`, true},
		{"spend enabled + critical alone", "", `{"spend_limit_reached": false}`, `{"enabled": true, "severity": "critical"}`, true},
		{"critical but spend disabled", "", `{"spend_limit_reached": false}`, `{"enabled": false, "severity": "critical"}`, false},
		{"nothing (Max, extra usage off)", "org_level_disabled", `null`, `null`, false},
	} {
		if got := capReached(tc.reason, json.RawMessage(tc.extra), json.RawMessage(tc.spend)); got != tc.want {
			t.Errorf("%s: capReached = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestReadCellTruncatedIsUnreadable(t *testing.T) {
	c := cellAt("jp", "perso", "j")
	full := maxCell(t, cellFx{UUID: uuidJ, Fetched: now0, P7: 50, R7: "2026-10-06T02:00:00+00:00", P5: 1, R5: "2026-10-05T12:00:00+00:00"})
	s := ReadCell(c, (&fakeFS{files: map[string][]byte{fileOf(c): full[:len(full)/2]}}).read)
	if s.Err == nil || s.HasReading || s.SevenDay != nil {
		t.Fatalf("truncated JSON must be an error with no reading, got %+v", s)
	}
}

func TestReadCellMissingFileIsUnreadable(t *testing.T) {
	s := ReadCell(cellAt("p", "perso", "m"), (&fakeFS{}).read)
	if !errors.Is(s.Err, fs.ErrNotExist) {
		t.Fatalf("Err = %v, want ErrNotExist wrapped", s.Err)
	}
}

func TestReadCellNoCacheHasNoReading(t *testing.T) {
	c := cellAt("p", "perso", "m")
	s := ReadCell(c, (&fakeFS{files: map[string][]byte{fileOf(c): noCacheCell(uuidM)}}).read)
	if s.Err != nil || s.AccountUUID != uuidM || s.HasReading {
		t.Fatalf("got %+v", s)
	}
}

func TestReadCellCacheOfAnotherAccountIsDropped(t *testing.T) {
	c := cellAt("mp", "perso", "m")
	s := ReadCell(c, (&fakeFS{files: map[string][]byte{fileOf(c): maxCell(t, cellFx{
		UUID: uuidM, CacheUUID: uuidJ, Fetched: now0, P7: 80, R7: "2026-10-06T02:00:00+00:00", P5: 1, R5: "2026-10-05T12:00:00+00:00"})}}).read)
	if s.HasReading || !s.Mismatch || s.CacheUUID != uuidJ || s.SevenDay != nil {
		t.Fatalf("got %+v", s)
	}
}

func TestReadCellBadResetIsUnknownNotDropped(t *testing.T) {
	c := cellAt("jp", "perso", "j")
	s := ReadCell(c, (&fakeFS{files: map[string][]byte{fileOf(c): maxCell(t, cellFx{
		UUID: uuidJ, Fetched: now0, P7: 40, R7: "next tuesday", P5: 1, R5: "2026-10-05T12:00:00+00:00"})}}).read)
	if s.SevenDay == nil || s.SevenDay.Percent != 40 || !s.SevenDay.ResetsAt.IsZero() {
		t.Fatalf("SevenDay = %+v", s.SevenDay)
	}
	want := `cell jp: seven_day.resets_at "next tuesday" unreadable — reset unknown`
	if len(s.Warnings) != 1 || s.Warnings[0] != want {
		t.Fatalf("Warnings = %q, want [%q]", s.Warnings, want)
	}
}

func TestReadCellReadsOnlyTheCellFile(t *testing.T) {
	f := &fakeFS{}
	ReadCell(cellAt("mp", "perso", "m"), f.read)
	if len(f.asked) != 1 || f.asked[0] != "/cells/mp/.claude.json" {
		t.Fatalf("asked %q, want only /cells/mp/.claude.json", f.asked)
	}
}
