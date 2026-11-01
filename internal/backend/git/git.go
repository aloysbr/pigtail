// Package git implements the Backend interface using git worktrees and branches.
package git

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/envlocal"
	"github.com/aloysbr/pigtail/internal/ports"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Backend is the git implementation of backend.Backend.
type Backend struct {
	allocator *ports.Allocator
	logf      func(format string, args ...any)

	// countWorktreeList is called once per `git worktree list --porcelain`
	// invocation ResolveRefs makes. nil in production; a test sets it to
	// assert that resolving N workspaces of one repo costs one call rather
	// than N — the claim that makes a periodic reconciliation affordable.
	countWorktreeList func()
}

// Option configures a Backend at construction time.
type Option func(*Backend)

// WithLogger overrides the diagnostic sink (default: log.Printf, i.e. the
// daemon's stderr → daemon.log). It follows the house pattern —
// watchdog.WithLogger, rammon.WithLogger — and exists because runGit folds
// git's output into the returned error: a git failure this package chooses not
// to propagate leaves no trace at all unless something logs it.
func WithLogger(f func(format string, args ...any)) Option {
	return func(b *Backend) { b.logf = f }
}

// New constructs a git Backend using the given port allocator.
// The allocator must already be wired to the project's port ranges.
func New(allocator *ports.Allocator, opts ...Option) *Backend {
	b := &Backend{allocator: allocator, logf: log.Printf}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Kind returns "git".
func (b *Backend) Kind() string { return "git" }

// DisplayRef returns the workspace's git branch name, or the free-form
// Intent when no branch is recorded. This is the only sanctioned reader of
// BackendData["branch"] for display; callers outside this package use the
// backend.Backend.DisplayRef seam instead (architecture.md §8).
func (b *Backend) DisplayRef(ws *pigtail.Workspace) string {
	if ws == nil {
		return ""
	}
	if branch, _ := ws.BackendData["branch"].(string); branch != "" {
		return branch
	}
	return ws.Intent
}

// SupportsDelivery returns true — GitBackend pairs with GitHubPRDelivery (V1).
func (b *Backend) SupportsDelivery() bool { return true }

// Create initializes a new git worktree for the workspace:
//  1. derive a branch name (slug from intent unless BranchOverride is set)
//  2. allocate ports for every kind in Project.PortRanges
//  3. fetch the base ref's remote so a remote-tracking base (e.g.
//     "origin/main") is at its true tip at branch time (skipped for a bare
//     local base, when NoFetch is set, or best-effort-skipped when offline)
//  4. git worktree add <path> -b <branch> <baseRef>
//  5. write .env.local with PORT/STORYBOOK_PORT etc.
//  6. return a *Workspace ready for an agent to start working in
//
// For the vertical slice we skip post_create hooks (npm install etc.).
//
// Ports are left pending in the allocator: the caller confirms them once the
// workspace is persisted, or releases them (internal/daemon/handlers.settlePorts).
func (b *Backend) Create(ctx context.Context, p *pigtail.Project, intent backend.CreateIntent) (*pigtail.Workspace, error) {
	if p == nil {
		return nil, errors.New("git: nil project")
	}
	if p.RootPath == "" {
		return nil, errors.New("git: project has no root_path")
	}
	if intent.Intent == "" && intent.BranchOverride == "" {
		return nil, errors.New("git: intent or branch_override required")
	}

	branch := intent.BranchOverride
	if branch == "" {
		if intent.Slug != "" {
			prefix := "feat/"
			if p.Defaults != nil && p.Defaults.BranchPrefix != "" {
				prefix = p.Defaults.BranchPrefix
			}
			slug := dedupePrefix(intent.Slug, prefix)
			if slug == "" {
				slug = "agent-" + shortID()
			}
			branch = prefix + slug
		} else {
			branch = DeriveBranch(p, intent.Intent, "")
		}
	}

	wsPath := WorktreePathFor(p, branch)

	gitDir := MainWorktreePath(p)

	// An explicit base always wins — intent first, then project defaults. Only
	// when neither names one do we resolve it, and the resolved default is the
	// remote-tracking branch rather than the local one: see defaultBaseRef for
	// why a bare "main" is the wrong default in a worktree-per-task container.
	base := intent.BaseRef
	if base == "" && p.Defaults != nil {
		base = p.Defaults.BaseRef
	}
	if base == "" {
		base = defaultBaseRef(ctx, gitDir)
	}

	// Refresh the base ref's remote so a remote-tracking base (e.g.
	// "origin/main") points at the true remote tip when we branch from it,
	// rather than whatever was last fetched into this repo. A bare local base
	// (e.g. "main") has no remote-tracking counterpart to refresh and is
	// skipped. Best-effort and never fatal — see fetchBaseRemote.
	//
	// Then fast-forward the main worktree onto that same fresh base, so the
	// checkout that gets READ stops drifting behind the ones we create from it.
	// Strictly guarded: anything but a risk-free fast-forward is declined and
	// logged with its reason — see refreshMainWorktree. The periodic
	// read-side counterpart is RefreshMain (mainrefresh.go).
	if !intent.NoFetch {
		fetchBaseRemote(ctx, gitDir, base)
		res := refreshMainWorktree(ctx, gitDir, base)
		b.logMainRefresh(res)
		// And hand the verdict to the caller, which is the one place that can
		// say it to the person who just typed `pigtail new`. A daemon log line
		// nobody greps is how the drift stayed invisible for two months.
		if intent.OnMainRefresh != nil {
			intent.OnMainRefresh(res)
		}
	}

	// Collision guard: fail loud (before allocating ports, so a rejected
	// create never leaks a port) when the derived branch or worktree dir
	// already exists. NEVER auto-suffix — the human chooses (cd in, --branch,
	// or --force to override).
	if !intent.Force {
		if branchExists(ctx, gitDir, branch) {
			return nil, &pigtail.APIError{
				Code:    pigtail.ErrConflict,
				Message: fmt.Sprintf("branch %q already exists — cd into its worktree, or pass --branch for a distinct name (--force to override)", branch),
				Details: map[string]interface{}{"branch": branch, "path": wsPath},
			}
		}
		if _, err := os.Stat(wsPath); err == nil {
			return nil, &pigtail.APIError{
				Code:    pigtail.ErrConflict,
				Message: fmt.Sprintf("a worktree already exists at %s — cd into it, or pass --branch (--force to override)", wsPath),
				Details: map[string]interface{}{"branch": branch, "path": wsPath},
			}
		}
	}

	allocated, err := b.allocatePorts(p)
	if err != nil {
		return nil, err
	}
	releaseOnErr := func() {
		for k, v := range allocated {
			b.allocator.Release(k, v)
		}
	}

	if err := runGit(ctx, gitDir, "worktree", "add", wsPath, "-b", branch, base); err != nil {
		releaseOnErr()
		return nil, fmt.Errorf("git worktree add: %w", err)
	}

	// Clone node_modules from the main worktree via APFS copy-on-write so the
	// new worktree starts ~free on disk and provisions near-instantly; the
	// post-create `npm install` hook then reconciles only the dependency diff
	// against this worktree's lockfile. Best-effort — see cloneNodeModules.
	cloneNodeModules(ctx, gitDir, wsPath)

	if err := writeEnvLocal(wsPath, gitDir, allocated, p.PortEnvs); err != nil {
		// Roll back what we just built. The branch cannot be stale here — it
		// was created a few lines above — so deleting it by name is safe; only
		// the silence was the problem. A rollback that itself fails leaves a
		// half-built workspace, which is exactly the state worth a log line.
		if rmErr := runGit(context.Background(), gitDir, "worktree", "remove", "--force", wsPath); rmErr != nil {
			b.logf("pigtail: rollback of worktree %s failed after .env.local error: %v", wsPath, rmErr)
		}
		if brErr := runGit(context.Background(), gitDir, "branch", "-D", branch); brErr != nil {
			b.logf("pigtail: rollback left branch %q behind after .env.local error: %v", branch, brErr)
		}
		releaseOnErr()
		return nil, fmt.Errorf("write .env.local: %w", err)
	}

	now := time.Now().UTC()
	ws := &pigtail.Workspace{
		ID:             "ws_" + shortID(),
		ProjectID:      p.ID,
		Intent:         intent.Intent,
		State:          pigtail.StateBuilding,
		Path:           wsPath,
		Ports:          allocated,
		RecipeID:       intent.RecipeID,
		BackendKind:    "git",
		BackendData:    map[string]interface{}{"branch": branch, "base_branch": base},
		CreatedAt:      now,
		LastActivityAt: now,
	}

	return ws, nil
}

// Snapshot, Diff — stubs for the slice. Land with the ship/PR arc.

// Snapshot is not implemented in the slice.
func (b *Backend) Snapshot(_ context.Context, _ *pigtail.Workspace, _ string) (backend.SnapshotID, error) {
	return "", errors.New("git: Snapshot not implemented (post-slice)")
}

// Diff is not implemented in the slice.
func (b *Backend) Diff(_ context.Context, _ *pigtail.Workspace, _, _ *backend.SnapshotID) (backend.Diff, error) {
	return backend.Diff{}, errors.New("git: Diff not implemented (post-slice)")
}

// Status reports the worktree's cleanliness via `git status --porcelain`
// and how far it is ahead/behind its base ref.
func (b *Backend) Status(ctx context.Context, ws *pigtail.Workspace) (backend.Status, error) {
	if ws == nil || ws.Path == "" {
		return backend.Status{}, errors.New("git: workspace path required")
	}
	out, err := captureGit(ctx, ws.Path, "status", "--porcelain")
	if err != nil {
		return backend.Status{}, err
	}
	var st backend.Status
	st.Clean = strings.TrimSpace(out) == ""
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		// Porcelain format: XY filename. "??" means untracked.
		code := line[:2]
		name := strings.TrimSpace(line[2:])
		if code == "??" {
			st.UntrackedFiles = append(st.UntrackedFiles, name)
		} else {
			st.ModifiedFiles = append(st.ModifiedFiles, name)
		}
	}
	base, _ := ws.BackendData["base_branch"].(string)
	if base != "" {
		branch, _ := ws.BackendData["branch"].(string)
		if branch != "" {
			// ahead / behind counts
			out, err := captureGit(ctx, ws.Path, "rev-list", "--left-right", "--count",
				branch+"..."+base)
			if err == nil {
				var ahead, behind int
				_, _ = fmt.Sscanf(strings.TrimSpace(out), "%d\t%d", &ahead, &behind)
				st.AheadOfBase, st.BehindBase = ahead, behind
				// merged check
				mergeErr := runGit(ctx, ws.Path, "merge-base", "--is-ancestor", branch, base)
				st.Merged = mergeErr == nil
				// recent commits
				commitLog, err := captureGit(ctx, ws.Path, "log", "--oneline", "--no-decorate", "-10", base+".."+branch)
				if err == nil {
					for _, line := range strings.Split(strings.TrimSpace(commitLog), "\n") {
						if line != "" {
							st.RecentCommits = append(st.RecentCommits, line)
						}
					}
				}
			}
		}
	}
	return st, nil
}

// Cleanup removes the worktree, optionally deletes the branch, and
// releases allocated ports. See backend.CleanupOptions for safety knobs.
//
// Idempotent: if the worktree directory is already gone, returns nil.
//
// Resumable, on purpose. A teardown can be interrupted halfway — the daemon
// being restarted mid-`worktree remove` is enough, since launchd reaps the
// process group and takes the git child with it. What that leaves behind is a
// directory full of deleted tracked files, usually without its .git file.
// Re-running Cleanup on that state must finish the job rather than refuse or,
// worse, report success: see the repoRoot fallback below and the porcelain
// classification in the safety check.
func (b *Backend) Cleanup(ctx context.Context, ws *pigtail.Workspace, opts backend.CleanupOptions) (backend.CleanupOutcome, error) {
	var outcome backend.CleanupOutcome
	if ws == nil || ws.Path == "" {
		return outcome, errors.New("git: workspace path required")
	}
	stored, _ := ws.BackendData["branch"].(string)
	base, _ := ws.BackendData["base_branch"].(string)
	if base == "" {
		base = "main"
	}

	// Find the parent git repo (where the worktree was registered).
	// `git -C <wsPath> rev-parse --show-toplevel` returns the worktree
	// path itself; we need `--git-common-dir` to find the canonical
	// .git directory and then walk up to the repo root.
	repoRoot := parentRepoRoot(ctx, ws.Path)
	if repoRoot == "" && opts.Project != nil {
		// The worktree can no longer name its own repo — its .git file is
		// gone, which is exactly what an interrupted teardown leaves behind.
		// Ask the project instead. Without this the removal below is skipped
		// silently and Cleanup reports a success it did not perform.
		repoRoot = parentRepoRoot(ctx, MainWorktreePath(opts.Project))
	}

	// Safety check: refuse on dirty worktree unless --force. We treat
	// "dirty" as having modified or staged tracked files; bare untracked
	// files (the user dropped a scratch file) don't represent lost work.
	//
	// Three levels, none of which weakens the guard:
	//  1. the directory is not a usable worktree any more → nothing to
	//     protect, proceed;
	//  2. classify the porcelain lines into *modified* (content that exists
	//     only here) and *missing* (unstaged deletions of tracked files —
	//     the blob is still in the object database at HEAD, so no unique
	//     content is at stake);
	//  3. modified wins and keeps the historical refusal; missing-only still
	//     refuses, but says what it actually sees.
	if !opts.Force {
		if _, err := os.Stat(ws.Path); err == nil && isUsableWorktree(ctx, ws.Path) {
			out, err := captureGit(ctx, ws.Path, "status", "--porcelain")
			if err == nil {
				modified, missing := classifyPorcelain(out)
				switch {
				case modified > 0:
					return outcome, &pigtail.APIError{
						Code:    pigtail.ErrConflict,
						Message: "worktree has uncommitted changes; use --force to clean anyway",
						Details: map[string]interface{}{"path": ws.Path},
					}
				case missing > 0:
					return outcome, &pigtail.APIError{
						Code: pigtail.ErrConflict,
						Message: "worktree is missing tracked files and has no other change — " +
							"this looks like an interrupted teardown; re-run with --force to finish it",
						Details: map[string]interface{}{
							"path":                        ws.Path,
							"missing_count":               missing,
							"likely_interrupted_teardown": true,
						},
					}
				}
			}
		}
	}

	// Resolve which branch this teardown may delete. BackendData["branch"] is
	// a snapshot taken at creation time and is never refreshed, so a `git
	// switch` inside the worktree leaves it describing a branch the worktree
	// no longer holds — see issue #53. Ask git what the worktree actually has
	// before deleting anything by that name.
	//
	// This must run *before* the removal below: once the worktree is gone so
	// is its administrative record, and with it the only live answer.
	branch, keptBranch, keptReason := b.branchToDelete(ctx, repoRoot, ws.Path, stored)

	// Decide branch deletion policy. Note the policy question is asked about
	// the *resolved* branch: "is this safe to delete?" is meaningless when
	// asked about a name the worktree left days ago.
	deleteBranch := false
	if branch != "" && repoRoot != "" && !opts.KeepBranch {
		if opts.Force {
			deleteBranch = true
		} else {
			// "Safe to delete" means the work is provably already in base —
			// not merely that the branch tip became an ancestor of it. A
			// multi-commit squash never satisfies the narrow test, so asking
			// only that question left a squash-merged branch behind after
			// every single teardown. That is how orphan branches accumulate
			// faster than anyone deletes them (issue #54).
			//
			// Asked about the git-resolved branch, never the stored name
			// (issue #53), and from repoRoot, which still exists at this
			// point — the worktree may not.
			ms, err := mergeStateFor(ctx, repoRoot, branch, base)
			deleteBranch = ms.Contained()
			// Not-merged is documented policy and stays quiet. "We could not
			// tell" is not policy — it is the one outcome the user cannot
			// predict, so it is the one worth saying out loud.
			if err != nil || ms.Status == backend.MergeUnknown {
				keptBranch = branch
				keptReason = "pigtail could not tell whether it is merged into " + base
				if ms.Note != "" {
					keptReason += " (" + ms.Note + ")"
				}
				b.logf("pigtail: keeping branch %q: containment undecided (%s)", branch, ms.Note)
			}
		}
	}

	// A branch we declined to delete is only worth reporting when the user
	// did not ask for it. --keep-branch is an explicit choice, not a surprise.
	if opts.KeepBranch {
		keptBranch, keptReason = "", ""
	}
	outcome.BranchKept, outcome.BranchKeptReason = keptBranch, keptReason

	// Remove worktree (idempotent — if path is gone, skip).
	if _, err := os.Stat(ws.Path); err == nil {
		if repoRoot == "" {
			// Nothing can be removed and nothing was. Say so instead of
			// returning nil: the caller deletes the store row on success,
			// which would orphan this directory, its administrative record
			// in .git/worktrees, and its branch.
			return outcome, fmt.Errorf("git: cannot resolve the repo owning worktree %s "+
				"(its .git file is missing and no project was supplied); "+
				"nothing was removed", ws.Path)
		}
		args := []string{"worktree", "remove", ws.Path}
		if opts.Force {
			args = []string{"worktree", "remove", "--force", ws.Path}
		}
		if err := runGit(ctx, repoRoot, args...); err != nil {
			// `git worktree remove` validates that <path>/.git exists and
			// refuses outright when it does not — precisely the state an
			// interrupted teardown leaves. Git itself calls such a worktree
			// "prunable", so finish by hand: drop the directory, then let
			// git reap the administrative record.
			if hasWorktreeGitEntry(ws.Path) {
				return outcome, fmt.Errorf("git worktree remove: %w", err)
			}
			// Deleting outside git is the one destructive path here that git
			// does not sanction, so prove ownership first: a prunable worktree
			// is still listed by `worktree list` (with a "prunable" marker),
			// while a directory that is not this repo's worktree never is.
			// Without this, a ws.Path grown stale against the store — moved by
			// hand, adopted wrong — would take an unrelated directory with it.
			if !registeredWorktree(ctx, repoRoot, ws.Path) {
				return outcome, fmt.Errorf("git worktree remove: %w "+
					"(refusing to delete %s by hand: the repo at %s has no worktree record for it)",
					err, ws.Path, repoRoot)
			}
			if rmErr := os.RemoveAll(ws.Path); rmErr != nil {
				return outcome, fmt.Errorf("git: removing prunable worktree %s: %w", ws.Path, rmErr)
			}
			if pErr := runGit(ctx, repoRoot, "worktree", "prune"); pErr != nil {
				return outcome, fmt.Errorf("git worktree prune: %w", pErr)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return outcome, err
	}

	// Delete branch if appropriate. A failure here does not fail the whole
	// teardown — the worktree is already gone and re-running would not help —
	// but it must not vanish either: before issue #53 this was a bare
	// `_ = err`, so a `branch -D` against a name that no longer existed
	// produced no output in the CLI and no line in daemon.log.
	if deleteBranch && repoRoot != "" {
		if err := runGit(ctx, repoRoot, "branch", "-D", branch); err != nil {
			b.logf("pigtail: leaving branch %q behind after cleaning %s: %v", branch, ws.Path, err)
			outcome.BranchKept = branch
			outcome.BranchKeptReason = fmt.Sprintf("deleting it failed: %v", err)
		} else {
			outcome.BranchDeleted = branch
		}
	}

	// Port release is implicit: the store row holding ws.Ports is
	// deleted by the caller (daemon handler) after Cleanup returns,
	// which makes Allocator.UsedPorts forget them on the next call.
	return outcome, nil
}

// branchToDelete decides which branch, if any, a teardown of the worktree at
// wsPath may delete. It returns that branch, plus the branch left behind and a
// user-facing reason when it declines.
//
// The recorded name (stored) is only trusted when git has nothing better to
// say. It is written once, at creation time, and never refreshed — issue #53 —
// so after a `git switch` it names a branch that has no relation to this
// workspace. `branch -D` being unconditional, acting on a stale name is how a
// teardown silently deletes an unrelated branch that merely shares it.
//
// Declining is the safe side of the asymmetry: a branch left behind is untidy
// and recoverable, a branch deleted by mistake is neither.
func (b *Backend) branchToDelete(ctx context.Context, repoRoot, wsPath, stored string) (branch, kept, reason string) {
	if stored == "" {
		return "", "", "" // nothing recorded, nothing to delete
	}
	res := resolveWorktreeBranch(ctx, repoRoot, wsPath)
	switch {
	case !res.known:
		// Neither the repo nor the worktree can say what is checked out —
		// the worktree was removed by hand and its record pruned, which is a
		// state `clean` legitimately has to finish. The store is all we have.
		// Git's own refusal to delete a branch checked out elsewhere is the
		// remaining net, and it is now logged rather than swallowed.
		return stored, "", ""
	case res.detached:
		reason = fmt.Sprintf("the worktree was in detached HEAD, so the recorded branch %q "+
			"no longer describes it — pigtail will not guess", stored)
		b.logf("pigtail: keeping branch %q: worktree %s was detached", stored, wsPath)
		return "", stored, reason
	case res.branch != stored:
		reason = fmt.Sprintf("the worktree was on %q, not the recorded %q — pigtail will not guess "+
			"which of the two to delete", res.branch, stored)
		b.logf("pigtail: keeping branch %q: worktree %s is on %q, not the recorded %q",
			res.branch, wsPath, res.branch, stored)
		return "", res.branch, reason
	}
	return stored, "", ""
}

// worktreeBranch is what git can tell us about the ref a worktree holds.
type worktreeBranch struct {
	branch   string // branch name, without the refs/heads/ prefix
	detached bool   // HEAD points at a commit, not a branch
	known    bool   // git could answer at all
}

// resolveWorktreeBranch asks git which branch the worktree at wsPath actually
// has checked out.
//
// The administrative record (`git worktree list --porcelain`, backed by
// .git/worktrees/<name>/HEAD) is the primary source rather than
// `git -C <wsPath> branch --show-current`, because it survives the working
// directory: an interrupted teardown destroys the worktree's .git file — the
// state issue #51 handles — and the worktree then cannot answer for itself,
// while the record still names the branch and git flags the entry "prunable".
//
// Falling back to the worktree covers the reverse case, a repo whose record
// was pruned while the directory lives on.
func resolveWorktreeBranch(ctx context.Context, repoRoot, wsPath string) worktreeBranch {
	if repoRoot != "" {
		if res, ok := branchFromWorktreeList(ctx, repoRoot, wsPath); ok {
			return res
		}
	}
	// `branch --show-current` prints nothing and exits 0 on a detached HEAD,
	// so an empty result here is a detachment, not a failure.
	out, err := captureGit(ctx, wsPath, "branch", "--show-current")
	if err != nil {
		return worktreeBranch{}
	}
	name := strings.TrimSpace(out)
	return worktreeBranch{branch: name, detached: name == "", known: true}
}

// branchFromWorktreeList scans `git worktree list --porcelain` for the entry
// describing wsPath. Records are separated by blank lines and open with a
// `worktree <path>` line; the ref line is either `branch refs/heads/<name>` or
// a bare `detached`.
//
// Paths are compared through EvalSymlinks for the same reason
// registeredWorktree does it: git reports resolved paths while wsPath comes
// from the store and may still carry a symlinked prefix (/var → /private/var
// on macOS).
func branchFromWorktreeList(ctx context.Context, repoRoot, wsPath string) (worktreeBranch, bool) {
	out, err := captureGit(ctx, repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return worktreeBranch{}, false
	}
	want, err := filepath.EvalSymlinks(wsPath)
	if err != nil {
		want = filepath.Clean(wsPath)
	}
	inTarget := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			got, err := filepath.EvalSymlinks(p)
			if err != nil {
				got = filepath.Clean(p)
			}
			inTarget = got == want
			continue
		}
		if !inTarget {
			continue
		}
		if name, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
			return worktreeBranch{branch: name, known: true}, true
		}
		if line == "detached" {
			return worktreeBranch{detached: true, known: true}, true
		}
	}
	return worktreeBranch{}, false
}

// Move relocates the workspace's worktree to newPath using
// `git worktree move`, run from the parent repo root. Git rewrites the
// worktree's gitdir pointer and the repo's administrative records, so the
// per-worktree exclude file, info/exclude entries, .env.local, and internal
// logs all travel with the directory. The store is not touched here.
func (b *Backend) Move(ctx context.Context, ws *pigtail.Workspace, newPath string) error {
	if ws == nil || ws.Path == "" {
		return errors.New("git: workspace path required")
	}
	if newPath == "" {
		return errors.New("git: new path required")
	}
	if ws.Path == newPath {
		return nil // idempotent no-op
	}
	repoRoot := parentRepoRoot(ctx, ws.Path)
	if repoRoot == "" {
		return fmt.Errorf("git: cannot resolve repo root for worktree %s", ws.Path)
	}
	if err := runGit(ctx, repoRoot, "worktree", "move", ws.Path, newPath); err != nil {
		return fmt.Errorf("git worktree move: %w", err)
	}
	return nil
}

// RenameBranch renames the workspace's current branch with `git branch -m`,
// run from the parent repo root. Renaming a branch that is checked out in a
// worktree is allowed: git updates that worktree's HEAD to follow the new
// name. The store is not touched here.
func (b *Backend) RenameBranch(ctx context.Context, ws *pigtail.Workspace, newBranch string) error {
	if ws == nil || ws.Path == "" {
		return errors.New("git: workspace path required")
	}
	if newBranch == "" {
		return errors.New("git: new branch required")
	}
	oldBranch, _ := ws.BackendData["branch"].(string)
	if oldBranch == "" {
		return errors.New("git: workspace has no branch in backend data")
	}
	if oldBranch == newBranch {
		return nil // idempotent no-op
	}
	repoRoot := parentRepoRoot(ctx, ws.Path)
	if repoRoot == "" {
		return fmt.Errorf("git: cannot resolve repo root for worktree %s", ws.Path)
	}
	if err := runGit(ctx, repoRoot, "branch", "-m", oldBranch, newBranch); err != nil {
		return fmt.Errorf("git branch -m: %w", err)
	}
	return nil
}

// isUsableWorktree reports whether git can still operate inside wsPath. It
// returns false once the .git file is gone or points at a missing admin dir
// — the state an interrupted teardown leaves — in which case there is no
// uncommitted work left to protect.
func isUsableWorktree(ctx context.Context, wsPath string) bool {
	_, err := captureGit(ctx, wsPath, "rev-parse", "--git-dir")
	return err == nil
}

// classifyPorcelain splits `git status --porcelain` output into files whose
// content would be lost by a teardown (modified) and tracked files that are
// merely absent from the working tree (missing).
//
// The distinction matters because an unstaged deletion destroys nothing
// unique: the blob is still in the object database at HEAD and `git checkout
// -- .` restores it. The guard exists to prevent losing work, not to preserve
// an absence. Anything else — content modifications, staged deletions,
// renames, unmerged paths — counts as modified. Untracked files ("??") are
// ignored, as they always were.
// Trimming is deliberately newline-only: the porcelain status code lives in
// the first two columns and an unstaged deletion reads " D", so a global
// TrimSpace would silently reclassify the very first line as staged.
func classifyPorcelain(out string) (modified, missing int) {
	for _, line := range strings.Split(strings.Trim(out, "\n"), "\n") {
		if len(line) < 2 || strings.HasPrefix(line, "??") {
			continue
		}
		x, y := line[0], line[1]
		if x == ' ' && y == 'D' {
			missing++
			continue
		}
		modified++
	}
	return modified, missing
}

// hasWorktreeGitEntry reports whether wsPath still has the `.git` file (or
// directory) that makes it a worktree in git's eyes. When it is gone, git
// refuses `worktree remove` with a validation error and marks the worktree
// prunable instead.
func hasWorktreeGitEntry(wsPath string) bool {
	_, err := os.Lstat(filepath.Join(wsPath, ".git"))
	return err == nil
}

// registeredWorktree reports whether the repo at repoRoot still holds an
// administrative record for a worktree at wsPath. It stays true for a worktree
// whose .git file is gone — git lists it and flags it "prunable" — which is
// exactly the interrupted-teardown state we need to recognise.
//
// Paths are compared through EvalSymlinks: git reports resolved paths, while
// ws.Path comes from the store and may still carry a symlinked prefix
// (/var → /private/var on macOS).
func registeredWorktree(ctx context.Context, repoRoot, wsPath string) bool {
	out, err := captureGit(ctx, repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	want, err := filepath.EvalSymlinks(wsPath)
	if err != nil {
		want = filepath.Clean(wsPath)
	}
	for _, line := range strings.Split(out, "\n") {
		p, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree ")
		if !ok {
			continue
		}
		got, err := filepath.EvalSymlinks(p)
		if err != nil {
			got = filepath.Clean(p)
		}
		if got == want {
			return true
		}
	}
	return false
}

// parentRepoRoot resolves the original repo root that owns the worktree
// at wsPath. Returns "" if wsPath is not a git worktree.
func parentRepoRoot(ctx context.Context, wsPath string) string {
	out, err := captureGit(ctx, wsPath, "rev-parse", "--git-common-dir")
	if err != nil {
		return ""
	}
	gitDir := strings.TrimSpace(out)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(wsPath, gitDir)
	}
	// gitDir points at the canonical `.git` directory. Its parent is
	// the original repo root.
	return filepath.Dir(filepath.Clean(gitDir))
}

// worktreeExcludeFile returns the path of the per-worktree exclude file
// (equivalent to .gitignore but private to the worktree).
func worktreeExcludeFile(wsPath string) (string, error) {
	out, err := captureGit(context.Background(), wsPath, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(out)
	if !filepath.IsAbs(p) {
		p = filepath.Join(wsPath, p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return "", err
	}
	return p, nil
}

func appendUnique(path, line string) error {
	if b, err := os.ReadFile(path); err == nil {
		for _, existing := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(existing) == line {
				return nil
			}
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(line + "\n")
	return err
}

// errNoGitDir rejects a git invocation with no working directory.
//
// An empty dir leaves exec.Cmd.Dir unset, so git inherits the *process*
// working directory — for pigtaild that is wherever it was launched, and for
// the test binaries it is the package directory inside this very checkout.
// Neither is ever the intended target. Nothing about it looks wrong at the
// call site either: `git -C ""` is not an error to git, the empty string is
// simply a no-op, so the command succeeds against the wrong repository and
// reports success.
//
// Refusing here turns "silently operated on the wrong repo" into an error the
// caller has to handle. Every call site already passes a resolved path, so
// this fires only on a bug.
func errNoGitDir(args []string) error {
	return fmt.Errorf("refusing to run `git %s` with no working directory: "+
		"it would run against the process working directory, not a repository we chose",
		strings.Join(args, " "))
}

func captureGit(ctx context.Context, dir string, args ...string) (string, error) {
	if dir == "" {
		return "", errNoGitDir(args)
	}
	out, err := gitCmd(ctx, dir, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// --- helpers --------------------------------------------------------------

// branchExists reports whether refs/heads/<branch> exists in the repo at gitDir.
func branchExists(ctx context.Context, gitDir, branch string) bool {
	return runGit(ctx, gitDir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch) == nil
}

// remoteRefExists reports whether refs/remotes/<ref> exists in the repo at gitDir.
func remoteRefExists(ctx context.Context, gitDir, ref string) bool {
	return runGit(ctx, gitDir, "show-ref", "--verify", "--quiet", "refs/remotes/"+ref) == nil
}

// defaultBaseRef picks the base to branch from when neither the create intent
// nor the project defaults name one.
//
// It resolves to the REMOTE-TRACKING default branch ("origin/main"), not the
// local one ("main"). The distinction is the whole point: a local `main` only
// advances on an explicit pull, so in a container where every piece of work
// happens in its own worktree, nothing ever moves it — and a new worktree
// silently starts N commits in the past. Measured on 2026-09-04:
// a large monorepo's main checkout sat 30 commits behind origin/main, and the session
// journal carries ~20 occurrences of the resulting failure since June
// (worktrees created 27, 47 and 53 commits behind), each costing a false
// conclusion: an `audit-ci` red that was a missing bump already present on
// origin/main, work reported "not merged" that had been merged for days.
//
// Returning a remote-tracking ref also arms fetchBaseRemote, which is skipped
// for a bare local ref — so the default now refreshes itself before branching
// instead of trusting whatever was last fetched into the repo.
//
// Resolution order, first hit wins:
//  1. origin/HEAD — what `git clone` records as the remote's default branch.
//  2. origin/<branch checked out in the main worktree> — covers a repo whose
//     origin/HEAD was never set (a manually wired or worktree-only clone).
//  3. origin/main — last remote-tracking attempt before giving up.
//  4. "main" — the historical behaviour, kept for a repo with no remote at all;
//     a purely local project must still be able to create worktrees.
func defaultBaseRef(ctx context.Context, gitDir string) string {
	if out, err := captureGit(ctx, gitDir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if ref := strings.TrimSpace(out); ref != "" {
			return ref
		}
	}
	if remoteExists(ctx, gitDir, "origin") {
		if out, err := captureGit(ctx, gitDir, "symbolic-ref", "--short", "HEAD"); err == nil {
			if br := strings.TrimSpace(out); br != "" && remoteRefExists(ctx, gitDir, "origin/"+br) {
				return "origin/" + br
			}
		}
		if remoteRefExists(ctx, gitDir, "origin/main") {
			return "origin/main"
		}
	}
	return "main"
}

// fetchBaseRemote runs `git fetch <remote>` for the remote implied by a
// remote-tracking base ref, so the base is fresh before we branch from it.
// The remote is the leading path segment of base (e.g. "origin/main" →
// "origin", "origin/feature/x" → "origin"). A base with no "/" is a local ref
// with no remote-tracking counterpart and is skipped; a leading segment that
// is not a configured remote is also skipped (we never guess "origin").
//
// Best-effort and never fatal: a failing fetch (offline, no network,
// unreachable remote) is logged to stderr and swallowed so worktree creation
// still proceeds against the last-known remote-tracking ref. This keeps
// bootstrap working when the user is on an unreachable network.
func fetchBaseRemote(ctx context.Context, gitDir, base string) {
	if err := fetchBaseRemoteErr(ctx, gitDir, base); err != nil {
		fmt.Fprintf(os.Stderr, "pigtail: fetch of base remote skipped (%v) — branching from last-known %s\n", err, base)
	}
}

// fetchBaseRemoteErr is fetchBaseRemote with the failure returned rather than
// printed, for the periodic caller: on an unreachable network the same failure
// recurs every sweep, and a line per sweep forever is its own way of being
// unreadable. A skip (local ref, unconfigured remote) is not a failure and
// returns nil.
func fetchBaseRemoteErr(ctx context.Context, gitDir, base string) error {
	slash := strings.IndexByte(base, '/')
	if slash <= 0 {
		return nil // local ref (no remote-tracking counterpart) — nothing to fetch
	}
	remote := base[:slash]
	if !remoteExists(ctx, gitDir, remote) {
		return nil // leading segment isn't a configured remote — don't guess
	}
	return runGit(ctx, gitDir, "fetch", remote)
}

// remoteExists reports whether `remote` is a configured git remote in gitDir.
func remoteExists(ctx context.Context, gitDir, remote string) bool {
	out, err := captureGit(ctx, gitDir, "remote")
	if err != nil {
		return false
	}
	for _, line := range strings.Fields(out) {
		if line == remote {
			return true
		}
	}
	return false
}

func (b *Backend) allocatePorts(p *pigtail.Project) (map[string]int, error) {
	out := map[string]int{}
	kinds := make([]string, 0, len(p.PortRanges))
	for k := range p.PortRanges {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds) // deterministic order
	for _, k := range kinds {
		port, err := b.allocator.Allocate(p.PortRanges, k)
		if err != nil {
			// release whatever was allocated so far
			for ak, av := range out {
				b.allocator.Release(ak, av)
			}
			return nil, err
		}
		out[k] = port
	}
	return out, nil
}

// envAssign is a single NAME=value pair for .env.local generation. val is a
// string, not an int, so a non-numeric value (the worktree name assigned to
// PIGTAIL_OWNER) can be written the same way as a port.
type envAssign struct {
	name string
	val  string
}

// cloneNodeModules clones <mainPath>/node_modules into the new worktree using
// APFS copy-on-write (`cp -c`). On APFS this makes node_modules near-free on
// disk (only diverging files cost space) and near-instant to provision; the
// subsequent `npm install --prefer-offline` hook then reconciles only the
// dependency diff against the worktree's lockfile.
//
// Best-effort and never fatal: a missing source, a symlinked node_modules, an
// already-populated destination, or a `cp -c` failure (non-APFS or cross-volume
// copy) are all silently skipped/logged — the npm install hook still builds
// node_modules from scratch, preserving the previous behaviour.
func cloneNodeModules(ctx context.Context, mainPath, wsPath string) {
	src := filepath.Join(mainPath, "node_modules")
	dst := filepath.Join(wsPath, "node_modules")

	info, err := os.Lstat(src)
	if err != nil || !info.IsDir() {
		return // no real source dir (absent, a file, or a symlink) — let the hook install
	}
	if _, err := os.Lstat(dst); err == nil {
		return // already present — keep idempotent
	}
	// cp -c requests a clonefile on APFS; macOS cp falls back to a full copy
	// on other filesystems. Either way npm install then reconciles the diff.
	cmd := exec.CommandContext(ctx, "cp", "-c", "-R", src, dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "pigtail: node_modules clonefile skipped (%v): %s\n",
			err, strings.TrimSpace(string(out)))
	}
}

func writeEnvLocal(wsPath, parentPath string, ports map[string]int, overrides map[string]string) error {
	// Drop a worktree-scoped exclude so .env.local doesn't show up as
	// untracked in `git status`. We use $GIT_DIR/info/exclude (private
	// to the worktree) rather than .gitignore (would leak into commits).
	if excludePath, err := worktreeExcludeFile(wsPath); err == nil {
		_ = appendUnique(excludePath, ".env.local")
	}

	keys := make([]string, 0, len(ports))
	for k := range ports {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assigns := make([]envAssign, 0, len(ports)+1)
	for _, k := range keys {
		assigns = append(assigns, envAssign{envVarName(k, overrides), strconv.Itoa(ports[k])})
	}
	// PIGTAIL_OWNER always gets an explicit assign, valued with the new
	// worktree's name — never left to inheritance. The block just below
	// pulls the parent's .env.local in verbatim, so without this explicit
	// assign a new worktree would inherit the PARENT's PIGTAIL_OWNER and
	// silently claim its identity; running it through mergeEnvLocal, like
	// every port, is what rewrites it instead.
	assigns = append(assigns, envAssign{envlocal.Owner, filepath.Base(wsPath)})

	// Inherit parent worktree's .env.local if present so worktrees start
	// with the same env (graphql endpoints, feature flags, etc.) instead
	// of crashing on a missing variable. Port lines get rewritten with
	// the freshly allocated values; everything else is preserved verbatim.
	var existing []byte
	if parentPath != "" {
		if data, err := os.ReadFile(filepath.Join(parentPath, ".env.local")); err == nil {
			existing = data
		}
	}

	// BROWSER=none: a dev server pigtail starts must never pop a browser tab
	// open. `pigtail dev` sources this file (see internal/devserver.wrapCommand),
	// and both Vite (openBrowser) and Storybook (via better-opn) treat
	// BROWSER=none as "do not open" — which is what makes this a single
	// repo-agnostic lever, working even on a repo whose own config hardcodes
	// `server.open: true` (two of a large monorepo's web apps do).
	//
	// Unlike the ports and PIGTAIL_OWNER, this one is a DEFAULT, not an
	// identity: it is only added when neither the parent's .env.local nor this
	// worktree's own already assigns BROWSER, so a deliberate `BROWSER=firefox`
	// survives instead of being silently rewritten on every `pigtail new`.
	if _, set := envlocal.Lookup(existing, envlocal.Browser); !set {
		assigns = append(assigns, envAssign{envlocal.Browser, envlocal.BrowserNone})
	}

	// No early-out on "nothing to write" here: assigns always carries at
	// least the PIGTAIL_OWNER assign above, so every worktree gets an
	// .env.local — the whole point of this task.
	content := mergeEnvLocal(existing, assigns)
	return os.WriteFile(filepath.Join(wsPath, ".env.local"), []byte(content), 0o644)
}

// mergeEnvLocal returns the parent .env.local content with each assign in
// `assigns` either rewriting the matching non-commented assignment or appended
// at the end if absent. Comments, blank lines, ordering, and any other
// variables are preserved unchanged.
//
// Which lines count as assignments is decided by internal/envlocal, the same
// reader `pigtail dev` uses on the result — so a parent written by a shell
// user (`export PORT=3000`) is rewritten rather than left in place with a
// contradicting duplicate appended below it. A rewrite keeps the line's
// existing form, `export` keyword and indentation included: this file is
// hand-maintained, and reformatting it is not this function's job.
func mergeEnvLocal(existing []byte, assigns []envAssign) string {
	targets := make(map[string]string, len(assigns))
	for _, a := range assigns {
		targets[a.name] = a.val
	}
	seen := make(map[string]bool, len(assigns))

	var b strings.Builder
	for _, line := range splitKeepingNewlines(string(existing)) {
		body := strings.TrimRight(line, "\n")
		name, _, ok := envlocal.ParseAssignment(body)
		if !ok {
			b.WriteString(line) // blank, comment, or not an assignment
			continue
		}
		val, wanted := targets[name]
		if !wanted || seen[name] {
			b.WriteString(line)
			continue
		}
		nl := ""
		if strings.HasSuffix(line, "\n") {
			nl = "\n"
		}
		fmt.Fprintf(&b, "%s%s=%s%s", envlocal.AssignPrefix(body, name), name, val, nl)
		seen[name] = true
	}

	missing := make([]envAssign, 0, len(assigns))
	for _, a := range assigns {
		if !seen[a.name] {
			missing = append(missing, a)
		}
	}
	if len(missing) > 0 {
		s := b.String()
		if len(s) > 0 && !strings.HasSuffix(s, "\n") {
			b.WriteByte('\n')
		}
		for _, a := range missing {
			fmt.Fprintf(&b, "%s=%s\n", a.name, a.val)
		}
	}
	return b.String()
}

// splitKeepingNewlines splits s on '\n' but keeps each terminator with its
// line, so the joined result round-trips exactly to the input.
func splitKeepingNewlines(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

// envVarName resolves the env var name for a given port kind. Per-project
// overrides (Project.PortEnvs) win; otherwise we fall back to the historic
// default mapping (DefaultPortEnv).
func envVarName(kind string, overrides map[string]string) string {
	return pigtail.PortEnvName(kind, overrides)
}

var nonSlugRE = regexp.MustCompile(`[^a-z0-9]+`)

// slugify converts a free-form intent into a short kebab-case slug
// suitable for a branch name (max 4 words, 60 chars).
func slugify(s string) string {
	s = strings.ToLower(s)
	s = nonSlugRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	words := strings.Split(s, "-")
	if len(words) > 4 {
		words = words[:4]
	}
	out := strings.Join(words, "-")
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

// dedupePrefix strips a leading "<type>-" (or bare "<type>") segment from slug
// when <type> equals the type embedded in prefix (e.g. prefix "feat/" → "feat").
// This avoids feat/feat-… when the intent text already starts with the type.
// A different type word inside the slug (e.g. "fix-…" under a "feat/" prefix)
// is left untouched.
func dedupePrefix(slug, prefix string) string {
	ptype := strings.TrimSuffix(prefix, "/")
	if i := strings.LastIndex(ptype, "/"); i >= 0 {
		ptype = ptype[i+1:]
	}
	if ptype == "" {
		return slug
	}
	if slug == ptype {
		return ""
	}
	if strings.HasPrefix(slug, ptype+"-") {
		return slug[len(ptype)+1:]
	}
	return slug
}

func shortID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func runGit(ctx context.Context, dir string, args ...string) error {
	if dir == "" {
		return errNoGitDir(args)
	}
	out, err := gitCmd(ctx, dir, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
