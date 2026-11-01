// Package launch is pigtaild's session launcher (sub_x000000000000015): it
// opens a cmux tab running the launcher on a subject, without a calling
// session, then follows it until the session registers. It is the third
// place pigtaild runs cmux (D1): `new-workspace`, and `list-windows`,
// the workspace listing (cmux.SidebarCLI.ListWorkspaces) and the group
// listing (cmux.FoldersCLI.ListGroups) to read. Nothing else in
// internal/daemon references cmux.NewWorkspace.
package launch

import (
	"github.com/aloysbr/pigtail/internal/usersettings"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Store is what the launcher reads from pigtaild's store.
type Store interface {
	GetSubject(id string) (*pigtail.Subject, error)
	WorkspaceForSubject(subjectID string) (*pigtail.Workspace, error)
	ListCmuxBindings(subjectID string) ([]pigtail.SubjectCmuxBinding, error)
	ListPeers(includeEnded bool) ([]pigtail.PeerSession, error)
}

// Deps are the launcher's seams; every one can be simulated. A nil cmux,
// process or mode seam takes the real one, here only.
type Deps struct {
	Store    Store
	Registry *spawnreg.Registry

	// Cell picks the cell of a scope (margin and fallback). It returns a
	// *launchmargin.OverMarginError as is, and ErrNoCell when the scope has
	// no cell.
	Cell func(scope string) (claudeprofile.Cell, error)
	// StaffingCell picks the cell of a Staffing launch (WithStaffing): the
	// margin and the weekly budget, no fallback. nil closes such a launch.
	StaffingCell func(scope string) (claudeprofile.Cell, error)
	// LaunchDir is the scope's declared launch_dir (D3), ~ expanded.
	LaunchDir func(scope string) (string, bool)
	// Mode reads the cell's default permission mode (D2).
	Mode func(configDir string) (string, error)
	// PreTrust pre-accepts a worktree in the cell's folder trust; a warning only.
	PreTrust func(configDir, worktree string)

	// SDKLive settles the subject's SDK sessions (a dead host's proven orphan
	// claude is killed), then says whether one still has a writer (the host, a
	// claude, or a launch under way). Nil = no SDK session. Read by both launch
	// paths, under the registry lock.
	SDKLive func(subID string) (id string, live bool)
	// SDKAcceptsAuto: the SDK path accepts a cell whose default mode is auto.
	// False is the measure (spikes/sdk/RESULTS.md, H6b): under auto the SDK
	// never calls canUseTool, so no permission card could appear.
	SDKAcceptsAuto bool

	// Procs and Environ are the seams of sessionlaunch.LiveWorkspaces.
	Procs   func() ([]claudesession.Proc, error)
	Environ func(pid int) (string, error)
	// Alive tells whether a reserve's owner still runs.
	Alive func(pid int) bool

	// cmux seams.
	Windows      func() ([]cmux.Window, error)
	Workspaces   func(windowID string) ([]cmux.WorkspaceInfo, error)
	Groups       func(windowID string) ([]cmux.Group, error)
	NewWorkspace func(cmux.WorkspaceSpec) (string, error)

	// Folders is the loop's state: on = Enabled && Folders, and the windows
	// the folders arrange (FoldersWindows). Nil = off.
	Folders func() (on bool, windows []string)
	// FolderOrder is the loop's own folder order (FolderOrder()), never
	// augmented with focus names; passed to Folder.
	FolderOrder func() []string
	// Folder is the folder a tab bound to the subject files in
	// (cmuxsync.SubjectFolder).
	Folder func(subjectID string, folders []string) (string, error)

	// Prompt is the first message of a launched session (settings.yaml,
	// usersettings.Settings.LaunchPrompt). Nil = the neutral default.
	Prompt func(id, title string) string

	// Bind links the new tab to the subject (handlers.BindLaunched).
	Bind func(subjectID, wsID string) error
	// Publish sends an event on the daemon's bus.
	Publish func(pigtail.Event)
	// SocketEnv is PIGTAIL_SOCKET=<daemon socket> when it differs from the
	// default path, so the session registers with the daemon that launched it.
	SocketEnv func() []string

	Now  func() time.Time
	Logf func(format string, a ...any)
	// After and PollEvery pace the confirmation's polling.
	After     func(time.Duration) <-chan time.Time
	PollEvery time.Duration
}

func (d *Deps) defaults() {
	if d.Prompt == nil {
		d.Prompt = usersettings.Defaults().LaunchPrompt
	}
	if d.Mode == nil {
		d.Mode = claudesession.DefaultPermissionMode
	}
	if d.PreTrust == nil {
		d.PreTrust = func(string, string) {}
	}
	if d.Procs == nil {
		d.Procs = claudesession.ClaudeProcesses
	}
	if d.Environ == nil {
		d.Environ = cmux.ProcessEnviron
	}
	if d.Alive == nil {
		d.Alive = claudesession.PidAlive
	}
	if d.Windows == nil {
		d.Windows = cmux.SidebarCLI{}.ListWindows
	}
	if d.Workspaces == nil {
		d.Workspaces = cmux.SidebarCLI{}.ListWorkspaces
	}
	if d.Groups == nil {
		d.Groups = cmux.FoldersCLI{}.ListGroups
	}
	if d.NewWorkspace == nil {
		d.NewWorkspace = cmux.NewWorkspace
	}
	if d.Folders == nil {
		d.Folders = func() (bool, []string) { return false, nil }
	}
	if d.FolderOrder == nil {
		d.FolderOrder = func() []string { return nil }
	}
	if d.Bind == nil {
		d.Bind = func(string, string) error { return nil }
	}
	if d.Publish == nil {
		d.Publish = func(pigtail.Event) {}
	}
	if d.SocketEnv == nil {
		d.SocketEnv = func() []string { return nil }
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	if d.Logf == nil {
		d.Logf = func(string, ...any) {}
	}
	if d.After == nil {
		d.After = time.After
	}
	if d.PollEvery == 0 {
		d.PollEvery = 2 * time.Second
	}
}
