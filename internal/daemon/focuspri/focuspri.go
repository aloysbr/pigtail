// Package focuspri answers one question for memory admission: is this caller
// "in focus"? A caller is when its subject, or the subject's theme, belongs to
// an open focus (pigtail focus), or when its cmux workspace is pinned in the
// sidebar.
//
// The answer comes from an immutable in-memory snapshot, rebuilt in the
// background by Refresh. IsFocus, the only call on the request path, reads
// memory and nothing else: the store is single-connection and Store.ActiveFocus
// opens a write transaction, so a store call per request would queue behind it.
//
// Log rule, for the whole package: only Refresh logs, never IsFocus. Absence is
// data and is silent (no snapshot yet, an expired snapshot, a workspace with no
// subject, an unknown caller, a nil Pinned). A refresh that fails a store or
// focus read logs one summary line on change of state only (ok to failing,
// then one "recovered" line when it is good again).
//
// The package imports no store: everything comes through Deps, and every dep
// is called only by Refresh.
package focuspri

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aloysbr/pigtail/internal/wspath"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Caller identifies who asks, as the client sent it. Every field is optional.
type Caller struct{ SessionID, CmuxWorkspaceID, Cwd string }

// Deps is what a refresh reads. Absence is ("", nil), never an error: the
// wiring translates store.ErrNotFound.
type Deps struct {
	// ReadFocus has the shape of Store.ActiveFocus: the open focus, a seal
	// error (the list is still usable) and a read error.
	ReadFocus func() (fs []pigtail.Focus, sealErr, err error)
	// Pinned is the set of pinned cmux workspace ids; nil when unknown (loop off).
	Pinned              func() map[string]bool
	Workspaces          func() ([]pigtail.Workspace, error)
	SubjectForWorkspace func(wsID string) (string, error)
	Peers               func() ([]pigtail.PeerSession, error)
	ThemeOfSubject      func(subjectID string) (string, error)
	Now                 func() time.Time
	Logf                func(string, ...any)
}

const (
	defaultRefreshEvery = 5 * time.Second
	defaultMaxAge       = 30 * time.Second
)

type snapshot struct {
	at             time.Time
	bySubject      map[string]bool
	byTheme        map[string]bool
	pinned         map[string]bool
	wsSubject      map[string]string
	subjectTheme   map[string]string
	sessionSubject map[string]string
	workspaces     []pigtail.Workspace
}

// Resolver resolves callers against the last snapshot.
type Resolver struct {
	d            Deps
	refreshEvery time.Duration
	maxAge       time.Duration
	snap         atomic.Pointer[snapshot]

	mu      sync.Mutex // serialises Refresh and guards failing
	failing bool
}

// New builds a resolver; it does nothing until Refresh or Run. Zero durations
// take the defaults (5 s, 30 s).
func New(d Deps, refreshEvery, maxAge time.Duration) *Resolver {
	if refreshEvery <= 0 {
		refreshEvery = defaultRefreshEvery
	}
	if maxAge <= 0 {
		maxAge = defaultMaxAge
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logf == nil {
		d.Logf = func(string, ...any) {}
	}
	return &Resolver{d: d, refreshEvery: refreshEvery, maxAge: maxAge}
}

// Run refreshes once synchronously, then every refreshEvery until ctx ends.
func (r *Resolver) Run(ctx context.Context) {
	r.Refresh()
	t := time.NewTicker(r.refreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Refresh()
		}
	}
}

type refreshErrs struct {
	n     int
	first error
}

func (e *refreshErrs) add(err error) {
	e.n++
	if e.first == nil {
		e.first = err
	}
}

// Refresh rebuilds the snapshot. A failed read of the focus, the workspaces
// or the peers keeps the previous snapshot; a failed per-entry read drops that
// entry only.
func (r *Resolver) Refresh() {
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs refreshErrs

	fs, sealErr, ferr := r.d.ReadFocus()
	if sealErr != nil {
		errs.add(sealErr)
	}
	wss, werr := r.d.Workspaces()
	peers, perr := r.d.Peers()
	hard := false
	for _, e := range []error{ferr, werr, perr} {
		if e != nil {
			errs.add(e)
			hard = true
		}
	}
	if hard {
		r.report(errs)
		return
	}

	s := &snapshot{
		at:             r.d.Now(),
		bySubject:      map[string]bool{},
		byTheme:        map[string]bool{},
		pinned:         map[string]bool{},
		wsSubject:      map[string]string{},
		subjectTheme:   map[string]string{},
		sessionSubject: map[string]string{},
		workspaces:     wss,
	}
	for _, f := range fs {
		for _, m := range f.Members {
			switch m.Kind {
			case pigtail.FocusMemberSubject:
				s.bySubject[m.ID] = true
			case pigtail.FocusMemberTheme:
				s.byTheme[m.ID] = true
			}
		}
	}
	for id, ok := range r.d.Pinned() {
		if ok {
			s.pinned[strings.ToUpper(id)] = true
		}
	}
	for _, ws := range wss {
		sub, err := r.d.SubjectForWorkspace(ws.ID)
		if err != nil {
			errs.add(err)
			continue
		}
		if sub != "" {
			s.wsSubject[ws.ID] = sub
		}
	}
	for _, p := range peers {
		if p.SessionID == "" {
			continue
		}
		sub := p.SubjectID
		if sub == "" {
			wsID := p.WorkspaceID
			if wsID == "" {
				if ws, ok := wspath.Deepest(p.Cwd, wss); ok {
					wsID = ws.ID
				}
			}
			sub = s.wsSubject[wsID]
		}
		if sub != "" {
			s.sessionSubject[p.SessionID] = sub
		}
	}
	subjects := map[string]bool{}
	for _, sub := range s.wsSubject {
		subjects[sub] = true
	}
	for _, sub := range s.sessionSubject {
		subjects[sub] = true
	}
	for sub := range subjects {
		th, err := r.d.ThemeOfSubject(sub)
		if err != nil {
			errs.add(err)
			continue
		}
		if th != "" {
			s.subjectTheme[sub] = th
		}
	}
	r.snap.Store(s)
	r.report(errs)
}

// report applies the log rule: one line per change of state. Caller holds mu.
func (r *Resolver) report(errs refreshErrs) {
	switch {
	case errs.n > 0 && !r.failing:
		r.failing = true
		r.d.Logf("focuspri: refresh: %d errors, first: %v", errs.n, errs.first)
	case errs.n == 0 && r.failing:
		r.failing = false
		r.d.Logf("focuspri: refresh: recovered")
	}
}

// IsFocus reports whether the caller is in focus. Memory only: no dep call, no
// lock, no log; any doubt is false.
func (r *Resolver) IsFocus(c Caller) bool {
	s := r.snap.Load()
	if s == nil || r.d.Now().Sub(s.at) > r.maxAge {
		return false
	}
	if c.CmuxWorkspaceID != "" && s.pinned[strings.ToUpper(c.CmuxWorkspaceID)] {
		return true
	}
	sub := ""
	if ws, ok := wspath.Deepest(c.Cwd, s.workspaces); ok {
		sub = s.wsSubject[ws.ID]
	}
	if sub == "" && c.SessionID != "" {
		sub = s.sessionSubject[c.SessionID]
	}
	if sub == "" {
		return false
	}
	if s.bySubject[sub] {
		return true
	}
	return s.byTheme[s.subjectTheme[sub]]
}
