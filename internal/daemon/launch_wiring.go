package daemon

import (
	"log"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeconfig"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/daemon/cmuxsync"
	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/daemon/launch"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/sdksession"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// wireLauncher builds the session launcher (sub_x000000000000015) on the
// daemon's services and sets svc.Launch. Called in buildRouter, before the
// subject routes. Config.LaunchOverrides replaces seams in tests: no test
// reaches the real ps nor cmux.
func (s *Server) wireLauncher(svc *handlers.Services) {
	if s.store == nil {
		return
	}
	reg := svc.MarginSpawns
	if reg == nil {
		reg = spawnreg.Open(spawnreg.DefaultPath())
	}
	focus := cmuxsync.FocusSource(s.store.ActiveFocus, time.Now, log.Printf)
	d := launch.Deps{
		Store:    s.store,
		Registry: reg,
		Prompt:   s.userSettings().LaunchPrompt,
		Cell:     handlers.LaunchCell(svc),
		// Read only under launch.WithStaffing (wireStaffing's Launch).
		StaffingCell: handlers.LaunchCellStaffing(svc),
		LaunchDir: func(sc string) (string, bool) {
			return scope.Active().LaunchDir(scope.Canonical(sc))
		},
		Mode: claudesession.DefaultPermissionMode,
		// The SDK sessions dir is read even when the SDK option is off: a host
		// still alive on disk must block a TUI launch (read-only, plus the kill of
		// a proven orphan claude).
		SDKLive: sdkLive(&sdksession.Registry{Dir: sdksession.DefaultDir()}),
		PreTrust: func(configDir, worktree string) {
			if r := claudeconfig.PreTrustWorkspace(configDir, worktree); r.Outcome == claudeconfig.Failed {
				log.Printf("launch: pre-trust %s: %s", worktree, r.Reason)
			}
		},
		Folders: func() (bool, []string) {
			if svc.CmuxSidebar == nil {
				return false, nil
			}
			st := svc.CmuxSidebar.State()
			return st.Enabled && st.Folders, st.FoldersWindows
		},
		FolderOrder: s.cmuxFolderOrder,
		Folder: func(subjectID string, folders []string) (string, error) {
			fs, err := focus()
			if err != nil {
				return "", err
			}
			return cmuxsync.SubjectFolder(s.store, fs, folders, subjectID)
		},
		Bind:    func(subjectID, wsID string) error { return handlers.BindLaunched(svc, subjectID, wsID) },
		Publish: func(e pigtail.Event) { s.bus.Publish(e) },
		SocketEnv: func() []string {
			if s.cfg.SocketPath == "" || s.cfg.SocketPath == defaultSocketPath() {
				return nil
			}
			return []string{"PIGTAIL_SOCKET=" + s.cfg.SocketPath}
		},
		Logf: log.Printf,
	}
	if s.cfg.LaunchOverrides != nil {
		s.cfg.LaunchOverrides(&d)
	}
	s.launcher = launch.New(d)
	svc.Launch = s.launcher
}

// sdkLive is launch.Deps.SDKLive on a registry: settle the subject's sessions
// (B3: a dead host's claude is a writer until proven and killed), then ask.
func sdkLive(r *sdksession.Registry) func(string) (string, bool) {
	return func(subID string) (string, bool) {
		if err := r.SettleSubject(subID); err != nil {
			log.Printf("launch: settle sdk sessions of %s: %v", subID, err)
		}
		return r.LiveForSubject(subID)
	}
}
