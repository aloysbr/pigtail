package claudeusage

import "time"

// Anticipatory switch (sub_x000000000000023): an interactive session whose
// account reaches its switch threshold on either window moves, at its turn
// end, to an account of its scope that is under its own ceiling with a fresh
// reading.

// Reasons AheadTarget gives when there is no target.
const (
	AheadSourceAbsent  = "source not in ranking"
	AheadSourceUnder   = "source under its switch threshold"
	AheadNoFreshTarget = "no fresh account under its ceiling"
)

// OverSwitch is the first window (5h first) at or above the candidate's
// switch threshold, with its raw percent.
func (c Candidate) OverSwitch() (window string, pct float64, ok bool) {
	return overSwitch(c.FiveHour, c.SevenDay, c.Thresholds)
}

func overSwitch(five, seven *WindowView, th Thresholds) (string, float64, bool) {
	switch {
	case th.OverSwitch(five):
		return "5h", five.RawPercent, true
	case th.OverSwitch(seven):
		return "7d", seven.RawPercent, true
	}
	return "", 0, false
}

// SnapshotOverSwitch is the same rule on one cell's reading (a passed reset
// is under the threshold; an error, no reading or a mismatched cache is
// never over).
func SnapshotOverSwitch(s Snapshot, th Thresholds, now time.Time) (window string, pct float64, ok bool) {
	if s.Err != nil || !s.HasReading || s.Mismatch {
		return "", 0, false
	}
	now = now.UTC()
	return overSwitch(view(s.FiveHour, now), view(s.SevenDay, now), th)
}

// AheadTarget is where a session on account source moves ahead of its limit:
// the picked candidate when the source is at or above its switch threshold,
// the pick is fresh (a stale reading is not enough to move a working
// session) and the pick is another account. Otherwise nil and the reason.
func AheadTarget(ch Choice, source string) (*Candidate, string) {
	var src *Candidate
	for i := range ch.Ranking {
		if ch.Ranking[i].AccountUUID == source {
			src = &ch.Ranking[i]
			break
		}
	}
	if src == nil {
		return nil, AheadSourceAbsent
	}
	if _, _, over := src.OverSwitch(); !over {
		return nil, AheadSourceUnder
	}
	if ch.Group != GroupFresh || ch.AccountUUID == source {
		return nil, AheadNoFreshTarget
	}
	return &ch.Candidate, ""
}
