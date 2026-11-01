package claudeusage

import (
	"testing"
	"time"
)

// A compte with no reading leaves no sample: nothing was read, nothing is
// recorded (never a 0 % sample).
func TestSamplesFromSkipsNoReading(t *testing.T) {
	cells := []CellInput{cellAt("mp", "perso", "m"), cellAt("jp", "perso", "j")}
	files := map[string][]byte{
		fileOf(cells[0]): maxCell(t, cellFx{UUID: uuidM, Fetched: now0.Add(-time.Hour),
			P7: 40, R7: "2026-10-11T17:00:00.512+00:00", P5: 3, R5: "2026-10-05T14:30:00.5+00:00"}),
		fileOf(cells[1]): noCacheCell(uuidJ),
	}
	accts := Build(snapshots(cells, files), "perso", now0).Accounts
	at := now0.Add(time.Minute)
	got := SamplesFrom(accts, at)
	if len(got) != 1 {
		t.Fatalf("samples = %+v, want only m", got)
	}
	s := got[0]
	if s.AccountUUID != uuidM || s.Label != "m" || !s.At.Equal(at) || !s.FetchedAt.Equal(now0.Add(-time.Hour)) {
		t.Fatalf("sample = %+v", s)
	}
	if s.SevenDay == nil || s.SevenDay.Percent != 40 || s.SevenDay.ResetsAt == nil ||
		s.FiveHour == nil || s.FiveHour.Percent != 3 {
		t.Fatalf("windows = %+v / %+v", s.SevenDay, s.FiveHour)
	}
}

// A 7-day window whose reset has passed is shown as 0 % but recorded raw: the
// history keeps what the reading said.
func TestSamplesFromKeepsRawPercent(t *testing.T) {
	c := cellAt("mp", "perso", "m")
	files := map[string][]byte{fileOf(c): maxCell(t, cellFx{UUID: uuidM, Fetched: now0.Add(-30 * time.Hour),
		P7: 64, R7: "2026-10-05T02:00:00.177378+00:00", P5: 1, R5: "2026-10-04T06:00:00+00:00"})}
	accts := Build(snapshots([]CellInput{c}, files), "perso", now0).Accounts
	if accts[0].Reading.SevenDay.Percent != 0 {
		t.Fatalf("precondition: shown percent %d, want 0", accts[0].Reading.SevenDay.Percent)
	}
	got := SamplesFrom(accts, now0)
	if len(got) != 1 || got[0].SevenDay == nil || got[0].SevenDay.Percent != 64 {
		t.Fatalf("samples = %+v, want the raw 64", got)
	}
}

// A per-model weekly limit (Fable 40 %) is copied with its reset.
func TestSamplesFromScoped(t *testing.T) {
	reset := now0.Add(48 * time.Hour)
	fetched := now0.Add(-5 * time.Minute)
	accts := []Account{{
		Label: "m", AccountUUID: uuidM,
		Reading: &Reading{
			FetchedAt:    fetched,
			SevenDay:     &WindowView{Percent: 20, RawPercent: 20.4, ResetsAt: &reset},
			WeeklyScoped: []ScopedLimit{{Model: "Fable", Percent: 40, Severity: "normal", ResetsAt: &reset}},
		},
	}}
	got := SamplesFrom(accts, now0)
	if len(got) != 1 {
		t.Fatalf("samples = %+v", got)
	}
	s := got[0]
	if s.FiveHour != nil || s.SevenDay == nil || s.SevenDay.Percent != 20.4 {
		t.Fatalf("windows = %+v / %+v", s.SevenDay, s.FiveHour)
	}
	if len(s.Scoped) != 1 || s.Scoped[0].Model != "Fable" || s.Scoped[0].Percent != 40 ||
		s.Scoped[0].ResetsAt == nil || !s.Scoped[0].ResetsAt.Equal(reset) {
		t.Fatalf("scoped = %+v", s.Scoped)
	}
	// A copy: the account's reset pointer is not shared with the sample.
	*s.Scoped[0].ResetsAt = now0
	if !reset.Equal(now0.Add(48 * time.Hour)) {
		t.Fatal("sample shares the reading's reset")
	}
}
