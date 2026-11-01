// Package backend defines the Backend interface that abstracts workspace
// lifecycle operations (create, snapshot, diff, cleanup) away from any
// specific VCS or storage system.
//
// v1 ships a single implementation: git (worktree + branch). The interface
// is shaped so that future backends (filesystem, Notion, …) can be added
// without touching daemon code that orchestrates workspaces.
package backend

import (
	"context"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// CreateIntent carries the information needed to create a new workspace.
// It is what the daemon passes to Backend.Create() after resolving the
// recipe + dispatch request.
type CreateIntent struct {
	// Intent is the free-form description from the user.
	Intent string

	// Slug is an optional override for the auto-generated branch slug
	// (e.g. "auth-refactor"). Empty means derive from Intent.
	Slug string

	// BranchOverride bypasses slug derivation entirely (e.g. "feat/foo").
	BranchOverride string

	// BaseRef is the ref the workspace branches from (e.g. "main" or
	// "origin/main").
	BaseRef string

	// NoFetch, when true, skips the auto-fetch of the base ref's remote that
	// Create otherwise performs before branching. Default (false) fetches so a
	// remote-tracking base (e.g. "origin/main") is fresh at branch time.
	NoFetch bool

	// RecipeID, if set, is recorded on the resulting Workspace.
	RecipeID string

	// OnMainRefresh, when non-nil, is called with the verdict of the shared
	// checkout refresh a backend attempts before branching (see
	// MainRefresher). Backends with no such checkout never call it.
	//
	// Why a callback rather than a second return value: the refresh is a side
	// effect of creating, not a property of the workspace created, and it has
	// exactly one interesting reader — the caller who is about to tell a human
	// "main was 23 commits behind, caught up" or "main is 23 commits behind
	// and I did not touch it". Widening Create's signature for that would put
	// it in every backend and every call site; a nil-able hook keeps it where
	// it is used. The call happens synchronously, inside Create.
	OnMainRefresh func(MainRefresh)

	// Force skips the pre-existence collision guard.
	Force bool
}

// SnapshotID is an opaque identifier returned by Backend.Snapshot. The
// daemon never inspects it — it is the backend's job to round-trip it
// through Diff() and any future Restore().
//
// For GitBackend this is a commit SHA. For FilesystemBackend it would be
// a snapshot directory name.
type SnapshotID string

// Diff describes a change between two snapshots (or snapshot ↔ HEAD).
// Backends produce a human-readable textual diff plus stat counters.
type Diff struct {
	Text      string             // patch / human-readable diff
	Summary   pigtail.DiffSummary // line / file counts
	FromID    SnapshotID
	ToID      SnapshotID         // empty means HEAD / working tree
}

// CleanupOptions tunes the destructiveness of Backend.Cleanup.
type CleanupOptions struct {
	// Force skips safety checks: cleanup will succeed even with
	// uncommitted changes in the worktree, and the branch is deleted
	// regardless of whether it was merged into its base ref.
	Force bool

	// KeepBranch keeps the branch around even if it would otherwise be
	// safe to delete (e.g. merged into base). Useful when shipping
	// asynchronously and the user wants the branch retained.
	KeepBranch bool

	// Project is the workspace's owning project, when the caller can supply
	// it. It lets a backend find its repo when the workspace directory is
	// too broken to answer for itself — e.g. a worktree whose .git file was
	// destroyed by an interrupted teardown. Optional: backends must still
	// work with a nil Project (they just have fewer recovery options).
	//
	// Passing the Project rather than a pre-built path keeps architecture.md
	// §8 intact: the daemon never constructs backend-specific paths.
	Project *pigtail.Project
}

// CleanupOutcome reports what a teardown actually did to the workspace's work
// ref, so the caller can tell the user instead of printing an unqualified
// success. It carries only *surprising* outcomes: a branch the user explicitly
// asked to keep (KeepBranch), or one left behind by a documented policy (not
// merged, on the non-force path), is expected and stays out of it.
//
// "Pigtail could not decide whether it was merged" is not that policy — it is
// the outcome no user can predict from the flags they passed — so it does get
// reported (issue #54).
//
// Backends with no branch concept leave every field empty.
type CleanupOutcome struct {
	// BranchDeleted is the branch this teardown removed, empty when none was.
	BranchDeleted string

	// BranchKept is a branch the teardown declined, or failed, to remove.
	BranchKept string

	// BranchKeptReason explains, in a clause fit to show a user, why
	// BranchKept survived. Empty exactly when BranchKept is.
	BranchKeptReason string
}

// Status reports cleanliness, conflicts, and modification state of a
// workspace. Daemon uses this to decide if a workspace can ship,
// needs cleanup, or has pending edits.
type Status struct {
	Clean             bool     // no uncommitted changes
	HasConflicts      bool
	ModifiedFiles     []string
	UntrackedFiles    []string
	HeadSnapshot      SnapshotID
	AheadOfBase       int      // commits ahead of base ref
	BehindBase        int      // commits behind base ref
	Merged            bool     // branch is ancestor of base ref
	RecentCommits     []string // first N commit subjects ahead of base
}

// Backend owns the physical lifecycle of a Workspace.
//
// Implementations: GitBackend (v1), FilesystemBackend (V2+), NotionBackend (V2+).
// See docs/architecture.md §3 for design rationale.
type Backend interface {
	// Kind returns the backend identifier ("git", "filesystem", "notion", …).
	Kind() string

	// Create initializes a new workspace from an intent. Returns a fully
	// populated *Workspace (Ports, Path, BackendData filled in) ready for
	// agents to start working in.
	// Allocated ports stay pending in the shared allocator; the caller confirms
	// them after persisting the returned workspace, or releases them if it
	// abandons it.
	Create(ctx context.Context, p *pigtail.Project, intent CreateIntent) (*pigtail.Workspace, error)

	// Snapshot records the current workspace state. Returns an opaque ID
	// the backend uses to compare/restore.
	Snapshot(ctx context.Context, ws *pigtail.Workspace, message string) (SnapshotID, error)

	// Diff produces a human-readable diff between two snapshots, or
	// between a snapshot and HEAD when `to` is nil.
	Diff(ctx context.Context, ws *pigtail.Workspace, from, to *SnapshotID) (Diff, error)

	// Status reports cleanliness, conflicts, modifications.
	Status(ctx context.Context, ws *pigtail.Workspace) (Status, error)

	// Cleanup tears down the workspace (remove directory, delete branch,
	// archive folder, reclaim ports, etc.). Idempotent: calling on an
	// already-cleaned workspace is a no-op.
	//
	// The outcome describes what was actually done to the work ref. It is
	// meaningful even when the error is non-nil (a teardown can remove the
	// directory and still fail afterwards), so callers should report it
	// before inspecting the error.
	Cleanup(ctx context.Context, ws *pigtail.Workspace, opts CleanupOptions) (CleanupOutcome, error)

	// Move relocates the workspace's worktree to newPath via the backend's
	// native mechanism (e.g. git worktree move). It does not touch the
	// store — the caller persists the new path afterwards.
	Move(ctx context.Context, ws *pigtail.Workspace, newPath string) error

	// RenameBranch renames the workspace's current branch locally (e.g.
	// git branch -m). It does not touch the store — the caller updates the
	// persisted backend data afterwards.
	RenameBranch(ctx context.Context, ws *pigtail.Workspace, newBranch string) error

	// DisplayRef returns a human label for the workspace's work ref (git:
	// branch name; Intent when absent). Callers outside backend code MUST
	// use this seam rather than reading backend-internal BackendData keys
	// directly (architecture.md §8).
	DisplayRef(ws *pigtail.Workspace) string

	// SupportsDelivery indicates whether this backend has a Delivery
	// counterpart (e.g. GitBackend pairs with GitHubPRDelivery). Some
	// ephemeral backends may not.
	SupportsDelivery() bool
}

// PortFile is implemented by backends whose workspaces carry their ports in a
// file the dev servers read (git: .env.local). Optional.
type PortFile interface {
	// ReadPorts returns kind → port for every kind the file assigns; a value
	// that is not a port is an error naming the variable and the file.
	ReadPorts(ws *pigtail.Workspace, kinds []string, envs map[string]string) (map[string]int, error)
	// WritePorts rewrites only the given kinds' variables, everything else
	// intact, and returns restore, which puts the file back exactly as it
	// was (same bytes, or absent) — for a caller whose next step fails.
	WritePorts(ws *pigtail.Workspace, ports map[string]int, envs map[string]string) (restore func() error, err error)
}
