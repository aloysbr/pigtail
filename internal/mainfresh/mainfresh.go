// Package mainfresh remembers the latest verdict about each project's main
// worktree, so "is the checkout I am about to read fresh?" can be answered
// without asking git.
//
// The question is asked BEFORE pointing an agent at the main directory, which
// is the moment the answer is worth anything — and it is asked from a
// read-only command (`pigtail servers`) that must stay fast. The daemon's
// refresh sweep already produces exactly that verdict on a timer, and the
// create path produces one too; both drop it here, and the display path only
// ever reads. Nothing in this package runs git: a display that computed
// freshness would turn a listing into a fetch, and a listing that is slow
// stops being consulted — which is the failure mode the whole visible-drift
// effort exists to fix, one layer up.
//
// The registry is in-memory on purpose. A verdict is a statement about a
// working tree at an instant; surviving a daemon restart would let it outlive
// its subject, and a stale claim of freshness is strictly worse than no claim.
// After a restart the sweep repopulates it within one immediate pass.
package mainfresh

import (
	"sort"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Status is the latest known verdict about one project's main worktree,
// with the project named so a reader can act on it.
type Status struct {
	ProjectID   string
	ProjectName string

	// Path is the main worktree directory the verdict is about, Base the ref
	// it is measured against (e.g. "origin/main").
	Path string
	Base string

	// Action / Reason / Behind mirror backend.MainRefresh.
	Action backend.MainRefreshAction
	Reason string
	Behind int

	// At is when the verdict was produced. A reader needs it: "level with
	// origin/main" measured three hours ago is a claim about three hours ago,
	// and the daemon may have died since.
	At time.Time
}

// Registry holds one Status per project. Safe for concurrent use, and every
// method tolerates a nil receiver so callers that were wired without one
// (tests, a daemon built before this existed) degrade to a no-op instead of
// panicking.
type Registry struct {
	mu        sync.RWMutex
	byProject map[string]Status
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{byProject: map[string]Status{}}
}

// Record stores the verdict for p, replacing any previous one.
//
// Last writer wins, deliberately: the sweep and the create path both produce
// real verdicts about the same directory, and the newest measurement is the
// only one that describes it now.
func (r *Registry) Record(p *pigtail.Project, res backend.MainRefresh, at time.Time) {
	if r == nil || p == nil || res.Action == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byProject == nil {
		r.byProject = map[string]Status{}
	}
	r.byProject[p.ID] = Status{
		ProjectID:   p.ID,
		ProjectName: p.Name,
		Path:        res.Path,
		Base:        res.Base,
		Action:      res.Action,
		Reason:      res.Reason,
		Behind:      res.Behind,
		At:          at,
	}
}

// Snapshot returns every known status, ordered by project name then id so a
// listing does not reshuffle between two calls. Nil when nothing is known —
// which a caller must render as *silence*, not as "everything is fine".
func (r *Registry) Snapshot() []Status {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.byProject) == 0 {
		return nil
	}
	out := make([]Status, 0, len(r.byProject))
	for _, s := range r.byProject {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectName != out[j].ProjectName {
			return out[i].ProjectName < out[j].ProjectName
		}
		return out[i].ProjectID < out[j].ProjectID
	})
	return out
}
