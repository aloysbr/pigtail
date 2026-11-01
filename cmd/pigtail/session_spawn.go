package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	gitbackend "github.com/aloysbr/pigtail/internal/backend/git"
	"github.com/aloysbr/pigtail/internal/claudeconfig"
	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/claudesession"
	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/sessionlaunch"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// envSpawnMaxLiveGone is the env --max-live read before the burst detector
// (D1, 2026-10-21): no longer read, named only to say so.
const envSpawnMaxLiveGone = "PIGTAIL_SPAWN_MAX_LIVE"

// spawnDeprecationWarnings: what the old live caps' settings now say (stderr only).
func spawnDeprecationWarnings(maxLiveSet bool, getenv func(string) string) []string {
	const why = "the burst detector replaced the live caps (more than 10 launches in 10 min is refused)"
	var out []string
	if maxLiveSet {
		out = append(out, "warning: --max-live is ignored: "+why+" — drop the flag")
	}
	if getenv(envSpawnMaxLiveGone) != "" {
		out = append(out, "warning: "+envSpawnMaxLiveGone+" is ignored: "+why+" — unset it")
	}
	return out
}

// Seams: every outside call spawn makes. Tests replace them all.
var (
	spawnCallerWSRef     = cmux.CallerWorkspaceRef
	spawnGroupOf         = cmux.GroupOf
	spawnCallerClaude    = func() (claudesession.Proc, bool, error) { return claudesession.CallerClaude(os.Getpid()) }
	spawnCellDefaultMode = claudesession.DefaultPermissionMode
	spawnCallerSession   = claudesession.PidSession
	spawnPermissionStamp = func(sid string) (string, error) { return readPermissionStamp(sid, time.Now()) }
	spawnConsumeStamp    = consumePermissionStamp
	spawnMainCheckout    = gitbackend.SharedMainCheckout
	spawnNewWorkspace    = cmux.NewWorkspace
	spawnPreTrust        = preTrustLaunch
	spawnMarkTrust       = func(ctx context.Context, c *pigtail.Client, wsID string, pid int) error {
		return c.MarkCmuxTrustPending(ctx, wsID, pid)
	}
	spawnReadScreen     = cmux.ReadScreen
	spawnSurfacesIn     = cmux.SurfaceRefsIn
	spawnVerifyTimeout  = 30 * time.Second
	spawnVerifyInterval = 250 * time.Millisecond
	spawnTrustWindow    = 5 * time.Second // how long a launched tab is watched for the trust dialog
	// spawnCallerBound reads whether the calling workspace is bound to a subject
	// (GET /cmux-workspaces/:wsid; 404 = not bound).
	spawnCallerBound = func(ctx context.Context, c *pigtail.Client, wsID string) (bool, error) {
		if _, err := c.CmuxWorkspace(ctx, wsID); err != nil {
			if pigtail.IsCmuxBoundElsewhere(err) {
				return true, nil // D7: bound, to a subject this cell cannot read
			}
			if isNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}
	spawnRenameWorkspace = func(wsID, title string) error { return cmux.SidebarCLI{}.Rename(wsID, title) }
)

type spawnRequest struct {
	SubjectID, Cwd, Prompt, Name string
	Cell                         string // --cell: the target cell ("" = the caller's)
	Branch                       string // --branch: the branch --new-worktree creates
	CrossScope, DryRun, Relay    bool
	NewWorktree                  bool // --new-worktree (D2)
	OverMargin                   bool // --over-margin: launch even past the usage margin (decision forced)
}

type spawnPlan struct {
	Subject pigtail.Subject
	Cell    claudeprofile.Cell
	// CallerCell is the calling session's cell; Cell is the target (Task 4).
	CallerCell  claudeprofile.Cell
	CellOrigin  string // how Cell was chosen: originCaller, originFlag, originAccount
	Subjectless bool   // D1: no subject — Subject is zero, nothing of a subject is read or written
	Cwd         string
	NewWorktree *plannedWorktree // D2: the worktree to create from the --cwd main checkout
	Perm        permDecision
	CallerWS    string     // UUID, upper case
	CallerWSRef string     // workspace:N
	GroupRef    string     // "" = caller in no group
	LiveSpawned int        // spawned sessions whose Claude is alive
	Burst       spawnBurst // launches of the last spawnBurstWindow (D1)
	Unreadable  []int      // Claude pids whose environment could not be read (dry-run only: a real spawn refuses)
	Prompt      string
	Spec        cmux.WorkspaceSpec
	// CallerSession keys the permission record executeSpawn consumes.
	CallerSession string
	// RelayFrom is the caller's workspace the new session takes over from
	// (--relay, D3·A); RelayNothing, why a --relay passed nothing.
	RelayFrom, RelayNothing string
	CallerTitle             string // dry-run: the title an unbound caller would take (D6)
	// Bindings: the subject's cmux bindings read by the plan; the reservation's
	// Check reuses them (a launch since is seen by its reserve or its live Claude).
	Bindings      []pigtail.SubjectCmuxBinding
	CallerWarning string // dry-run: why it cannot tell
	// Rotation: the account rotation's pick (or why it fell back), when no
	// --cell was given for a subject; its Warnings go to stderr.
	Rotation spawnRotation
}

var cellIDRe = regexp.MustCompile(`^[a-z]+$`)

func spawnErr(format string, a ...any) error { return fmt.Errorf("session spawn: "+format, a...) }

// spawnFrozen refuses a spawn during a --freeze maintenance, before anything
// happens (spawn never reaches the daemon's frozen /workspaces/new: it opens
// the tab through cmux). Same text as the daemon's 423; works with the daemon
// down. An unreadable state is closed (D8), for every session.
func spawnFrozen(getenv func(string) string) error {
	dir := maintenanceDir(getenv)
	st, ok, err := maintenance.Load(dir)
	if err != nil {
		return retryable(retryLong, spawnErr("%s: %s — nothing launched", pigtail.ErrMaintenanceFrozen, maintenance.UnreadableMessage(dir, err)))
	}
	if ok && st.FrozenFor(getenv("CLAUDE_CODE_SESSION_ID")) {
		if sid := getenv("CLAUDE_CODE_SESSION_ID"); sid != "" {
			_ = maintenance.MarkFrozen(dir, sid, st.ID) // D4: the sidebar's Blocked · freeze; never fails the refusal
		}
		return retryable(retryLong, spawnErr("%s: %s — nothing launched", pigtail.ErrMaintenanceFrozen, maintenance.FrozenMessage(st, scope.Scope(sessionScope(getenv)))))
	}
	return nil
}

// liveWorkspaces maps each cmux workspace UUID to the pids of the Claude
// processes running in it, and lists the Claude pids whose environment could
// not be read: each of them could be a live session of the subject we could not rule out.
func liveWorkspaces() (map[string][]int, []int, error) {
	return sessionlaunch.LiveWorkspaces(claudeProcs, processEnviron)
}

// spawnWorkspaceAt finds the pigtail workspace whose worktree contains path
// (workspaceForDir: worktree root + sameDir, so --cwd on a subdirectory or a
// symlinked path is still refused). nil, nil when none: a directory outside
// any git worktree, or one pigtail does not manage. A var so tests stub it.
var spawnWorkspaceAt = func(ctx context.Context, c *pigtail.Client, path string) (*pigtail.Workspace, error) {
	ws, _, err := workspaceForDir(ctx, c, path)
	if errors.Is(err, errNotInWorktree) {
		return nil, nil
	}
	return ws, err
}

// checkWorktreeHooks refuses a worktree whose post-create hooks failed or
// still run (D2). Unknown (nil) never refuses.
func checkWorktreeHooks(ws *pigtail.Workspace) error {
	if ws == nil || ws.Hooks == nil {
		return nil
	}
	switch ws.Hooks.Status {
	case pigtail.HooksFailed:
		hook := ws.Hooks.Hook
		if hook == "" {
			hook = "?"
		}
		return spawnErr("worktree %s (%s): post-create hook %q failed (%s) — log: %s; the hooks after it did not run: fix the cause, then pigtail hooks run %s — or remove it with pigtail clean %s and create it again",
			ws.ID, ws.Path, hook, ws.Hooks.Error, ws.Hooks.Log, ws.ID, ws.ID)
	case pigtail.HooksRunning:
		return retryable(retryLong, spawnErr("worktree %s (%s): post-create hooks still running — log: %s; retry once they end (pigtail list shows …hooks until then)",
			ws.ID, ws.Path, ws.Hooks.Log))
	}
	return nil
}

func planSpawn(ctx context.Context, c *pigtail.Client, reg *spawnreg.Registry, req spawnRequest) (spawnPlan, error) {
	var p spawnPlan
	if err := validatePrompt(req.Prompt); err != nil {
		return p, spawnErr("%v", err)
	}
	if req.SubjectID == "" && req.Cell == "" {
		return p, spawnErr("--subject is required — or launch a subjectless session in another scope's cell: --cell <id> --cwd <dir>")
	}
	if req.Branch != "" && !req.NewWorktree { // R21
		return p, spawnErr("--branch only names the branch --new-worktree creates — add --new-worktree, or drop --branch")
	}
	if err := spawnFrozen(os.Getenv); err != nil {
		return p, err
	}
	p.CallerWS = strings.ToUpper(os.Getenv(cmux.EnvWorkspaceID))
	if p.CallerWS == "" {
		return p, spawnErr("not in a cmux pane ($%s unset) — spawn opens a workspace next to its caller", cmux.EnvWorkspaceID)
	}

	// resolveCaller's three steps, with the target cell resolved between the
	// subject read and the permission steps (respawn keeps resolveCaller).
	caller, err := resolveCallerCell("spawn", spawnErr)
	if err != nil {
		return p, err
	}
	var target claudeprofile.Cell
	var origin string
	if req.SubjectID == "" {
		// D1: no subject read at all — the caller reads and binds nothing of
		// the other scope.
		if target, origin, p.Rotation, err = resolveTargetCell(caller, req.Cell, nil, req.OverMargin); err != nil {
			return p, err
		}
		if err := checkSubjectless(req, caller, target); err != nil {
			return p, err
		}
		p.Subjectless = true
	} else {
		if err := readCallerSubject(ctx, c, &caller, req.SubjectID, req.CrossScope, spawnErr); err != nil {
			return p, err
		}
		if target, origin, p.Rotation, err = resolveTargetCell(caller, req.Cell, &caller.Subject, req.OverMargin); err != nil {
			return p, err
		}
	}
	if err := resolveCallerPerm(&caller, "spawn", spawnErr); err != nil {
		return p, err
	}
	p.Cell, p.CallerCell, p.CellOrigin, p.Subject = target, caller.Cell, origin, caller.Subject
	p.CallerSession, p.Perm = caller.SessionID, caller.Perm
	sub, cell := &p.Subject, p.Cell

	cwd := req.Cwd
	if !p.Subjectless && (cwd == "" || req.NewWorktree) {
		ws, err := c.SubjectWorktree(ctx, sub.ID)
		if err != nil && !isNotFound(err) {
			return p, retryable(retryShort, spawnErr("read worktree of %s: %v", sub.ID, err))
		}
		live := ws != nil && ws.Path != "" && pathExists(ws.Path)
		switch {
		case live && req.NewWorktree: // R17
			return p, spawnErr("subject %s already has worktree %s (%s) — drop --new-worktree to launch in it", sub.ID, ws.ID, ws.Path)
		case live:
			cwd = ws.Path
		case cwd == "" && req.NewWorktree: // R22
			return p, spawnErr("--new-worktree needs --cwd <repo main checkout> — the subject has no worktree to say which repository")
		case cwd == "": // R15
			return p, spawnErr("subject %s has no live worktree — pass --cwd <dir>, create one with --new-worktree --cwd <repo main checkout>, or: pigtail new --subject %s \"<intent>\"", sub.ID, sub.ID)
		}
	}
	if cwd, err = filepath.Abs(cwd); err != nil {
		return p, spawnErr("--cwd: %v", err)
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return p, spawnErr("cwd %s is not a directory — pass an existing directory with --cwd, or omit it to use the subject's worktree", cwd)
	}
	if strings.ContainsAny(cwd, " \t\n") {
		return p, spawnErr("cwd %q contains whitespace — the launch could not be verified (ps -E); use a path without spaces", cwd)
	}
	if p.Subjectless {
		if err := checkCwdScope(caller.Cells, cwd, target); err != nil {
			return p, err
		}
	}
	if req.NewWorktree {
		var bindSub *pigtail.Subject
		if !p.Subjectless {
			bindSub = sub
		}
		pw, err := planWorktree(ctx, c, cwd, bindSub, req.Branch, caller.Cells, p.Cell)
		if err != nil {
			return p, err
		}
		p.NewWorktree = pw
		cwd = pw.Path // the launch dir is the new worktree: no shared-main check
		if strings.ContainsAny(cwd, " \t\n") {
			return p, spawnErr("cwd %q contains whitespace — the launch could not be verified (ps -E); use a path without spaces", cwd)
		}
	} else if shared, err := spawnMainCheckout(cwd); err != nil {
		return p, spawnErr("%v", err)
	} else if shared { // R16
		return p, spawnErr("cwd %s is the main checkout of a repo with linked worktrees (shared by several sessions) — pass --new-worktree to create a worktree from it, or --cwd an existing worktree", cwd)
	}
	p.Cwd = cwd
	if !req.NewWorktree {
		ws, err := spawnWorkspaceAt(ctx, c, cwd)
		if err != nil {
			return p, retryable(retryShort, spawnErr("read workspaces: %v", err))
		}
		if err := checkWorktreeHooks(ws); err != nil {
			return p, err
		}
	}

	live, unreadable, err := readLiveStrict(req.DryRun)
	if err != nil {
		return p, err
	}
	p.Unreadable = unreadable
	entries, err := reg.Entries()
	if err != nil {
		return p, registryUnreadable(err)
	}
	now := time.Now().UTC()
	p.Burst = burstAt(entries, now)
	if !p.Subjectless {
		if p.Bindings, err = c.CmuxWorkspaces(ctx, sub.ID); err != nil {
			return p, retryable(retryShort, spawnErr("read workspaces of %s: %v", sub.ID, err))
		}
	}
	if err := spawnGuards(entries, live, p.Bindings, p, now); err != nil {
		return p, err
	}
	if !p.Subjectless {
		if err := sdkLiveGuard(ctx, c, sub.ID, spawnErr); err != nil {
			return p, err
		}
	}
	liveSet := map[string]bool{}
	for ws, pids := range live {
		liveSet[ws] = len(pids) > 0
	}
	liveSpawned := spawnreg.Live(entries, liveSet)
	p.LiveSpawned = len(liveSpawned)
	relayWS, nothing, err := relaySlot(req.Relay, caller.Env, caller.Proc.PID, p.CallerWS, live, liveSpawned)
	if err != nil {
		return p, spawnErr("%v", err)
	}
	p.RelayFrom, p.RelayNothing = relayWS, nothing

	if p.CallerWSRef, err = spawnCallerWSRef(); err != nil {
		return p, retryable(retryShort, spawnErr("locate the calling workspace: %v", err))
	}
	if p.GroupRef, err = spawnGroupOf(p.CallerWSRef); err != nil {
		return p, retryable(retryShort, spawnErr("read cmux groups: %v", err))
	}
	name := req.Name
	switch {
	case name != "":
	case p.Subjectless && p.NewWorktree != nil:
		name = p.NewWorktree.Branch
	case p.Subjectless:
		name = filepath.Base(cwd) // T11
	default:
		name = pigtail.ShortTitle(*sub)
	}
	p.Prompt = req.Prompt
	p.Spec = cmux.WorkspaceSpec{
		Name:      name,
		Cwd:       cwd,
		Command:   launchLine(cell.ID, p.Perm.Mode, spawnGateReason(p.RelayFrom)),
		Env:       sessionlaunch.SpawnEnv(cell, cwd, req.Prompt),
		GroupRef:  p.GroupRef,
		CallerRef: p.CallerWSRef,
	}
	return p, nil
}

// readLiveStrict maps the live Claude processes by workspace. A real spawn
// refuses (retryable) when the listing fails or an environment is unreadable:
// another live session on the subject could not be ruled out. A dry-run keeps
// the unreadable pids to show them.
func readLiveStrict(dry bool) (map[string][]int, []int, error) {
	live, unreadable, err := liveWorkspaces()
	if err != nil {
		return nil, nil, retryable(retryShort, spawnErr("list Claude processes: %v", err))
	}
	if len(unreadable) > 0 && !dry {
		return nil, nil, retryable(retryShort, spawnErr("%d Claude process(es) have an unreadable environment (pid %v) — another live session on the subject cannot be ruled out; retry, or look at those processes", len(unreadable), unreadable))
	}
	return live, unreadable, nil
}

func registryUnreadable(err error) error {
	return retryable(retryShort, spawnErr("%v — the launches of the last %s cannot be counted, so nothing is launched (fail-safe); repair or remove that line", err, burstWindowText()))
}

// spawnPendingWindow: the duplicate guard's window, from the launch-ref.
const spawnPendingWindow = spawnreg.PendingWindow

// spawnPendingOpts: the same liveness and exclusion for every read of a launch in progress.
func spawnPendingOpts(p spawnPlan) spawnreg.PendingOpts {
	return spawnreg.PendingOpts{Window: spawnPendingWindow, Cap: spawnreg.ReserveCap, Alive: pidAlive, ExcludeWorkspaceID: p.CallerWS}
}

// pendingRefusal is the « launch in progress » refusal: exit 1, never
// retryable — the answer is to wait for the confirmation, not to loop.
func pendingRefusal(sub string, e spawnreg.Entry) error {
	where := "a tab not created yet"
	if e.WorkspaceRef != "" {
		where = e.WorkspaceRef
	}
	return spawnErr("subject %s: launch in progress in %s — it is confirmed or fails within %d min; then retry", sub, where, int(spawnreg.ConfirmTimeout/time.Minute))
}

// spawnGuards are the plan's registry and liveness refusals: the burst, a
// launch in progress on the subject, another live session among its
// candidate workspaces. planSpawn runs them as a diagnosis (dry-run too); the
// reservation runs them again under the lock, on the entries and the live set
// re-read there.
func spawnGuards(entries []spawnreg.Entry, live map[string][]int, bindings []pigtail.SubjectCmuxBinding, p spawnPlan, now time.Time) error {
	if b := burstAt(entries, now); b.full() {
		return b.refusal(now)
	}
	if p.Subjectless {
		return nil
	}
	sub := p.Subject.ID
	if e, ok := spawnreg.Pending(entries, sub, now, spawnPendingOpts(p)); ok {
		return pendingRefusal(sub, e)
	}
	if ws, pids, found := sessionlaunch.LiveConflict(entries, live, bindings, sub, p.CallerWS); found {
		return spawnErr("subject %s already has a live session in workspace %s (pid %v) — finish or close it first", sub, ws, pids)
	}
	return nil
}

func printSpawnPlan(w io.Writer, p spawnPlan) {
	group := "none (caller in no group)"
	if p.GroupRef != "" {
		group = p.GroupRef + " (right under the caller " + p.CallerWSRef + ")"
	}
	argv := cmux.NewWorkspaceArgs(p.Spec)
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = shQuote(a)
	}
	fmt.Fprintf(w, "pigtail session spawn (dry-run) — nothing created\n")
	if p.Subjectless {
		fmt.Fprintf(w, "  subject   none — the new session binds itself (subjectless spawn into cell %s)\n", p.Cell.ID)
	} else {
		fmt.Fprintf(w, "  subject   %s (%s) · scope %s\n", p.Subject.ID, p.Subject.Title, p.Subject.Scope)
	}
	fmt.Fprintf(w, "  cell      %s (%s) — %s\n", p.Cell.ID, p.Cell.ConfigDir, p.CellOrigin)
	if m := p.Rotation.Margin; m != nil && m.Action != launchmargin.ActionOK {
		fmt.Fprintf(w, "  margin    %s (requested %s, launching on %s)\n", m.Action, orDash(m.Requested.ID), p.Cell.ID)
	}
	if ch := p.Rotation.Choice; ch != nil {
		for i, c := range ch.Ranking {
			fmt.Fprintf(w, "  rotation  %d. %s\n", i+1, rankingLine(c))
		}
	}
	fmt.Fprintf(w, "  cwd       %s\n", p.Cwd)
	if pw := p.NewWorktree; pw != nil {
		bind := "unbound"
		if pw.BindTo != "" {
			bind = "bound to " + pw.BindTo
		}
		fmt.Fprintf(w, "  worktree  would create %s · branch %s · project %s · %s\n", pw.Path, pw.Branch, pw.Project.ID, bind)
	}
	fmt.Fprintf(w, "  mode      %s (launch %s, %s; current %s) — passed as --permission-mode\n", p.Perm.Mode, p.Perm.Launch, p.Perm.Origin, p.Perm.Current)
	fmt.Fprintf(w, "  prompt    %s\n", p.Prompt)
	fmt.Fprintf(w, "  group     %s\n", group)
	if p.CallerTitle != "" {
		fmt.Fprintf(w, "  caller    would rename %s to %q\n", p.CallerWSRef, p.CallerTitle)
	}
	fmt.Fprintf(w, "  launches  %s\n", p.Burst.line())
	if p.RelayFrom != "" {
		fmt.Fprintf(w, "  relay     from %s — the new session takes over from it (end that session once this one runs)\n", p.RelayFrom)
	}
	fmt.Fprintf(w, "  cmux      %s\n", strings.Join(q, " "))
	if len(p.Unreadable) > 0 {
		fmt.Fprintf(w, "⚠ %s\n", unreadableWarning(p.Unreadable))
	}
	if p.RelayNothing != "" {
		fmt.Fprintf(w, "⚠ --relay passed nothing: %s\n", p.RelayNothing)
	}
	if p.CallerWarning != "" {
		fmt.Fprintf(w, "⚠ %s\n", p.CallerWarning)
	}
}

func sessionSpawnCmd() *cobra.Command {
	var req spawnRequest
	var dryRun, asJSON bool
	var maxLiveIgnored int
	var watchRun string
	cmd := &cobra.Command{
		Use:         "spawn (--subject <id> | --cell <id> --cwd <dir>) --prompt <line>",
		Annotations: scopedWrite(),
		Short:       "Open a fresh Claude session on a subject in a new cmux workspace",
		Long: `Opens a new cmux workspace right under the caller, in the caller's group (no focus), and
launches Claude in it through the launcher, under the caller's cell, in the
subject's worktree (or --cwd), with --prompt as its first message. Binds the
workspace to the subject (source ptnew) as soon as cmux names it, before the
launch is verified: a launch that cannot be verified leaves it open and bound.
From a workspace bound to no subject, renames the calling tab "↗ <short title>"
of the subject launched — also when the launch cannot be verified; each spawn
overwrites the previous one (Claude Code's own title of that tab is then
hidden). A caller bound to a subject, readable or not by this cell, keeps its
title. The result says so (caller: line, caller_title); a failed rename is a
warning, never a failed spawn. --dry-run shows the rename it would make.

--cell launches under another cell. With --subject the cell must run the subject's
scope: a cell of your own scope, or, for the master with --cross-scope, one of the
subject's (without --cell, the cell of the same account there). Without --subject,
--cell must name another scope's cell and --cwd is required: the caller reads and binds
nothing of that scope, and the new session binds a subject only if its prompt leads it
to one (start it with your workflow command and what to work on); the calling
tab's title is left untouched (no subject to name it after). The --cwd must
lie under that cell's home: the longest cwd_home of claude-cells.yaml containing it
(see docs/CONFIG.md § claude-cells.yaml) must belong to the cell's scope.

Launch margin guard: the target cell's account must be under
` + claudeusage.CeilingHelp + `, read from the cells' cached readings like pigtail usage.
Over the margin, the launch moves to a free account of the same scope
("warning: launch margin: …" on stderr, "margin" in --json); when every account
of the scope is over it, the spawn is refused (exit 3) with the first release
time, unless --over-margin. Without a reading the launch goes on; when the
guard cannot measure (PIGTAIL_ROTATION=off) it says so and goes on. Every
decision but ok is traced (a margin line in spawns.jsonl, a launch-margin
journal row); --dry-run traces nothing.

--new-worktree creates a worktree from the --cwd main checkout of a registered pigtail
project (branch derived from the subject's short title, or --branch; required without
a subject), binds it to the subject so the next spawn finds it, and launches there. A
shared main checkout is refused without it. If the launch fails after the worktree
exists, the error names it and how to remove it (pigtail clean <id>).

The create runs the project's post-create hooks (minutes on some projects): an agent
runs --new-worktree in the background, or with a Bash timeout of at least 7 minutes
(420000 ms: 5 for the create, 2 for the launch) to keep the result inline. Past its
timeout, a call in Claude Code's Bash tool is moved to the background, not killed:
the create goes on, and the result and stderr arrive in the task's output and
completion notice. Wait for it and never re-run the spawn meanwhile: a second spawn on the
same subject is refused as launch in progress until the first is confirmed or fails, up to
5 min; only the short registry check can queue on the lock, and says so on stderr after 10 s.
Before the create, spawn prints on stderr the worktree path and branch, so a call
interrupted for real (Ctrl-C, kill) still says where to look: pigtail list, by that
branch; then pigtail clean <id>, or reuse it with --cwd.

Refuses, creating nothing: outside cmux or a cell; a subject of the other scope
without --cross-scope; no worktree and no --cwd; a shared main checkout; a caller
or cell default in bypassPermissions; no fresh record of the caller's current
permission mode (written by the permission-stamp hook for this very call);
another live session on the subject (any live Claude, busy or idle); a launch in progress
on the subject (a tab created and not yet confirmed, up to 5 min — or a --new-worktree
create still running), exit 1: wait for it, do not loop;
the 11th launch within 10 minutes (burst detector: respawns do not count, a refusal is exit 1
and never retried); an unreadable spawn registry; a Claude process whose environment
cannot be read; --cell of a scope other than the subject's; a subjectless spawn to
your own scope, without --cwd, or with a cwd outside the cell's home; --new-worktree
outside a project's main checkout or on a subject that already has a worktree.

How many sessions are alive never refuses a spawn. The burst detector does:
more than 10 launches within 10 minutes reads as a runaway, and the next one
is refused (exit 1, a rule refusal never retried; the message says when
the oldest launch leaves the window). Respawns do not count; a --relay launch does. Measured: at most 6
launches in any 10 minutes over 3–20 October.

--relay marks the new session as taking over from the caller (a chain passing
to its next subject): the caller's workspace, read from the calling Claude's
own environment, must be a live spawned workspace whose only Claude is the
caller; it is recorded as relay_from, and the caller should end at once
(with settings.yaml session.done_command when set). Otherwise --relay passes
nothing and says why (a warning).

With --relay (with or without --new-worktree), the confirmation wait adds up to
5 minutes (about 7 in all for the relay alone): run the spawn in the background,
or use the maximum Bash timeout, 600000 ms; past it the call moves to the
background and is not killed, as above.

The mode is inherited, never chosen here: the stricter of the caller's launch
mode and its current mode (a shift-tab to plan is honoured), always passed as
--permission-mode. A real spawn consumes the permission record. The record is
keyed by the Claude session id, which an orchestrator shares with its
subagents: a record written by any agent of the same session in the last 10
minutes can serve a spawn (it guards against mistakes, not a hostile agent).

Claude stops a session in a folder it was never told to trust on its "trust
this folder" dialog. Before launching, spawn pre-accepts trust for the
repository's main checkout (the key Claude Code itself writes) when the cwd is
a pigtail workspace and the target cell already trusts a directory above the
repository and strictly below $HOME ($HOME itself, /Users and / never count):
it inherits a grant the human already made, and says so. Otherwise it
writes nothing. spawn never answers the dialog itself: when it shows, spawn
warns (stderr, --json warnings, with why trust was not pre-accepted) and the
cmux sidebar shows "Blocked · trust folder" on the new workspace until the
session starts.

Needs the launcher contract: C_LAUNCH_PROTO=1
and C_CWD. An older launcher prints why in the new tab and spawn fails.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("watch") {
				var others []string
				cmd.Flags().Visit(func(f *pflag.Flag) {
					if f.Name != "watch" && cmd.InheritedFlags().Lookup(f.Name) == nil {
						others = append(others, "--"+f.Name)
					}
				})
				if len(others) > 0 {
					return spawnErr("--watch runs alone (got %s) — it is started by spawn itself, never by hand", strings.Join(others, " "))
				}
				// The watcher reports its lease with the cell scope that owns it
				// (the detached process inherits the spawn's environment).
				var lc *pigtail.Client // nil: the watcher reports nothing
				if wc, err := newScopedClient(); err == nil {
					lc = wc
				}
				return runSpawnWatcher(lc, watchRun)
			}
			if !cmd.Flags().Changed("prompt") { // checked here, after --watch: never MarkFlagRequired
				return errors.New(`required flag(s) "prompt" not set`)
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if req.CrossScope {
				c = c.WithCrossScopeWrite()
			}
			req.DryRun = dryRun
			for _, w := range spawnDeprecationWarnings(cmd.Flags().Changed("max-live"), os.Getenv) {
				fmt.Fprintln(cmd.ErrOrStderr(), w)
			}
			return runSpawn(c, req, dryRun, asJSON, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&req.SubjectID, "subject", "", "subject id (required, except a subjectless spawn with --cell into another scope's cell)")
	cmd.Flags().StringVar(&req.Cell, "cell", "", "cell to launch under (default: this session's; another scope's cell only without --subject)")
	cmd.Flags().StringVar(&req.Prompt, "prompt", "", "the new session's first message, one line (required)")
	cmd.Flags().StringVar(&req.Cwd, "cwd", "", "directory to launch in (default: the subject's live worktree); wins over the cell's default dir")
	cmd.Flags().BoolVar(&req.NewWorktree, "new-worktree", false, "create a worktree from the --cwd main checkout and launch in it (bound to the subject when there is one)")
	cmd.Flags().StringVar(&req.Branch, "branch", "", "branch for --new-worktree (default: derived from the subject's short title)")
	cmd.Flags().StringVar(&req.Name, "name", "", "workspace name (default: the subject's short title)")
	cmd.Flags().IntVar(&maxLiveIgnored, "max-live", 0, "ignored (deprecated): the burst detector replaced the live caps")
	_ = cmd.Flags().MarkHidden("max-live")
	cmd.Flags().BoolVar(&req.OverMargin, "over-margin", false, "launch even when the account is over the usage margin and no free account of its scope is left (decision forced; never reads usage with --cell)")
	cmd.Flags().BoolVar(&req.Relay, "relay", false, "the new session takes over from this one, itself a spawned session that ends right after (chain relay; recorded as relay_from)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "run every check, print what would be launched (as JSON with --json), create nothing")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result, the dry-run plan or the refusal as one JSON object")
	cmd.Flags().StringVar(&watchRun, "watch", "", "internal: the detached watcher of a pending launch (started by spawn itself)")
	_ = cmd.Flags().MarkHidden("watch")
	addCrossScopeFlag(cmd, &req.CrossScope)
	return cmd
}

// runSpawn plans without any lock (read only), then launches through the
// scoped reservation: the checks, the live set re-read and the reserve under
// one short lock; the launch itself outside it, closed on every exit.
func runSpawn(c *pigtail.Client, req spawnRequest, dry, asJSON bool, stdout, stderr io.Writer) error {
	reg := spawnreg.Open(spawnreg.DefaultPath())
	budget := spawnBaseBudget
	if req.NewWorktree && !dry {
		budget += devserver.NewWorkspaceTimeout // createWorktree's own budget, the rest keeps its 2 min
	}
	if req.Relay && !dry {
		budget += spawnConfirmTimeout // the --relay wait, from the tab's birth (a superset: RelayFrom is resolved later)
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	p, err := planSpawn(ctx, c, reg, req)
	if !dry {
		recordLaunchMargin(c, p.Rotation.Margin, req.SubjectID, err, stderr)
	}
	if err != nil {
		return spawnJSONError(stdout, err, dry, asJSON)
	}
	for _, w := range p.Rotation.Warnings {
		fmt.Fprintf(stderr, "warning: %s\n", w)
	}
	for _, w := range p.Rotation.MarginWarnings {
		fmt.Fprintf(stderr, "warning: %s\n", w)
	}
	if dry {
		if title, err := planCallerTitle(ctx, c, p); err != nil {
			p.CallerWarning = "calling workspace would not be renamed: cannot tell whether it is bound (" + cmux.OneLine(err.Error(), 240) + ")"
		} else {
			p.CallerTitle = title
		}
		if asJSON {
			return printSpawnPlanJSON(stdout, p)
		}
		printSpawnPlan(stdout, p)
		fmt.Fprintf(stdout, "  command   %s\n", p.Spec.Command)
		return nil
	}
	// The one admitting ask: after the plan (margin decided, relay resolved),
	// before the registry lock; refused, nothing is registered nor launched.
	leaseID := ""
	if spawnBudgetNeed(dry, p.RelayFrom, os.Getenv) == spawnNeedAsk {
		if leaseID, err = askSpawnBudget(ctx, c, stdout, asJSON); err != nil {
			return err
		}
	}
	var res spawnResult
	_, err = reg.WithReservation(spawnreg.ReserveRequest{
		SubjectID: p.Subject.ID, Origin: "cli", Window: spawnPendingWindow,
		Alive: pidAlive, ExcludeWorkspaceID: p.CallerWS,
		Check: func(entries []spawnreg.Entry) error {
			live, _, err := readLiveStrict(false)
			if err != nil {
				return err
			}
			if err := spawnGuards(entries, live, p.Bindings, p, time.Now().UTC()); err != nil {
				return err
			}
			if p.Subjectless {
				return nil
			}
			return sdkLiveGuard(ctx, c, p.Subject.ID, spawnErr)
		},
		Tabs: spawnListTabs,
		// A launch left open with a tab: the detached watcher confirms it or
		// records the failure. Not armed, it expires PendingWindow after its launch-ref.
		Handoff: func(id string, tab spawnreg.Tab, born time.Time) {
			run := spawnWatchRun{ID: id, SubjectID: p.Subject.ID, WorkspaceID: tab.ID, WorkspaceRef: tab.Ref,
				Cell: p.Cell.ID, ConfigDir: p.Cell.ConfigDir, Cwd: res.Cwd, RelayFrom: p.RelayFrom, Prompt: p.Prompt,
				Mode: p.Perm.Mode, Before: res.Before, Deadline: spawnWatchDeadline(born), LeaseID: leaseID}
			if run.Cwd == "" {
				run.Cwd = p.Cwd
			}
			if _, err := spawnArmWatch(reg, run, stderr); err != nil {
				w := fmt.Sprintf("⚠ launch watcher not armed (%v) — nothing will confirm %s: look at the tab; the launch stays in progress until %s",
					err, tab.Ref, born.Add(spawnPendingWindow).Local().Format("15:04"))
				res.ArmWarning = w
				res.Warnings = append(res.Warnings, w)
				fmt.Fprintln(stderr, w)
			}
		},
		Lock: func() (func(), error) {
			unlock, err := spawnRegistryLock(reg, stderr, spawnLockNotice)
			if err != nil {
				return nil, retryable(retryShort, spawnErr("%v", err))
			}
			return unlock, nil
		},
	}, func(l *spawnreg.Launch) error {
		var err error
		res, err = executeSpawn(ctx, c, l, p, stderr)
		return err
	})
	endSpawnLease(c, leaseID, err, res)
	var pe *spawnreg.PendingError
	switch {
	case errors.As(err, &pe):
		err = pendingRefusal(p.Subject.ID, pe.Entry)
	case errors.Is(err, spawnreg.ErrRegistryUnreadable):
		err = registryUnreadable(err)
	}
	if err != nil {
		if res.PreTrust.Outcome == claudeconfig.Written {
			fmt.Fprintf(stderr, "trust: pre-accepted %s in cell %s (inherited from %s) before the launch failed — the entry stays\n",
				res.PreTrust.Key, p.Cell.ID, res.PreTrust.Ancestor)
		}
		return spawnJSONFailure(stdout, err, asJSON, res.failureFields())
	}
	if res.Worktree != nil {
		p = p.withCwd(res.Worktree.Path) // executeSpawn got p by value: the daemon's path is the truth
	}
	return printSpawnResult(stdout, p, res, asJSON)
}

// spawnJSONError keeps --json output machine-readable on a refusal: one
// {"error": …} object on stdout, and the error still returned (exit 1, or 75 for a
// retryable refusal; the usual "Error:" line on stderr). A pre-trust result, when one was evaluated
// before the failure, is carried as "pretrust".
func spawnJSONError(w io.Writer, err error, dry, asJSON bool, pt ...claudeconfig.Result) error {
	if !asJSON {
		return err
	}
	obj := map[string]any{"error": err.Error(), "dry_run": dry}
	retryFields(obj, err)
	if len(pt) > 0 && pt[0].Outcome != "" {
		obj["pretrust"] = pt[0]
	}
	if b, mErr := json.Marshal(obj); mErr == nil {
		_, _ = fmt.Fprintln(w, string(b))
	}
	return err
}

// spawnJSONFailure is spawnJSONError for a real spawn that failed after
// creating a workspace: the error object also carries fields.
func spawnJSONFailure(w io.Writer, err error, asJSON bool, fields map[string]any) error {
	if !asJSON {
		return err
	}
	obj := map[string]any{"error": err.Error(), "dry_run": false}
	retryFields(obj, err)
	for k, v := range fields {
		obj[k] = v
	}
	if b, mErr := json.Marshal(obj); mErr == nil {
		_, _ = fmt.Fprintln(w, string(b))
	}
	return err
}

// unreadableWarning is the dry-run warning for Claude processes whose
// environment cannot be read (a real spawn refuses on them).
func unreadableWarning(pids []int) string {
	return fmt.Sprintf("%d Claude process(es) have an unreadable environment (pid %v): a real spawn would refuse", len(pids), pids)
}

// printSpawnPlanJSON is --dry-run --json: what would be launched, as one object.
func printSpawnPlanJSON(w io.Writer, p spawnPlan) error {
	warnings := []string{}
	if len(p.Unreadable) > 0 {
		warnings = append(warnings, unreadableWarning(p.Unreadable))
	}
	if p.RelayNothing != "" {
		warnings = append(warnings, "--relay passed nothing: "+p.RelayNothing)
	}
	if p.CallerWarning != "" {
		warnings = append(warnings, p.CallerWarning)
	}
	unreadable := p.Unreadable
	if unreadable == nil {
		unreadable = []int{}
	}
	obj := map[string]any{
		"dry_run": true, "subject_id": p.Subject.ID, "subject_title": p.Subject.Title, "scope": p.Subject.Scope, "subjectless": p.Subjectless,
		"cell": p.Cell.ID, "config_dir": p.Cell.ConfigDir, "cell_origin": p.CellOrigin, "cwd": p.Cwd,
		"permission_mode": p.Perm.Mode, "launch_mode": p.Perm.Launch, "launch_mode_origin": p.Perm.Origin, "current_mode": p.Perm.Current,
		"prompt": p.Prompt, "group_ref": p.GroupRef, "caller_workspace_ref": p.CallerWSRef,
		"live_spawned": p.LiveSpawned, "launches_recent": p.Burst.Recent, "launches_max": spawnBurstMax,
		"launches_window_s": int(spawnBurstWindow / time.Second), "relay_from": p.RelayFrom, "caller_title": p.CallerTitle,
		"cmux_argv": cmux.NewWorkspaceArgs(p.Spec), "command": p.Spec.Command,
		"unreadable_pids": unreadable, "warnings": warnings, "rotation": p.Rotation.json(),
		"margin": p.Rotation.marginJSON(),
	}
	if pw := p.NewWorktree; pw != nil {
		obj["new_worktree"] = map[string]any{"project_id": pw.Project.ID, "branch": pw.Branch, "path": pw.Path, "bind_subject": pw.BindTo}
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

var (
	errSpawnNotSeen     = errors.New("launch not observed")
	errSpawnWrongConfig = errors.New("launched under the wrong account")
	errSpawnWrongCwd    = errors.New("launched in the wrong directory")
)

type spawnResult struct {
	WorkspaceRef, WorkspaceID string
	PID                       int
	Bound                     bool
	Warnings                  []string            // every ⚠ line printed, in order
	CallerTitle               string              // title posed on an unbound caller (D1), "" if none
	PreTrust                  claudeconfig.Result // folder trust pre-accepted before launch, or why not
	Worktree                  *createdWorktree    // --new-worktree: the worktree created, nil otherwise
	State                     string              // "launched", or "pending": the tab is open, its Claude not seen yet
	LaunchID                  string              // the reservation's id
	Tail                      string              // pending: the tab's screen (text only)
	Cwd                       string              // where Claude was launched: the created worktree's path, else the plan's
	Before                    []int               // Claude PIDs listed before the create (the watcher skips them)
	ArmWarning                string              // pending: the watcher could not be armed
	BindNote                  string              // pending, bind refused: the cause and the way out
	TabCreated                bool                // a tab exists or may exist (Launch.Create made one, or failed in a way that may have)
}

// failureFields is what a failed real spawn still reports in --json: the
// folder trust pre-accepted before the failure, and once a workspace exists,
// where it is, whether it is bound, the warnings.
func (r spawnResult) failureFields() map[string]any {
	fields := map[string]any{}
	if r.PreTrust.Outcome != "" {
		fields["pretrust"] = r.PreTrust
	}
	if r.WorkspaceRef != "" {
		fields["workspace_ref"], fields["workspace_id"] = r.WorkspaceRef, r.WorkspaceID
		fields["bound"], fields["caller_title"] = r.Bound, r.CallerTitle
		fields["warnings"] = append([]string{}, r.Warnings...)
	}
	if r.Worktree != nil { // a cmux failure leaves a worktree but no workspace
		fields["new_worktree"] = *r.Worktree
	}
	return fields
}

// verifySpawn is observeSpawn bounded by spawnVerifyTimeout.
func verifySpawn(wsID string, p spawnPlan, before map[int]bool) (int, error) {
	return observeSpawn(wsID, p.Cell.ConfigDir, p.Cwd, before, spawnVerifyTimeout)
}

// observeSpawn waits up to timeout for a Claude process absent from before,
// running in the workspace, then checks its account and its directory. A wait
// for an observable state, bounded — never a sleep.
func observeSpawn(wsID, cfgDir, cwd string, before map[int]bool, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for {
		procs, listErr := claudeProcs()
		for _, pr := range procs {
			if before[pr.PID] {
				continue
			}
			env, err := processEnviron(pr.PID)
			if err != nil {
				continue
			}
			if ws, _ := cmux.EnvValue(env, cmux.EnvWorkspaceID); !strings.EqualFold(ws, wsID) {
				continue
			}
			cfg, _ := cmux.EnvValue(env, "CLAUDE_CONFIG_DIR")
			if canonicalPath(cfg) != canonicalPath(cfgDir) {
				return 0, fmt.Errorf("%w: pid %d has CLAUDE_CONFIG_DIR=%q, not %s", errSpawnWrongConfig, pr.PID, cfg, cfgDir)
			}
			pwd, _ := cmux.EnvValue(env, "PWD")
			if canonicalPath(pwd) != canonicalPath(cwd) {
				return 0, fmt.Errorf("%w: pid %d runs in %s, not %s — the launcher in its tab ignored C_CWD, or its shell changed directory", errSpawnWrongCwd, pr.PID, pwd, cwd)
			}
			return pr.PID, nil
		}
		if time.Now().After(deadline) {
			detail := "no Claude process appeared in it"
			if listErr != nil {
				detail = "process listing failed: " + listErr.Error()
			}
			return 0, fmt.Errorf("%w after %s: %s", errSpawnNotSeen, timeout, detail)
		}
		time.Sleep(spawnVerifyInterval)
	}
}

// trustMarkers are lower-case fragments of Claude Code's folder-trust dialog
// (its wording changed across versions), readyMarkers of its normal input
// screen, which ends the watch early.
var (
	trustMarkers = []string{"do you trust the files in this folder", "is this a project you created or one you trust", "trust this folder"}
	readyMarkers = []string{"shift+tab to cycle", "? for shortcuts", "esc to interrupt"}
)

// trustDialogShown reports whether a screen shows the folder-trust dialog.
func trustDialogShown(screen string) bool {
	l := strings.ToLower(screen)
	for _, m := range trustMarkers {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

// waitsOnTrust watches the new tab for spawnTrustWindow once its Claude is
// seen: a never-opened folder stops the session on the trust dialog. spawn
// never answers the dialog (pre-trust happens before launch and only inherits
// a human's grant on an ancestor) but must not leave it silent. An unreadable
// screen is no detection.
func waitsOnTrust(surface string) bool {
	if surface == "" {
		return false
	}
	deadline := time.Now().Add(spawnTrustWindow)
	for {
		if sc, err := spawnReadScreen(surface); err == nil {
			if trustDialogShown(sc) {
				return true
			}
			l := strings.ToLower(sc)
			for _, m := range readyMarkers {
				if strings.Contains(l, m) {
					return false
				}
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(spawnVerifyInterval)
	}
}

// firstSurface is the new workspace's first surface ref, "" when cmux cannot
// say (no screen to watch).
func firstSurface(wsRef string) string {
	refs, err := spawnSurfacesIn(wsRef)
	if err != nil || len(refs) == 0 {
		return ""
	}
	return refs[0]
}

// screenTail returns the last non-empty lines of the new tab: where the
// launcher prints its own reason (too old, C_CWD missing, …).
func screenTail(wsRef string) string {
	refs, err := spawnSurfacesIn(wsRef)
	if err != nil || len(refs) == 0 {
		return "(screen unreadable)"
	}
	s, err := spawnReadScreen(refs[0])
	if err != nil {
		return "(screen unreadable: " + err.Error() + ")"
	}
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, strings.TrimRight(l, " "))
		}
	}
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return strings.Join(lines, "\n    ")
}

// bindSpawned binds the new workspace to the subject (source ptnew). On a
// refusal it returns the attach error untouched and the way out.
func bindSpawned(ctx context.Context, c *pigtail.Client, p spawnPlan, wsID string) (string, error) {
	if _, err := c.AttachCmuxWorkspace(ctx, p.Subject.ID, pigtail.AttachCmuxWorkspaceRequest{
		CmuxWorkspaceID: wsID, Source: pigtail.CmuxSourcePtnew,
	}); err != nil {
		return bindHint(p, wsID, err), err
	}
	return "", nil
}

// bindHint is the way out of a refused bind. A workspace already bound to
// another subject is a 409 for a plain attach: the hint names the conflict and
// gives the explicit replace (sub_x000000000000020: a hint that ends in a 409
// is no way out). Any other cause keeps today's wording.
func bindHint(p spawnPlan, wsID string, err error) string {
	cross := ""
	// The opt-in is about the CALLER's scope: the target cell's always equals
	// the subject's (invariant), so comparing it would always drop the flag.
	if p.Subject.Scope != p.CallerCell.Scope {
		cross = " --cross-scope"
	}
	cmd := fmt.Sprintf("pigtail subject attach %s --workspace %s --source ptnew", p.Subject.ID, wsID)
	var ae *pigtail.APIError
	if errors.As(err, &ae) && ae.Code == pigtail.ErrConflict {
		return "it is bound to another subject — to replace it: " + cmd + " --replace" + cross
	}
	return "by hand: " + cmd + cross
}

// callerTitlePrefix marks a tab that launched a session elsewhere (D1).
const callerTitlePrefix = "↗ "

// callerTitle is the title an unbound caller takes: the launched subject's
// short title on one line — never --name (D1).
func callerTitle(sub pigtail.Subject) string {
	return callerTitlePrefix + strings.Join(strings.Fields(pigtail.ShortTitle(sub)), " ")
}

// planCallerTitle decides the title the calling tab takes (D1): "" outside
// cmux, when the caller is bound — the sidebar loop owns its title —, or on a
// subjectless spawn (D6: no subject to title it after, and no lookup). An
// error: the lookup failed and the caller may be bound.
func planCallerTitle(ctx context.Context, c *pigtail.Client, p spawnPlan) (string, error) {
	if p.Subjectless {
		return "", nil
	}
	if p.CallerWS == "" {
		return "", nil
	}
	bound, err := spawnCallerBound(ctx, c, p.CallerWS)
	if err != nil || bound {
		return "", err
	}
	return callerTitle(p.Subject), nil
}

// renameCaller titles an unbound calling workspace after the subject it just
// launched, so the tab no longer shows Claude Code's title drawn from the
// spawn command (D1). Each spawn overwrites the previous one (D5). Never
// fails the spawn: every failure is a warning (D3). Returns the title posed.
func renameCaller(ctx context.Context, c *pigtail.Client, p spawnPlan, warn func(string, ...any)) string {
	title, err := planCallerTitle(ctx, c, p)
	if err != nil {
		warn("⚠ calling workspace not renamed: cannot tell whether it is bound (%s)", cmux.OneLine(err.Error(), 240))
		return ""
	}
	if title == "" {
		return ""
	}
	if err := spawnRenameWorkspace(p.CallerWS, title); err != nil {
		warn("⚠ calling workspace not renamed to %q (%s) — the spawn itself succeeded", title, cmux.OneLine(err.Error(), 240))
		return ""
	}
	return title
}

func executeSpawn(ctx context.Context, c *pigtail.Client, l *spawnreg.Launch, p spawnPlan, stderr io.Writer) (spawnResult, error) {
	var r spawnResult
	// One record, one real spawn, consumed once every guard has passed: a later
	// call that escaped the hook's pre-filter finds none and is refused.
	if err := spawnConsumeStamp(p.CallerSession); err != nil {
		return r, spawnErr("cannot consume the permission record of session %s: %v — nothing launched", p.CallerSession, err)
	}
	before, err := claudePIDs()
	if err != nil {
		return r, retryable(retryShort, spawnErr("list Claude processes: %v — nothing launched", err))
	}
	for pid := range before {
		r.Before = append(r.Before, pid)
	}
	slices.Sort(r.Before)
	r.LaunchID = l.ID
	// failAfter ends every failure once a worktree exists with where it stays.
	failAfter := func(msg string) error {
		if r.Worktree != nil {
			msg += r.Worktree.note(p.Subject.ID)
		}
		return spawnErr("%s", msg)
	}
	if p.NewWorktree != nil {
		announceCreate(stderr, p.NewWorktree) // T15: before the request, never after
		w, err := createWorktree(ctx, c, p.NewWorktree)
		if err != nil {
			return r, err
		}
		r.Worktree = &w
		if w.BindErr != "" { // one line, printed and recorded alike (spawnResult.Warnings = every ⚠ printed)
			msg := w.bindWarning(p.NewWorktree.BindTo)
			r.Warnings = append(r.Warnings, msg)
			fmt.Fprintln(stderr, msg)
		}
		p = p.withCwd(w.Path) // the daemon's path is the truth, whatever was predicted
	}
	r.Cwd = p.Cwd
	r.PreTrust = spawnPreTrust(ctx, c, p.Cell.ConfigDir, p.Cwd)
	if r.PreTrust.Outcome == claudeconfig.Failed {
		w := fmt.Sprintf("⚠ folder trust not pre-accepted (%s) — the session may stop on \"trust this folder\"", r.PreTrust.Reason)
		r.Warnings = append(r.Warnings, w)
		fmt.Fprintln(stderr, w)
	}
	// One create per launch: the tab is the one cmux names, else the single new
	// tab of the before/after listing (a failed resolve no longer loses it).
	tab, err := l.Create(func() (spawnreg.Tab, error) {
		ref, err := spawnNewWorkspace(p.Spec)
		if err != nil {
			return spawnreg.Tab{}, err
		}
		id, rerr := resolveCmuxRef(ref)
		return spawnreg.Tab{ID: strings.ToUpper(id), Ref: ref}, rerr
	})
	// A tab exists unless the create left none (cmux unreachable or refusing).
	r.TabCreated = err == nil || (!errors.Is(err, spawnreg.ErrCmuxUnreachable) && !errors.Is(err, spawnreg.ErrCmuxCreateFailed))
	if err != nil {
		nothing := errors.Is(err, spawnreg.ErrCmuxUnreachable) || errors.Is(err, spawnreg.ErrCmuxCreateFailed)
		if !nothing { // several new tabs, or the listing failed after the create: a tab may exist
			return r, failAfter(fmt.Sprintf("cmux new-workspace: %v — look at cmux; nothing bound", err))
		}
		if r.Worktree == nil { // with a worktree created, a retry would hit R17
			return r, retryable(retryArm, failAfter(fmt.Sprintf("cmux new-workspace failed: %v — nothing launched", err)))
		}
		return r, failAfter(fmt.Sprintf("cmux new-workspace failed: %v — nothing launched", err))
	}
	ref := tab.Ref
	r.WorkspaceRef, r.WorkspaceID = tab.Ref, tab.ID
	entry := spawnreg.Entry{SubjectID: p.Subject.ID, WorkspaceID: r.WorkspaceID,
		WorkspaceRef: ref, Cell: p.Cell.ID, Cwd: p.Cwd, Prompt: p.Prompt, Mode: p.Perm.Mode, Result: spawnreg.ResultLaunched, RelayFrom: p.RelayFrom}
	warn := func(format string, a ...any) {
		w := fmt.Sprintf(format, a...)
		r.Warnings = append(r.Warnings, w)
		fmt.Fprintln(stderr, w)
	}
	// Bound as soon as cmux names it: a launch that cannot be verified still
	// leaves a workspace of this subject, and the sidebar must say whose.
	// A subjectless spawn (D1) binds nothing: the new session binds itself.
	var hint string
	var bindErr error
	if !p.Subjectless {
		hint, bindErr = bindSpawned(ctx, c, p, r.WorkspaceID)
		r.Bound = bindErr == nil
	}
	r.CallerTitle = renameCaller(ctx, c, p, warn)

	pid, verr := verifySpawn(r.WorkspaceID, p, before)
	if errors.Is(verr, errSpawnNotSeen) && p.RelayFrom == "" {
		// Not seen yet: pending, left open — WithReservation's Handoff arms
		// the watcher. The report the unverified launch gave stays.
		if p.RelayNothing != "" {
			warn("⚠ --relay passed nothing: %s", p.RelayNothing)
		}
		if bindErr != nil {
			r.BindNote = fmt.Sprintf("%v — %s", bindErr, hint)
			warn("⚠ not bound to the subject (%s)", r.BindNote)
		}
		r.State, r.Tail = "pending", screenTail(ref)
		return r, nil
	}
	if errors.Is(verr, errSpawnNotSeen) {
		// --relay: no watcher — the caller ends right after; wait here up to
		// the confirmation deadline.
		rest := time.Until(spawnWatchDeadline(l.Born())).Round(time.Second)
		fmt.Fprintf(stderr, "still launching… waiting for Claude in %s (up to %s)\n", ref, rest)
		pid, verr = observeSpawn(r.WorkspaceID, p.Cell.ConfigDir, p.Cwd, before, time.Until(spawnWatchDeadline(l.Born())))
	}
	if verr != nil {
		// Seen wrong (account, directory), or not seen by the --relay
		// deadline: closed failed, the tab named.
		recErr := l.Fail(verr.Error(), tab)
		state := ", bound to " + p.Subject.ID
		switch {
		case p.Subjectless:
			state = "" // nothing was bound: « left open » alone
		case bindErr != nil:
			state = fmt.Sprintf(", not bound to %s (%v — %s)", p.Subject.ID, bindErr, hint)
		}
		msg := fmt.Sprintf("%s created, but %v\n  the tab shows:\n    %s\n  left open%s", ref, verr, screenTail(ref), state)
		if recErr != nil {
			msg += fmt.Sprintf("; registry not written: %v", recErr)
		}
		if r.CallerTitle != "" {
			msg += fmt.Sprintf("\n  calling tab %s renamed %q", p.CallerWSRef, r.CallerTitle)
		}
		return r, failAfter(msg)
	}
	r.PID, r.State = pid, spawnreg.ResultLaunched
	if p.RelayNothing != "" {
		warn("⚠ --relay passed nothing: %s", p.RelayNothing)
	}
	entry.PID = pid
	if err := l.Launched(entry); err != nil {
		warn("⚠ session running but not recorded in the spawn registry (%v) — the burst detector will not count this launch", err)
	}
	if bindErr != nil {
		warn("⚠ not bound to the subject (%v) — %s", bindErr, hint) // today's line for a non-conflict cause
	}
	if waitsOnTrust(firstSurface(ref)) {
		note := ""
		switch {
		case r.PreTrust.Outcome == claudeconfig.Written:
			note = " (pre-accepted " + r.PreTrust.Key + " but Claude still asks)"
		case r.PreTrust.Reason != "" && r.PreTrust.Outcome != claudeconfig.Failed:
			note = " (not pre-accepted: " + r.PreTrust.Reason + ")"
		}
		warn("⚠ the new session waits on \"trust this folder\" in %s — accept it there%s", ref, note)
		if err := spawnMarkTrust(ctx, c, r.WorkspaceID, r.PID); err != nil {
			// One line: an older daemon's 404 body ends with a newline (R1).
			warn("⚠ the trust-folder pill could not be posted on %s (%s) — the sidebar will not show it: accept it there by hand", ref, strings.TrimSpace(err.Error())) // D21·C10
		}
	}
	return r, nil
}

func printSpawnResult(w io.Writer, p spawnPlan, r spawnResult, asJSON bool) error {
	if asJSON {
		b, err := json.Marshal(map[string]any{
			"workspace_ref": r.WorkspaceRef, "workspace_id": r.WorkspaceID, "subject_id": p.Subject.ID, "subjectless": p.Subjectless,
			"cell": p.Cell.ID, "cwd": p.Cwd, "pid": r.PID, "permission_mode": p.Perm.Mode, "prompt": p.Prompt,
			"bound": r.Bound, "relay_from": p.RelayFrom, "caller_title": r.CallerTitle, "warnings": append([]string{}, r.Warnings...),
			"pretrust": r.PreTrust, "new_worktree": r.Worktree, "rotation": p.Rotation.json(),
			"margin": p.Rotation.marginJSON(),
			"state":  r.State, "launch_id": r.LaunchID,
		})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}
	if r.State == "pending" {
		printSpawnPending(w, p, r)
		return nil
	}
	if p.Subjectless {
		fmt.Fprintf(w, "✓ fresh session (no subject — it binds itself) → %s, cell %s, cwd %s, pid %d, mode %s\n  prompt: %s\n",
			r.WorkspaceRef, p.Cell.ID, p.Cwd, r.PID, p.Perm.Mode, p.Prompt)
	} else {
		fmt.Fprintf(w, "✓ fresh session on %s (%s) → %s, cell %s, cwd %s, pid %d, mode %s\n  prompt: %s\n",
			p.Subject.ID, pigtail.ShortTitle(p.Subject), r.WorkspaceRef, p.Cell.ID, p.Cwd, r.PID, p.Perm.Mode, p.Prompt)
	}
	if r.Worktree != nil {
		fmt.Fprintf(w, "  worktree: created %s · branch %s · %s\n", r.Worktree.ID, r.Worktree.Branch, r.Worktree.state(p.Subject.ID))
	}
	if r.PreTrust.Outcome == claudeconfig.Written {
		fmt.Fprintf(w, "  trust: pre-accepted %s in cell %s (inherited from %s)\n", r.PreTrust.Key, p.Cell.ID, r.PreTrust.Ancestor)
	}
	if r.CallerTitle != "" {
		fmt.Fprintf(w, "  caller: %s renamed %q\n", p.CallerWSRef, r.CallerTitle)
	}
	if p.RelayFrom != "" && r.State == spawnreg.ResultLaunched {
		end := "end this one now"
		if d := waittype.DoneHint(); d != "" {
			end += " (" + d + ")"
		}
		fmt.Fprintf(w, "  relay: %s takes over from this session — %s\n", r.WorkspaceRef, end)
	}
	return nil
}

// printSpawnPending is the text of a launch left open, its Claude not seen
// yet: where, whether it is bound, the tab's screen, the caller renamed.
func printSpawnPending(w io.Writer, p spawnPlan, r spawnResult) {
	state := ""
	switch {
	case p.Subjectless:
	case r.Bound:
		state = ", bound to " + p.Subject.ID
	default:
		state = fmt.Sprintf(", not bound to %s (%s)", p.Subject.ID, r.BindNote)
	}
	fmt.Fprintf(w, "… launch pending in %s — left open%s\n", r.WorkspaceRef, state)
	fmt.Fprintf(w, "  Claude not seen yet in the tab: a watcher confirms it or records the failure within %s of its creation\n", spawnConfirmTimeout)
	if r.Tail != "" {
		fmt.Fprintf(w, "  the tab shows:\n    %s\n", r.Tail)
	}
	if r.Worktree != nil {
		fmt.Fprintf(w, "  worktree: created %s · branch %s · %s\n", r.Worktree.ID, r.Worktree.Branch, r.Worktree.state(p.Subject.ID))
	}
	if r.ArmWarning != "" {
		fmt.Fprintf(w, "  %s\n", r.ArmWarning)
	}
	if r.CallerTitle != "" {
		fmt.Fprintf(w, "  calling tab %s renamed %q\n", p.CallerWSRef, r.CallerTitle)
	}
}
