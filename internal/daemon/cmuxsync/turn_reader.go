package cmuxsync

import (
	"errors"
	"time"

	"github.com/aloysbr/pigtail/internal/claudesession"
)

// turnCursor is where the loop stopped reading one session's transcript for
// its declared wait (loop-owned, like promptCursor: no lock). The verdict is
// kept: once a turn-opening entry showed, it stays shown for that wait.
type turnCursor struct {
	since    time.Time // the declared wait's since: a new declaration resets it
	cfg      string    // the config dir the path was resolved in
	path     string    // the transcript; "" = not resolved
	off      int64     // -1 = read the tail
	lookedAt uint64    // slowSeq+1 of the last lookup (0 = never)
	ran      bool      // a turn-opening entry was seen after since (sticky)
	gap      bool      // the first window did not reach since (sticky): no « no turn » verdict
	unknown  bool      // the last full verdict was unknown (unreadable or gap)
	logged   bool      // a read error was logged for this cursor
}

// findTranscript is Deps.FindTranscript, or claudesession.Find in the
// session's config dir.
func (r *Runner) findTranscript() func(sessionID, configDir string) (string, bool) {
	if r.deps.FindTranscript != nil {
		return r.deps.FindTranscript
	}
	return func(id, cfg string) (string, bool) {
		tr, ok, err := claudesession.Find(id, []string{cfg})
		if err != nil || !ok {
			return "", false
		}
		return tr.JSONLPath(), true
	}
}

// turnCursorFor returns s's cursor for its declared wait, fresh when the
// declaration or the config dir changed.
func (r *Runner) turnCursorFor(s Session) turnCursor {
	c, ok := r.turns[s.SessionID]
	if !ok || !c.since.Equal(s.Declared.Since) || c.cfg != s.ConfigDir {
		c = turnCursor{since: s.Declared.Since, cfg: s.ConfigDir, off: -1}
	}
	return c
}

// turnRanFull is WaitTracker.turnRan of a full pass: it reads the transcript
// of s from its cursor, once per call, and tells whether a turn opened after
// the declared wait. known false = unreadable (no config dir, no transcript,
// a read error) or a window that did not reach the declaration: the tracker
// then falls back to the native status alone.
func (r *Runner) turnRanFull(s Session) (ran, known bool) {
	if s.ConfigDir == "" {
		return false, false
	}
	read := r.deps.ReadTurns
	if read == nil {
		read = claudesession.TurnOpenedAfter
	}
	if r.turns == nil {
		r.turns = map[string]turnCursor{}
	}
	c := r.turnCursorFor(s)
	defer func() { r.turns[s.SessionID] = c }()
	if c.ran {
		return true, true
	}
	if c.path == "" && c.lookedAt != r.slowSeq+1 {
		c.lookedAt = r.slowSeq + 1
		if p, found := r.findTranscript()(s.SessionID, s.ConfigDir); found {
			c.path = p
		}
	}
	if c.path == "" {
		c.unknown = true
		return false, false
	}
	opened, covered, next, err := read(c.path, c.off, s.Declared.Since)
	switch {
	case errors.Is(err, claudesession.ErrNoTranscript):
		c.path, c.off, c.unknown = "", -1, true // looked up again at the next slow pass
		return false, false
	case err != nil:
		if !c.logged {
			c.logged = true
			r.deps.Logf("cmuxsync: declared-wait transcript of %s: %v", s.SessionID, err)
		}
		c.unknown = true
		return false, false
	}
	c.off = next
	if opened {
		c.ran, c.unknown = true, false
		return true, true
	}
	if !covered {
		// Bytes between two reads were skipped (a replaced file, a capped
		// window): a turn may lie in them, so « no turn » is never told.
		c.gap = true
	}
	c.unknown = c.gap
	return false, !c.gap
}

// turnRanCached is WaitTracker.turnRan of a pills-only sync: it reads no
// transcript (runner.go « Transcript reads belong to the full sync »). It
// answers the cached verdict; with none, « no turn yet » — the verdict waits
// for the next full pass, so a ghost flicker is never read as a turn here —
// except for a cursor that knows the transcript is unreadable.
func (r *Runner) turnRanCached(s Session) (ran, known bool) {
	c, ok := r.turns[s.SessionID]
	if !ok || !c.since.Equal(s.Declared.Since) || c.cfg != s.ConfigDir {
		return false, true
	}
	if c.ran {
		return true, true
	}
	return false, !c.unknown
}

// sweepTurnCursors drops the cursors of the sessions that hold no declared
// wait (WaitTracker.Sweep only clears the tracker's own map).
func (r *Runner) sweepTurnCursors(ss []Session) {
	if len(r.turns) == 0 {
		return
	}
	keep := map[string]bool{}
	for _, s := range ss {
		if s.Declared.Set() {
			keep[s.SessionID] = true
		}
	}
	for id := range r.turns {
		if !keep[id] {
			delete(r.turns, id)
		}
	}
}

// primeTurnCursors reads, at the first full pass that sees a declared wait,
// the transcript of its session from the tail: the window still covers the
// declaration while it is near the end of the file, so the later incremental
// reads (at the first candidate turn, maybe long after) leave no gap.
func (r *Runner) primeTurnCursors(ss []Session) {
	for _, s := range ss {
		if !s.Declared.Set() || !s.Alive || s.ConfigDir == "" {
			continue
		}
		if c, ok := r.turns[s.SessionID]; ok && c.since.Equal(s.Declared.Since) && c.cfg == s.ConfigDir {
			continue
		}
		r.turnRanFull(s)
	}
}
