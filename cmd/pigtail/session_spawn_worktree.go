package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	gitbackend "github.com/aloysbr/pigtail/internal/backend/git"
	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// spawnCreateWorktree is POST /workspaces/new (a seam: the create tests replace it).
var spawnCreateWorktree = func(ctx context.Context, c *pigtail.Client, req pigtail.NewWorkspaceRequest) (*pigtail.Workspace, error) {
	return c.New(ctx, req)
}

// spawnCreatePhase binds a created worktree to its subject (a seam: the bind-failure test).
var spawnCreatePhase = func(ctx context.Context, c *pigtail.Client, subjectID string, ph pigtail.SubjectPhase) error {
	_, err := c.CreatePhase(ctx, subjectID, ph)
	return err
}

// spawnLockNotice is T16's delay before « waiting for the spawn registry lock »
// (a var so the lock test shortens it).
var spawnLockNotice = 10 * time.Second

// Seams over the git backend: the only git spawn runs (never exec'd here).
var (
	spawnMainCheckoutRoot = gitbackend.MainCheckoutRoot
	spawnLinkedWorktrees  = gitbackend.LinkedWorktrees // R19, D3
)

// plannedWorktree is the worktree --new-worktree would create (D2): the
// project whose main checkout is the --cwd, the exact branch (sent as
// branch_override) and the path the backend will use for it.
type plannedWorktree struct {
	Project pigtail.Project
	Branch  string
	Path    string // predicted (WorktreePathFor); the daemon's answer wins
	Intent  string
	BindTo  string // subject id, "" = unbound (subjectless, D1)
}

// createdWorktree is what the daemon created, and whether the subject phase
// that binds it was written.
type createdWorktree struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	Bound   bool   `json:"bound"`
	BindErr string `json:"-"`
}

// planWorktree checks the --cwd is a main checkout of a registered project
// (R20, R19) and computes the branch and path the create will use (T5, T6).
func planWorktree(ctx context.Context, c *pigtail.Client, cwd string, sub *pigtail.Subject, branch string, cells *claudeprofile.Table, target claudeprofile.Cell) (*plannedWorktree, error) {
	root, main, linked, err := spawnMainCheckoutRoot(cwd)
	if err != nil {
		return nil, spawnErr("%v", err)
	}
	if !main {
		what := "not in a git repository"
		if linked {
			what = "a linked worktree"
		}
		return nil, spawnErr("--new-worktree needs --cwd in a repository's main checkout; %s is %s — drop --new-worktree to launch in it, or point --cwd at the main checkout", cwd, what)
	}
	projs, err := c.Projects(ctx)
	if err != nil {
		return nil, spawnErr("list pigtail projects: %v", err)
	}
	var proj *pigtail.Project
	for i := range projs {
		if canonicalPath(gitbackend.MainWorktreePath(&projs[i])) == canonicalPath(root) {
			proj = &projs[i]
			break
		}
	}
	if proj == nil {
		return nil, notAProject(root, cells, target)
	}
	pw := &plannedWorktree{Project: *proj}
	if sub != nil {
		pw.Intent, pw.BindTo = pigtail.ShortTitle(*sub), sub.ID
	} else {
		pw.Intent = branch
	}
	pw.Branch = gitbackend.DeriveBranch(proj, pw.Intent, branch)
	pw.Path = gitbackend.WorktreePathFor(proj, pw.Branch)
	return pw, nil
}

// notAProject is R19 (D3): the repo's linked worktrees the caller can pass as
// --cwd, kept only under a home of the target cell's scope (T14), at most 5
// then "+N more"; none → say so and name the next step.
func notAProject(root string, cells *claudeprofile.Table, target claudeprofile.Cell) error {
	const head = "%s is not the main worktree of a registered pigtail project"
	if !cells.HasHomes() { // no H1: the T14 filter cannot tell which paths belong — R13
		return spawnErr("claude-cells.yaml carries no cwd_home — set cwd_home on its cells (see docs/CONFIG.md § claude-cells.yaml), then retry")
	}
	all, err := spawnLinkedWorktrees(root)
	if err != nil {
		return spawnErr(head+"; its worktrees could not be listed (%v) — run git -C %s worktree list, then pass one with --cwd", root, err, root)
	}
	var mine []string
	for _, p := range all {
		if h, herr := cells.HomeFor(p); herr == nil && h.Scope == target.Scope {
			mine = append(mine, p)
		}
	}
	if len(mine) == 0 {
		return spawnErr(head+", and it has no linked worktree under cell %s's home — create one (git -C %s worktree add <dir> -b <branch>), or register the repo with pigtail init, then pass the worktree with --cwd", root, target.ID, root)
	}
	shown, more := mine, ""
	if len(mine) > 5 {
		shown, more = mine[:5], fmt.Sprintf(" (+%d more)", len(mine)-5)
	}
	return spawnErr(head+", and --new-worktree only creates worktrees through pigtail — pass one of its worktrees with --cwd: %s%s", root, strings.Join(shown, ", "), more)
}

// createWorktree asks the daemon for the worktree, then binds it to the
// subject through a phase (T7). It prints nothing: executeSpawn prints and
// records the ⚠ of a failed bind.
func createWorktree(ctx context.Context, c *pigtail.Client, pw *plannedWorktree) (createdWorktree, error) {
	// POST /workspaces/new waits for the project's post-create hooks before
	// answering (bounded by devserver.HooksTimeout on the daemon; a large monorepo takes
	// minutes): pigtail new's budget, not the run's.
	cctx, cancel := context.WithTimeout(ctx, devserver.NewWorkspaceTimeout)
	defer cancel()
	ws, err := spawnCreateWorktree(cctx, c, pigtail.NewWorkspaceRequest{ProjectID: pw.Project.ID, Intent: pw.Intent, BranchOverride: pw.Branch})
	if err != nil {
		return createdWorktree{}, createFailure(err, pw)
	}
	w := createdWorktree{ID: ws.ID, Path: ws.Path, Branch: pw.Branch}
	if pw.BindTo == "" {
		return w, nil
	}
	if err := spawnCreatePhase(ctx, c, pw.BindTo, pigtail.SubjectPhase{Kind: pigtail.PhaseImplement, State: pigtail.PhaseActive, WorkspaceID: ws.ID}); err != nil {
		w.BindErr = strings.TrimSpace(err.Error())
		return w, nil // executeSpawn prints and records bindWarning()
	}
	w.Bound = true
	return w, nil
}

// createFailure words a failed POST /workspaces/new (§ How each failure shows).
// Only the codes the daemon answers BEFORE anything exists say « not created »:
// FreezeMW's 423, the handler's 400/404 checks, and Create's CONFLICT (collision
// guard) / PORT_EXHAUSTED (allocatePorts) — both returned before git worktree
// add — plus PROJECT_NOT_INITIALIZED (mapped to 409, no producer today).
// Everything else — a timeout, a transport error, BACKEND_ERROR / INTERNAL
// (a post-create hook or persist failure comes after the worktree exists) —
// may have left a worktree: say where, never « pass --branch ».
func createFailure(err error, pw *plannedWorktree) error {
	e := strings.TrimSpace(err.Error())
	var api *pigtail.APIError
	if errors.As(err, &api) {
		switch api.Code {
		case pigtail.ErrMaintenanceFrozen:
			return spawnErr("worktree not created (%s) — nothing launched; retry once the maintenance is lifted", e)
		case pigtail.ErrConflict:
			return spawnErr("worktree not created (%s) — nothing launched; pass --branch <another name>, or --cwd the existing worktree", e)
		case pigtail.ErrPortExhausted:
			return spawnErr("worktree not created (%s) — nothing launched; free ports by cleaning merged worktrees (pigtail clean --merged --dry-run, then pigtail clean --merged), then retry", e)
		case pigtail.ErrProjectNotInit:
			return spawnErr("worktree not created (%s) — nothing launched; initialise the project (pigtail init in %s), then retry", e, pw.Project.RootPath)
		case pigtail.ErrInvalidRequest, pigtail.ErrProjectNotFound:
			return spawnErr("worktree not created (%s) — nothing launched; re-plan with --dry-run, then retry", e)
		}
	}
	return spawnErr("worktree creation did not complete (%s) — nothing launched; a worktree may exist at %s (branch %s): find it by that branch in pigtail list and remove it with pigtail clean <id>, or reuse it with --cwd %s",
		e, pw.Path, pw.Branch, pw.Path)
}

// announceCreate is T15: written BEFORE the request, so a call its caller kills
// mid-create (Ctrl-C, kill — the daemon then stops after persistWorkspace) still
// named the worktree. A Bash-tool timeout does not kill it: the tool backgrounds
// the call, and this line lands in the task's output file. stderr in every mode: stdout stays one JSON object
// under --json. os.Stderr is unbuffered: nothing to flush.
func announceCreate(stderr io.Writer, pw *plannedWorktree) {
	fmt.Fprintf(stderr, "creating worktree %s (branch %s) — post-create hooks can take minutes; if this call is interrupted, find it by that branch in pigtail list, then pigtail clean <id>, or reuse it with --cwd %s\n",
		pw.Path, pw.Branch, pw.Path)
}

// bindWarning is the ⚠ line of a failed bind (§ How each failure shows).
func (w createdWorktree) bindWarning(subject string) string {
	return fmt.Sprintf("⚠ worktree %s (%s) created but not bound to %s (%s) — the next spawn will not find it: pass --cwd %s", w.ID, w.Path, subject, w.BindErr, w.Path)
}

// note ends every failure after the worktree exists: where it stays and how
// to reuse or remove it.
func (w createdWorktree) note(subject string) string {
	b := "unbound"
	if w.Bound {
		b = "bound to " + subject
	}
	return fmt.Sprintf(" — worktree %s stays at %s (%s): reuse it with --cwd %s, or remove it: pigtail clean %s", w.ID, w.Path, b, w.Path, w.ID)
}

// state is the result line's binding: bound, unbound (no subject), or why not.
func (w createdWorktree) state(subject string) string {
	switch {
	case w.Bound:
		return "bound to " + subject
	case w.BindErr != "":
		return "NOT bound (" + w.BindErr + ")"
	}
	return "unbound"
}

// withCwd moves the launch to cwd: Spec.Cwd and the launcher's own dir
// (launchEnv's PIGTAIL_SPAWN_CWD — the launcher reads it, not Spec.Cwd). The
// env slice is copied, never mutated in place.
func (p spawnPlan) withCwd(cwd string) spawnPlan {
	p.Cwd = cwd
	p.Spec.Cwd = cwd
	env := make([]string, 0, len(p.Spec.Env))
	for _, e := range p.Spec.Env {
		if strings.HasPrefix(e, envSpawnCwd+"=") || strings.HasPrefix(e, envSpawnPrompt+"=") {
			continue
		}
		env = append(env, e)
	}
	env = append(env, launchEnv(cwd, p.Prompt)...)
	p.Spec.Env = env
	return p
}

// lockWithNotice takes the registry lock; past `after`, it says once on stderr
// that the spawn waits (T16). The notice never races a later write: the timer is
// stopped, or the running notice is waited for, before returning.
func lockWithNotice(reg *spawnreg.Registry, stderr io.Writer, after time.Duration) (func(), error) {
	done := make(chan struct{})
	t := time.AfterFunc(after, func() {
		defer close(done)
		fmt.Fprintln(stderr, "session spawn: waiting for the spawn registry lock — another spawn or respawn is checking the registry (a few seconds); nothing is created while waiting: retry in a moment")
	})
	unlock, err := reg.Lock()
	if !t.Stop() {
		<-done
	}
	return unlock, err
}
