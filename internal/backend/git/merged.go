package git

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// This file implements the backend.MergeInspector capability for git: deciding
// whether a workspace's branch is already contained in its base ref.
//
// It lives apart from git.go deliberately — the detection has its own tuning
// knobs, its own failure modes and its own tests, and none of it belongs in
// the file that owns the workspace lifecycle.
//
// # Why three detection paths
//
// The naive answer ("is the branch an ancestor of base?") is right only for
// fast-forward and true merges. The dominant style in practice is a GitHub
// squash-merge, which rewrites N commits into one new commit with a new
// patch-id, so both the ancestor test and `git cherry` come back negative for
// every branch with more than one commit. Measured on a real repo (issue #54):
// 82% of merged-but-worktree-less branches went undetected, which made
// `clean --merged` quietly select nothing and read as "nothing to clean".
//
// So we run, cheapest first:
//
//  1. ancestor    — `merge-base --is-ancestor`: normal merge / fast-forward.
//  2. cherry      — every branch commit is patch-equivalent to one already in
//                   base. Catches rebases and single-commit squashes.
//  3. squash scan — the branch's *cumulative* change since the merge base has
//                   the same patch-id as one single commit in base. This is
//                   exactly the shape a squash-merge leaves behind, whatever
//                   the commit count, which is what (1) and (2) both miss.
//
// # Why the answer is three-valued
//
// Each path can prove containment. None can prove its absence: a squash whose
// conflicts were resolved by hand carries a different patch-id and is
// undetectable by any mechanical means. Hence backend.MergeAbsent means "we
// looked everywhere and found nothing", never "this branch is unmerged", and
// backend.MergeUnknown is reserved for "we could not look". A caller that
// deletes things must be able to tell those apart.
//
// The bias throughout is conservative: a false negative skips a workspace, a
// false positive deletes unmerged work. When in doubt we do not guess.

// Scan knobs. Vars rather than consts so tests can shrink them.
var (
	// squashPathProbes caps how many of the branch's changed paths are used to
	// narrow the candidate commits in base. Each probe is one cheap
	// `git log -- <path>`; the intersection collapses fast (two probes took a
	// 706-commit range down to a single candidate on the issue's repro).
	squashPathProbes = 12

	// squashCandidateTarget stops probing once the candidate set is this small
	// — further narrowing costs more than just computing the patch-ids.
	squashCandidateTarget = 5

	// squashMaxCandidates caps how many base commits get a patch-id computed.
	// Overrunning it yields MergeUnknown, never a silent negative.
	squashMaxCandidates = 40
)

// MergeState implements backend.MergeInspector.
//
// It honors ctx on every git invocation: this runs during the selection phase
// of `clean --merged`, before the first deletion, where a hang would be
// invisible to any per-deletion timeout (issue #54).
func (b *Backend) MergeState(ctx context.Context, ws *pigtail.Workspace) (backend.MergeState, error) {
	if ws == nil {
		return unknownMerge("", "no workspace"), nil
	}
	branch, _ := ws.BackendData["branch"].(string)
	base, _ := ws.BackendData["base_branch"].(string)
	if base == "" {
		base = "main"
	}
	if branch == "" {
		return unknownMerge(base, "workspace records no branch"), nil
	}
	if ws.Path == "" {
		return unknownMerge(base, "workspace has no path"), nil
	}
	return mergeStateFor(ctx, ws.Path, branch, base)
}

// mergeStateFor is the ref-level check MergeState wraps: same question, asked
// about a branch and a base by name rather than about a workspace record.
//
// The split exists because the teardown path must NOT go through the
// workspace-shaped door. `BackendData["branch"]` is a snapshot taken at
// creation and never refreshed, so it can name a branch the worktree left days
// ago; issue #53 made Cleanup resolve the real branch from git before deleting
// anything, and it needs to ask the containment question about *that* branch.
//
// dir can be any worktree of the repo — only refs are read, never the working
// tree, so the main worktree answers for a branch checked out elsewhere.
func mergeStateFor(ctx context.Context, dir, branch, base string) (backend.MergeState, error) {
	// A stale local base is the single biggest source of false negatives: the
	// squash commit exists on the remote and simply isn't in the local ref
	// yet. Prefer the base's configured upstream when it strictly contains the
	// local base. We never guess a remote name — @{upstream} is what the user
	// actually configured (see fetchBaseRemote for the same principle).
	base = freshestBase(ctx, dir, base)

	if err := runGit(ctx, dir, "rev-parse", "--verify", "--quiet", branch+"^{commit}"); err != nil {
		if ctx.Err() != nil {
			return unknownMerge(base, "cancelled"), ctx.Err()
		}
		return unknownMerge(base, "branch "+branch+" not found in "+dir), nil
	}
	if err := runGit(ctx, dir, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
		if ctx.Err() != nil {
			return unknownMerge(base, "cancelled"), ctx.Err()
		}
		return unknownMerge(base, "base ref "+base+" not found in "+dir), nil
	}

	// 1. Normal merge / fast-forward: the branch tip is already in base.
	if runGit(ctx, dir, "merge-base", "--is-ancestor", branch, base) == nil {
		return backend.MergeState{Status: backend.MergeContained, How: "merged", BaseRef: base}, nil
	}
	if ctx.Err() != nil {
		return unknownMerge(base, "cancelled"), ctx.Err()
	}

	// 2. Rebase / single-commit squash: every branch commit is patch-equivalent
	//    to one already in base (git cherry marks them '-').
	out, err := captureGit(ctx, dir, "cherry", base, branch)
	if err != nil {
		if ctx.Err() != nil {
			return unknownMerge(base, "cancelled"), ctx.Err()
		}
		return unknownMerge(base, "git cherry failed: "+err.Error()), nil
	}
	allEquivalent, sawCommit := true, false
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sawCommit = true
		if !strings.HasPrefix(line, "-") {
			allEquivalent = false
			break
		}
	}
	if sawCommit && allEquivalent {
		return backend.MergeState{Status: backend.MergeContained, How: "squashed", BaseRef: base}, nil
	}
	if !sawCommit {
		// No commits ahead of base at all, yet not an ancestor: nothing to
		// compare. Refuse to guess.
		return backend.MergeState{Status: backend.MergeAbsent, BaseRef: base, Note: "no commits ahead of " + base}, nil
	}

	// 3. Multi-commit squash-merge.
	return squashScan(ctx, dir, branch, base)
}

// squashScan looks for a single commit in base whose change is patch-identical
// to the branch's cumulative change since the merge base. That is precisely
// what a squash-merge produces, regardless of how many commits it collapsed.
//
// Candidates are narrowed by path before any patch is generated: computing a
// patch-id for every commit in base since the fork point took 38s on the
// issue's repro, while intersecting two `git log -- <path>` probes took 0.2s
// and left one candidate.
func squashScan(ctx context.Context, dir, branch, base string) (backend.MergeState, error) {
	mergeBase, err := captureGit(ctx, dir, "merge-base", branch, base)
	if err != nil {
		if ctx.Err() != nil {
			return unknownMerge(base, "cancelled"), ctx.Err()
		}
		return unknownMerge(base, "no merge base with "+base), nil
	}
	mergeBase = strings.TrimSpace(mergeBase)

	// The branch's cumulative change, as a patch-id. --no-renames on every
	// diff in this file: rename detection changes the patch text and therefore
	// the patch-id, so both sides of the comparison must use the same rule.
	netIDs, err := patchIDs(ctx, dir, "", "diff-tree", "--no-renames", "-p", mergeBase, branch)
	if err != nil {
		if ctx.Err() != nil {
			return unknownMerge(base, "cancelled"), ctx.Err()
		}
		return unknownMerge(base, "patch-id failed: "+err.Error()), nil
	}
	if len(netIDs) == 0 {
		return backend.MergeState{Status: backend.MergeAbsent, BaseRef: base, Note: "branch has no net change since " + short(mergeBase)}, nil
	}
	netID := netIDs[0].id

	paths, err := changedPaths(ctx, dir, mergeBase, branch)
	if err != nil {
		if ctx.Err() != nil {
			return unknownMerge(base, "cancelled"), ctx.Err()
		}
		return unknownMerge(base, "cannot list changed paths: "+err.Error()), nil
	}
	if len(paths) == 0 {
		return backend.MergeState{Status: backend.MergeAbsent, BaseRef: base, Note: "branch changes no path"}, nil
	}

	candidates, err := narrowCandidates(ctx, dir, mergeBase, base, paths)
	if err != nil {
		if ctx.Err() != nil {
			return unknownMerge(base, "cancelled"), ctx.Err()
		}
		return unknownMerge(base, "cannot list candidate commits: "+err.Error()), nil
	}
	if len(candidates) == 0 {
		return backend.MergeState{Status: backend.MergeAbsent, BaseRef: base, Note: "no commit in " + base + " touches the branch's files"}, nil
	}
	truncated := false
	if len(candidates) > squashMaxCandidates {
		candidates = candidates[:squashMaxCandidates]
		truncated = true
	}

	pairs, err := patchIDs(ctx, dir, strings.Join(candidates, "\n")+"\n",
		"diff-tree", "--no-renames", "-p", "--stdin")
	if err != nil {
		if ctx.Err() != nil {
			return unknownMerge(base, "cancelled"), ctx.Err()
		}
		return unknownMerge(base, "patch-id failed: "+err.Error()), nil
	}
	for _, p := range pairs {
		if p.id == netID {
			return backend.MergeState{
				Status:  backend.MergeContained,
				How:     "squash-merged",
				BaseRef: base,
				Note:    "carried by " + short(p.object) + " in " + base,
			}, nil
		}
	}
	if truncated {
		// Honest: we stopped looking, so we do not know.
		return unknownMerge(base, fmt.Sprintf("scan budget exhausted (>%d candidate commits in %s)", squashMaxCandidates, base)), nil
	}
	return backend.MergeState{Status: backend.MergeAbsent, BaseRef: base, Note: "no commit in " + base + " carries the branch's change"}, nil
}

// narrowCandidates returns commits in mergeBase..base that touch every probed
// path of the branch. A squash-merge commit touches all of them; intersecting
// a handful of single-path queries is far cheaper than diffing the whole range.
//
// Probing a subset of the paths keeps the result a superset of the true
// candidates, so narrowing early never loses the answer.
func narrowCandidates(ctx context.Context, dir, mergeBase, base string, paths []string) ([]string, error) {
	var set []string
	for i, p := range paths {
		if i >= squashPathProbes {
			break
		}
		out, err := captureGit(ctx, dir, "log", "--format=%H", "--no-merges",
			mergeBase+".."+base, "--", p)
		if err != nil {
			return nil, err
		}
		hits := splitLines(out)
		if i == 0 {
			set = hits
		} else {
			set = intersect(set, hits)
		}
		if len(set) <= squashCandidateTarget {
			break
		}
	}
	return set, nil
}

// changedPaths lists the paths the branch changed since the merge base.
func changedPaths(ctx context.Context, dir, mergeBase, branch string) ([]string, error) {
	out, err := captureGit(ctx, dir, "diff", "--no-renames", "--name-only", mergeBase, branch)
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// freshestBase prefers base's configured upstream when it strictly contains
// base, so a local ref the user has not pulled in a week does not produce a
// false "not merged". Returns base unchanged when there is no upstream, when
// base is already remote-qualified, or when the local ref has commits the
// upstream lacks.
func freshestBase(ctx context.Context, dir, base string) string {
	if strings.Contains(base, "/") {
		return base // already a remote-tracking or fully-qualified ref
	}
	out, err := captureGit(ctx, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", base+"@{upstream}")
	if err != nil {
		return base
	}
	upstream := strings.TrimSpace(out)
	if upstream == "" || upstream == base {
		return base
	}
	if runGit(ctx, dir, "merge-base", "--is-ancestor", base, upstream) != nil {
		return base // local base has commits the upstream lacks — trust local
	}
	return upstream
}

// patchPair is one line of `git patch-id` output: the patch-id and the object
// the patch came from (a commit SHA when fed by `diff-tree --stdin`).
type patchPair struct {
	id     string
	object string
}

// patchIDs runs `git <args…>` (optionally fed stdin) and pipes its patch
// output through `git patch-id --stable`, streaming rather than buffering the
// patch itself — a squash of a large branch is megabytes of diff we never need
// to hold.
func patchIDs(ctx context.Context, dir, stdin string, args ...string) ([]patchPair, error) {
	gen := exec.CommandContext(ctx, "git", args...)
	gen.Dir = dir
	if stdin != "" {
		gen.Stdin = strings.NewReader(stdin)
	}
	pipe, err := gen.StdoutPipe()
	if err != nil {
		return nil, err
	}

	pid := exec.CommandContext(ctx, "git", "patch-id", "--stable")
	pid.Dir = dir
	pid.Stdin = pipe
	var out bytes.Buffer
	pid.Stdout = &out

	if err := gen.Start(); err != nil {
		return nil, err
	}
	if err := pid.Start(); err != nil {
		_ = gen.Wait()
		return nil, err
	}
	genErr := gen.Wait() // closes our end of the pipe → patch-id sees EOF
	pidErr := pid.Wait()
	if genErr != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), genErr)
	}
	if pidErr != nil {
		return nil, fmt.Errorf("git patch-id: %w", pidErr)
	}

	var pairs []patchPair
	for _, line := range splitLines(out.String()) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		p := patchPair{id: fields[0]}
		if len(fields) > 1 {
			p.object = fields[1]
		}
		pairs = append(pairs, p)
	}
	return pairs, nil
}

func unknownMerge(base, reason string) backend.MergeState {
	return backend.MergeState{Status: backend.MergeUnknown, BaseRef: base, Note: reason}
}

func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func intersect(a, b []string) []string {
	keep := make(map[string]struct{}, len(b))
	for _, s := range b {
		keep[s] = struct{}{}
	}
	var out []string
	for _, s := range a {
		if _, ok := keep[s]; ok {
			out = append(out, s)
		}
	}
	return out
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
