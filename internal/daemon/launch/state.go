package launch

import (
	"encoding/json"

	"github.com/aloysbr/pigtail/internal/sessionlaunch"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// record is one launch as the registry holds it.
type record struct {
	id      string
	reserve *spawnreg.Entry
	ref     *spawnreg.Entry
	armed   *spawnreg.Entry // the latest launch-armed
	last    spawnreg.Entry
}

// records groups the registry's launch records by id, in first-seen order.
func records(entries []spawnreg.Entry) []*record {
	byID := map[string]*record{}
	var out []*record
	for i := range entries {
		e := entries[i]
		if e.ID == "" {
			continue
		}
		switch e.Kind {
		case "", spawnreg.KindLaunchReserve, spawnreg.KindLaunchRef, spawnreg.KindLaunchArmed, spawnreg.KindLaunchResult:
		default:
			continue
		}
		r, ok := byID[e.ID]
		if !ok {
			r = &record{id: e.ID}
			byID[e.ID] = r
			out = append(out, r)
		}
		switch e.Kind {
		case spawnreg.KindLaunchReserve:
			if r.reserve == nil {
				r.reserve = &e
			}
		case spawnreg.KindLaunchRef:
			r.ref = &e
		case spawnreg.KindLaunchArmed:
			r.armed = &e
		}
		r.last = e
	}
	return out
}

func (r *record) subjectID() string {
	if r.reserve != nil && r.reserve.SubjectID != "" {
		return r.reserve.SubjectID
	}
	return r.last.SubjectID
}

func (r *record) origin() string {
	o := r.last.Origin
	if r.reserve != nil {
		o = r.reserve.Origin
	}
	if o == "" {
		return "cli"
	}
	return o
}

func (l *Launcher) pendingOpts() spawnreg.PendingOpts {
	return spawnreg.PendingOpts{Window: spawnreg.PendingWindow, Cap: spawnreg.ReserveCap, Alive: l.d.Alive}
}

// Get reads a launch from the registry, any origin (CLI included). An
// unknown id is false. cell, cwd and permission_mode come from the
// launch-armed record: empty before it.
func (l *Launcher) Get(id string) (pigtail.SessionLaunch, bool, error) {
	entries, err := l.d.Registry.Entries()
	if err != nil {
		return pigtail.SessionLaunch{}, false, err
	}
	for _, r := range records(entries) {
		if r.id == id {
			return l.view(entries, r), true, nil
		}
	}
	return pigtail.SessionLaunch{}, false, nil
}

// List reads every launch of the registry, any origin (CLI included), in
// first-seen order, each as Get would answer it.
func (l *Launcher) List() ([]pigtail.SessionLaunch, error) {
	entries, err := l.d.Registry.Entries()
	if err != nil {
		return nil, err
	}
	rs := records(entries)
	out := make([]pigtail.SessionLaunch, 0, len(rs))
	for _, r := range rs {
		out = append(out, l.view(entries, r))
	}
	return out, nil
}

// Burst counts the registry's recent launches by the rule Launch refuses a
// burst with (sessionlaunch.BurstAt). It reads only: no session budget is
// consulted here.
func (l *Launcher) Burst() (sessionlaunch.Burst, error) {
	entries, err := l.d.Registry.Entries()
	if err != nil {
		return sessionlaunch.Burst{}, err
	}
	return sessionlaunch.BurstAt(entries, l.d.Now(), l.d.Alive), nil
}

func (l *Launcher) view(entries []spawnreg.Entry, r *record) pigtail.SessionLaunch {
	s := pigtail.SessionLaunch{LaunchID: r.id, SubjectID: r.subjectID(), Origin: r.origin(), State: pigtail.LaunchPending,
		StartedAt: r.last.TS, UpdatedAt: r.last.TS}
	if r.reserve != nil {
		s.StartedAt = r.reserve.TS
	}
	if r.ref != nil {
		s.WorkspaceID, s.WorkspaceRef = r.ref.WorkspaceID, r.ref.WorkspaceRef
	}
	if r.armed != nil {
		s.Cell, s.Cwd, s.PermissionMode = r.armed.Cell, r.armed.Cwd, r.armed.Mode
	}
	out, closed := spawnreg.Outcome(entries, r.id, l.d.Now(), l.pendingOpts())
	if !closed {
		return s
	}
	s.UpdatedAt = out.TS
	if s.WorkspaceID == "" {
		s.WorkspaceID, s.WorkspaceRef = out.WorkspaceID, out.WorkspaceRef
	}
	if out.Result == spawnreg.ResultLaunched {
		s.State = pigtail.LaunchLaunched
		if r.armed == nil {
			s.Cell, s.Cwd, s.PermissionMode = out.Cell, out.Cwd, out.Mode
		}
		return s
	}
	s.State, s.Cause = pigtail.LaunchFailed, out.Detail
	return s
}

// eventData is a SessionLaunch as the SSE event carries it: without
// deduplicated.
func eventData(s pigtail.SessionLaunch) map[string]interface{} {
	b, err := json.Marshal(s)
	if err != nil {
		return map[string]interface{}{"launch_id": s.LaunchID, "state": string(s.State)}
	}
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	delete(m, "deduplicated")
	return m
}

// publish sends the launch's current state, tagged with the subject's scope.
func (l *Launcher) publish(id, scope string) {
	s, ok, err := l.Get(id)
	if err != nil || !ok {
		l.d.Logf("launch %s: read its state to publish it: ok=%v err=%v", id, ok, err)
		return
	}
	l.d.Publish(pigtail.Event{Type: pigtail.EventSessionLaunchChanged, Scope: scope, Data: eventData(s)})
}
