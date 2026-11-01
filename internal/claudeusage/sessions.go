package claudeusage

import "github.com/aloysbr/pigtail/internal/scope"

// SessionRef is one Claude session as the daemon's session table returns it
// to the caller (already filtered by the caller's scope). CellID is the cell
// of its row — its process / pane — not of its transcripts.
type SessionRef struct {
	ID, CellID string
	Open       bool // not ended, not dead
	Busy       bool // Claude's status busy or shell (a Bash-tool command running)
}

// AttachSessions sets each account's busy/open session counts (D2). ok=false:
// the table could not be read — counts stay nil (null in JSON, "—" in text),
// never 0/0. The view is complete only for the master scope (perso, D3): any
// other scope sees its own sessions only. A session counts once (its first
// row), on its row's cell; one on a cell no listed account owns is skipped.
func (r *Report) AttachSessions(ss []SessionRef, ok bool) {
	r.SessionsAvailable = ok
	r.SessionsComplete = ok && scope.SeesAll(scope.Scope(r.CallerScope))
	if !ok {
		return
	}
	byCell := map[string]int{}
	for i := range r.Accounts {
		busy, open := 0, 0
		r.Accounts[i].SessionsBusy, r.Accounts[i].SessionsOpen = &busy, &open
		for _, c := range r.Accounts[i].Cells {
			byCell[c.ID] = i
		}
	}
	seen := map[string]bool{}
	for _, s := range ss {
		if !s.Open || seen[s.ID] {
			continue
		}
		seen[s.ID] = true
		i, known := byCell[s.CellID]
		if !known {
			continue
		}
		*r.Accounts[i].SessionsOpen++
		if s.Busy {
			*r.Accounts[i].SessionsBusy++
		}
	}
}
