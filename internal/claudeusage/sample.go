package claudeusage

import "time"

// SampleWindow is one usage window as recorded: the raw percent of the
// reading, never zeroed once its reset has passed (the history keeps what
// the reading said; the reader decides what a passed reset means).
type SampleWindow struct {
	Percent  float64    `json:"percent"`
	ResetsAt *time.Time `json:"resets_at"`
}

// SampleScoped is a per-model weekly limit as recorded (Fable, …).
type SampleScoped struct {
	Model    string     `json:"model"`
	Percent  float64    `json:"percent"`
	ResetsAt *time.Time `json:"resets_at"`
}

// Sample is one account's reading at one pass of the daemon's sweep: At is
// the pass, FetchedAt the reading's own age. One line of the usage history.
type Sample struct {
	At          time.Time      `json:"at"`
	AccountUUID string         `json:"account_uuid"`
	Label       string         `json:"label"`
	FetchedAt   time.Time      `json:"fetched_at"`
	SevenDay    *SampleWindow  `json:"seven_day,omitempty"`
	FiveHour    *SampleWindow  `json:"five_hour,omitempty"`
	Scoped      []SampleScoped `json:"scoped,omitempty"`
}

// SamplesFrom returns one Sample per account that has a reading, in the
// accounts' order; an account without a reading leaves none (never a 0 %).
func SamplesFrom(accts []Account, at time.Time) []Sample {
	out := []Sample{}
	for _, a := range accts {
		r := a.Reading
		if r == nil {
			continue
		}
		s := Sample{At: at.UTC(), AccountUUID: a.AccountUUID, Label: a.Label, FetchedAt: r.FetchedAt.UTC(),
			SevenDay: sampleWindow(r.SevenDay), FiveHour: sampleWindow(r.FiveHour)}
		for _, l := range r.WeeklyScoped {
			s.Scoped = append(s.Scoped, SampleScoped{Model: l.Model, Percent: l.Percent, ResetsAt: copyTime(l.ResetsAt)})
		}
		out = append(out, s)
	}
	return out
}

func sampleWindow(v *WindowView) *SampleWindow {
	if v == nil {
		return nil
	}
	return &SampleWindow{Percent: v.RawPercent, ResetsAt: copyTime(v.ResetsAt)}
}

func copyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := t.UTC()
	return &c
}
