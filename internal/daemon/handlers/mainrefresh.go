package handlers

import (
	"time"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The create path fast-forwards the project's main worktree before branching
// from it (see internal/backend/git/git.go). Until now that verdict went to
// daemon.log and nowhere else, so the one person guaranteed to be watching —
// whoever just typed `pigtail new` — never learned that the checkout they read
// from was 23 commits behind, nor that pigtail had declined to fix it.
//
// These two helpers carry it: into the create response, so the CLI can say it,
// and into the freshness registry, so `pigtail servers` keeps answering
// "is main fresh?" with something measured minutes ago rather than at the last
// sweep tick.

// onMainRefresh builds the Create hook. It stores the verdict in *seen for the
// caller to report, and records it for the read-only /servers view.
//
// Synchronous, inside Create: no goroutine, no lock held across it.
func onMainRefresh(svc *Services, p *pigtail.Project, seen *backend.MainRefresh) func(backend.MainRefresh) {
	return func(res backend.MainRefresh) {
		if seen != nil {
			*seen = res
		}
		if svc != nil {
			svc.MainFresh.Record(p, res, time.Now())
		}
	}
}

// mainRefreshNote converts a verdict for the wire, or nil when there is none
// to report — a backend with no shared checkout, or a create that was asked
// not to fetch, must produce no note at all rather than an empty one that
// reads as "checked, nothing to say".
func mainRefreshNote(res backend.MainRefresh) *pigtail.MainRefreshNote {
	if res.Action == "" {
		return nil
	}
	return &pigtail.MainRefreshNote{
		Action: string(res.Action),
		Path:   res.Path,
		Base:   res.Base,
		Reason: res.Reason,
		Behind: res.Behind,
		From:   res.From,
		To:     res.To,
	}
}
