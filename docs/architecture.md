# Architecture & abstractions

> Internal architecture reference. Captures the core abstractions of pigtail
> and the principles that keep the codebase from being git-only forever.
> Audience: contributors, future maintainers, MCP/backend authors.
>
> Last updated: 2026-05-15.

---

## 1. Why abstractions matter here

Pigtail v1 ships **git-only** (workspace = git worktree, ship = create PR,
cleanup = remove worktree + branch). That's intentional: dev tooling is
where we have the deepest insight and the clearest product fit.

But the **product insight** ("pratiques dev rendent l'IA plus puissante
pour tout projet") implies that the same paradigm — isolation, versioning,
review, ship, cleanup — applies beyond code. Marketing campaigns, research
syntheses, ops procedures: every project that involves N parallel drafts
under human supervision can benefit from this model.

We design v1 in a way that doesn't bake "git" into every layer. **Cost of
this discipline upfront**: ~100 lines of Go interfaces + a discriminator
field. **Cost of retrofitting in 18 months**: weeks of refactor.

This doc captures the abstractions we hold sacred and what stays
backend-specific.

---

## 2. Core insight: separate "what" from "how"

Aspect              | Product primitive (universal) | v1 implementation (git)
--------------------|-------------------------------|------------------------
Isolated workspace  | `Workspace`                   | git worktree + branch
Versioned state     | `Snapshot`                    | git commit
Diff comparison     | `Diff`                        | git diff
Handoff for review  | `Delivery`                    | GitHub PR
Cleanup             | `Cleanup`                     | git worktree remove + branch -D

The left column is the contract. The right column is the v1 default.
Future backends (filesystem, Notion, Airtable, …) fill the right column
differently while honoring the left column.

---

## 3. The `Backend` interface

A Backend owns the **physical lifecycle** of a Workspace.

```go
type Backend interface {
    // Kind identifies the backend ("git", "filesystem", "notion", …).
    Kind() string

    // Create initializes a new workspace from an intent + a base reference.
    Create(ctx context.Context, p *Project, intent CreateIntent) (*Workspace, error)

    // Snapshot records the current workspace state. Returns an opaque ID
    // the backend uses to compare/restore.
    Snapshot(ctx context.Context, ws *Workspace, message string) (SnapshotID, error)

    // Diff produces a human-readable diff between two snapshots (or
    // between a snapshot and HEAD).
    Diff(ctx context.Context, ws *Workspace, from, to *SnapshotID) (Diff, error)

    // Status reports cleanliness, conflicts, modifications.
    Status(ctx context.Context, ws *Workspace) (Status, error)

    // Cleanup tears down the workspace (remove directory, delete branch,
    // archive folder, etc.).
    Cleanup(ctx context.Context, ws *Workspace) error

    // SupportsDelivery indicates whether this backend has a `Delivery`
    // counterpart (e.g. GitBackend has GitDelivery; ephemeral backends
    // may not).
    SupportsDelivery() bool
}
```

### Reference implementation: `GitBackend`

```go
func (g *GitBackend) Create(ctx, p, intent) (*Workspace, error) {
    // 1. Allocate ports (from project's port range)
    // 2. git worktree add <path> -b <branch> <base>
    // 3. Write .env.local with PORT/STORYBOOK_PORT, PIGTAIL_OWNER, BROWSER=none
    // 4. Run post_create hooks (e.g. npm install)
    // 5. Return Workspace with backend_data = { branch, base_branch }
}

func (g *GitBackend) Snapshot(ctx, ws, msg) (SnapshotID, error) {
    // git -C <ws.path> add -A && git commit -m <msg>
    // Return commit SHA as SnapshotID
}

func (g *GitBackend) Cleanup(ctx, ws) error {
    // 1. Kill any running dev/sb procs on ws.ports
    // 2. rm -rf .next
    // 3. git worktree remove <ws.path>
    // 4. git branch -D <branch> (if merged)
}
```

### Hypothetical: `FilesystemBackend` (V2+)

```go
func (fs *FilesystemBackend) Create(ctx, p, intent) (*Workspace, error) {
    // 1. mkdir <p.root>/.pigtail/workspaces/<id>
    // 2. cp -r <source_folder> <ws_path> (if base reference is a folder)
    // 3. Return Workspace with backend_data = { source_folder }
}

func (fs *FilesystemBackend) Snapshot(ctx, ws, msg) (SnapshotID, error) {
    // cp -r <ws.path> <ws.path>/.snapshots/<timestamp-id>/
    // Return snapshot dir name as SnapshotID
}
```

The same `Workspace` API works. Only the backend implementation differs.

---

## 4. The `Delivery` interface

A Delivery is the **act of handing off completed work** for external review
or publication. Decoupled from Backend because some products want git
workspaces with non-PR delivery (e.g. push to internal review tool) or
filesystem workspaces with approval delivery (e.g. marketing campaign).

```go
type Delivery interface {
    Kind() string // "github_pr", "publish", "approval", …

    Create(ctx context.Context, ws *Workspace, intent DeliveryIntent) (*DeliveryHandle, error)
    Update(ctx context.Context, h *DeliveryHandle, patch DeliveryPatch) error
    Status(ctx context.Context, h *DeliveryHandle) (DeliveryStatus, error)
    Comments(ctx context.Context, h *DeliveryHandle) ([]Comment, error)
    Reviewers(ctx context.Context, h *DeliveryHandle) ([]Reviewer, error)
    Merge(ctx context.Context, h *DeliveryHandle) error
    Cancel(ctx context.Context, h *DeliveryHandle) error
}

type DeliveryStatus struct {
    State        string // "draft", "ready", "approved", "merged", "rejected"
    CIStatus     string // "passing", "failing", "pending", "n/a"
    Mergeable    bool
    UnresolvedComments int
    AutoMerge    bool
}
```

### Reference implementation: `GitHubPRDelivery`

Maps onto `gh pr create / view / merge` + webhook subscriptions. CI status
from check runs. Reviewers from `requested_reviewers`. Comments from `gh
api /repos/.../pulls/X/comments`.

### Hypothetical: `ApprovalDelivery` (V2+)

A handoff to a human reviewer via email/Slack/Notion. State machine =
{ pending, approved, rejected }. Reviewers = manual list.

---

## 5. The `Workspace` data model

Backend-agnostic fields (always present, universal meaning):

```go
type Workspace struct {
    ID            string
    ProjectID     string
    Intent        string  // free-form description
    State         WorkspaceState
    Path          string  // filesystem path agents work in
    Ports         map[string]int  // {"dev": 3007, "storybook": 6013}
    RecipeID      string
    CreatedAt     time.Time
    LastActivityAt time.Time
    SnoozedUntil  *time.Time

    BackendKind   string                 // "git", "filesystem", …
    BackendData   map[string]interface{} // backend-specific extras

    AgentSessions []AgentSession
}

type WorkspaceState string

const (
    StatePlanning      WorkspaceState = "planning"
    StateBuilding                     = "building"
    StateReadyToReview                = "ready_to_review"
    StateReviewing                    = "reviewing"
    StateReadyToShip                  = "ready_to_ship"
    StateDeliveryOpen                 = "delivery_open"  // was "pr_open"
    StateShipped                      = "shipped"
    StateNeedsCleanup                 = "needs_cleanup"
    StateArchived                     = "archived"
)
```

### Backend-specific extras (in `BackendData`)

- **GitBackend**: `{ "branch": "feat/foo", "base_branch": "main", "head_commit": "abc..." }`
- **FilesystemBackend**: `{ "source_folder": "/path/...", "snapshot_dir": "/path/..." }`
- **NotionBackend**: `{ "page_id": "...", "snapshot_pages": [...] }`

Code that needs git-specific info must check `BackendKind == "git"` first.
**Never** assume `ws.BackendData["branch"]` exists.

---

## 6. What the agent sees

Agents (Claude Code, Codex, Aider) always work on a **folder**
(`workspace.Path`) regardless of backend. They edit files, run commands,
read context. **They don't know or care about git vs filesystem vs
Notion.**

When the agent finishes, the backend handles versioning:
- GitBackend: `git add -A && git commit -m "agent: snapshot"`
- FilesystemBackend: copies the folder to a snapshot dir
- NotionBackend: creates a versioned page snapshot

This decoupling is what makes non-dev backends possible without rewriting
agent integration.

---

## 7. Recipes can declare their backend

```yaml
# pigtail.yaml in a project
recipes:
  fix-ui:
    backend: git
    delivery: github_pr
    agent: claude
    mode: plan-first

  campaign-draft:                  # future, V2+
    backend: filesystem
    delivery: approval
    agent: claude
    mode: direct
    pre_dispatch:
      - load_context: brand-guidelines.md
```

The default recipe inherits the project's default backend, which inherits
`git` if not specified.

---

## 8. Anti-corner-painting checklist

Before merging any PR that touches the workspace lifecycle, ask:

- [ ] Does this code assume `workspace.BackendData["branch"]` exists?
      → Bad. Use `backend.Status(ws)` or type-switch on backend kind.
- [ ] Does this code call `exec.Command("git", ...)` directly?
      → Bad. Call `backend.Method(...)`.
- [ ] Does this code reference `Pull Request` in user-facing strings?
      → Better: use `Delivery` (the abstract term). PR is just the v1 default.
- [ ] Does this code assume merging is the only way to ship?
      → Bad. Use `delivery.Merge()` which a non-git Delivery may implement
      differently (publish, approve, etc.).

These checks are codified in the linter (`pigtail-lint`) once we have one.

---

## 9. What stays git-only forever

Some features are inherently gitisms and should NOT be abstracted:

- **Stacked PRs** (spr/graphite integration)
- **`git stash` recovery**
- **Rebase / merge conflict resolution**
- **Branch history visualization**

These are exposed as **git-only capabilities**, with type assertions or
guard clauses. Non-git backends simply don't get them.

### Optional capabilities (the same mechanism, one step out)

Some questions are *meaningful* beyond git but not *universal*, so they live
in `internal/backend` as small optional interfaces rather than in `Backend`
itself. A caller asserts, and degrades when the assertion fails:

```go
if mi, ok := be.(backend.MergeInspector); ok { … }
```

- **`MergeInspector`** (`internal/backend/merge.go`) — "is this workspace's
  work already contained in its base ref?". Only meaningful for backends with
  a shared base that work flows into; a filesystem or Notion backend has none,
  and a stub implementation would be worse than an honest assertion. Its
  answer is deliberately **three-valued** (`contained` / `absent` / `unknown`):
  containment can be proven, its absence essentially cannot, and a caller that
  deletes things must be able to tell "we looked and found nothing" from "we
  could not look" (issue #54).

- **`MainRefresher`** (`internal/backend/mainrefresh.go`) — "bring the shared
  checkout people READ level with its base". Only meaningful for a backend
  with one long-lived checkout that nothing else ever updates; a backend where
  every workspace is self-contained has nothing to refresh. Its outcome is
  three-valued too (`updated` / `up-to-date` / `skipped` + reason): abstaining
  is the normal, safe answer — over a dirty or repurposed checkout it is the
  *only* acceptable one — and an abstention a caller cannot name is
  indistinguishable from a feature that never ran.

  The verdict also carries **`Behind`**, how many commits of the base the
  checkout was missing, measured read-only before any guard. It is what turns
  a verdict into a sentence someone acts on: "left as-is because the tree is
  dirty" is a fact about pigtail, "23 commits behind and left as-is because
  the tree is dirty" is a fact about the directory an agent is about to read.
  `0` means level *or* unmeasurable — never proven freshness.

- **`PortFile`** (`internal/backend/backend.go`) — "read or rewrite the
  ports this workspace's dev servers launch on". The git backend keeps them
  in `.env.local` (read through `internal/envlocal`, written through
  `mergeEnvLocal`); `WritePorts` returns a `restore` that puts the previous
  bytes back, so `PATCH /workspaces/{id}/ports` can undo a file write when
  the registry refuses the next step.

  `CreateIntent.OnMainRefresh` is the same verdict on the **create** path: the
  refresh is a side effect of creating, not a property of the workspace
  created, so it travels by nil-able hook rather than by widening `Create`'s
  signature across every backend and call site.

Prefer this shape over widening `Backend` whenever a stub implementation would
have to lie.

### Where a verdict goes once produced

`internal/mainfresh` holds the latest main-worktree verdict per project,
in memory. Both producers write to it — the daemon's refresh sweep
(`internal/daemon/refreshmain.go`) and every create path — and the read-only
`GET /servers` handler only ever reads it.

Two rules make it useful rather than decorative:

- **The display path never runs git.** "Is my `main` fresh?" is asked before
  pointing an agent at the checkout, from a command that must stay instant. A
  listing that computed freshness would be the slowest command in the tool, and
  a slow check is one nobody runs — the same failure, one layer up, as the
  staleness this exists to expose.
- **It is deliberately not persisted.** A verdict is a statement about a
  working tree at an instant; surviving a daemon restart would let it outlive
  its subject, and a stale claim of freshness is strictly worse than no claim.
  The sweep repopulates it on its first (immediate) pass. For the same reason
  the create response's `main_refresh` note is stamped on a **copy** of the
  workspace: the store caches the pointer it is handed, so writing the note
  onto it would serve "main was 23 commits behind" from every later `GET` of
  that workspace, long after it stopped being true.

---

## 10. Daemon ↔ backend interaction

```
┌──────────┐      ┌────────────┐      ┌──────────────┐
│ pigtail  │ ───► │ pigtaild   │ ───► │ Backend impl │
│ CLI/TUI  │      │ (daemon)   │      │ (git, fs, …) │
└──────────┘      └────────────┘      └──────────────┘
                       │
                       └──► Delivery impl ──► external (GitHub, Slack, …)
```

The daemon never touches git directly. It always calls
`backend.Method(...)`. The TUI/CLI never touches git directly either —
they speak to the daemon. This single chokepoint makes adding a new
backend a localized change.

**One deliberate exception: `pigtail db`.** The `db list` / `db restore`
commands read and replace the database directly instead of going through the
daemon. They have to: they exist only for when the daemon is *down*, and `db
restore` refuses to run while it answers a health check. Routing them through
the daemon would mean asking a process to replace the database it is holding
open. The code lives in `internal/dbtool` and runs inside `pigtaild -db-tool`,
a one-shot mode like `-version` (the daemon never starts): `pigtail db` keeps
the commands, the help, the caller's scope view and the « daemon is up »
refusal, and runs the sibling `pigtaild -db-tool …` with the terminal
inherited. That keeps the SQLite driver out of the `pigtail` binary
(`TestCLIDoesNotLinkTheSQLiteDriver`), which hooks launch thousands of times a
day. The exception is not a precedent for any command that could go through
the daemon instead.

### Focus priority in admission

`internal/daemon/admission` stays pure: it learns only a `Focus bool` on a
request, and orders starving, then focus, then fewest leases, then oldest. It
never imports the store, cmux or `focuspri`. The decision lives in
`internal/daemon/focuspri`: a resolver that answers `IsFocus(caller)` from an
atomic snapshot (pinned cmux workspaces of the sidebar pass via
`cmuxsync.Runner.PinnedWorkspaces`, plus the open `pigtail focus` themes and
subjects), refreshed in the background and stale after 30 s. The admission
handler asks it once per request, in memory only, so no cmux or store call sits
on the hot path. Wiring is in `daemon/cmuxsync_wiring.go`.

### Daemon-side monitors

Beyond request handlers, the daemon runs several continuous monitors with the
same lifecycle (started in `Run`, stopped on ctx cancel, publishing on the
event bus):

- **Activity watcher** (`internal/daemon/activity`) — one goroutine per
  tmux agent session, classifying pane content into agent states and
  emitting `workspace.activity` events.
- **CPU slot arbiter** (`internal/daemon/cpuslots`) — a fair-share budget of
  "slots" distributed between every CPU-intensive consumer on the machine,
  chiefly concurrent vitest runs, which otherwise each size their worker pool
  on `os.availableParallelism()` as if they were alone. A slot is held for
  exactly as long as the client's `POST /cpu/slots` connection stays open, so
  the kernel closing that socket is the release — there is no heartbeat and no
  PID to watch. It is the one monitor that **never kills**: the holding
  ceiling revokes a slot's *accounting*, redistributes it, and leaves the
  client running. That is the same invariant as the rest of this section — the
  daemon arbitrates, it does not execute — held one step further, because the
  arbiter does not even know what process is behind a connection.
- **Memory admission controller** (`internal/daemon/admission`) — a
  machine-wide queue that holds heavy commands (tsc, eslint, jest, vitest,
  nx, storybook build) back while available memory, net of what admitted
  commands are still expected to grow into, would fall under a floor, while
  the kernel reports pressure, or while the machine swaps. Same lease shape
  as the CPU arbiter — the `POST /admission/leases` connection is the lease —
  and the same invariant: it kills nothing. Its one extra: it samples the memory
  (physical footprint) of each lease's process tree (the client's PID, taken at its word on the
  trusted socket) and learns from the peaks what each class costs on each
  repo. It also caps how many runs of one *class* may be held at once, for
  the classes whose scarce resource is not a number of bytes — a browser-mode
  vitest run holds one live browser per worker. Such a run takes no CPU slot
  at all, so each resource still has exactly one owner per class of run. The
  CPU arbiter and this controller coexist on purpose: one shares cores per
  test file over seconds, the other memory per process over minutes. Agents reach it through `pigtail admit` — directly, or inserted by
  the `pigtail hook claude-bash` PreToolUse hook.
- **cmux sidebar loop** (`internal/daemon/cmuxsync`) — off by default
  (`pigtaild --cmux-sidebar`, or `POST /cmux/sidebar`). It shows
  "waiting for you" / "dead" pills on cmux workspaces and keeps the title and
  description of workspaces bound to a subject, from the native session
  files (1 s), cmux listings (30 s) and a `list-status` reconcile (10 min).
  "Dead" needs a surface cmux still shows (`cmux tree`, per window, each slow
  pass): `cmux sessions list` is a persisted history that keeps the session
  of a pane closed long ago. A failing `cmux tree` keeps the last set for
  four slow passes, then proves nothing — it never lights "dead". A
  pure engine (`Compute`, `Damper`, `Diff`) decides; the `Runner` observes and
  applies the difference through `cmux.Sidebar`, serially and rate-limited.
  It is one of the **three** places the daemon itself executes cmux, with
  the relay's idle wake (`handlers/relay_wake.go`) and the session launcher
  below (the post-relaunch check runs it from a `pigtail` subprocess), and
  the one monitor that
  writes to another application's UI — hence the extra guards: it writes only
  its own keys (`cmuxsync.OwnedKeys`), reorders only to arrange its folders
  (`--cmux-folders`, D2, every allowed window on its own, D8, while no cmux
  window is key, focus D11), yields a title for
  good once renamed by hand, recovers its panics and switches itself off
  after 5 in 10 minutes, giving every title back. Its `pigtail-chk` pill
  shows the defects of the last `pigtail cmux check`: it reads `last.json` at
  most every 30 s, keeps the report only when it is about the cmux running
  now (and not fatal), and puts the pill on the workspace whose open surfaces
  hold the surface in defect — `Check · N to fix` in orange when `--fix` can
  lift every one, `Check · N left` in red as soon as one is blocked or its
  repair failed; a row only reported (background work lost) gets none; a row
  whose surface now runs another live Claude process than the one the check
  saw is dropped once that one has ended (proven from the peer's native file,
  never a ghost; a row with no reported process, once a session started after
  the report serves the surface). With
  `--cmux-sidebar-window`, a workspace of an excluded window gets no
  `pigtail-chk` pill: the loop does not see it.
- **session launch** (`internal/daemon/launch`, wired in `launch_wiring.go`,
  sub_x000000000000015) — `POST /subjects/{id}/launch` opens a cmux tab
  running the `c` launcher on a subject, with no calling session: the third
  place the daemon executes cmux (`new-workspace`, and `list-windows`,
  `workspace list`, `workspace-group list` to read). The launcher takes the
  reservation the CLI's `session spawn` shares (`spawnreg.WithReservation`
  under a file lock on `spawns.jsonl`: a duplicate, a live session, a burst
  are refused before any tab), picks the cell through the launch margin
  guard (lowest-rank cell when unmeasured), the cwd (the subject's live
  worktree, else the scope's `launch_dir`), the cell's default permission
  mode (`bypassPermissions` refused), and, folders on, adds the tab at the
  bottom of the subject's folder (`cmuxsync.SubjectFolder`). It binds the tab
  to the subject, then confirms the launch by its session's registration: a
  live peer on the tab, updated at or after its birth, within 5 min, else
  `failed`. Every state is in the registry (`GET /launches/{id}`) and on the
  bus (`session.launch_changed`, scoped by the subject). A daemon restart
  resumes the open API launches to their original deadline. Its cmux calls
  run with no `CMUX_*` of the daemon's pane (`stripCmuxEnv`); launchd's HOME
  and PATH are enough (`TestLaunchdEnvIsEnough`). Only this package
  references `cmux.NewWorkspace` in the daemon (`TestNewWorkspaceOnlyInLaunch`).
- **post-relaunch check** (`internal/daemon/cmuxwatch`, wired in
  `cmuxcheck_wiring.go`) — `pigtaild --cmux-check-after-relaunch=off|report|pilot|fix`,
  default `pilot` (repair the pilot's session only, report the rest; no
  pilot = a report), and
  `--cmux-check-ctrl-c=rescue|unread` (default `rescue`). Every 30 s it reads
  the cmux sentinel pid; a new pid is a relaunch (or a crash). 90 s later,
  once cmux has resumed its sessions, it runs `pigtail cmux check --scope all
  --unattended [--fix-pilot | --fix] [--ctrl-c unread]` as a subprocess,
  outside any Claude cell and without the pane variables pigtaild may have
  inherited (`--unattended`: its `--surface` names nothing for Ctrl+C). In
  `pilot` or `fix` mode, after a pass that looked, it reads `last.json`
  (`Report.BusyLeft`): rows left busy that this mode repairs get one second
  pass `--surface …` 5 min later — in memory only, never retried, dropped if
  the cmux pid changes, forgotten by a pigtaild restart. The pilot is
  resolved by the check, in order: the one-shot marker `pilot.json` in the
  check's state dir (`pigtail cmux pilot`; set under an earlier cmux, at most
  2 h before this launch, its session still bound to that surface, marked
  used by the launch that consumes it), then the active maintenance's owner;
  neither = no pilot, a note. After its repairs the pilot is typed a message
  (`PilotText`, or `PilotTextCut` in place of its continue) once per cmux
  launch, by a pass pigtaild started only (`--unattended`, D13) — through
  the keyboard focus (also when the focus alone holds its repair), but never
  over a draft, never Enter on an input that no longer reads the exact text,
  and never erased; a busy pilot leaves it pending for the second pass. Once
  Enter has gone out it counts as typed, turn seen or not; `pilot-msg.json`
  (launch pid, surface, time), written by that pass only and read by every
  pass, remembers it — not `last.json`, which a fatal or non-master pass
  rewrites. `pigtail cmux pilot` outside the master never overwrites nor
  clears a pending marker bound to another scope's session. The
  pid checked is kept in `last-cmux-pid` (a daemon restart does not re-run
  it; with no such file, pigtaild's first start adopts the running cmux
  without a check), and an exit 2 (could not look) is retried, 3 attempts in
  all. The subprocess output goes to `daemon.log` next to `last.json`
  (`$XDG_STATE_HOME/pigtail/cmux-check/`): it holds the full output of a
  `--scope all` pass, so like `last.json` it is for the master only, and no
  message points an agent to it. The defects show as the `pigtail-chk` pill of
  the sidebar loop — pigtail posts no cmux notification. With the sidebar off
  the check still runs, and a check that exits 1 logs
  `cmuxwatch: sidebar off — no pill for the defects: pigtail cmux check --last`.
  The check (`internal/cmuxcheck`, in the `pigtail` process) reads a gate
  right before every keystroke (`Repairer.gate`): no Claude Code dialog on
  the pane, not the surface holding cmux's keyboard focus (`cmux identify`),
  not busy — the focused pane ends `busy: pane has keyboard focus`. An idle
  claude whose frozen-width screen is unreadable is quit by Ctrl+C (`ActQuit`:
  `\x03` bytes typed by `cmux send`, each press read back, never a signal)
  then started from its shell, under `--ctrl-c rescue|unread`; a `/exit` or a
  cleared restore line that does not read back on a frozen row is rescued the
  same way (`Repairer.rescue`). A `--fix` pass (never an inventory) writes
  its pid to `running` next to `last.json`, refreshes its mtime before each
  row and removes it at the end; the relay's idle wake reads it
  (`cmuxcheck.Running`: a live pid, refreshed within 20 min — older is a
  killed check whose pid may be reused) at the head of each sweep and right
  before typing, and holds meanwhile (`wake: held — …` / `wake: resumed — …`,
  one line per transition) — a file read, no cmux call.
- **Runaway watchdog** (`internal/daemon/watchdog`) — a single sweep loop
  over all workspaces. Dev servers are discovered by their **port**, read
  through `devserver.WorkspacePorts` (`.env.local`, registry fallback — the
  resolver rammon uses), then lsof → listener PID, keeping only the pids
  whose cwd is inside the workspace (no process-name matching), their
  process-subtree CPU is measured as a delta of accumulated CPU time over
  a sampling window, and only servers hot across multiple windows
  separated by a confirmation gap are killed (via the regular stop path,
  with a process-group fallback). Kills stamp the workspace
  (`last_runaway_at/-reason`) and emit `resources.warning` events.
  Per-project tuning lives under `watchdog:` in `pigtail.yaml`; the
  `pigtaild --watchdog=false` flag is the global kill switch.

Dev servers themselves are intentionally **not** daemon-spawned (the CLI
launches them detached, rediscovered by port) — the watchdog respects that
boundary by keying on ports, never on global process scans.

**Two port probes, not one.** Everything above discovers a dev server by
asking the OS who is listening on a port, but through one of two calls, and
the distinction is load-bearing:

- `devserver.IsPortBusy(port)` — one `lsof -t -sTCP:LISTEN -i :P` per call.
  For **one** port on a decision or interactive path: `pigtail dev`'s
  already-taken guard, the stop path, the watchdog's escalation, the rename
  guard. A machine-wide inventory would be wider and slower here.
- `devserver.ListListeners()` — one `lsof -nP -iTCP -sTCP:LISTEN -FpPn` for
  the **whole machine**, returning port → holding PIDs. For the two places
  that probe *every* configured port at once: `GET /servers` (via
  `rammon.snapshot`) and the RAM monitor's sweep (via `observeActivity`).
  Both used to fork one `lsof` per port, in series, which priced a single
  request at the number of worktrees ever created. The one semantic
  divergence between the two is a UDP-only port.

Both carry `-sTCP:LISTEN`, so a process merely *connected* to a port is never
mistaken for the server that owns it — a distinction paid for by an incident.

**Static servers are intentionally outside the watchdog's scope.** Static
servers (see §11) are in-process `http.FileServer` goroutines the daemon
spawns itself; they are not workspaces, hold no agent subtree, and cannot
run away — so the runaway watchdog, which sweeps *workspaces* only, never
touches them, and nobody should wire them in later.

---

## 11. Static servers

A **static server** is a named, persistent record that serves an arbitrary
directory of static files over HTTP. It is **not** tied to a git worktree —
it belongs to no project. Typical use: serving a folder of static HTML
mockups for review without spinning up a full dev server.

### Record vs running state

Two layers, deliberately separated:

- **The record** lives in the store (`static_servers` table): `{ id (ss_…),
  name (unique), dir (absolute), port, command (empty), created_at }`. This
  is durable — it survives daemon restarts.
- **The running state** lives only in memory, in the static-server
  **Manager** (`internal/staticsrv`). The Manager owns one `http.Server`
  goroutine per *running* server, each an `http.FileServer(http.Dir(dir))`
  bound to **127.0.0.1** on the record's port, serving a plain directory
  listing at `/`.

**Registered ≠ running.** Adding a static server only writes the record;
it does not start it. And the daemon does **not** auto-start registered
static servers on boot — they come up only on an explicit
`pigtail start <name>` (or the `…/start` endpoint). `pigtail serve ls`
reports both: the record fields plus a live `running` flag resolved against
the Manager.

### Why it's daemon-spawned (and what that implies)

Unlike dev servers — which the CLI launches detached and the daemon merely
rediscovers by port — a static server's HTTP listener runs *inside*
pigtaild. The daemon owns its lifecycle directly (start/stop/stop-all),
which is why removing or stopping a record can synchronously tear the
listener down. It also means static servers are **not workspaces** and are
therefore outside the runaway-watchdog's scope (see §10) — by construction.

### Port allocation

Static servers allocate from a **daemon-global** range
(`pigtaild --static-port-range`, default `8080-8099`; threaded into
`daemon.Config.StaticPortRange`, falling back to `DefaultStaticPortRange =
{8080, 8099}`) — global because static servers belong to no project. Their
ports are folded into the store's `UsedPorts(kind)` for every kind, so the
per-project worktree allocator can never reuse a static port and vice-versa.

The daemon holds **one** `ports.Allocator` (`daemon.NewAllocator`), shared
with the git backend and the handlers. A port is taken when any of these
says so:

- the registry (`UsedPorts`: every workspace row, every static server);
- the allocator's own pending set — a workspace's ports stay pending from
  `Backend.Create` until the handler has persisted the row (`Confirm`) or
  abandoned the create (`Release`);
- any port variable of any **live** workspace's `.env.local` (row in the
  store and directory on disk), whatever the kind: `pigtail dev` launches on
  that file, not on the registry;
- the machine: a bind probe on both loopbacks (`127.0.0.1` and `::1`, never a
  wildcard — no macOS firewall dialog; a host without IPv6 is tolerated) plus
  **one** `lsof` listener inventory per `Allocate`, which sees wildcard and
  LAN listeners. If `lsof` cannot run, the loopback binds decide alone and
  the failure is logged.

**"Up" means owned.** A port only says *something* listens. rammon, the
watchdog, the TUI and every stop path (`pigtail stop`, `stop --all`, the TUI
toggle, `rename --force`, the watchdog escalation) resolve listener pids to
their cwd (`devserver.ProcessCwds`, one `lsof -d cwd`) and count a pid for a
workspace only when it runs inside its path. Another worktree's listener is
a **conflict** (`rammon.Conflicts`), never killed; an unknown cwd is treated
the same way. `pigtail dev` refuses to start on such a port and names the
holder; `pigtail ports reassign` gives the worktree a fresh port.

SDK-held Claude sessions (a detached host per session, files read by the
daemon) are specified in `docs/wire-protocol.md` § SDK sessions.

---

## 12. cmux sidebar (daemon)

pigtaild can keep the cmux sidebar in order. This is optional and off by default; the rules below are pinned by tests (`internal/daemon/*_docs_test.go`).

**Opt-in flags.** The sidebar loop is started by `--cmux-sidebar` (or `pigtail cmux sidebar on`). `--cmux-folders` acts only with the loop on: without the loop there is no folder call at all. Folders are the only thing pigtail reorders in the sidebar, and only to arrange its own groups (`TestOnlyFoldersReorderOrGroup`).

**Sort switch.** The sort is on by default. With `--cmux-folder-sort=false` the loop files workspaces without sorting: no block move and no whole-window reorder, because cmux's `reorder-workspaces` puts pinned rows first and glues each group around them, so a folder holding a pinned member would rise (cmux probe P26). A tab that a band change, an unpin or an add has to move is moved alone with `reorder-workspace`, right after a neighbour of its own folder, never above a pinned one.

**Where pigtaild runs cmux.** pigtaild executes cmux in five places and nowhere else:

1. the sidebar loop (status and folder writes);
2. the dead-input probe (`ping` and `read-screen` only, each bounded at 3 s);
3. the relay's idle wake (`internal/daemon/handlers/relay_wake.go`);
4. the session launcher (`internal/daemon/launch`, `POST /subjects/{id}/launch`; the only code that references `cmux.NewWorkspace`, guarded by `TestNewWorkspaceOnlyInLaunch`);
5. the "go to session" reveal (`internal/daemon/handlers/reveal.go`, `POST /peers/{session}/reveal`; the only code that references `cmux.Reveal`, guarded by `TestRevealOnlyInRevealHandler`).

Every call aims at a surface by UUID and runs with no `CMUX_*` variable in its environment, except `CMUX_SOCKET_PATH` when `pigtaild -cmux-socket <path>` aims all calls at one instance. Tests use a simulated cmux, never the real sidebar.

---

## 13. Open questions

- Should the Backend interface include `Watch()` (for filesystem watchers
  on dev folders)? Probably yes for non-git backends.
- How does multi-backend project work (one repo with git + Notion side)?
  TBD — likely the recipe carries the backend kind, and Workspaces inherit.
- Backend plugins via Go plugin system vs HTTP plugin vs WASM? Decide
  when we add the 2nd backend.

These don't need to be answered for v1. Mentioned here so we keep them in
mind.
