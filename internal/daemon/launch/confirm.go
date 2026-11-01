package launch

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// armed is what a launch-armed record carries; empty for a launch resumed
// without one.
type armed struct{ cell, cwd, mode, prompt string }

// follow is one launch the launcher confirms.
type follow struct {
	id, subjectID, scope string
	tab                  spawnreg.Tab
	born                 time.Time
	armed                armed
	writeArmed           bool // a fresh handoff writes its launch-armed; a resume has one or none
}

// track runs confirm on the daemon's context, counted for Wait.
func (l *Launcher) track(f follow) {
	ctx := l.daemonCtx()
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.confirm(ctx, f)
	}()
}

// Wait returns once every confirmation has ended (the daemon's drain).
func (l *Launcher) Wait() { l.wg.Wait() }

// failCause is the failed launch's cause when no session registered.
var failCause = fmt.Sprintf("no session registered within %d min of the tab's birth", int(spawnreg.ConfirmTimeout/time.Minute))

// confirm waits for the session to register on the tab: a live peer whose
// cmux workspace is the tab, updated at or after its birth, until
// born + spawnreg.ConfirmTimeout. A stop of the daemon writes nothing: Resume
// takes the launch over at the next start.
func (l *Launcher) confirm(ctx context.Context, f follow) {
	d := &l.d
	if f.writeArmed {
		if err := d.Registry.Append(spawnreg.Entry{TS: time.Now().UTC(), Kind: spawnreg.KindLaunchArmed, ID: f.id,
			SubjectID: f.subjectID, Origin: Origin, WorkspaceID: f.tab.ID, WorkspaceRef: f.tab.Ref,
			Cell: f.armed.cell, Cwd: f.armed.cwd, Mode: f.armed.mode, Prompt: f.armed.prompt,
			WorkerPID: os.Getpid(), Result: spawnreg.ResultArmed}); err != nil {
			d.Logf("launch %s: write launch-armed: %v", f.id, err)
		}
		l.publish(f.id, f.scope)
	}
	deadline := f.born.Add(spawnreg.ConfirmTimeout)
	for {
		if ctx.Err() != nil {
			return
		}
		if p, ok := l.peerOn(f); ok {
			l.close(f, spawnreg.Entry{TS: time.Now().UTC(), ID: f.id, SubjectID: f.subjectID, Origin: Origin,
				WorkspaceID: f.tab.ID, WorkspaceRef: f.tab.Ref, PID: p.PID,
				Cell: f.armed.cell, Cwd: f.armed.cwd, Prompt: f.armed.prompt, Mode: f.armed.mode, Result: spawnreg.ResultLaunched})
			return
		}
		now := d.Now()
		if !now.Before(deadline) {
			l.close(f, spawnreg.Entry{TS: time.Now().UTC(), Kind: spawnreg.KindLaunchResult, ID: f.id, SubjectID: f.subjectID,
				Origin: Origin, WorkspaceID: f.tab.ID, WorkspaceRef: f.tab.Ref, Result: spawnreg.ResultFailed, Detail: failCause})
			return
		}
		wait := d.PollEvery
		if left := deadline.Sub(now); left < wait {
			wait = left
		}
		select {
		case <-ctx.Done():
			return
		case <-d.After(wait):
		}
	}
}

// peerOn finds the session registered on the tab. A listing error is logged
// and never read as « no peer ».
func (l *Launcher) peerOn(f follow) (pigtail.PeerSession, bool) {
	peers, err := l.d.Store.ListPeers(false)
	if err != nil {
		l.d.Logf("launch %s: list peers: %v (polling on)", f.id, err)
		return pigtail.PeerSession{}, false
	}
	for _, p := range peers {
		if p.EndedAt == nil && strings.EqualFold(p.CmuxWorkspaceID, f.tab.ID) && !p.UpdatedAt.Before(f.born) {
			return p, true
		}
	}
	return pigtail.PeerSession{}, false
}

// close writes the launch's end and publishes it.
func (l *Launcher) close(f follow, e spawnreg.Entry) {
	if err := l.d.Registry.Append(e); err != nil {
		l.d.Logf("launch %s: write its %s end: %v", f.id, e.Result, err)
		return
	}
	l.publish(f.id, f.scope)
}

// Resume takes over, at the daemon's start, every API launch still open with
// a tab: its confirmation goes on to the original deadline. A launch without
// a tab is left to the registry (its owner is gone, it closes at read).
func (l *Launcher) Resume(_ context.Context) {
	entries, err := l.d.Registry.Entries()
	if err != nil {
		l.d.Logf("launch resume: read the registry: %v", err)
		return
	}
	now := l.d.Now()
	for _, r := range records(entries) {
		if r.origin() != Origin || r.ref == nil {
			continue
		}
		if _, closed := spawnreg.Outcome(entries, r.id, now, l.pendingOpts()); closed {
			continue
		}
		f := follow{id: r.id, subjectID: r.subjectID(), tab: spawnreg.Tab{ID: r.ref.WorkspaceID, Ref: r.ref.WorkspaceRef}, born: r.ref.TS}
		if r.armed != nil {
			f.armed = armed{cell: r.armed.Cell, cwd: r.armed.Cwd, mode: r.armed.Mode, prompt: r.armed.Prompt}
		}
		if sub, err := l.d.Store.GetSubject(f.subjectID); err == nil && sub != nil {
			f.scope = sub.Scope
		} else {
			l.d.Logf("launch %s: read subject %s: %v (its events stay untagged)", r.id, f.subjectID, err)
		}
		l.d.Logf("launch %s: resumed on %s, until %s", r.id, f.tab.Ref, f.born.Add(spawnreg.ConfirmTimeout).Format(time.RFC3339))
		l.track(f)
	}
}
