package daemon

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/cmuxcheck"
	"github.com/aloysbr/pigtail/internal/daemon/cmuxsync"
	"github.com/aloysbr/pigtail/internal/daemon/cmuxwatch"
	"github.com/aloysbr/pigtail/internal/daemon/focuspri"
	"github.com/aloysbr/pigtail/internal/daemon/rammon"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// newCmuxSync builds the cmux sidebar loop (spec "Sidebar pigtail", PR 4).
// It is always constructed, so GET /cmux/sidebar answers and POST can switch
// it on; it calls cmux only while switched on (--cmux-sidebar, or the toggle).
//
// This loop is one of the places pigtaild itself executes the cmux CLI (four,
// CLAUDE.md § cmux sidebar: with the launcher and the dead-input probe);
// and the relay's idle wake (handlers/relay_wake.go: sessions list,
// identify, read-screen, send — bounded, aimed by UUID, no CMUX_* env). The
// post-relaunch check runs cmux from a `pigtail` subprocess (cmuxcheck_wiring.go).
func (s *Server) newCmuxSync() *cmuxsync.Runner {
	return cmuxsync.New(s.cmuxSyncConfig(), s.cmuxSyncDeps())
}

// focusPriDeps wires the focus resolver (internal/daemon/focuspri) to the
// store and to the sidebar loop's pinned workspaces. Every func is called by
// the resolver's refresh only, never on the admission request path. s.cmuxSync
// is read at call time, not at build time. Absence is ("", nil): the store's
// ErrNotFound is translated here, focuspri imports no store.
func (s *Server) focusPriDeps() focuspri.Deps {
	notFound := func(v string, err error) (string, error) {
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		return v, err
	}
	return focuspri.Deps{
		ReadFocus: func() ([]pigtail.Focus, error, error) { return s.store.ActiveFocus(time.Now()) },
		Pinned: func() map[string]bool {
			if s.cmuxSync == nil {
				return nil
			}
			return s.cmuxSync.PinnedWorkspaces()
		},
		Workspaces:          func() ([]pigtail.Workspace, error) { return s.store.ListWorkspaces("") },
		SubjectForWorkspace: func(id string) (string, error) { return notFound(s.store.SubjectForWorkspace(id)) },
		Peers:               func() ([]pigtail.PeerSession, error) { return s.store.ListPeers(false) },
		ThemeOfSubject: func(id string) (string, error) {
			sub, err := s.store.GetSubject(id)
			if err != nil {
				return notFound("", err)
			}
			return sub.ThemeID, nil
		},
		Now:  time.Now,
		Logf: log.Printf,
	}
}

// cmuxSyncConfig is the loop's configuration from the daemon's flags. The
// folder arrangement (--cmux-folders) acts only while the loop is on.
func (s *Server) cmuxSyncConfig() cmuxsync.Config {
	return cmuxsync.Config{
		Enabled:           s.cfg.CmuxSidebar,
		Windows:           s.cfg.CmuxSidebarWindows,
		ClaudeCodeIdleFix: !s.cfg.CmuxClaudeCodeFixDisabled,
		Folders:           s.cfg.CmuxFolders,
		GroupMoves:        !cmux.ReorderMovesGroups,     // P4b
		AddNeedsRemove:    !cmux.AddMovesFromGroup,      // P2 (b)
		FolderNoSort:      s.cfg.CmuxFolderSortDisabled, // sub_x000000000000030, D5
		BirthGrace:        s.cfg.CmuxBirthGrace,
	}
}

// cmuxSyncDeps wires the sidebar loop's seams to this daemon (pulled out of
// newCmuxSync so TestServerWiresMaintenanceSidebar can check them).
func (s *Server) cmuxSyncDeps() cmuxsync.Deps {
	deps := cmuxsync.Deps{
		Sidebar:      cmux.SidebarCLI{},
		Store:        s.store,
		RefreshPeers: s.refreshPeersFromCmux,
		Publish:      func(e pigtail.Event) { s.bus.Publish(e) },
		Warnings: func() []string {
			return cmuxsync.SettingsWarnings(cmuxsync.SettingsPath(), s.cfg.CmuxFolders && s.cfg.CmuxFolderSortDisabled, s.cfg.CmuxCheckMode == cmuxwatch.ModeFix)
		},
		// A wait the loop judges over goes through the relay: store, marker
		// and session.wait_changed, like a DELETE /peers/{session}/wait. The
		// relay is built with the router, after the loop.
		// A gone session keeps its subject's wait (D29-A).
		ClearWaits: func(session string, sources []string, gone bool) {
			if s.relay == nil {
				if gone {
					_, _ = s.store.ClearGoneSessionWaits(session, sources...)
				} else {
					_, _ = s.store.ClearSessionWaits(session, sources...)
				}
				return
			}
			if err := s.relay.ClearWaitsKeeping(session, gone, sources...); err != nil {
				log.Printf("cmuxsync: clear waits of %s: %v", session, err)
			}
		},
		// Same for a held wait, by its holder: the relay refreshes the marker
		// and publishes; without a relay yet, the store alone.
		ClearHeldWait: func(session string, holderPID int) (bool, error) {
			if s.relay == nil {
				return s.store.ClearHeldWait(session, holderPID)
			}
			return s.relay.ClearHeldWait(session, holderPID)
		},
	}
	// A Standby whose workspace is gone (two listings): through the relay, so
	// the marker and session.standby_changed follow; without a relay yet, the
	// row alone (the marker is rebuilt at the next start).
	deps.StandbyGone = func(session, workspace string) error {
		if s.relay == nil {
			_, err := s.store.ClearStandby(session, time.Now())
			return err
		}
		return s.relay.EndStandbyGone(session, workspace)
	}
	// A Standby whose tab is pinned by hand (D29): through the relay too, so
	// the marker and session.standby_changed follow; without a relay yet,
	// the row alone.
	deps.StandbyPinned = func(session, workspace string) error {
		if s.relay == nil {
			_, err := s.store.ClearStandby(session, time.Now())
			return err
		}
		return s.relay.EndStandbyPinned(session, workspace)
	}
	// A closed workspace that lost its link or off-topic row: the hook's markers follow at once.
	deps.MarkersChanged = s.syncWorkspaceMarkers
	// The cell ids, so a cell-only description posed before a restart is
	// still recognised as pigtail's (D1). One small file, read per pass.
	deps.KnownCells = knownCells
	if s.ramMonitor != nil {
		deps.Servers = s.cmuxServers
	}
	if s.maintenance != nil {
		deps.Maintenance = s.maintenance.Current
		deps.MaintenanceDir = s.maintenance.Dir()
		deps.FrozenSessions = s.frozenSessions
	}
	// D13: the last cmux check's defects, as the pigtail-chk pill.
	deps.CmuxCheck = func() ([]cmuxsync.CheckDefect, error) {
		return cmuxCheckDefects(cmuxcheck.StateDir(), cmuxcheck.DefaultSentinel())
	}
	// The daemon's dead-input probe (sub_x000000000000024): memory only,
	// read each pass; none with --cmux-input-probe off.
	deps.DeadInputs = s.deadInputs
	s.wireCmuxFolders(&deps)
	return deps
}

// wireCmuxFolders gives the loop its folder seams (D2, sub_x000000000000041).
// They are set whatever --cmux-folders says; Config.Folders decides whether
// the loop uses them.
func (s *Server) wireCmuxFolders(deps *cmuxsync.Deps) {
	deps.Folders = cmux.FoldersCLI{}
	spaces := s.cfg.Spaces
	deps.FolderOrder = s.cmuxFolderOrder
	deps.FolderIcon = func(f string) string {
		if spaces == nil {
			switch f {
			case space.NoSubject:
				return space.NoSubjectIcon
			case space.Standby:
				return space.StandbyIcon
			case space.MacMaintenance:
				return space.MacMaintenanceIcon
			}
			return ""
		}
		return spaces.Icon(f)
	}
	// The « Paused » pill's read of the pause table (sub_x000000000000036).
	deps.Paused = s.store.ListPauses
	// The « Role » pill: every open role alert, whatever its scope (F-B2:
	// the loop is the master's view, the scope guard is for agents).
	deps.RoleAlerts = func() ([]cmuxsync.RoleAlert, error) {
		rows, err := s.store.OpenRoleAlerts(scope.Any)
		if err != nil {
			return nil, err
		}
		return roleAlertsForPill(rows), nil
	}
	// A pause whose workspace is gone (two listings, final review I1):
	// through the relay, so session.pause_changed follows.
	deps.PauseGone = func(session, workspace string) error {
		if s.relay == nil {
			_, err := s.store.EndPause(session)
			return err
		}
		return s.relay.EndPauseGone(session, workspace)
	}
	// The arrangement's read of the Standby table: display only, never a turn
	// nor a typed text (Prédicat Standby, rangement).
	deps.Standby = func() (map[string]bool, error) {
		rows, err := s.store.ListStandby()
		if err != nil {
			return nil, err
		}
		out := make(map[string]bool, len(rows))
		for _, r := range rows {
			out[r.SessionID] = true
		}
		return out, nil
	}
	// The arrangement's read of the held Mac waits that announced a duration
	// (maintenance window, C4): the « Maintenance » folder; display only, an
	// error skips the round.
	deps.MacNeed = func() (map[string]bool, error) {
		waits, err := s.store.ListSessionWaits()
		if err != nil {
			return nil, err
		}
		return cmuxsync.MacNeedSessions(waits), nil
	}
	// The open focus (sub_x000000000000007), judged at read; a seal error is
	// logged once per distinct error, never read as « no focus ».
	deps.Focus = cmuxsync.FocusSource(s.store.ActiveFocus, time.Now, log.Printf)
	// A hand drag into a space folder (lot 3 of sub_x000000000000033): the
	// store's traced writes (subject_log note, journal line); the space's
	// scope from the spaces table, « Inbox » and the rest no space.
	deps.SubjectSpaceSet = func(subjectID, sp, from string) error {
		_, err := s.store.SetSubjectSpaceByDrag(subjectID, sp, from, time.Now())
		return err
	}
	deps.OffTopicSpaceSet = func(workspace, sp, from string) error {
		_, err := s.store.SetOffTopicSpaceByDrag(workspace, sp, from, time.Now())
		return err
	}
	deps.SpaceScope = func(name string) (string, bool) {
		if spaces == nil {
			return "", false
		}
		sp, ok := spaces.Get(name)
		return sp.Scope, ok
	}
	deps.ShellAtPrompt = cmuxcheck.ShellAtPrompt
	deps.SurfaceNeverRendered = cmux.SurfaceNeverRendered
	// The folded state lives next to the database: a branch daemon (--db
	// elsewhere) never overwrites the real one's.
	path := filepath.Join(filepath.Dir(s.cfg.DBPath), "cmux-folders.json")
	deps.LoadCollapse = func() (map[string]bool, error) { return cmuxsync.LoadCollapseFile(path) }
	deps.SaveCollapse = func(m map[string]bool) error { return cmuxsync.SaveCollapseFile(path, m) }
	// The birth clock (D31, D35) sits beside it.
	births := filepath.Join(filepath.Dir(s.cfg.DBPath), "cmux-births.json")
	deps.LoadBirths = func() (map[string]time.Time, bool, error) { return cmuxsync.LoadBirthsFile(births) }
	deps.SaveBirths = func(m map[string]time.Time) error { return cmuxsync.SaveBirthsFile(births, m) }
	deps.BirthsModTime = func() (time.Time, error) { return cmuxsync.BirthsFileModTime(births) }
}

// frozenSessions is the sessions a maintenance freeze holds (D4), read by the
// sidebar loop and by the relay's idle wake (D32); nil without maintenance.
func (s *Server) frozenSessions() map[string]bool {
	if s.maintenance == nil {
		return nil
	}
	st, ok, err := s.maintenance.Current()
	if err != nil || !ok {
		return nil
	}
	return maintenance.FrozenSessions(s.maintenance.Dir(), st)
}

// knownCells reads the cell ids of the cell table; nil when it cannot be
// read (no recognition of a cell text then, only pigtail's memory).
func knownCells() map[string]bool {
	tbl, err := claudeprofile.Load(setup.ConfigDir())
	if err != nil || tbl == nil {
		return nil
	}
	out := make(map[string]bool, len(tbl.Cells))
	for id := range tbl.Cells {
		if id != "" {
			out[id] = true
		}
	}
	return out
}

// cmuxServers reads the dev servers for the server pill: the listening ones
// (a live port probe, the same one GET /servers makes) and rammon's recent
// deaths. A death carries no owner, so it is read back from the server's
// marker, which only a stop removes — a death leaves it in place.
func (s *Server) cmuxServers() cmuxsync.ServerSnapshot {
	// Read before the probe: a sweep that lands in between is then credited
	// to the next reading, never one that has not seen what we saw.
	snap := cmuxsync.ServerSnapshot{SweptAt: s.ramMonitor.LastSweepAt()}
	stats, _, _ := s.ramMonitor.Inspect()
	for _, st := range stats {
		snap.Up = append(snap.Up, cmuxsync.ServerObs{WorkspaceID: st.WorkspaceID, Server: st.Server, Port: st.Port, Owner: st.Owner})
	}
	for _, d := range s.ramMonitor.RecentDeaths(s.ramMonitor.Now()) {
		names := d.Servers
		if len(names) == 0 && d.Server != "" {
			names = []string{d.Server}
		}
		snap.Deaths = append(snap.Deaths, cmuxsync.ServerDeath{
			WorkspaceID: d.WorkspaceID, Servers: names, Port: d.Port, At: d.At, Owner: s.deadServerOwner(d.WorkspaceID, names),
		})
	}
	// Conflicts recorded by the Inspect above; the session registry is read
	// once for all of them.
	if conflicts := s.ramMonitor.Conflicts(); len(conflicts) > 0 {
		peers, err := s.store.ListPeers(false)
		if err != nil {
			peers = nil
		}
		sort.SliceStable(peers, func(i, j int) bool { return peers[i].UpdatedAt.After(peers[j].UpdatedAt) })
		for _, c := range conflicts {
			for _, owner := range s.conflictOwners(c, peers) {
				snap.Conflicts = append(snap.Conflicts, cmuxsync.ServerObs{WorkspaceID: c.WorkspaceID, Server: c.Server, Port: c.Port, Owner: owner})
			}
		}
	}
	return snap
}

// conflictOwners names the cmux workspaces a port conflict shows under:
// the server's marker when the conflicted worktree has one, else every
// cmux workspace hosting a live Claude session in that worktree (the
// session registry: workspace_id, or a cwd inside the worktree when the
// session registered before the workspace). None: no pill — the conflict
// stays visible in `pigtail servers` and `pigtail list`.
func (s *Server) conflictOwners(c rammon.PortConflict, peers []pigtail.PeerSession) []string {
	ws, err := s.store.GetWorkspace(c.WorkspaceID)
	if err != nil || ws == nil || ws.Path == "" {
		return nil
	}
	if o := devserver.ReadOwner(ws.Path, c.Server); o != "" {
		return []string{strings.ToUpper(strings.TrimSpace(o))}
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range peers {
		if p.CmuxWorkspaceID == "" {
			continue
		}
		in := p.WorkspaceID == c.WorkspaceID || (p.WorkspaceID == "" && p.Cwd != "" && devserver.Within(p.Cwd, ws.Path))
		if !in {
			continue
		}
		id := strings.ToUpper(strings.TrimSpace(p.CmuxWorkspaceID))
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (s *Server) deadServerOwner(wsID string, names []string) string {
	ws, err := s.store.GetWorkspace(wsID)
	if err != nil || ws == nil || ws.Path == "" {
		return ""
	}
	for _, n := range names {
		if o := devserver.ReadOwner(ws.Path, n); o != "" {
			return o
		}
	}
	return ""
}

// watchServerEvents subscribes now and returns the relay to run: it calls
// wake (the sidebar loop's ServersChanged) when a dev server dies or is
// evicted, so "dead" shows as soon as rammon judges the death instead of on
// the next slow tick. Harmless while the loop is off: a switched-off pass
// makes no call.
func (s *Server) watchServerEvents(wake func()) func(context.Context) {
	return s.watchEvents(wake, pigtail.EventServerDied, pigtail.EventServerEvicted)
}

// watchWaitEvents subscribes now and returns the relay to run: it calls wake
// (the sidebar loop's WaitsChanged) on every wait recorded or cleared — the
// hooks, the DELETE route, the done command, and the loop's own clears (they go through
// the relay, which publishes when a row went: one extra pass, bounded) — and every
// transition of a wait record, so the pill follows at the next pass instead
// of the tick. A wake costs one pass without listing; a dropped event (full
// subscriber) is caught by the tick.
func (s *Server) watchWaitEvents(wake func()) func(context.Context) {
	return s.watchEvents(wake, pigtail.EventSessionWaitChanged, pigtail.EventWaitOpened, pigtail.EventWaitAnswered,
		pigtail.EventWaitDelivered, pigtail.EventWaitSnoozed, pigtail.EventWaitClosed)
}

// watchEvents subscribes now to the given event types and returns the relay
// to run: it calls wake on each one until ctx ends.
func (s *Server) watchEvents(wake func(), types ...pigtail.EventType) func(context.Context) {
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = string(t)
	}
	sub := s.bus.Subscribe(names...)
	return func(ctx context.Context) {
		defer sub.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-sub.C:
				if !ok {
					return
				}
				wake()
			}
		}
	}
}

// refreshPeersFromCmux snapshots the Claude sessions cmux hosts into the
// registry, the join `pigtail session ls` makes CLI-side, and returns their
// ids. The cell table gives each session its scope; without it, scopes
// already stored are kept (an empty field never erases one).
func (s *Server) refreshPeersFromCmux() ([]string, error) {
	b, err := cmux.SessionsJSONListing()
	if err != nil {
		return nil, err
	}
	cs, err := peers.ParseCmuxSessions(b)
	if err != nil {
		return nil, err
	}
	cells, _ := claudeprofile.Load(setup.ConfigDir())
	ps := peers.Build(peers.Inputs{Cmux: cs, Cells: cells})
	if s.relay != nil {
		if err := s.relay.RecordSnapshot(ps); err != nil {
			return nil, err
		}
	}
	ids := make([]string, 0, len(ps))
	for _, p := range ps {
		ids = append(ids, p.SessionID)
	}
	return ids, nil
}

// cmuxFolderOrder is the loop's folder order, from the space table (« Inbox »
// alone without one), « Standby » last (D6). The session launcher passes the
// same order to cmuxsync.SubjectFolder.
func (s *Server) cmuxFolderOrder() []string {
	if s.cfg.Spaces != nil {
		return s.cfg.Spaces.FolderOrder()
	}
	return []string{space.NoSubject}
}

// roleAlertsForPill turns the open role alert rows into the pill's input; the
// state is read from the row's source (pigtail:roles/<name>?state=<state>).
func roleAlertsForPill(rows []store.RoleAlertRow) []cmuxsync.RoleAlert {
	out := make([]cmuxsync.RoleAlert, 0, len(rows))
	for _, r := range rows {
		state := "silent"
		if _, q, ok := strings.Cut(r.Source, "?state="); ok && q != "" {
			state = q
		}
		out = append(out, cmuxsync.RoleAlert{SubjectID: r.SubjectID, Role: r.Name, State: state, Since: r.Key})
	}
	return out
}
