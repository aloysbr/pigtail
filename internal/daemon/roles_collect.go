package daemon

import (
	"fmt"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/roles"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// peersPerRole bounds the sessions read for one role.
const peersPerRole = 5

// collector builds a role's Observation from its subject's sessions. It never
// reads session_standby: Standby is not an excuse. Its seams are the store
// and the claude files; tests fake them.
type collector struct {
	loops      *claudesession.LoopReader
	peers      func(subjectID string, includeEnded bool, limit int) ([]pigtail.PeerSession, error)
	readNative func(cfg string, pid int) (claudesession.Native, bool)
	alive      func(cfg string, pid int, sessionID string) bool
	find       func(id string, configDirs []string) (claudesession.Transcript, bool, error)
}

func (c *collector) collect(r pigtail.Role) roles.Observation {
	var o roles.Observation
	ps, err := c.peers(r.SubjectID, true, peersPerRole)
	if err != nil {
		o.Err = fmt.Errorf("peers of %s: %w", r.SubjectID, err)
		return o
	}
	var loop claudesession.LoopState
	capped := false
	for _, p := range ps {
		live := p.EndedAt == nil && p.ConfigDir != "" && c.alive(p.ConfigDir, p.PID, p.SessionID)
		var obs roles.SessionObs
		if live {
			n, ok := c.readNative(p.ConfigDir, p.PID)
			if !ok {
				o.Err = fmt.Errorf("native status unreadable for %s", p.SessionID)
				return o
			}
			obs = roles.SessionObs{SessionID: p.SessionID, Native: n.Status}
			if n.StatusUpdatedAt > 0 {
				obs.StatusSince = time.UnixMilli(n.StatusUpdatedAt).UTC()
			}
			obs.ActivityMtime = obs.StatusSince
		}
		var cfgs []string
		if p.ConfigDir != "" {
			cfgs = []string{p.ConfigDir}
		}
		tr, found, err := c.find(p.SessionID, cfgs)
		if err != nil {
			o.Err = fmt.Errorf("transcript of %s: %w", p.SessionID, err)
			return o
		}
		if found {
			path := tr.JSONLPath()
			m, err := claudesession.ActivityMtime(path)
			if err != nil {
				o.Err = fmt.Errorf("activity of %s: %w", p.SessionID, err)
				return o
			}
			if live {
				obs.ActivityMtime = m
			}
			st, err := c.loops.Read(path)
			if err != nil {
				o.Err = fmt.Errorf("loop state of %s: %w", p.SessionID, err)
				return o
			}
			loop = mergeLoop(loop, st)
			capped = capped || st.Capped
		}
		if live {
			o.Live = append(o.Live, obs)
		}
	}
	loop.Capped = loop.ArmAt.IsZero() && capped
	o.Loop = loop
	return o
}

// mergeLoop merges the loop states of a subject's transcripts: the arm with
// the latest ArmAt and the loop fire after it, the max fire and turn.
func mergeLoop(a, b claudesession.LoopState) claudesession.LoopState {
	out := a
	if b.ArmAt.After(a.ArmAt) {
		out.ArmAt, out.ScheduledFor, out.LoopFireAt = b.ArmAt, b.ScheduledFor, b.LoopFireAt
	} else if b.ArmAt.Equal(a.ArmAt) && b.LoopFireAt.After(out.LoopFireAt) {
		out.LoopFireAt = b.LoopFireAt
	}
	// A loop fire in another transcript after the merged arm consumes it.
	if !out.ArmAt.IsZero() && b.LoopFireAt.After(out.ArmAt) && b.LoopFireAt.After(out.LoopFireAt) {
		out.LoopFireAt = b.LoopFireAt
	}
	if !out.ArmAt.IsZero() && a.LoopFireAt.After(out.ArmAt) && a.LoopFireAt.After(out.LoopFireAt) {
		out.LoopFireAt = a.LoopFireAt
	}
	if b.FireAt.After(out.FireAt) {
		out.FireAt = b.FireAt
	}
	if b.TurnAt.After(out.TurnAt) {
		out.TurnAt = b.TurnAt
	}
	return out
}
