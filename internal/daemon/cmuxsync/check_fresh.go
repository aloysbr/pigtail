package cmuxsync

import (
	"strings"

	"github.com/aloysbr/pigtail/internal/claudesession"
)

// replacement finds the proof (D3, 2026-10-24) that the surface of defect d
// now serves another process than the report's. Positive proof only: a peer
// of the surface that is alive, has a pid, is named by its native file
// (StartedAt set) and is interactive. Then either
//   - branch 1: the report named a process (d.PID > 0), it ended, and the peer
//     is another one; or
//   - branch 2: the report named none (d.PID == 0: no claude, or several) and
//     the peer started strictly after the report, which cannot have seen it.
//
// A ghost, a peer without pid or native file, or a non-interactive kind never
// proves anything (no Session.Alive alone: a peer with no pid reads alive).
func replacement(d CheckDefect, ss []Session, pidAlive func(int) bool) (Session, bool) {
	surface := strings.ToUpper(strings.TrimSpace(d.SurfaceID))
	if surface == "" {
		return Session{}, false
	}
	for _, s := range ss {
		if s.SurfaceID != surface || !s.Alive || s.PID <= 0 || s.StartedAt.IsZero() ||
			(s.Kind != "" && s.Kind != "interactive") {
			continue
		}
		switch {
		case d.PID > 0:
			if !pidAlive(d.PID) && s.PID != d.PID {
				return s, true
			}
		case !d.At.IsZero() && s.StartedAt.After(d.At):
			return s, true
		}
	}
	return Session{}, false
}

// freshCheckDefects drops the defects whose surface now runs another live
// process than the check's, from the sessions the loop already knows (no cmux
// call). The cached r.checkDefects is never mutated: the decision is
// stateless and re-judged each pass; only the log line keeps state, once per
// drop.
func (r *Runner) freshCheckDefects(ds []CheckDefect, ss []Session) []CheckDefect {
	if len(ds) == 0 {
		r.droppedLogged = nil
		return ds
	}
	alive := r.deps.PidAlive
	if alive == nil {
		alive = claudesession.PidAlive
	}
	out := make([]CheckDefect, 0, len(ds))
	dropped := map[string]int{}
	for _, d := range ds {
		s, stale := replacement(d, ss, alive)
		if !stale {
			out = append(out, d)
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(d.SurfaceID))
		dropped[key] = d.PID
		if last, seen := r.droppedLogged[key]; seen && last == d.PID {
			continue
		}
		if d.PID > 0 {
			r.deps.Logf("cmuxsync: cmux check pill: dropped surface %s, reported pid %d ended, surface now served by pid %d", d.SurfaceID, d.PID, s.PID)
		} else {
			r.deps.Logf("cmuxsync: cmux check pill: dropped surface %s, no process at report time, surface now served by pid %d started after the report", d.SurfaceID, s.PID)
		}
	}
	r.droppedLogged = dropped
	return out
}
