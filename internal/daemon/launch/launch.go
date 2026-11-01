package launch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/sessionlaunch"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Origin is the spawnreg origin of a launch made through the API.
const Origin = "api"

// ErrNoCell is what Deps.Cell returns when the scope has no cell.
var ErrNoCell = errors.New("no cell for the scope")

// Refusal is a launch refused with a contract code; the handler answers it
// as is (docs/wire-protocol.md § Session launch).
type Refusal struct {
	Code    pigtail.ErrorCode
	Status  int
	Msg     string
	Details map[string]any
}

func (r *Refusal) Error() string { return r.Msg }

func refuse(sub string, status int, code pigtail.ErrorCode, details map[string]any, format string, a ...any) *Refusal {
	if details == nil {
		details = map[string]any{}
	}
	details["subject_id"] = sub
	return &Refusal{Code: code, Status: status, Msg: fmt.Sprintf(format, a...), Details: details}
}

// Launcher launches sessions on subjects.
type Launcher struct {
	d Deps

	mu   sync.Mutex
	base context.Context // the daemon's context, set by Start
	wg   sync.WaitGroup  // the confirmations in flight
}

// New builds a launcher; nil seams take their real default.
func New(d Deps) *Launcher {
	d.defaults()
	return &Launcher{d: d, base: context.Background()}
}

// Start records the daemon's context — the launcher's goroutines live on
// it, never on a request's — then resumes the API launches left open.
func (l *Launcher) Start(ctx context.Context) {
	l.mu.Lock()
	l.base = ctx
	l.mu.Unlock()
	l.Resume(ctx)
}

func (l *Launcher) daemonCtx() context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.base
}

// tabLister lists the cmux tabs through the launcher's seams.
type tabLister struct{ d *Deps }

func (t tabLister) ListWindows() ([]cmux.Window, error) { return t.d.Windows() }
func (t tabLister) ListWorkspaces(id string) ([]cmux.WorkspaceInfo, error) {
	return t.d.Workspaces(id)
}

// errNoStaffingPicker: a Staffing launch on a launcher with no budget-aware
// picker; it never falls back on Deps.Cell.
var errNoStaffingPicker = errors.New("staffing launch: no budget-aware cell picker")

type staffingKey struct{}

// WithStaffing marks ctx as a Staffing launch: the launcher picks its cell by
// the weekly budget (Deps.StaffingCell). Only Staffing's wiring sets it, so no
// other launch can ever be refused by the budget (D6).
func WithStaffing(ctx context.Context) context.Context {
	return context.WithValue(ctx, staffingKey{}, true)
}

// FromStaffing reports the mark of WithStaffing.
func FromStaffing(ctx context.Context) bool {
	v, _ := ctx.Value(staffingKey{}).(bool)
	return v
}

// Launch opens a tab running the launcher on sub, reserved in the spawn
// registry with Origin "api". The caller has checked the scope. A launch
// already in flight for sub through the API is answered as is
// (Deduplicated).
func (l *Launcher) Launch(ctx context.Context, sub pigtail.Subject) (pigtail.SessionLaunch, error) {
	d := &l.d
	now := d.Now()
	if _, err := d.Windows(); err != nil {
		return pigtail.SessionLaunch{}, refuse(sub.ID, http.StatusServiceUnavailable, pigtail.ErrCmuxUnreachable,
			map[string]any{"cause": err.Error()}, "cmux unreachable: list windows: %v", err)
	}
	bindings, err := d.Store.ListCmuxBindings(sub.ID)
	if err != nil {
		return pigtail.SessionLaunch{}, fmt.Errorf("list the subject's cmux links: %w", err)
	}

	// Filled in fn, read by the Handoff (it runs after fn, in finish).
	var (
		cell   claudeprofile.Cell
		cwd    string
		mode   string
		prompt string
		out    pigtail.SessionLaunch
	)
	id, err := d.Registry.WithReservation(spawnreg.ReserveRequest{
		SubjectID: sub.ID, Origin: Origin, Now: now,
		Window: spawnreg.PendingWindow, Cap: spawnreg.ReserveCap, Alive: d.Alive,
		Tabs: func() ([]spawnreg.Tab, error) { return sessionlaunch.ListTabs(tabLister{d}) },
		Check: func(entries []spawnreg.Entry) error {
			return l.check(sub.ID, entries, bindings)
		},
		Handoff: func(id string, tab spawnreg.Tab, born time.Time) {
			l.handoff(sub, id, tab, born, cell.ID, cwd, mode, prompt)
		},
	}, func(r *spawnreg.Launch) error {
		res, err := l.resolve(ctx, sub)
		if err != nil {
			return err
		}
		cell, mode, cwd = res.Cell, res.Mode, res.Cwd
		prompt = d.Prompt(sub.ID, sub.Title)
		spec := cmux.WorkspaceSpec{
			Name:    pigtail.ShortTitle(sub),
			Cwd:     cwd,
			Command: sessionlaunch.Line(cell.ID, mode, ""),
			Env:     append(sessionlaunch.SpawnEnv(cell, cwd, prompt), d.SocketEnv()...),
		}
		l.place(sub.ID, &spec)
		tab, err := r.Create(func() (spawnreg.Tab, error) {
			ref, err := d.NewWorkspace(spec)
			return spawnreg.Tab{Ref: ref}, err
		})
		if err != nil {
			if errors.Is(err, spawnreg.ErrCmuxUnreachable) {
				return refuse(sub.ID, http.StatusServiceUnavailable, pigtail.ErrCmuxUnreachable,
					map[string]any{"cause": err.Error()}, "cmux unreachable: %v", err)
			}
			return refuse(sub.ID, http.StatusBadGateway, pigtail.ErrCmuxCreateFailed,
				map[string]any{"cause": err.Error()}, "cmux new-workspace failed: %v", err)
		}
		if err := d.Bind(sub.ID, tab.ID); err != nil {
			d.Logf("launch %s: bind tab %s to %s: %v (the tab exists)", r.ID, tab.Ref, sub.ID, err)
		}
		out = pigtail.SessionLaunch{
			LaunchID: r.ID, SubjectID: sub.ID, State: pigtail.LaunchPending, Origin: Origin,
			WorkspaceID: tab.ID, WorkspaceRef: tab.Ref, Cell: cell.ID, PermissionMode: mode, Cwd: cwd,
			TabTitle: spec.Name, StartedAt: now, UpdatedAt: r.Born(),
		}
		return nil
	})
	var pe *spawnreg.PendingError
	switch {
	case err == nil:
		out.LaunchID = id
		return out, nil
	case errors.Is(err, spawnreg.ErrRegistryUnreadable):
		return pigtail.SessionLaunch{}, refuse(sub.ID, http.StatusServiceUnavailable, pigtail.ErrRegistryUnreadable,
			map[string]any{"cause": err.Error()}, "%v: nothing is launched (fail-safe)", err)
	case errors.As(err, &pe):
		if pe.Entry.Origin == Origin {
			s, err := l.dedup(pe.Entry)
			s.TabTitle = pigtail.ShortTitle(sub) // the name the first launch gave its tab
			return s, err
		}
		return pigtail.SessionLaunch{}, refuse(sub.ID, http.StatusConflict, pigtail.ErrLaunchInProgress,
			map[string]any{"launch_id": pe.Entry.ID, "workspace_ref": pe.Entry.WorkspaceRef},
			"subject %s: %v — it is confirmed or fails within %d min; then retry", sub.ID, pe, int(spawnreg.ConfirmTimeout/time.Minute))
	}
	return pigtail.SessionLaunch{}, err
}

// dedup answers an API launch already in flight on the subject, as Get
// reads it (the reserve's view when the registry cannot be read again).
func (l *Launcher) dedup(e spawnreg.Entry) (pigtail.SessionLaunch, error) {
	if s, ok, err := l.Get(e.ID); err == nil && ok {
		s.Deduplicated = true
		return s, nil
	}
	s := pigtail.SessionLaunch{
		LaunchID: e.ID, SubjectID: e.SubjectID, State: pigtail.LaunchPending, Origin: e.Origin,
		WorkspaceID: e.WorkspaceID, WorkspaceRef: e.WorkspaceRef,
		StartedAt: e.TS, UpdatedAt: e.TS, Deduplicated: true,
	}
	return s, nil
}

// check runs under the registry lock: the burst, then the live sessions read
// again here, then a live session on one of the subject's tabs.
func (l *Launcher) check(subID string, entries []spawnreg.Entry, bindings []pigtail.SubjectCmuxBinding) error {
	d := &l.d
	now := d.Now()
	if b := sessionlaunch.BurstAt(entries, now, d.Alive); b.Full() {
		return refuse(subID, http.StatusTooManyRequests, pigtail.ErrLaunchBurst,
			map[string]any{"recent": b.Recent, "frees_at": b.FreesAt()},
			"%d launches in the last %s — the next one frees at %s", b.Recent, sessionlaunch.BurstWindow, b.FreesAt().Format(time.RFC3339))
	}
	live, unreadable, err := sessionlaunch.LiveWorkspaces(d.Procs, d.Environ)
	if err != nil {
		return refuse(subID, http.StatusServiceUnavailable, pigtail.ErrLiveUnknown,
			map[string]any{"pids": []int{}}, "list Claude processes: %v — another live session on the subject cannot be ruled out", err)
	}
	if len(unreadable) > 0 {
		return refuse(subID, http.StatusServiceUnavailable, pigtail.ErrLiveUnknown,
			map[string]any{"pids": unreadable},
			"%d Claude process(es) have an unreadable environment (pid %v) — another live session on the subject cannot be ruled out", len(unreadable), unreadable)
	}
	if d.SDKLive != nil {
		if id, live := d.SDKLive(subID); live {
			return refuse(subID, http.StatusConflict, pigtail.ErrSessionLive,
				map[string]any{"sdk_session_id": id},
				"subject %s already has a live SDK session %s — end it first", subID, id)
		}
	}
	if ws, pids, found := sessionlaunch.LiveConflict(entries, live, bindings, subID, ""); found {
		return refuse(subID, http.StatusConflict, pigtail.ErrSessionLive,
			map[string]any{"workspace_id": ws, "pids": pids},
			"subject %s already has a live session in workspace %s (pid %v) — finish or close it first", subID, ws, pids)
	}
	return nil
}

// Resolved is what a launch of the subject runs on: the cell, its default
// permission mode and the working directory. Both launch paths take it from
// Resolve, so they cannot drift.
type Resolved struct {
	Cell claudeprofile.Cell
	Mode string
	Cwd  string
}

// Resolve picks the cell, mode and cwd of a launch of sub, with the same
// refusals as Launch (no cell, margin, bypass, unreadable mode, no launch dir).
func (l *Launcher) Resolve(sub pigtail.Subject) (Resolved, error) {
	return l.resolve(context.Background(), sub)
}

// resolve is Resolve under the launch's context: only a launch marked
// WithStaffing reads the weekly budget, never a Resolve from outside (D6).
func (l *Launcher) resolve(ctx context.Context, sub pigtail.Subject) (Resolved, error) {
	cell, err := l.cell(ctx, sub)
	if err != nil {
		return Resolved{}, err
	}
	mode, err := l.mode(sub.ID, cell)
	if err != nil {
		return Resolved{}, err
	}
	cwd, err := l.cwd(sub, cell)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{Cell: cell, Mode: mode, Cwd: cwd}, nil
}

// WithSDKReservation runs fn as the only launch of sub, SDK side. It takes the
// launch registry's flock (the TUI launch's), refuses a launch under way
// (spawnreg.Pending) and a live session (check, which settles the subject's SDK
// sessions first), then calls fn with the resolved cell, still under the lock:
// fn writes meta.json and starts the host, and that reservation counts as live
// for any later check. The auto mode is refused here and nowhere else.
func (l *Launcher) WithSDKReservation(sub pigtail.Subject, fn func(Resolved) (id string, err error)) (string, error) {
	d := &l.d
	res, err := l.Resolve(sub)
	if err != nil {
		return "", err
	}
	if res.Mode == "auto" && !d.SDKAcceptsAuto {
		return "", refuse(sub.ID, http.StatusForbidden, pigtail.ErrBypassRefused,
			map[string]any{"cell": res.Cell.ID},
			"cell %s defaults to auto: a SDK session would run without permission cards", res.Cell.ID)
	}
	bindings, err := d.Store.ListCmuxBindings(sub.ID)
	if err != nil {
		return "", fmt.Errorf("list the subject's cmux links: %w", err)
	}
	unlock, err := d.Registry.Lock()
	if err != nil {
		return "", refuse(sub.ID, http.StatusServiceUnavailable, pigtail.ErrRegistryUnreadable,
			map[string]any{"cause": err.Error()}, "%v: nothing is launched (fail-safe)", err)
	}
	defer unlock()
	entries, err := d.Registry.Entries()
	if err != nil {
		return "", refuse(sub.ID, http.StatusServiceUnavailable, pigtail.ErrRegistryUnreadable,
			map[string]any{"cause": err.Error()}, "%v: nothing is launched (fail-safe)", err)
	}
	if e, ok := spawnreg.Pending(entries, sub.ID, d.Now(), spawnreg.PendingOpts{Window: spawnreg.PendingWindow, Cap: spawnreg.ReserveCap, Alive: d.Alive}); ok {
		return "", refuse(sub.ID, http.StatusConflict, pigtail.ErrSessionLive,
			map[string]any{"launch_id": e.ID, "workspace_ref": e.WorkspaceRef},
			"subject %s has a launch under way (%s) — it is confirmed or fails within %d min; then retry", sub.ID, e.ID, int(spawnreg.ConfirmTimeout/time.Minute))
	}
	if err := l.check(sub.ID, entries, bindings); err != nil {
		return "", err
	}
	return fn(res)
}

// cell picks the subject scope's cell; its config dir must exist. A launch
// marked WithStaffing picks through Deps.StaffingCell, and closes without one.
func (l *Launcher) cell(ctx context.Context, sub pigtail.Subject) (claudeprofile.Cell, error) {
	noCell := func(format string, a ...any) error {
		return refuse(sub.ID, http.StatusConflict, pigtail.ErrNoCell, map[string]any{"scope": sub.Scope}, format, a...)
	}
	pick := l.d.Cell
	if FromStaffing(ctx) {
		if l.d.StaffingCell == nil {
			return claudeprofile.Cell{}, errNoStaffingPicker
		}
		pick = l.d.StaffingCell
	}
	if pick == nil {
		return claudeprofile.Cell{}, noCell("no cell for scope %s", sub.Scope)
	}
	c, err := pick(sub.Scope)
	if errors.Is(err, ErrNoCell) {
		return c, noCell("no cell for scope %s", sub.Scope)
	}
	if err != nil {
		return c, err // *launchmargin.OverMarginError, OverBudgetError… as is
	}
	if st, serr := os.Stat(c.ConfigDir); c.ConfigDir == "" || serr != nil || !st.IsDir() {
		return c, noCell("cell %s: config dir %q is missing", c.ID, c.ConfigDir)
	}
	return c, nil
}

// mode is the cell's default permission mode, always passed explicitly (D2,
// Q3); bypassPermissions is refused.
func (l *Launcher) mode(subID string, c claudeprofile.Cell) (string, error) {
	m, err := l.d.Mode(c.ConfigDir)
	if err != nil {
		return "", refuse(subID, http.StatusServiceUnavailable, pigtail.ErrCellModeUnreadable,
			map[string]any{"cell": c.ID, "cause": err.Error()}, "cell %s: default permission mode unreadable: %v", c.ID, err)
	}
	if m == claudesession.ModeBypass {
		return "", refuse(subID, http.StatusForbidden, pigtail.ErrBypassRefused,
			map[string]any{"cell": c.ID}, "cell %s defaults to %s: a launch without a human never bypasses permissions", c.ID, m)
	}
	if m == "" {
		m = "default"
	}
	if _, ok := claudesession.Stricter(m, m); !ok {
		cause := fmt.Sprintf("unknown permission mode %q", m)
		return "", refuse(subID, http.StatusServiceUnavailable, pigtail.ErrCellModeUnreadable,
			map[string]any{"cell": c.ID, "cause": cause}, "cell %s: %s", c.ID, cause)
	}
	return m, nil
}

// cwd is the subject's live worktree, else the scope's launch_dir (D3).
func (l *Launcher) cwd(sub pigtail.Subject, c claudeprofile.Cell) (string, error) {
	d := &l.d
	ws, err := d.Store.WorkspaceForSubject(sub.ID)
	switch {
	case err == nil && ws != nil && isDir(ws.Path):
		d.PreTrust(c.ConfigDir, ws.Path)
		return ws.Path, nil
	case err != nil && !errors.Is(err, store.ErrNotFound):
		d.Logf("launch on %s: read its worktree: %v — falling back to the launch_dir", sub.ID, err)
	}
	if d.LaunchDir != nil {
		if dir, ok := d.LaunchDir(sub.Scope); ok && isDir(dir) {
			return dir, nil
		}
	}
	return "", refuse(sub.ID, http.StatusConflict, pigtail.ErrNoLaunchDir, map[string]any{"scope": sub.Scope},
		"subject %s has no live worktree, and scope %s declares no existing launch_dir", sub.ID, sub.Scope)
}

func isDir(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// place puts the tab at the bottom of the subject's folder, in the first
// arranged window that holds it. Folders off: no folder call at all.
func (l *Launcher) place(subID string, spec *cmux.WorkspaceSpec) {
	d := &l.d
	on, windows := d.Folders()
	if !on || d.Folder == nil {
		return
	}
	name, err := d.Folder(subID, d.FolderOrder())
	if err != nil {
		d.Logf("launch on %s: its folder: %v — cmux places the tab", subID, err)
		return
	}
	for _, w := range windows {
		groups, err := d.Groups(w)
		if err != nil {
			d.Logf("launch on %s: list the folders of window %s: %v", subID, w, err)
			continue
		}
		for _, g := range groups {
			if !g.Pinned && g.Name == name && g.Ref != "" {
				spec.WindowID, spec.GroupRef, spec.CallerRef = w, g.Ref, ""
				return
			}
		}
	}
}

// handoff takes over a launch left open with its tab: called synchronously
// by the reservation's finish, it starts the confirmation and returns.
func (l *Launcher) handoff(sub pigtail.Subject, id string, tab spawnreg.Tab, born time.Time, cell, cwd, mode, prompt string) {
	l.track(follow{id: id, subjectID: sub.ID, scope: sub.Scope, tab: tab, born: born,
		armed: armed{cell: cell, cwd: cwd, mode: mode, prompt: prompt}, writeArmed: true})
}
