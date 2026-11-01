# Wire protocol — `pigtail` CLI/TUI ↔ `pigtaild` daemon

> Contract between any pigtail client (CLI, TUI, MCP server, future web UI)
> and the `pigtaild` daemon. Stable, versioned, and the canonical interface.
>
> Audience: client authors, MCP plugin authors, integrators.
> Last updated: 2026-05-15.

---

## 1. Transport

### Unix domain socket (preferred)

```
~/.config/pigtail/pigtail.sock
```

- Single-user, local-only.
- Auth via filesystem permissions (mode 0600).
- Implicit uid match (no token needed).
- This is the default for `pigtail` CLI and TUI.

### HTTP loopback

```
http://127.0.0.1:<port>
```

- Same routes and payloads as the socket.
- Auth via bearer token in `~/.config/pigtail/token` (mode 0600, generated
  at daemon startup).
- Used by:
  - MCP server (`pigtail-mcp`) — talks to daemon over HTTP
  - The web front (`pigtail web`), with a session cookie instead of
    the token, on an exact GET allowlist — § Web (browser)
  - Anything cross-process that can't easily use Unix socket

The address comes from `pigtaild --http` (default `127.0.0.1:7800`; `""`
turns HTTP off). No file records it: a client is told it (`--http` on the
CLI) or reads it from the daemon (`POST /web/login-code` answers it).

### Versioning

All requests must include header:

```
X-Pigtail-API: v1
```

Daemon rejects requests with unsupported major version (`400` + clear
error). Minor changes are additive (new fields, new optional params) and
do not bump the major.

---

## 2. Format

Request: `application/json` for `POST` / `PATCH`.
Response: `application/json` for normal endpoints, `text/event-stream`
for `/events`.

### Standard response shape (success)

```json
{
  "data": { ... },
  "meta": { "request_id": "req_...", "took_ms": 12 }
}
```

### Standard response shape (error)

```json
{
  "error": {
    "code": "WORKSPACE_NOT_FOUND",
    "message": "no workspace with id 'ws_xyz'",
    "details": { "id": "ws_xyz" }
  },
  "meta": { "request_id": "req_..." }
}
```

### Error codes (stable, additive)

```
INTERNAL                  — unexpected; report bug
INVALID_REQUEST           — bad payload, schema, or params
UNAUTHORIZED              — token missing/invalid (HTTP only)
NOT_FOUND                 — generic 404
WORKSPACE_NOT_FOUND
AGENT_NOT_FOUND
PROJECT_NOT_FOUND
PROJECT_NOT_INITIALIZED
DELIVERY_NOT_FOUND
INVALID_RECIPE
INVALID_PROFILE
LAUNCH_OVER_MARGIN        — HTTP 409: dispatch/run refused, every account of
                            the profile's scope is over the usage margin;
                            over_margin: true launches anyway
PORT_EXHAUSTED            — no port available in configured range
BACKEND_ERROR             — git/fs/etc. failed; see details.cause
TMUX_NOT_AVAILABLE
GH_AUTH_REQUIRED          — gh CLI not authenticated
AGENT_SPAWN_FAILED
DAEMON_BUSY               — try again
CONFLICT                  — state transition not allowed
CPU_SLOTS_DISABLED        — the daemon runs with -cpu-slots=0; treat it
                            exactly like an absent daemon and run unarbitrated
ADMISSION_DISABLED        — the daemon runs with -admit-max-jobs=0; treat it
                            exactly like an absent daemon and run unadmitted
ADDRESS_UNRESOLVED        — relay: no live session matches the address
ADDRESS_AMBIGUOUS         — relay: several sessions match; details.candidates
SCOPE_MISMATCH            — relay: a message across scopes that is not a master
                            notice with X-Pigtail-Cross-Scope: write, or a scope unknown
RELAY_REFUSED             — relay: a limit refused the message (size, secret,
                            unread cap, rate, thread cap, self-reply)
DEV_SERVER_RUNNING        — HTTP 409: a dev server of the workspace survived
                            the stop, or its owner could not be established;
                            details.servers[{kinds,port,pids,reason}]; nothing
                            was removed (`force` does not lift it)
USAGE_UNAVAILABLE         — GET /usage/overview: not wired (501) or the cell
                            table / readings could not be read (503)
MAINTENANCE_FROZEN        — HTTP 423: a `pigtail maintenance on --freeze` is
                            in progress (or its state file is unreadable);
                            details.id|since|until|late, or
                            details.state="unreadable" + details.path
SESSION_LIVE              — HTTP 409: POST /subjects/{id}/launch, a live
                            session runs on a tab of the subject;
                            details.workspace_id|pids
LAUNCH_IN_PROGRESS        — HTTP 409: a CLI launch of the subject is under
                            way; details.launch_id|workspace_ref
LAUNCH_BURST              — HTTP 429: more than 10 launches in 10 min;
                            details.recent|frees_at
NO_CELL                   — HTTP 409: no cell for the subject's scope;
                            details.scope
BYPASS_REFUSED            — HTTP 403: the cell's default permission mode is
                            bypassPermissions; details.cell
CELL_MODE_UNREADABLE      — HTTP 503: the cell's settings.json is unreadable,
                            or its mode unknown; details.cell|cause
NO_LAUNCH_DIR             — HTTP 409: no live worktree, and no launch_dir
                            declared or existing for the scope; details.scope
CMUX_UNREACHABLE          — HTTP 503: cmux does not answer (windows or tabs
                            listing); details.cause
CMUX_CREATE_FAILED        — HTTP 502: new-workspace failed, or its tab was not
                            found after; details.cause
LIVE_UNKNOWN              — HTTP 503: Claude processes unreadable, a duplicate
                            cannot be ruled out; details.pids
REGISTRY_UNREADABLE       — HTTP 503: the launch registry (spawns.jsonl) is
                            unreadable; details.cause
```

HTTP status codes follow REST conventions (200/201/202 for success,
400/401/403/404/409/422/500 for errors). The `error.code` field is the
canonical machine identifier; status is for HTTP correctness.

---

## 3. Resource model (recap)

| Resource | Identifier prefix | Notes |
|---|---|---|
| Project | `prj_` | One per `pigtail.yaml` |
| Workspace | `ws_` | Per-subject, one or many agents |
| AgentSession | `agt_` | One agent in one workspace |
| Delivery | `del_` | E.g. GitHub PR |
| BacklogItem | `bli_` | Ideas not yet dispatched |
| Profile | `prf_` | E.g. claude/globex auth bundle |
| Recipe | `rec_` | Templated dispatch params |
| Event | `evt_` | Daemon emitted events |
| StaticServer | `ss_` | Named directory served over HTTP; tied to no project |

---

## 4. Endpoints

### Health & meta

```
GET  /health          → 200 { data: { ok: true, daemon_version: "0.1.0" } }
GET  /version         → 200 { data: { daemon_version, daemon_branch, daemon_commit, daemon_dirty, supported_apis } }
GET  /capabilities    → { backends: ["git"], agents: ["claude","codex"], ... }
```

`/version` carries the daemon's build identity, stamped by the Makefile
(`internal/buildinfo`): `daemon_branch` (`detached` on a detached HEAD),
`daemon_commit` (full sha), `daemon_dirty` (`true` | `false`: tracked files
modified at build time). An unstamped build serves `unknown` in all three; a
daemon older than the stamp omits them, and only `daemon_version` (`git
describe --tags --always --dirty`) tells what it is. `pigtail version
--daemon` prints them as one line; `pigtaild -version` prints the same line
for a binary, without a daemon.

`GET /health?deep=1` is the watchdog's probe: it pings the store (bounded at
2 s) and reads the role sweep's heartbeat. `200 { data: { ok: true } }` when
both are fine, `503 { data: { ok: false, failing: ["store: <error>", "<roles sweep message>"] } }`
otherwise. The plain `/health` is unchanged and stays a liveness answer. The
sweep is stale when its last beat is older than 3 intervals + 30 s; a daemon
started with `-role-watch=false` has no sweep and never fails on it.

### Maintenance

```
GET  /maintenance     → 200 { data: { dir, present, id?, on, freeze, late, unreadable? } }
```

What the daemon reads from the system maintenance state (`maintenance.json`
in `$PIGTAIL_MAINTENANCE_DIR`, else the socket's directory). Written by
`pigtail maintenance on|off` (a Claude session, or the user outside Claude
Code), never through the daemon.
The CLI compares `dir` and `id` with the state it just wrote and warns when
the daemon looks elsewhere. Never returns the reason nor the exempt session.
A state file that exists but cannot be read still answers 200, with
`present: true` and `unreadable` carrying the refusal text (path and repair).

**Freeze.** While a maintenance is on with `--freeze`, these writes answer
**423 `MAINTENANCE_FROZEN`** (message: since when, the reason, who set it and who lifts it):
`POST /themes`, `PATCH|DELETE /themes/{id}`, `POST /subjects`,
`POST /subjects/{id}`, `POST /subjects/{id}/phases`, `POST /phases/{id}`,
`POST /subjects/{id}/log`, `POST /journal`, `POST /workspaces/new`,
`POST /dispatch`, `POST /focus`, `POST /focus/{id}/members`,
`DELETE /focus/{id}/members/{kind}/{member}`, `POST /focus/{id}/end`. Reads, `POST /workspaces/{id}/run` and the
session<->subject link routes (`/subjects/{id}/cmux-workspaces`) are never
frozen. The owner (the session that ran `on`) and the session named at
`--exempt` pass: the CLI sends
`X-Pigtail-Session: $CLAUDE_CODE_SESSION_ID` on every request (declarative,
like `X-Pigtail-Scope`). An unreadable state file refuses every caller, the
owner and the exempt session included (`details.state: "unreadable"`, `details.path`).

**Frozen marker.** Each refused caller that named itself leaves a marker
`<maintenance dir>/maintenance-frozen/<session_id>` whose content is the
maintenance id, one line. Written by the daemon's 423 above, by
`pigtail session spawn`'s own refusal, and by the user's own merge guard — same
format. The cmux sidebar shows that session's workspace a red
`Blocked · freeze` while the maintenance with that id is on with a freeze; a
marker of another id is ignored. `pigtail maintenance off` removes the folder
once the lift succeeds. A header-less caller and an unreadable state leave none.

### Runtime claim

```
GET  /runtime/claim    → 200 { data: { dir, present, unreadable?, id?, session?, branch?, sha?, dirty?, pr?, since?, liveness?, expires_at? } }
POST /runtime/claim    body { session?, label?, label_scope?, pid?, config_dir?, branch?, sha?, dirty?, pr?, reason, take_over? }
                       → 200 (the GET view) | 409 CONFLICT | 400 INVALID_REQUEST
POST /runtime/release  body { session?, force? } → 200 { data: { released: "<id>" | null } } | 409 CONFLICT
```

The lease on the installed pigtail runtime (binaries, pigtaild, its
LaunchAgent plist) — D3 of sub_x000000000000005, `pigtail runtime
claim|release|status`. **The file is the truth**: `runtime-claim.json` (and
the one-line `runtime-claim.token` the hooks read) in the maintenance
directory, written under the flock of `runtime-claim.lock`; the daemon keeps
nothing in memory, and the CLI writes the file directly when the daemon is
unreachable or older than these routes (404), so a claim taken during a
bootout is the one the restarted daemon serves.

`session` empty = the user outside Claude Code. The same session claiming again
updates the lease (id and `since` kept). A lease held by another session
answers **409 `CONFLICT`** with `details: { id, session, since, liveness }`
— never the label, the branch nor the sha: the CLI names the holder by
reading the file. `take_over` replaces a holder whose `liveness` is
`stale` only; `force` releases for the user (`session` empty) or over a stale
holder. `liveness` = `alive | stale | unknown` (`unknown`: no pid or config
dir recorded — never taken over). A session id that is not one, or a reason
empty or over 500 bytes → **400**. No route returns the holder's label nor
the reason (D23). An unreadable file answers GET with `present: true` and
`unreadable` (path and cause), and is never overwritten by a claim (500).

**Expiry.** A lease lasts 4 h from its holder's last claim: a re-claim by the
same session keeps `id` and `since` and pushes `expires_at`; a file without it
expires at `since` + 4 h. At the deadline the lease is lifted like a release
(token `<id> off <epoch>`, file removed), by any route before it acts (expiry
is judged before the holder test, so a claim by another session succeeds; the
claim by the holder itself after the deadline makes a new lease), by pigtaild's
minute sweep, and by the CLI when the daemon did not answer for its directory.
`GET` lifts without waiting for the lock (busy: it only reads). The lift leaves
`runtime-claim-expired.json` (the lease and the instant), which the sweep
journals. A lease is never lifted before its deadline, nor an unreadable file.

### Projects

```
GET    /projects                      → []Project
GET    /projects/:id                  → Project
POST   /projects                      → create (usually `pigtail init`)
PATCH  /projects/:id                  → update config
DELETE /projects/:id                  → deregister (does not delete files)
```

`Project` payload:

```json
{
  "id": "prj_globex",
  "name": "globex",
  "root_path": "/Users/ada/code/globex",
  "remote": { "kind": "github", "owner": "globex", "repo": "globex-workspace" },
  "default_profile_id": "prf_globex",
  "default_backend": "git",
  "port_ranges": { "dev": [3000, 3099], "storybook": [6006, 6099] },
  "port_envs":   { "simulator": "PIGTAIL_SIM_PORT" },
  "defaults":    { "agent": "claude", "base_ref": "main", "branch_prefix": "feat/" }
}
```

- `port_envs` — optional map from port kind to env var name written into
  `.env.local`. Only present for kinds whose env name differs from the
  default. Defaults: `dev → PORT`, `storybook → STORYBOOK_PORT`,
  everything else → `<UPPER>_PORT`. Set this to host non-Node projects
  (Swift, Python, Rails, …) whose binaries expect a custom var name.
- `defaults` — optional project-wide dispatch defaults. Applied when
  the dispatch request omits the corresponding field. Explicit per-call
  overrides (`--agent`, `--branch`) still win.
  - `agent` — default `agent_kind` (e.g. `claude`, `codex`).
  - `base_ref` — default branch base for new worktrees (e.g. `main`).
  - `branch_prefix` — slug prefix prepended when no `branch_override`
    is provided (e.g. `feat/`, `research/`).

### Workspaces

```
GET    /workspaces                    → []Workspace
       ?project_id=&state=&agent_kind=&has_pr=&snoozed=

GET    /workspaces/:id                → Workspace (full)
POST   /workspaces                    → create (usually via /dispatch)
PATCH  /workspaces/:id                → update state, intent
DELETE /workspaces/:id                → cleanup
                                        body: { force?: bool, keep_branch?: bool }

POST   /workspaces/:id/snapshot       → backend.Snapshot()
                                        body: { message }
GET    /workspaces/:id/diff           → backend.Diff()
                                        query: ?from=&to=&session_scoped=
POST   /workspaces/:id/ship           → create delivery
                                        body: { title?, body?, draft?: bool }
GET    /workspaces/:id/status         → backend.Status() (cleanliness, conflicts)
GET    /workspaces/:id/preflight      → is this workspace safe to clean?
POST   /workspaces/:id/open           → open in browser/editor
                                        body: { target: "dev"|"storybook"|"diff"|"pr" }
PATCH  /workspaces/:id/ports          → resync | reassign the workspace's ports
                                        body: { mode: "resync"|"reassign", kinds?: [string] }
POST   /workspaces/:id/hooks/run      → re-run the post-create hooks, record the outcome
```

**`DELETE /workspaces/:id` is synchronous and can take minutes.** Removing a
worktree with a populated `node_modules` is tens of thousands of `unlink()`
calls, so the response is held for the whole teardown. Three consequences for
clients:

- **A client disconnect does not cancel it.** The daemon detaches the teardown
  from the request context on purpose: killing git halfway through
  `worktree remove` leaves a directory full of deleted tracked files. A client
  that gives up should say the teardown continues in the background and point
  the user at `GET /workspaces`, not present it as a failure.
- **Budget accordingly.** The daemon caps a teardown at 30 minutes as a
  hung-git backstop; the UX budget belongs to the client (`pigtail clean
  --timeout`, default 10 minutes, per workspace).
- **A second concurrent DELETE on the same workspace gets `409 conflict`**
  (`cleanup already in progress for this workspace`). Two concurrent
  `git worktree remove` on one path corrupt each other's administrative
  records.

**Dev servers are stopped first.** Before any agent kill, backend teardown or
state removal, the daemon stops every dev server listening on the workspace's
ports (from `.env.local`, the database row as the fallback) from inside the
worktree, with its descendant processes, and verifies for up to 5 s (counted
after the kill) that the ports are free and the processes gone. A listener
whose cwd is known and outside the worktree is another worktree's server and is
left alone. Anything that cannot be attributed (a failed port or cwd probe, an
unreadable cwd) or that survives refuses the clean with `409
DEV_SERVER_RUNNING` and removes nothing, `force` included: `force` skips the
guards on the user's work, never this proof. The response lists what was
stopped in `servers_stopped` (`[{kinds, port, pids}]`, absent when none).

On failure the daemon emits `workspace.cleanup_failed` and keeps the workspace
row, so the operation can be retried once the cause is fixed.

**A `200` does not mean everything is gone.** The response reports what the
teardown actually did to the work ref, and a client that prints an unqualified
success is lying to the user (issue #53):

```json
{
  "workspace_id": "ws_fb94d9",
  "branch_kept": "fix/entity-open-button-reveal-scope",
  "branch_kept_reason": "the worktree was on \"fix/entity-open-button-reveal-scope\", not the recorded \"feat/fix-entity-open-button\" — pigtail will not guess which of the two to delete"
}
```

- `branch_deleted` — the branch the teardown removed. Absent when none was.
- `branch_kept` / `branch_kept_reason` — a branch the teardown declined, or
  failed, to remove, with a clause fit to show the user. **Clients MUST surface
  these** next to their success message.

Only *surprising* outcomes are reported. A branch retained because the caller
passed `keep_branch`, or because it is unmerged on the non-force path, is the
documented behaviour and leaves all three fields empty. Backends with no branch
concept always leave them empty, so a client must treat them as optional.

`Workspace` payload:

```json
{
  "id": "ws_h7s9",
  "project_id": "prj_globex",
  "intent": "Refactor auth flow to support refresh tokens",
  "state": "building",
  "path": "/Users/ada/code/globex-feat-auth-refactor",
  "ports": { "dev": 3007, "storybook": 6013 },
  "recipe_id": "rec_feat_ui",
  "backend_kind": "git",
  "backend_data": { "branch": "feat/auth-refactor", "base_branch": "main" },
  "created_at": "2026-05-15T10:30:00Z",
  "last_activity_at": "2026-05-15T14:22:00Z",
  "snoozed_until": null,
  "agent_sessions": [ { ... } ],
  "active_delivery_id": null,
  "diff_summary": { "additions": 184, "deletions": 22, "files": 6 },
  "hooks": { "status": "failed", "hook": "husky", "error": "hook husky failed: exit status 3",
             "log": "/Users/ada/.local/state/pigtail/servers/<wt>/hooks.log", "at": "2026-05-15T10:34:10Z" }
}
```

`hooks` is the recorded outcome of the project's post-create hooks
(`HooksResult`): `status` is `running`, `ok` or `failed`; `hook` (the hook
that stopped) and `error` are set on `failed` only; `log` is the worktree's
`hooks.log`. **Absent = unknown** — a workspace created before it was
recorded (migration 0023), or a project without hooks — never a failure. It
is returned by `GET /workspaces` / `GET /workspaces/:id` and by `POST
/workspaces/:id/hooks/run`, **not** by the response of `POST
/workspaces/new` (the hooks' failure there is the 500 body, which names the
hook and the log).

**`GET /workspaces/:id/preflight`** answers "is this workspace safe to tear
down?" by joining the backend's view with the delivery's:

```json
{
  "workspace_id": "ws_h7s9",
  "branch": "feat/auth-refactor",
  "git": {
    "clean": true,
    "ahead": 4,
    "behind": 12,
    "merged": false,
    "merge_status": "contained",
    "merged_how": "squash-merged",
    "merge_base_ref": "origin/main",
    "merge_note": "carried by 03c85e73 in origin/main"
  },
  "delivery": { "url": "…", "state": "merged", "ci_status": "success" },
  "verdict": "safe"
}
```

`merge_status` is the containment answer and it is **three-valued**:

- `contained` — the work is provably present in the base ref. `merged_how`
  says which detection path proved it (`merged` = the work ref is an ancestor
  of the base; `squashed` = every commit is patch-equivalent to one already in
  base; `squash-merged` = the branch's cumulative change is carried by one
  commit in base, which is what a multi-commit squash leaves behind).
- `absent` — every available check ran and found no trace. **Not** a proof
  that the work is unmerged: a squash whose conflicts were resolved by hand
  leaves nothing mechanical to follow.
- `unknown` — the question could not be answered (missing ref, vanished
  workspace directory, exhausted scan budget, cancelled context). `merge_note`
  carries the reason.

Clients that destroy things **must** keep `absent` and `unknown` apart and
report the `unknown` count, so a sweep that classified nothing never reads as
"nothing to clean" (issue #54). The field is empty when the backend implements
no containment check at all; `merged` (the narrow ancestor test) stays
populated for older clients.

**`PATCH /workspaces/:id/ports`** repairs a disagreement between the
registry (`Workspace.ports`) and the workspace's port file (`.env.local` for
the git backend, which is what `pigtail dev` launches on). CLI: `pigtail
ports resync|reassign`. Unscoped (workspace layer, `classNoSubject`) and
outside the maintenance freeze, like `rename`.

```json
{ "mode": "resync" }
{ "mode": "reassign", "kinds": ["dev"] }
```

```json
{
  "workspace_id": "ws_efb2e9",
  "ports": { "dev": 3013, "storybook": 6010 },
  "changed": [ { "kind": "dev", "from": 3000, "to": 3013 } ]
}
```

`changed` is `[]` when nothing differed.

- **`resync`** — registry ← file, for every port kind of the project whose
  file value differs from the registry. Each new port obeys the allocator's
  "taken" rules (minus the bind probe: the port is expected to be this
  workspace's own server), and every reservation is released if any kind is
  refused, so a refusal writes nothing.
- **`reassign`** — for each kind asked (`kinds` empty = every kind of the
  project): a fresh port from the allocator (same rules as `pigtail new`),
  written into the file (only that variable's line, the rest intact), then
  recorded. Allowed when the current port is held only by another worktree's
  process — that is the case it repairs.

Refusals (standard error envelope, no new code):

| Status | Code | When | `details` |
|---|---|---|---|
| 400 | `INVALID_REQUEST` | bad JSON, `mode` not `resync`/`reassign`, unknown kind in `kinds` | `{mode}` / `{kind}` |
| 404 | `WORKSPACE_NOT_FOUND` / `PROJECT_NOT_FOUND` | unknown workspace, or its project is gone | `{id}` / `{project_id}` |
| 409 | `CONFLICT` | resync: the port is registered to another workspace or a static server | `{kind, port, reason: "registered", held_by_workspace}` |
| 409 | `CONFLICT` | resync: the port is being allocated to another workspace, or named in another live workspace's `.env.local` | `{kind, port, reason: "pending"}` / `{kind, port, reason: "env_local", holder_path}` |
| 409 | `CONFLICT` | resync: a listener running outside the worktree holds the port (unknown cwd counts as outside), or its owner is unknown — the cwd probe failed twice (`owner_unknown: true`, `holder_path` empty; retry) | `{kind, port, holder_path, pids[, owner_unknown]}` |
| 409 | `CONFLICT` | reassign: the workspace's own server is running on the current port — `pigtail stop` first | `{kind, port, pids}` |
| 409 | `CONFLICT` | reassign: the current port's owner is unknown (cwd probe failed twice) — it may be the workspace's own server; retry | `{kind, port, holder_path: "", pids, owner_unknown: true}` |
| 409 | `PORT_EXHAUSTED` | reassign: no free port left in the kind's range | |
| 422 | `BACKEND_ERROR` | the backend keeps no port file; the file holds a value that is not a port (resync); the port could not be probed; the file could not be written | |
| 500 | `INTERNAL` | reassign wrote the file, then the registry refused: the file is restored byte for byte and the port released — "could not record the new ports: … — .env.local restored, nothing changed". If the restore fails too, the message names both errors and the file. | `{path}` when the restore failed |

**`POST /workspaces/:id/hooks/run`** re-runs the post-create hooks of
`pigtail.yaml` in an existing workspace, on the daemon's own lifetime and
with the same budget as at creation (`devserver.HooksTimeout`, 10 min for
the whole sequence). Body empty. CLI: `pigtail hooks run [workspace_id]`.
Unscoped (workspace layer, `classNoSubject`).

Once the hooks end it answers **200 + `Workspace`** with its `hooks` field
up to date — `ok`, or `failed` naming the hook — whatever the outcome: a
failed hook is data, not a transport error. A client that leaves does not
stop the hooks; their outcome is recorded and shown by `GET /workspaces`.
The whole sequence runs again, hooks that passed before included.

| Status | Code | When |
|---|---|---|
| 400 | `INVALID_REQUEST` | the project declares no post-create hooks, or its `pigtail.yaml` cannot be read |
| 404 | `WORKSPACE_NOT_FOUND` | unknown workspace (`details.id`) |
| 409 | `CONFLICT` | the hooks are already recorded `running` (claimed in one conditional update, so two runs never overlap); the message names `hooks.log` |

### Dispatch (high-level)

```
POST   /dispatch
  body: {
    project_id: "prj_globex",
    intent: "fix the date picker locale",
    recipe_id: "rec_fix_ui",          // optional, falls back to default
    profile_id: "prf_globex",     // optional
    agent_kind: "claude",             // optional, recipe default
    plan_first: true,                 // optional, recipe default
    branch_override: null,            // optional, uses slug from intent
    over_margin: false,               // optional, see « Launch margin » below
    fanout: null                      // optional, see below
  }

  → 201 { data: { workspace_id, agent_session_id, margin? } }
  → 409 LAUNCH_OVER_MARGIN (nothing created)
  → emits events: workspace.created, agent.started

POST   /workspaces/:id/run            → spawn one more agent in an existing workspace
  body: { intent, profile_id?, agent_kind?, plan_content?, plan_filename?, plan_first?, over_margin? }
  → 201 { data: { workspace_id, agent_session_id, margin? } }
  → 409 LAUNCH_OVER_MARGIN (no plan file written, no agent)
```

**Launch margin** (sub_x000000000000011). Before anything is created or
written, the daemon resolves the profile and, when its `config_dir` is a
cell of the cell table, runs the launch margin guard (the one `pigtail usage
pick --cell` and `pigtail session spawn` run): an account under the margin
launches as asked; past its account's ceiling (5 h or 7 d at or above
`select_pct`, default 90 %), the launch
switches to a free account of the same scope — `CLAUDE_CONFIG_DIR` changes
for this launch only, `pigtail.yaml` is never touched; when every account of
the scope is over the margin the call is refused, `409 LAUNCH_OVER_MARGIN`,
with the first release time, unless `over_margin: true` (decision
`forced`). A profile with no `config_dir`, one that matches no cell, or a
guard that cannot measure (`PIGTAIL_ROTATION=off`, no candidate) launches as
before, decision `unmeasured`. A request with no profile is not guarded.
`margin` is present when the decision is not `ok`:

```json
{ "decision": "switched|forced|unmeasured", "requested": "h1", "final": "h2",
  "warning": "launch margin: cell h1 is over the margin — launching on h2 instead" }
```

These routes are unscoped (the caller's scope is unknown), so every margin
text — `warning`, the refusal message, the trace — is the bounded form: cell
ids and the release time, never an account nor a scope name. Each decision
but `ok` is traced: one `kind: "margin"` entry in `spawns.jsonl` and one
journal row (`kind: launch-margin`, source `pigtail:spawns`, in the
requested cell's scope). The CLI (`pigtail dispatch`, `pigtail run`) prints
`warning: launch margin: …` on stderr, and exits 3 on the refusal.

For fanout:

```json
{
  ...,
  "fanout": [
    { "agent_kind": "claude", "count": 2 },
    { "agent_kind": "codex", "count": 1 }
  ]
}
```

→ creates N workspaces + N agent sessions, all linked by a `fanout_id`.
Response is an array.

### Agent sessions

```
GET    /agents                        → []AgentSession
       ?workspace_id=&state=

GET    /agents/:id                    → AgentSession (with messages truncated)
GET    /agents/:id/messages           → []Message (full)
                                        query: ?since=<timestamp>&limit=100

POST   /agents/:id/reply              → send user input
                                        body: { content }
POST   /agents/:id/attach_screenshot  → attach an image to next message
                                        body: { path | base64 }
POST   /agents/:id/pause              → pause
POST   /agents/:id/resume
POST   /agents/:id/kill
POST   /agents/:id/handoff            → switch to another agent kind
                                        body: { agent_kind: "codex" }
```

`AgentSession` payload:

```json
{
  "id": "agt_42",
  "workspace_id": "ws_h7s9",
  "agent_kind": "claude",
  "profile_id": "prf_globex",
  "state": "paused_waiting_input",
  "tmux_session_name": "pigtail-ws_h7s9-agt_42",
  "started_at": "2026-05-15T10:30:12Z",
  "last_event_at": "2026-05-15T12:08:45Z",
  "external_session_id": "claude-sess-abc",  // for `claude --resume`
  "message_count": 14
}
```

### Deliveries

Cached/derived from external systems (GitHub via gh CLI + webhooks).

```
GET    /deliveries                    → []Delivery
       ?workspace_id=&state=

GET    /deliveries/:id                → Delivery (full)
POST   /deliveries/:id/refresh        → force refresh from upstream
POST   /deliveries/:id/comment        → reply to a comment
                                        body: { thread_id, content }
POST   /deliveries/:id/address_all    → dispatch agent to address all comments
POST   /deliveries/:id/merge          → backend.Merge()
POST   /deliveries/:id/cancel
```

`Delivery` payload:

```json
{
  "id": "del_1502",
  "workspace_id": "ws_h7s9",
  "kind": "github_pr",
  "external_ref": { "owner": "globex", "repo": "globex-workspace", "number": 1502 },
  "url": "https://github.com/.../pull/1502",
  "state": "ready",
  "ci_status": "passing",
  "reviewers": [
    { "login": "marie", "state": "pending" },
    { "login": "thomas", "state": "pending" }
  ],
  "comments": [
    { "id": "...", "author": "marie", "resolved": false, "file": "Toast.tsx", "line": 142, "body": "..." }
  ],
  "unresolved_comments": 3,
  "mergeable": true,
  "auto_merge": true,
  "last_synced_at": "2026-05-15T14:22:00Z"
}
```

### Triage (computed)

```
GET    /triage                        → []TriageItem
       ?repo=&category=attention&sort=priority
```

Server-computed view across workspaces + agents + deliveries + CI.

```json
[
  {
    "workspace_id": "ws_h7s9",
    "category": "attention",
    "reason": "agent paused, needs decision on token storage",
    "priority_score": 0.92,
    "age_seconds": 7200,
    "summary": { "intent": "...", "agent_state": "...", "diff": "+184 −22" }
  }
]
```

### Backlog

```
GET    /backlog                       → []BacklogItem
       ?project_id=&tag=&state=

GET    /backlog/:id                   → BacklogItem
POST   /backlog                       → add
PATCH  /backlog/:id                   → edit
DELETE /backlog/:id
POST   /backlog/:id/dispatch          → convert to workspace
                                        body: { recipe_id?, profile_id?, ... }
POST   /backlog/groom                 → run groomer agent
                                        body: { mode: "dedupe"|"prioritize"|"all" }
```

`BacklogItem`:

```json
{
  "id": "bli_99",
  "project_id": "prj_globex",
  "title": "fix date picker locale",
  "description": "doesn't respect i18n config",
  "tags": ["fix", "ui"],
  "size_estimate": "S",
  "area": ["src/components/date/"],
  "priority": 7,
  "state": "ready",
  "created_at": "..."
}
```

### Static servers

Named, persistent records that serve a directory of static files over HTTP
via an in-process file server inside the daemon, bound to `127.0.0.1`. Tied
to no project. **Registered ≠ running**: creating a record does not start
it, and the daemon never auto-starts them on boot.

`pigtail serve add` also registers the server as a `server` output (path = its
dir, url = `http://127.0.0.1:<port>`, title = its name) of `--subject`, else of
the pane's bound subject (§ Subject outputs); no subject or a dir named
`_outputs` registers nothing, and a failed registration is one `warn: output
not registered: …` line on stderr, never a failure.

```
POST   /static-servers                → 201 register a static server
GET    /static-servers                → 200 []StaticServerView (records + running state)
POST   /static-servers/stop-all       → 200 stop every running static server
DELETE /static-servers/{name}         → 200 unregister (stops it first if running)
POST   /static-servers/{name}/start   → 200 start the file server
POST   /static-servers/{name}/stop    → 200 stop the file server
```

`POST /static-servers` — register:

```json
{ "name": "mockups", "dir": "/Users/ada/mockups", "port": 8081 }
```

- `dir` is resolved to an absolute path and must exist and be a directory.
- `port` is optional; omit it to auto-allocate from the daemon-global static
  range (`pigtaild --static-port-range`, default `8080-8099`).
- `→ 201` with the `StaticServer` payload.
- `→ 400 INVALID_REQUEST` if `name`/`dir` are missing or `dir` is not an
  existing directory.
- `→ 409 CONFLICT` if the name already exists, the port is already in use,
  or the port is not bindable.

`StaticServer` payload:

```json
{
  "id": "ss_a1b2",
  "name": "mockups",
  "dir": "/Users/ada/mockups",
  "port": 8081,
  "command": "",
  "created_at": "2026-06-12T10:30:00Z"
}
```

`GET /static-servers` → `200` array of `StaticServerView` — the record plus
live running state:

```json
[
  {
    "id": "ss_a1b2",
    "name": "mockups",
    "dir": "/Users/ada/mockups",
    "port": 8081,
    "command": "",
    "created_at": "2026-06-12T10:30:00Z",
    "running": true,
    "running_port": 8081
  }
]
```

`running_port` is present only when up and equals the record `port`.

`POST /static-servers/{name}/start` → `200` `StaticServerView`
(`running: true`, `running_port`). `→ 404 NOT_FOUND` (no such server),
`→ 409 CONFLICT` (cannot bind / start).

`POST /static-servers/{name}/stop` → `200 { "name": "...", "running": false }`.
`→ 404`, `→ 409`.

`DELETE /static-servers/{name}` → `200 { "name": "...", "id": "ss_..." }`
(stops it first if running). `→ 404 NOT_FOUND`.

`POST /static-servers/stop-all` → `200 { "stopped": ["mockups", ...] }` —
the names of the servers that were running.

#### Outputs server

A second, permanent loopback listener of pigtaild (sub_x000000000000025, D1)
that serves, read-only, the files of the subject outputs registry (§ Subject
outputs) and nothing else: an origin distinct from `--http`, so a served page
never shares the web UI's origin. No record, no `static_servers` row: it lives
and stops with the daemon, and binds again at every start.

- **Address**: `pigtaild -outputs-http <host:port>`, default
  `127.0.0.1:7801` when `-http` is on, **off** when `-http ""`; an explicit
  `-outputs-http ""` turns it off too. The daemon refuses to start when the
  address equals `-http` or is not a loopback one (`127.0.0.1`, `::1`,
  `localhost`). A failed bind (port taken, a second pigtaild) is one log line
  `outputs: listener … off: …`, never fatal: the listener stays off and no
  output carries `served_url`.
- **What it opens**: every scope's non-purged rows that have a `path`. A
  `mockup`, `report` or `html` output inside a round folder (the parent of
  its file, or the folder itself when the path is one, named
  `^t(\d+)([-_].*)?$` as in § Subject outputs, Groups) opens that round
  folder whole; outside a round it opens its file alone (a folder path: its
  own `index.html` alone, no sibling, no subfolder); a `capture`, `pdf` or
  `file` output opens its file alone; `server`, `pr`, `clip`, `link` nothing.
  Paths are resolved through their symlinks; an output whose folder equals
  `$HOME`, a scope's `launch_dir` (the loaded scope table), or holds a `.git` is
  refused and logged, never served. The index is rebuilt when the registry's
  fingerprint (`MAX(seq)` + purged count) moves — a new output is served at
  its next request, a purged one stops being.
- **URL**: the file's absolute path, each segment escaped
  (`http://127.0.0.1:7801/Users/…/mockup/t3/`): a round's relative links
  (`../t2/`) work when that round is registered too. `…/index.html` answers
  301 to `…/`; a folder opens only through its own `index.html` (no listing).
- **Refusals**: a method other than `GET`/`HEAD` → 405; a `Host` other than
  the bound address or `localhost:<its port>` → 403; a segment starting with
  `.` (`..`, a dotfile) → 404; a path whose symlinks lead out of the opened
  folders and files → 404; anything else outside the registry → 404, without
  saying why.
- **Headers**: `X-Content-Type-Options: nosniff`, `Cache-Control: no-cache`,
  `Content-Security-Policy: frame-ancestors http://<-http address>
  http://localhost:<its port>` (`'none'` when `-http` is off). The web UI
  frames these pages in an `iframe sandbox`; a served page cannot write to
  the API: a request from its origin carries `Origin:
  http://127.0.0.1:7801`, refused 403 on the cookie path (§ Web (browser)).

### Dev-server monitoring

#### `GET /servers`

Read-only introspection of running dev servers — memory (physical
footprint on darwin, VmRSS on Linux) and idle time per server, plus system
memory headroom vs. the eviction floor.
Socket-trusted, no token required. Returns `200` always (empty `servers`
list when no RAM monitor is active).

A server is listed only when a listener pid runs inside the workspace's path
(its cwd, resolved through symlinks; an unknown cwd counts as outside); its
port is read from `.env.local`, falling back to the registry. Another
worktree's process on that port is never listed, stopped or evicted under
this workspace.

```
GET /servers → 200 ServersResponse
```

`ServersResponse` payload:

```json
{
  "available_bytes": 8589934592,
  "floor_bytes": 6442450944,
  "servers": [
    {
      "workspace_id": "ws_h7s9",
      "server": "dev",
      "port": 3007,
      "rss_bytes": 524288000,
      "idle_seconds": 120,
      "owner": "sid:4711@laptop",
      "owner_alive": true
    }
  ],
  "recent_deaths": [
    {
      "workspace_id": "ws_k2m1",
      "server": "dev",
      "servers": ["dev", "web"],
      "port": 3016,
      "ago_seconds": 240
    }
  ],
  "main_worktrees": [
    {
      "project_id": "prj_globex",
      "project_name": "globex",
      "path": "/Users/me/dev/globex-workspace/main",
      "base": "origin/main",
      "action": "skipped",
      "reason": "working tree not pristine (2 uncommitted or untracked entries)",
      "behind": 23,
      "checked_ago_seconds": 118
    }
  ],
  "port_conflicts": [
    {
      "workspace_id": "ws_efb2e9", "server": "dev", "kind": "dev",
      "port": 3013, "registered_port": 3000, "reason": "env_diverges",
      "fix": "pigtail ports reassign ws_392f9b --kind dev && pigtail ports resync ws_efb2e9"
    },
    {
      "workspace_id": "ws_392f9b", "server": "dev", "kind": "dev",
      "port": 3013, "registered_port": 3013, "reason": "foreign_listener",
      "holder_path": "/Users/me/dev/big-web/efb2e9", "holder_workspace_id": "ws_efb2e9",
      "fix": "pigtail ports reassign ws_392f9b --kind dev && pigtail ports resync ws_efb2e9"
    }
  ]
}
```

- `available_bytes` — system free RAM at the time of the call.
- `floor_bytes` — the configured eviction floor (`--ram-floor-mb`).
- `servers[].rss_bytes` — memory of the server's process tree: physical
  footprint on darwin (top's MEM column, compressed pages included), VmRSS on
  Linux. The key keeps its name for wire compatibility (Pigtail, older
  CLIs); until 2026-10 it carried `ps` RSS, which leaves compressed memory out.
- `servers[].idle_seconds` — seconds since the server last counted as in use.
  Every sweep the daemon samples per-process CPU; the server is in use while,
  over the last 5 minutes, its subtree's mean CPU (divided by real elapsed
  time) is at least `pigtaild -idle-cpu-threshold-percent` (share of one core,
  default `2.5`). Rule D8: use is not guessed from the network — a server to
  keep at rest is pinned (`POST /servers/{ws}/{server}/pin`). With `pigtaild -idle-net-signal`
  (off by default) a navigation burst also counts: at least `K` = 4 new
  connections on the server's port (ESTABLISHED endpoints via lsof, TIME_WAIT
  keys via netstat/ss, absent at the previous sweep) in one sweep. The same
  signal for a cmux sidecar and a background daemon.
  A server the daemon has never sampled reports `0`, and so does a server for
  its first 5 minutes — less than one window is never read as idle. The
  tracker and its window are persisted next to the database
  (`rammon-state.json`), so a `pigtaild` restart continues them. With
  `-idle-net-signal` on, each server's peer set (the connections seen at the
  last sweep) is persisted too; with it off there is none — the set is
  dropped at the first sweep, so turning the signal back on starts from a
  fresh baseline. A restored
  figure is reported at once but no eviction rests on it until this daemon has
  observed the server. Never evicting on an unknown key is deliberate:
  uncertainty must not kill.
  (Before 2026-07-27 this was the mtime of `.pigtail-<server>.log`, a
  file only the background-daemon launch path writes — so a cmux sidecar
  read as idle-forever and a worktree with an old log read as idle for
  days. That proxy is gone.)
- `owner` — the session claiming this server; absent when unclaimed.
  `sid:<n>@<host>` is derived from the POSIX session id; anything else is an
  opaque identity (a cmux workspace id, an exported `PIGTAIL_OWNER`).
- `owner_alive` — `omitempty`, so it is only ever present as `true`: it
  appears exactly when `owner` is present *and* that owner is confirmed
  alive (an opaque identity, or a derived identity whose session leader is
  still running). It is **absent** in two different situations, which a
  caller tells apart by whether `owner` itself is present: an unclaimed
  server (no `owner` either), or a claimed server whose derived owner's
  session leader is gone — the case `pigtail stop --all` reclaims (`owner`
  is present, `owner_alive` is not). There is no wire representation of
  "confirmed dead" — only "confirmed alive" or "not confirmed alive".
- `pinned_until` — a timed pin's expiry, Unix seconds; omitted when the server
  is not pinned or is pinned until unpinned.
- `pinned_durable` — `true` when the server is pinned until unpinned; omitted
  otherwise. A client that predates it reads such a server as unpinned
  (display only: the daemon still honours the pin).
- `recent_deaths` — dev servers that vanished on their own (crash, OOM
  kill, a kill from outside pigtail) inside the death detector's 30-minute
  reporting window, oldest first. Omitted/empty when there are none — this
  is the only place any client (CLI, menu bar, TUI) can see what
  `resources.server_died` announced live, so a poller that only checks
  `GET /servers` (rather than staying subscribed to `/events`) still finds
  out. `ago_seconds` is computed fresh on every call from the detector's
  stored timestamp, not cached.
- `recent_deaths[]` — one entry per **process**, not per server name. A
  project may declare several servers on one port (a large monorepo lists `dev` and
  `web` on the dev port): they are one process, so its end is one death.
  `servers` names every server of that workspace last seen on the port,
  sorted; `server` is `servers[0]`, kept for clients that only read it.
  `servers` is `omitempty` on the type, but the daemon always fills it (a
  death restored from a state file older than the field gets `[server]`);
  a client talking to an older daemon falls back to `server`.
- `main_worktrees` answers "is the checkout I am about to read fresh?" — the
  question asked *before* pointing an agent at a project's `main` directory.
  It is served **from the daemon's last refresh verdict, never computed on the
  request**: this endpoint has to stay a fast read, and a display path that
  shelled out to `git fetch` would make the freshness check the slowest command
  in the tool — one nobody runs. `action` is the three-valued verdict
  (`updated` / `up-to-date` / `skipped`), `behind` how many commits of `base`
  the checkout was missing, `reason` why an abstention abstained.
  `checked_ago_seconds` is recomputed on every call: every line here is a claim
  *as of a moment*, and without it a daemon that stopped sweeping an hour ago
  would keep answering "level with origin/main" in the present tense.
- `behind: 0` means level **or unmeasurable** (no remote-tracking base, an
  unreachable remote). Read it as "no known drift", never as proven freshness.
- The list is empty when the daemon holds no verdict at all — nothing
  registered, a daemon that just started, or `-refresh-main=false`. That is
  nothing *known*, not nothing *wrong*; a client must render it as silence.
- `port_conflicts` — workspaces whose port is in conflict, each with `fix`:
  **one** shell command that repairs it and runs as shown in the state the
  daemon observed (races aside: a server (re)started meanwhile, an owner
  unknown for a moment, an allocation in flight). `fix` may chain steps with
  ` && `; the daemon plans it against `PATCH /workspaces/{id}/ports`' own
  refusals (`ports.Allocator.ResyncBlockers`), per workspace — `resync` covers
  every kind, so blocked kinds are reassigned first. Entries one repair
  covers carry the **same** `fix` (e.g. W's `env_diverges` and H's
  `foreign_listener` when H registers W's port and W's server holds it:
  `pigtail ports reassign H --kind dev && pigtail ports resync W`). Fixes
  never contradict each other: entries one repair covers carry the same
  `fix`, and when one port carries different fixes (e.g. W gives the port up
  while H, which registers it, keeps its own repair), each runs as shown, in
  any order. A fix that gives a port up while the
  workspace's server runs on it starts with `(cd <path> && pigtail stop
  --server <name>) && ` (a sub-shell: the caller's directory is left as
  is); every inserted word is shell-quoted when it needs to be. Kinds no
  server is declared for are not checked: when one of them diverges and is
  blocked, a `resync` fix is refused on it. Omitted when there are none. `reason`: `env_diverges` —
  `.env.local` names `port` while the registry says `registered_port`
  (recorded whether or not the server runs); `foreign_listener` — `port` is
  held by a process running outside the worktree, never listed under it
  (`holder_path` is the owning worktree, else the holder's cwd, omitted when
  unknown; `holder_workspace_id` the live workspace containing it).
  `pigtail servers` marks a conflicted row `⚠port` and prints these under
  `port conflicts:`, each with its `fix`; `pigtail list` marks the workspace
  `⚠port` only.
- Death detection itself only runs while the daemon's RAM-pressure sweep
  loop is running: `pigtaild --ram-budget=false` disables that loop
  entirely (see `POST /battery` and the `-ram-budget` flag), so
  `recent_deaths` stays empty for the whole session even if a server dies —
  not because nothing died, but because nothing was watching.

#### `POST /servers/{ws}/{server}/pin`, `DELETE /servers/{ws}/{server}/pin`

`POST …/pin?duration=30m|1h|4h|until-unpinned` pins a running server; `400`
for another value, `404` when the server is not running. Response:
`{"workspace", "server", "pinned_until": <unix or 0>, "pinned_durable": true}`
(`pinned_durable` only for `until-unpinned`). A new pin replaces the previous
one, whatever its kind. `DELETE …/pin` clears either kind; idempotent.

A pin protects the server from every eviction the daemon performs on its own —
idle purge, the server cap, battery eviction — and from `stop --idle`, but
**not from the RAM floor (mains and battery, D9/D11)**: under the floor a
pinned server is evictable like any other. IDLE keeps counting during a pin. Pins are keyed
by workspace and server name (a server restarted by `pigtail dev` keeps its
pin), persisted in `rammon-state.json` (`pins`), restored at daemon start
(durable ones whatever the file's age, timed ones until their expiry), not
cleared when mains power returns, and dropped when their workspace is gone.

#### `GET /backup`

The daemon's classification of the offsite backup destination — the same
verdict `pigtail db list` renders on its "status:" line, read from the same
file (`offsite-state.json`, beside the database) and classified by the same
pure function (`store.ClassifyOffsite`). This endpoint holds no state of its
own and computes nothing new: it re-reads that file on every call, which is
what keeps `pigtail db list` (daemon stopped) and a live client polling
`GET /backup` unable to disagree about the answer.

Not folded into `GET /health`: that endpoint is a liveness probe ("is the
daemon up"), and grafting a backup verdict onto it would blur the one
distinction this feature exists to make — "the daemon is fine" and "the
backups are fine" are different questions. Not folded into `GET /servers`
either: that is a hot path polled every few seconds, about RAM, and adding an
unrelated concern to it would be the wrong kind of coupling.

```
GET /backup → 200 BackupResponse
```

`BackupResponse` payload:

```json
{
  "offsite": {
    "dest_dir": "/Volumes/backup/pigtail",
    "status": "failing",
    "severity": "alarm",
    "detail": "FAILING — offsite destination unreachable: /Volumes/backup/pigtail",
    "last_ok_age_seconds": 2700,
    "last_try_age_seconds": 300,
    "consecutive_failures": 3,
    "same_device": false,
    "last_err": "offsite destination unreachable: /Volumes/backup/pigtail"
  }
}
```

- `offsite` is **absent** — not `null`, not an empty object — whenever the
  daemon has nothing *known* to report about a backup, which is a broader
  condition than "no destination configured". `ReadOffsiteState` degrades
  every one of the following to the same zero value rather than surfacing an
  error (`internal/daemon/store/offsite_state.go`): no destination
  configured, a nil `Store`, an in-memory (unnamed) database with nowhere to
  keep `offsite-state.json` beside it, and a state file that could not be
  read, was not valid JSON, or was written by a version this build does not
  recognize. That degradation is deliberate — nothing about a missing or
  corrupt report should make an incident harder to look at — but it means a
  client must read the absent key as "nothing known", not as "not
  configured": reading it as the latter would render "no backup configured"
  on a daemon whose state file just got corrupted, which is silence read as
  health again, the exact failure this feature exists to close. Check for the
  key's presence; do not infer a specific cause from its absence.
- `status` is a stable discriminant a client can switch on without parsing
  `detail`'s prose: `ok`, `failing`, `stuck`, `in_progress`, `stale`,
  `never_succeeded`, or `no_attempt` today. The list is additive — see
  "Unknown values" below.
- `severity` is an **ordered** scale, `ok < warn < alarm`, not just a flat set
  of three labels — see "Unknown values" below for what that ordering is for.
  It escalates past what `status` alone implies in four cases:
  - the last successful copy is ≥26h old, or there have been ≥3 consecutive
    failures — a fact about the destination the 2026-09-06 outage showed
    matters even while other signals stay quiet;
  - `status` is `no_attempt` or `never_succeeded` and the destination has
    stood declared for ≥26h (`declared_at`): neither status ever has a
    `last_ok_at` for the first rule above to measure against, so how long the
    daemon has known it should be backing up here stands in instead;
  - `same_device` is `true`: the destination shares a filesystem with the
    database, which is accepted but survives nothing the local generations
    don't, so it always escalates to at least `warn`, unconditionally.
- `detail` is the exact phrase `pigtail db list` has always shown for this
  state (`store.RenderOffsiteDetail`), reused verbatim rather than
  re-rendered, so the CLI and this endpoint cannot say different things about
  the same state.
- `status`, `severity`, and `detail` coexist on purpose: `severity` drives a
  glyph, `detail` carries the sentence, `status` is what a client branches on
  without parsing prose.
- `last_ok_age_seconds` — seconds since the last successful copy landed.
  Absent when no copy has ever succeeded (an age of `0` would otherwise be
  indistinguishable from "just now").
- `last_try_age_seconds` — seconds since the daemon last attempted a copy,
  successful or not. Absent when it has never tried.
- `consecutive_failures` — attempts that failed back-to-back, reset by any
  success. `omitempty`, so it is absent at its zero value like the rest of
  this payload's counters.
- `same_device` — the destination shares a filesystem with the database:
  accepted, but not the guarantee it looks like, since it then survives
  nothing the local generations don't. No `omitempty`: unlike
  `consecutive_failures`, a `bool` a client branches on must not have its
  `false` collapse into the same absent key an older daemon that never sent
  the field at all would produce, so it is always present, `false` included.
- `last_err` — the most recent failure, verbatim. Absent when the last
  attempt succeeded.

**`fallback`.** `offsite.fallback` is the daemon's classification of the
*redundant* route: a destination a fallback exercise pings on its own,
independently of whichever path the primary backup actually took. An `ok`
primary only proves the path that was used, never the one held in reserve:
the backup host's VPN credentials can expire and the backup keep reading
`ok`, because `~/.ssh/config` routes the LAN first when it answers; the
daemon then never has a reason to try the other path. `fallback` is
what answers "does the redundancy still work" as its own question. It is
classified by the same pure-function model as `offsite` itself
(`store.ClassifyOffsiteFallback(state, primaryHealth, now)`), re-read and
re-classified on every call — no state of its own, same as the rest of this
endpoint.

`offsite.fallback` is **absent** — not `null`, not an empty object — under
the exact same rule `offsite` itself follows: no fallback destination is
configured (`fallback_dest` empty), so there is nothing to report. Its
absence says nothing about the primary route's own health; read `offsite`
for that independently, the same way its presence says nothing about
`offsite` either.

Example, a fallback that has failed twice while the primary reads clean:

```json
{
  "offsite": {
    "dest_dir": "/Volumes/backup/pigtail",
    "status": "ok",
    "severity": "ok",
    "detail": "ok",
    "last_ok_age_seconds": 1200,
    "last_try_age_seconds": 1200,
    "same_device": false,
    "fallback": {
      "dest": "backup-remote:/srv/pigtail",
      "status": "failing",
      "severity": "warn",
      "detail": "FALLBACK FAILING — 2 in a row: dial tcp: i/o timeout",
      "last_ok_age_seconds": 108000,
      "last_try_age_seconds": 1800,
      "consecutive_failures": 2,
      "last_err": "dial tcp: i/o timeout",
      "via": "127.0.0.1"
    }
  }
}
```

- `dest` — the raw fallback destination alias, unparsed, exactly as
  `fallback_dest` was declared.
- `status` — the fallback's own discriminant, independent of `offsite.status`
  — the two routes are never the same fact: `ok`, `failing`,
  `not_yet_exercised`, or `misconfigured`. Additive, same rule as `offsite`'s
  own `status` — see "Unknown values" below, which applies here too.
- `severity` — the same ordered `ok < warn < alarm` scale as
  `offsite.severity`, with one guarantee `offsite.severity` does not carry:
  **`fallback.severity` never reaches `alarm`.** The moment the primary route
  itself needs attention (`offsite.severity` is `warn` or worse), the
  fallback's own verdict is muted to `ok`, unconditionally, regardless of
  what its own counters say. Without that rule, a laptop away from its backup host
  with the VPN down would read the primary as failing and the fallback as
  warn *at the same time*, for the same underlying cause — two alarms for
  one outage, which is how alarms stop getting read. `status`, `detail`, and
  the counters below are untouched by the mute and keep telling the truth;
  only the number a client alerts on is silenced.
- **Contract for a client driving a single glyph.** Take the **maximum
  rank** between `offsite.severity` and `offsite.fallback.severity` — the
  same ordered comparison "Unknown values" below already asks for on
  `offsite.severity` alone, just applied to two values instead of one.
  Because `fallback.severity` never reaches `alarm`, folding it in this way
  can only ever promote a quieter primary reading up to `warn`; it can never
  manufacture an alarm the primary route does not already have on its own.
  Without this rule a client showing one indicator picks arbitrarily between
  the two severities and will sometimes show the calmer one while the
  redundant route is actually down.
  **Corrected 2026-09-15 (final whole-branch review, ruling 4): neither of
  the two production readers this section used to name actually takes this
  maximum**, and the correction is worth spelling out precisely rather than
  just naming true readers in their place, because each surface protects the
  same underlying guarantee — never show a calmer combined picture than
  warranted — by a different route:
    - `printOffsiteAlarm` (`cmd/pigtail/servers.go`), the CLI's own display
      function for `pigtail servers`, opens **two independent gates**, one
      per route, each at the alarm floor, each printing that route's own
      `detail` on its own line. It never folds the two severities into one
      value — see that function's own doc comment for why: folding first and
      then printing only the primary's `detail` would misrepresent an
      unrecognized (and therefore alarm-ranked) fallback severity next to a
      healthy primary as "⚠ offsite backup: ok", which is nonsense. Two
      lines, never merged, is how this surface honors the "never calmer than
      warranted" guarantee without a max.
    - `BackupHealth.fold` (client-side, no longer in this tree)
      does compute the literal maximum, and that computation still decides
      whether the menu-bar banner exists at all (a folded severity of `ok`
      shows nothing) and what the status-item marker reads at `alarm` or an
      unrecognized value. But at exactly `warn` the marker no longer reads
      the fold directly: `BackupHealth.banner(from:)`'s `markerSeverity`
      applies one more filter there (`routineStatusesAtWarn` — see that
      function's own doc comment), narrowing a `warn`-ranked fold down to
      `ok` for either route's own routine, resolves-on-its-own conditions.
      The struct field that used to expose the unfiltered fold on its own,
      for a caller to read directly, has been retired — nothing in
      production ever read it for anything the local values inside
      `banner(from:)` didn't already cover, and a documented field with no
      reader is worse than not documenting one at all.
- `detail` — the exact phrase `store.RenderOffsiteFallbackDetail` renders,
  reused verbatim, on the same model as `offsite.detail`.
- `last_ok_age_seconds` — seconds since the fallback last exercised
  successfully. Absent when it never has.
- `last_try_age_seconds` — seconds since the fallback was last exercised at
  all, successful or not. Absent when it has never been tried — including
  the `not_yet_exercised` status itself: `detail`'s prose may say "configured
  3h00m ago and not once tried", naming how long the fallback has sat idle
  since it was declared, but that duration is for the sentence only and is
  not repeated as `last_try_age_seconds` — the two fields must not be read as
  agreeing that a try happened.
- `consecutive_failures` — the fallback's own streak, counted independently
  of `offsite.consecutive_failures`. `omitempty`, absent at zero, same
  convention as the primary field.
- `last_err` — the fallback's own most recent failure, verbatim. Absent when
  the last exercise succeeded.
- `via` — the source address the destination reported seeing on the
  fallback's last successful exercise. **Read this literally — it is not a
  bug.** On a backup host whose VPN runs in userspace networking, its VPN address
  is never bound to any kernel interface, so a probe that genuinely took the
  *pinned* route arrives relayed through the loopback interface and the
  destination reports `via: "127.0.0.1"`. That is the **healthy** reading for
  such a host on the fallback route — not evidence the probe silently
  dropped to loopback instead of using the VPN. A probe that took the plain
  *local* route (the LAN, no VPN involved) instead reports a private-network
  address (`192.168.x.x`, `10.x.x.x`, …). Do not read "arrived via loopback"
  as a bug report; on this class of host it is what success looks like.
  **Assumed blind spot:** if two destination aliases actually resolve to the
  same underlying network route — one meant to be the redundant path, one
  not — this exercise cannot detect that on its own; a probe that "succeeds"
  by quietly reusing the primary's own path looks identical to one that
  genuinely exercised a separate route. `via` is the one field that makes
  this visible to a human: comparing it against whichever address the
  fallback's *actual*, distinct network path is expected to report is the
  only way to notice the aliases collapsed into one route. `status: "ok"`
  alone proves nothing about which physical path was actually taken.

**Unknown values.** `status` and `severity` are additive lists: a daemon
newer than a given client may send a value this document does not yet
enumerate, and that must not be treated as malformed input. A client that
receives an unrecognized `status` or `severity` should fall back to the other
fields — `detail`'s prose, the timestamps, `last_err` — rather than assume
health from a string it does not recognize. `severity` specifically is an
ordered scale, not a bag of three labels: a client should compare it by rank
(`is this at least warn`) rather than by exact string match (`is this
literally "alarm"`), and must treat an unrecognized `severity` as **at least
`warn`** — never quieter than the least urgent severity this document already
treats as worth a look. Reading `severity != "alarm"` as "nothing to show" is
exactly the mistake this rule exists to prevent: a comparison by equality
reads any value it has never seen as safe, which is the one thing an unknown
value must never be read as.

Same auth as every other endpoint here (§5): socket-trusted, bearer token
over HTTP loopback. No special exemption — unlike `/board`, this is not
public.

#### `POST /backup/offsite/run`

"I just fixed something, prove it now" instead of waiting for the hourly
primary rotation or the six-hourly fallback exercise. Triggers **both**
routes in one call, each under its own independent in-flight guard — the
primary copy may start while the fallback exercise is still busy, and vice
versa. The reason to press this is a past outage, and that outage
lived on the *fallback* route, at six-hour cadence: a call that only
restarted the primary copy would leave exactly the failure this feature
exists to surface unverifiable for up to six more hours.

```
POST /backup/offsite/run → 200 BackupRunResponse
```

`BackupRunResponse` payload:

```json
{
  "offsite_started": true,
  "fallback_started": true,
  "fallback_configured": true
}
```

- `offsite_started` — whether this call actually started an immediate
  primary copy. `false` both when a copy was already in flight and when no
  primary destination is configured (though the latter is instead a `400`
  — see below).
- `fallback_started` — whether this call actually started an immediate
  fallback exercise. This call bypasses the fallback's own 6h due-check —
  a human asked for this, explicitly, right now — but never its correction
  guards: an empty, malformed, or same-as-primary destination, or a store
  with no file-backed state to persist to, still exercises nothing, and
  still reads `false` here. Also `false` whenever an exercise was already
  in flight, and whenever `fallback_configured` is `false`.
- `fallback_configured` — whether a fallback destination string is set at
  all, independent of whether it is valid. The same "set, not necessarily
  usable" meaning `GET /backup`'s `offsite.fallback` presence carries on the
  read side. **This is `true` even when the alias is malformed or names the
  same destination as the primary** — faithful to that same read-side
  contract, but misleading taken alone: a client must not read
  `fallback_configured: true` as "the fallback route is usable". Combine it
  with `fallback_started` to tell apart "configured but this call refused
  it" (`fallback_configured: true`, `fallback_started: false`) from "nothing
  configured" (`fallback_configured: false`, `fallback_started: false`) —
  see the client-side
  `BackupRunResult.FallbackOutcome` for the client-side type that makes this
  three-way read the only way to ask the question, and
  `BackupMonitor.retryNow()` for the one caller that reads it.

No `omitempty` on any of the three fields — see `pkg/pigtail/types.go`'s own
doc comment on `BackupRunResponse` for why that is deliberate: this is the
one payload in this document where a client's synthesized decoder throws on
a missing key, so omitting a `false` would silently break the decode rather
than just being an inconsistency.

| case | code | body |
|---|---|---|
| started | `200` | all three booleans, as they actually came out |
| primary copy already in flight | `200` | `offsite_started: false` |
| no fallback destination configured | `200` | `fallback_started: false`, `fallback_configured: false` |
| no primary destination configured | `400` `INVALID_REQUEST` | `no offsite destination is configured` |

**`200`, never `409`.** This repo's own precedent (`CleanupWorkspace`,
`cleanup.go`) answers `409` because it guards **one** action: either it
started, or it did not, and a conflict status expresses that cleanly. This
route starts **two** independent actions — the primary copy can start while
the fallback exercise is still busy, and vice versa — and a single `409`
cannot express "one of the two was already running" without inventing a
rule for which one wins. So `200` always, and "already in progress" reads
instead as one of the three response booleans coming back `false`, on
exactly the same footing as "nothing configured".

**The response is never a fresh verdict.** Both the primary copy and the
fallback exercise are asynchronous: this call returns as soon as the daemon
has decided whether to *start* each one, before either one's own copy or
probe has run. Reading a verdict off this response at the moment it returns
would therefore mean reading whatever the *previous* cycle left behind —
not this run's own result — and presenting that as "here is what the retry
just did" would be exactly the stale-read-as-fresh-result mistake this
whole feature exists to close. This endpoint reports **what started**,
never **how it turned out**. Poll `GET /backup` afterward — on its own
schedule, not synchronized to this call — to see the result land once the
copy and the exercise have actually finished.

**The due-check bypass, and the guards that survive it.** This call always
runs the fallback exercise immediately regardless of its normal 6h cadence
— `RunOffsiteFallbackNow` (`internal/daemon/offsite_run.go`) skips the
persisted due-check `exerciseFallbackIfDue` otherwise applies. It does
**not** skip any of that same exercise's *correction* guards
(`fallbackGuardsOK`, `internal/daemon/offsite_fallback.go`): an empty
fallback destination, an alias that fails to parse, an alias equal to the
primary destination, or a `Store` with no file-backed state to persist to
all still refuse the exercise, silently, exactly as they do on the periodic
path. A human asking "run it now" is a request to ignore the *clock*, not a
request to ignore *correctness* — the guards that exist to stop a malformed
or self-defeating fallback from ever running stay in force on every call
site, scheduled or manual.

**A manual trigger rearms the six-hour clock too, not just this once.**
`doFallbackExercise` (`internal/daemon/offsite_fallback.go`) — the exercise
body this endpoint shares verbatim with the scheduled path, so a probe and
its outcome are recorded exactly once regardless of which caller asked for
it — writes `FallbackLastTryAt` unconditionally, whoever called it. The
persisted deadline `exerciseFallbackIfDue` reads is exactly that timestamp,
so a manual run does not merely run an extra, out-of-band exercise: it
**moves the next scheduled one**, six hours out from the moment this call
returned, whether or not the manual exercise itself succeeded. Pressing the
retry button twice in one day can silently skip one of the day's automatic
exercises — not a bug, the shared body is what keeps one exercise's outcome
from ever being recorded twice, but an effect worth knowing before reading
"nothing ran on schedule today" as evidence of anything being wrong.

Same auth as every other endpoint here (§5): socket-trusted, bearer token
over HTTP loopback. No special exemption.

#### `POST /servers/stop-idle`

Stops every running dev server idle at least `grace_seconds`, sparing pinned
servers, and reports **every** server's outcome — stopped, or the reason it
was kept. This is the deliberate inverse of the daemon's own silent
evictions (idle-purge, battery mode, the server cap): where those act
quietly, this endpoint exists to say what it spared and why. It backs
`pigtail stop --idle`, the safe alternative to a blanket `--all`/`--everywhere`
sweep when the actual goal is freeing RAM from forgotten servers.
Socket-trusted, no token required.

```
POST /servers/stop-idle
  body: { "grace_seconds": int }
  → 200 StopIdleResponse
```

`StopIdleResponse` payload:

```json
{
  "servers": [
    { "workspace_id": "ws_h7s9", "server": "dev", "port": 3007,
      "idle_seconds": 8040, "stopped": true },
    { "workspace_id": "ws_k2m1", "server": "dev", "port": 3016,
      "idle_seconds": 180, "stopped": false, "reason": "active" }
  ]
}
```

- **Scope: every server the daemon knows** — all registered projects, all
  sessions, whoever started them. The endpoint runs inside the daemon,
  which has no notion of the per-session ownership that bounds
  `pigtail stop --all` (see the `<server>.owner` marker in
  `$XDG_STATE_HOME/pigtail/servers/<worktree>/`), so there is no
  owner filter here and no `--force`-style gate. Deliberate: the
  criterion is "nobody has touched this for `grace_seconds`", and a
  forgotten server is forgotten whoever started it. Callers that mean
  "stop *my* servers" want the CLI's `--all`, not this.
- `grace_seconds` — how long a server must have been idle to be stopped.
  Omitted or `0` falls back to the daemon's configured idle-purge grace
  (`GET /purge`).
- `servers[].stopped` — `true` if this server was stopped by the sweep.
- `servers[].reason` — present only when `stopped` is `false`: `"active"`
  (idle for less than the grace) or `"pinned"` (idle long enough, but
  currently pinned against eviction).
- `→ 400 INVALID_REQUEST` if `grace_seconds` is negative, the body is not
  valid JSON, or `grace_seconds` is omitted/zero **and** the daemon's
  configured idle-purge grace is also `0` (disabled) — the endpoint refuses
  rather than silently stopping nothing or everything.
- `RAMMonitor` not wired → `200 { "servers": [] }` (never an error).

#### `POST /servers/stopped`

Tells the daemon that a dev server is being stopped **on purpose**, so its
death detector does not report the disappearance as a server that died on
its own. The daemon marks its own stops in-process (eviction, idle purge,
server cap, menu-bar stop, watchdog kill); this endpoint is how the local
CLI announces the stops *it* makes (`pigtail stop`, `pigtail stop --all`).
Socket-trusted, no token required.

```
POST /servers/stopped
  body: { "workspace_id": "ws_h7s9", "server": "dev" }
  → 204 (no body)
```

- The mark is valid for 60 s or two sweep intervals, whichever is longer —
  a dev server can take several seconds to release its port after SIGTERM.
- A stop kills the port's listeners, so a mark covers every server name of
  that workspace sharing the port (`dev` and `web` in a large monorepo): the daemon
  marks the names it last saw on the same port, and at detection time one
  valid mark on any name of a vanished port makes the whole process a
  deliberate stop. The CLI still announces every name it stops.
- `→ 400 INVALID_REQUEST` if the body is not valid JSON or either field is
  missing.
- `RAMMonitor` not wired → still `204` (nothing to mark, not an error).
- Best-effort by contract: callers ignore failures. A daemon that never
  hears about a stop reports one harmless false death; it never misses a
  real one. Callers should send this **before** killing the process — the
  daemon sweeps on its own clock and can otherwise see the free port first.
- `workspace_id` must be resolved from the worktree's **absolute path**,
  matched against `GET /workspaces`' `path`. A worktree's directory *name*
  does not identify a workspace: `main` is the main worktree of every
  pigtail project, so a name match over the (global) workspace list marks
  an arbitrary project's server as soon as two are registered — the real
  stop then resurfaces as a death, and the homonym's genuine death is
  swallowed by the spurious mark.

#### `GET /battery`

Battery-mode status: power source, mode state, pending grace period, and
per-server idle countdown to eviction.
Socket-trusted, no token required. Returns `200` always (empty `servers`
list when no RAM monitor is active).

```
GET /battery → 200 BatteryResponse
```

`BatteryResponse` payload:

```json
{
  "on_battery": true,
  "enabled": true,
  "veto": false,
  "grace_seconds": 600,
  "servers": [
    {
      "workspace_id": "ws_h7s9",
      "server": "dev",
      "port": 3007,
      "idle_seconds": 480,
      "evict_in_seconds": 120,
      "spared": false
    }
  ]
}
```

- `on_battery` — `true` when the host is drawing power from the battery
  (i.e. AC adapter is not connected).
- `enabled` — `true` when battery mode is active for this daemon session.
  Reflects the current in-memory state (overridable via `POST /battery`);
  defaults to the `-battery-mode` flag value at startup (default `true` =
  battery mode enabled).
- `veto` — whether the per-kill veto prompt is enabled (config). Reflects
  `BatteryVeto` from the daemon config (`-battery-veto` flag, default `true`).
- `grace_seconds` — the per-server idle threshold on battery (seconds): a
  server must have been idle at least this long before it is eligible for
  eviction.
- `servers[].evict_in_seconds` — seconds until this server is eligible
  for eviction (`0` means it is already past the idle threshold and will
  be evicted on the next sweep).
- `servers[].spared` — `true` when the server was skipped during the last
  sweep because the user vetoed its eviction.

#### `POST /battery`

Toggle battery mode for the running daemon session (in-memory; reverts to
the `-battery-mode` flag default on daemon restart).

```
POST /battery
  body: { "enabled": bool }
  → 200 BatteryResponse   (same shape as GET /battery)
```

- `enabled: true` → activate battery mode immediately (the daemon will
  start evicting idle dev servers when the host is on battery and the
  grace period has elapsed).
- `enabled: false` → deactivate battery mode for this session (no
  evictions until re-enabled or the daemon restarts; reverts to the
  `-battery-mode` flag default on restart).
- Response is the fresh `BatteryResponse` after applying the toggle.

#### `GET /lid`

Lid sleep control (`pigtail lid`): keep the Mac awake with the lid closed.
The real state (`pmset -g`, `SleepDisabled`) plus pigtail's timer.
Socket-trusted, no token required. Wired by pigtaild on macOS only.

```
GET /lid → 200 LidResponse
         → 500 INTERNAL         (pmset -g failed)
         → 501 LID_UNAVAILABLE  (not macOS: no lid manager)
```

`LidResponse` payload:

```json
{
  "sleep_disabled": true,
  "deadline": "2026-10-16T23:00:00+02:00",
  "battery_floor": 25,
  "last_off": { "at": "2026-10-16T03:06:00+02:00", "reason": "deadline" },
  "thermal_state": "fair",
  "lid_closed": false
}
```

- `sleep_disabled` — the real `SleepDisabled` value read from `pmset -g`.
- `deadline` — when pigtail turns sleep control off. **Absent = no pigtail
  timer**: with `sleep_disabled: true`, sleep was disabled by something
  else (a manual `pmset`, an older tool) — "no timer"; `pigtail lid off`
  restores sleep.
- `battery_floor` — on battery at or below this charge (percent, 25), the
  daemon turns sleep control off, lid open or closed.
- `last_off` — the last off and its reason: `deadline`, `battery N%`,
  `thermal critical`, `thermal serious`, `manual` or `external` (sleep was
  restored outside pigtail; no sudo call).
- `last_error` — an automatic off that failed (e.g. the sudoers rule was
  removed by hand); retried at the next tick (every minute).
- `guard_error` — omitempty: a guard reading that failed at the last tick,
  mode on (`battery reading failed: …`, `thermal reading failed: …`, `lid
  reading failed: …`, joined by `; `). A guard that cannot read never turns
  sleep control off; this field makes it visible.
- `thermal_state` — omitempty: the last thermal reading, mode on
  (`nominal` | `fair` | `serious` | `critical`; `notifyutil -g
  com.apple.system.thermalpressurelevel`).
- `lid_closed` — omitempty: the last lid reading, mode on (`ioreg`
  `AppleClamshellState`). **Absent = unknown** (the reading failed, or never
  read) **or mode off**; present `false` = lid open.

The heat guard acts **with the lid closed only**: `critical` with the lid
closed turns sleep control off at that tick (`thermal critical`); `serious`
with the lid closed does after 10 consecutive readings (10 min,
`thermal serious`). With the lid open, heat never turns it off (and resets
the `serious` count); an unknown lid applies no heat rule at all. Reason
order at one tick: `deadline` > `thermal critical` > `battery N%` >
`thermal serious`.

The state (on, deadline, last off) persists in `lid-state.json` next to the
database; at start the daemon applies a deadline that passed while it was
down.

#### `POST /lid`

```
POST /lid
  body: { "on": bool, "hours": int }   (hours: 1..24, omitted/0 = 8; ignored when on=false)
  → 200 LidResponse
  → 400 INVALID_REQUEST   (invalid JSON, hours outside 1..24)
  → 412 LID_NOT_SET_UP    (sudo asks for a password: run `pigtail lid setup` once)
  → 500 INTERNAL
  → 501 LID_UNAVAILABLE
```

- `on: true` → `sudo -n /usr/bin/pmset -a disablesleep 1`, deadline =
  now + hours, persisted (a failed save restores sleep and answers 500).
  Already on → re-arms the deadline to now + hours (never 409).
- `on: false` → `sudo -n /usr/bin/pmset -a disablesleep 0`, always
  (idempotent; also covers "no timer").
- The response is built from the value just written, without a new read.
- Every state change publishes `lid.changed` on `/events` (§ Events): a
  successful `on` (`reason: "on"`, re-arm included), a successful `off`
  (`"manual"`), and the daemon's own auto-off (`"deadline"`, `"battery N%"`,
  `"thermal critical"`, `"thermal serious"`, `"external"`) or a failed auto-off (`on: true`, `"auto-off failed"`, once
  per distinct error). A failed `POST` that leaves the state unchanged
  publishes nothing.

#### `POST /lid/probe`

Checks that the sudoers rule of `pigtail lid setup` works for the daemon's
own process. No body. Under the manager lock (never interleaved with the
deadline/battery tick, `on` or `off`): read `SleepDisabled`, then a no-op
write of that same value, `sudo -n -k /usr/bin/pmset -a disablesleep
<current>` (`-k`: a cached sudo timestamp cannot pass for the rule). POST
because it writes, though without effect.

```
POST /lid/probe
  → 200 LidProbeResponse   { "rule_ok": bool, "warning": string? }
  → 500 INTERNAL           (pmset -g failed: state unreadable)
  → 501 LID_UNAVAILABLE
```

- `rule_ok: false` is a `200` answer: the rule is not in effect.
- `warning` — `sleep is disabled with no timer — run pigtail lid off`
  when, after the probe, `SleepDisabled` is 1 and pigtail armed no deadline.

### Gates

#### `GET /gates`

The gates an external tool declares, read by
running pigtaild's `--gate-cmd` as `<cmd> list --json` on every call (10 s
bound, no cache) — pigtail never replays `gates.jsonl` itself (SI-D7: the tool
declares, pigtail shows). Scoped (behind `X-Pigtail-Scope`, D19): a caller
reads the gates of the scopes it may read; `complete` says whether that is
every scope. Pigtail reads it as the master. Santé du system reads
`counts` (D3). For a non-master caller `counts` is partial (`complete: false`):
a consumer of the machine total reads as the master or checks `complete`.

Always 200. `configured: false` when `--gate-cmd` is empty. When the command
cannot be read, `error {kind, message}` with `kind` in `not_found | failed |
timeout | invalid_output`, and **no `counts`**: a source that cannot be read
is never reported as zero gates.

```json
{"configured": true, "complete": true,
 "counts": {"fired": 1, "open": 1, "broken": 0},
 "fired": [{"id": "pr-9225-merged", "title": "…", "action": "…", "scope": "globex",
            "status": "fired", "added_at": "…", "fired_at": "…", "fired_age_seconds": 432000,
            "subject_id": "sub_…", "value": "9225=MERGED", "errors": 0}],
 "open": [{"id": "…", "status": "open", "…": "…"}],
 "last_check_at": "…", "last_check_age_seconds": 1800, "read_at": "…"}
```

`fired` holds `fired` and `firing` gates, oldest fire first (unknown
`fired_at` last); `open` the rest, oldest first. `broken` = open gates that
`bin/gate` reports `broken` (consecutive errors for ≥ 72 h; the daemon does not
recompute it). `last_check_at` / `last_check_age_seconds` = the last complete
`gate check` pass (`last_run` of `gate list --json`), not the gates' own
`last_check`; absent when `bin/gate` does not expose it. `scope` is the
canonical scope name.
Additive: consumers ignore unknown keys and statuses.

### Claude usage

#### `GET /usage/overview`

The master's view of the Claude accounts, for Pigtail (sub_x000000000000044).
Behind `X-Pigtail-Scope`: a caller that is not the master scope gets **403
`SCOPE_MISMATCH`** (« usage overview: master scope only »). 501
`USAGE_UNAVAILABLE` when the daemon has no source wired; 503
`USAGE_UNAVAILABLE` + the reason when the cell table cannot be read.

Built at each call from the cells' `.claude.json` (the readings), the
daemon's usage history (`usage-history.jsonl` in `-usage-state-dir`, one
sample per account and sweep pass, the last 72 h in memory) and the sweep's
last state (`usage-refresh.json`).

```json
{"generated_at": "…",
 "accounts": [{"account_uuid": "…", "label": "m", "in_verdict": true, "unusable": "",
               "fetched_at": "…",
               "seven_day": {"percent": 40, "resets_at": "…", "full": false},
               "five_hour": {"percent": 10, "resets_at": "…", "full": false},
               "scoped": [{"model": "Fable", "percent": 0, "resets_at": "…", "full": false}],
               "window_left": 0.43, "fast": false, "refresh_failed": false,
               "history_since": "…"}],
 "verdict": {"state": "ok", "provisional": true, "oldest_reading_at": "…", "next_renewal": {"label": "i", "at": "…"}, "blocked_from": null, "frees": null},
 "next_session": [{"scope": "perso", "display": "perso", "master": true, "account_uuid": "…"},
                  {"scope": "globex", "display": "gx", "master": false, "error": "no-candidate"}]}
```

- `accounts[]`: every account, in the order of `pigtail usage` (D9). `label`
  is the account's letter, else `other-<8 first chars of the uuid>`. `percent`
  is the shown one (rounded, 0 once its reset passed); `full` = the raw percent
  is at or above the account's ceiling (`select_pct`, default 90) on 5 h, 7 days
  and per-model, and
  the reset has not passed. `unusable`: `no-reading` (no 7-day reading), else
  `cap` (credits cap reached), else `""`. `in_verdict` = a master-scope account
  with `unusable` empty. `window_left` = share of the 7-day window still to
  run (0..1; a passed reset counts as a fresh window); `fast` = the 7-day
  availability is more than 5 points below `window_left`. `refresh_failed` =
  the sweep's last refresh of this account failed. `history_since` = the
  oldest sample the daemon keeps for it (null without history).
- `next_session[]`: one entry per declared scope (canonical name, table
  order), the account `Pick` would start its next session on — the same
  function as `pigtail usage pick`. It designates an entry of `accounts[]` by
  `account_uuid` (I11), never by label; `display` is the scope's first alias
  in the scope table, else its name. `error: "no-candidate"` when the scope
  has no logged-in readable cell.
- `verdict`: whether the week holds on the accounts of the verdict (`in_verdict`), at their current rhythm: a 10-minute simulation of the rotation over the next 7 days (`internal/claudeusage`). `state`: `ok` (holds until the renewals), `tight` (holds, minimum reserve under 10 % of the default ceiling (90 points)), `later` (blocked from `blocked_from`, freed at `frees`), `now` (blocked now, freed at `frees`), `none` (no account in the verdict). `provisional` = at least one account has less than 48 h of history (its week rhythm is the mean since its renewal, its 5 h and per-model rhythms 0). `oldest_reading_at` = the oldest `fetched_at` among the accounts of the verdict. `next_renewal` (`ok`, `tight`) and `frees` (`later`, `now`; null when unknown) are `{label, at}` with the account letter (`other-<8 first chars of the uuid>` without one), never a cell id. `blocked_from` (`later` only) is a time. `oldest_reading_at`, `next_renewal`, `blocked_from` and `frees` are always present, `null` outside their state.
- `unreadable_count` (int, omitted at 0): the number of visible cells whose
  `.claude.json` could not be read or parsed (I13). Such a cell contributes
  no account; this count is how it still shows. It never carries a cell id, a
  config dir or an error message.
- D19 of the rotation: the response names no cell — neither an id nor a
  `cell` field.

### Alerts

What pigtail itself raises for the user, until it is dealt with (subject
« abandon respawn visible », D3/D5). Three kinds. `respawn`: an alert is a
journal line `respawn-abandoned|respawn-failed` (written by the respawn route
above) with **no** `alert-resolved:<run>` line of the same run in the same
scope. `disk`: the machine's free space is under the alert threshold (below). `role`: a role missed its deadline
(below). All are derived from the journal alone, nothing in memory: an alert survives a
daemon restart, and what `pigtail journal list` shows is what Pigtail
shows. No migration, no table.

**The disk alert is the one alert that belongs to the machine, not to a
scope.** `pigtaild` reads the free space of the home and temp volumes (the
tighter of the two) every minute, the first reading right at start, so a disk already low alerts at once. Under `-disk-alert-gb` (default 40; 0 or -1 = off)
it writes a `disk-low` journal line (id `disk-low:<unix nanoseconds>`, scope
the master scope, no subject, summary `free_bytes=… threshold_bytes=…
floor_bytes=…`), publishes `disk.low`, and the alert opens; once the space is
back **10 GB above the threshold** (50 GB at the default) it writes `disk-ok`
and publishes `disk.ok`, and the alert closes. The gap is the hysteresis. A
failed read says nothing (logged once per failure run); a restart under the threshold re-reads the journal
and writes nothing new; `-disk-alert-gb 0` or `-1` closes an alert left open at the
next start. 1 GB is 2^30 bytes throughout.

**Kind `role`.** A role that missed its deadline (see § Roles) is an alert too:
journal kind `role-alert`, id `role-silent:<name>:<ms of the episode key>`
(one id for silent, stuck and absent of one episode; the state is the
source's `?state=`), closed by a `role-recovered:<name>:<ms>` line of the same
key, acknowledged by `alert-resolved:role-silent:<name>:<ms>`. `GET /alerts`
lists them with `kind: "role"`, `state`, and `id` = the alert id;
`POST /alerts/{id}/ack` takes that id; with a `session_id` the session must
belong to the alert's subject (else the unknown-session answer), without one
it is acknowledged by hand.

#### `GET /alerts`

Scoped (`classScoped`): a caller reads the alerts of the scopes it may read;
Pigtail reads it as the master. Every open alert, oldest first (every
alert line is read, page by page, and only the resolutions of their runs —
never a window of the latest lines). A store error is a `500`, never an empty
list.

```json
{"alerts": [{"id": "20261013T101000.000-4242", "kind": "respawn", "state": "abandoned",
             "scope": "perso", "subject_id": "sub_…", "subject_title": "…",
             "detail": "gave up after 10m0s: background shell: `npm run dev` (14m)",
             "created_at": "…", "age_seconds": 720}],
 "read_at": "…"}
```

`id` is the run id; `alerts` is never null. A **disk alert** appears for a
caller of **any** scope, read by its own query (`disk-low` / `disk-ok` lines of
the master scope, whoever asks), and carries no `scope`, `subject_id` or
`subject_title` (the `scope` key is omitted):

```json
{"id": "disk-low:1793450000000000000", "kind": "disk", "state": "low",
 "detail": "31 GB free on disk", "free_bytes": 33285996544,
 "floor_bytes": 21474836480, "created_at": "…", "age_seconds": 120}
```

`free_bytes` and `floor_bytes` (the admission floor, 0 = gate off or admission off) are the
values when it opened. It is open as long as no `disk-ok` follows the last
`disk-low`. `subject_title` is the subject's
short title, present only for a caller that reads the subject's scope (D23).
`detail` is the line's summary without its `respawn <state> <run>: ` prefix
nor its `— no live session to tell` tail.

#### `POST /alerts/{id}/ack`

A `disk` alert cannot be acknowledged: the id `disk-low:…` answers `400` with
an explicit message; it closes by itself (above). Respawn alerts:
`{"session_id": "<id>"}` (optional; an empty body is accepted) → `200
{"id", "resolved", "summary"}`. Writes `alert-resolved:<run>` (kind
`alert-resolved`, the alert's scope and subject, `source=pigtail:alerts/<run>`),
summary `respawn alert <run> resolved: acked by session <8>` or `… acked by
hand` without a session. With `session_id`, the session must act in the
caller's scope — otherwise the answer of an unknown session, byte for byte
(D19) — **and** be the session the notice told (named by `?session=` in the
line's source; a session with no subject in the registry included) or a
session of the alert's subject (`GetPeer`) — otherwise `409 CONFLICT`,
nothing written. `404` = a run unknown or in a scope the caller cannot read
(the same body); `403 SCOPE_MISMATCH` = the master on another scope's alert
without the cross-scope opt-in; `400` on a run or session id off
`[A-Za-z0-9._:-]{1,100}`. Idempotent: a second ack answers `resolved: false`
and the first resolution's summary. Acts as the named session:
`classSessionBound`.

#### `POST /alerts/resolve`

`{"subject_id": "sub_…", "cause": "respawn <run>"}` → `200 {"ids": [...]}`
(never null). A relaunch of the subject succeeded (`pigtail session respawn`
ending `respawned`): each open alert of that subject, in a scope the caller
may write (with its `--cross-scope`), gets its line `… resolved: subject
relaunched (<cause>)`. `400` on a subject id off its shape, or a cause empty,
on two lines or over 120 bytes; a failed write is logged and answered `500`
after the others were tried. `classScoped`.

Neither route sits behind the maintenance freeze: an acknowledgement is never
held. CLI: `pigtail alerts list [--json]`, `pigtail alerts ack <run>
[--by-hand] [--cross-scope]` (with `CLAUDE_CODE_SESSION_ID` set and no
`--by-hand`, it acts as that session; otherwise by hand, in the caller's
scope, `--scope` / `PIGTAIL_SCOPE` honoured outside a cell).

### Roles

A role is a subject bound to a cadence (role supervision): pigtaild's sweep
judges each role every minute and writes one journal alert per missed deadline.
A role's scope is its subject's; every route is scoped (`classScoped`), the
writes sit behind the maintenance freeze.

```
GET    /roles          → 200 { roles: [Role] }
GET    /roles/status   → 200 { watch: bool, roles: [RoleStatus] }
POST   /roles          { name, subject_id, cadence_ms, grace_ms?, stall_ms? } → 200 Role
DELETE /roles/{name}   → 200 { name, scope } (closes the role's open episodes)
```

`name` matches `^[a-z0-9][a-z0-9-]{0,31}$`; `grace_ms` defaults to 10 min,
`stall_ms` to 20 min. `RoleStatus` = `{ role, state, reason, deadline, episode,
judged_at, alert_id, held }`; `watch: false` means the daemon runs without the
sweep (`-role-watch=false`) and the statuses are empty.

- **Deadline.** A wakeup armed and not fired: its `scheduledFor`. Else the last
  fire plus the cadence, else the last turn plus the cadence, else the role's
  creation plus the cadence. A human turn on a wakeup-driven role moves nothing.
- **States.** `ok`, `silent` (idle past deadline + grace), `stuck` (busy with no
  transcript write for the stall), `absent` (no live session past deadline +
  grace), `unknown` (a read error or panic: never an alert).
- **Episode.** Keyed on the missed deadline (`max(deadline, created_at)`), so
  one episode is one alert whatever its state shifts. It closes only on an `ok`
  pass whose deadline is in the future; a closed key is never raised again.
- **Standby is not an ok reason.** A session in Standby is not read as alive
  nor as an excuse: a parked role alerts once at its missed deadline; ack it or
  `role rm`.
- **Sleep gap.** A pass more than 3 intervals after the previous one (wall
  clock) holds its writes for one max grace.

### cmux check notice

#### `GET /cmux/check/notice`

The end-of-pass line of the running cmux launch, for the Pigtail app
(sub_x000000000000037, D2). pigtaild writes `notice.json` (in the cmux-check
state dir) once the last pass of a launch is done, then publishes
`cmux.check_notice`; the bar reads this route and posts one macOS
notification when `line` is not empty.

Behind `X-Pigtail-Scope` (`classScoped`): the line names sessions of every
scope, so a caller that is not the master scope gets **403 `SCOPE_MISMATCH`**
(« cmux check notice: master scope only »). A notice file that exists but
cannot be read is a `500`.

```json
{ "launch_pid": 4242, "at": "2026-10-28T08:30:00Z",
  "line": "14 relancées · PLG vision report attend ta réponse — pigtail cmux check --last" }
```

`line` is empty (and `launch_pid` 0) when there is no notice, when nothing is
left for the user, or when the notice belongs to another launch than the cmux
that runs now. Text is French; commands quoted in it stay English.

Event `cmux.check_notice`: a notice was written; no payload — re-read the
route.

### CPU slots

A machine-wide budget of "slots", shared by every CPU-intensive consumer —
vitest above all. One slot = the right to run one unit of work (for vitest,
one test file). Groups (conventionally `<workspace>:vitest`) are the unit of
equity: a lone run gets the whole budget, N runs share it, and a run that
ends gives its share back.

The daemon arbitrates and nothing else. It spawns no process and kills none.

#### `POST /cpu/slots`

Acquire one slot. **The connection carries the slot**: the grant is streamed
as soon as the slot is attributed, then the response stays open, and closing
it releases. A finished file, a killed worker, a Ctrl-C, the OOM killer, a
daemon restart — all of them close the socket, and all of them release. There
is no heartbeat, no TTL, no release endpoint.

```
POST /cpu/slots
     body: { "group": "ws_ab12:vitest", "label": "Button.test.tsx", "weight": 1 }
     → 200 (streaming) { "granted": true, "waited_ms": 340, "degraded": false }
     → 400 INVALID_REQUEST    — group is missing
     → 503 CPU_SLOTS_DISABLED — the daemon runs with -cpu-slots=0
```

`session_id`, `cmux_workspace_id` and `cwd` are optional and only used to
decide the request's focus at enqueue (the CLI sends `$CLAUDE_CODE_SESSION_ID`,
`$CMUX_WORKSPACE_ID` and its working directory). The decision is read from an
in-memory snapshot, never from a store or cmux call on this path; unknown or
absent means non-focus. The identifiers are never echoed back on the wire:
`focus` (bool, omitted when false) appears on the queue rows of
`GET /admission` and on the records of `GET /admission/history`, and nothing
else about the caller does.

`weight` is reserved for a future weighted policy and is ignored in v1.

`degraded: true` means the slot was handed out without real arbitration
(the daemon's waiting queue was at its ceiling). The work runs regardless:
the arbiter must never be the reason a test does not run.

A slot held longer than `-cpu-slot-max-hold` (default 10 min) is **revoked
from the accounting**: the daemon drops it from its books, redistributes it
and publishes `cpu.slot_revoked`. Nothing is killed and the client is not
told — it keeps working to the end of its unit.

#### `GET /cpu/slots`

```
GET /cpu/slots → 200 CPUSlotsResponse
```

```json
{
  "budget": 9,
  "held": 9,
  "waiting": 6,
  "groups": [
    { "group": "ws_ab12:vitest", "held": 5, "waiting": 0, "share": 5 },
    { "group": "ws_cd34:vitest", "held": 4, "waiting": 6, "share": 4 }
  ]
}
```

`budget: 0` means the arbiter is disabled. Groups are ordered oldest-first —
the order in which the budget's remainder is handed out. Serves `pigtail slots`.

### Memory admission

A machine-wide queue for **heavy commands** — type checkers, linters, test
runners, monorepo task runners, static storybook builds. A command asks to
start; the daemon admits it once the machine has the memory for one more.
Groups (conventionally the worktree name) are the unit of fairness.

It complements CPU slots rather than replacing them: CPU slots share a static
budget of cores one test file at a time; admission shares *measured* memory
one process tree at a time. A `pigtail test` run takes both.

The daemon arbitrates and nothing else. It spawns no process and kills none.

A request is admitted when **every** gate passes:

- `available − Σ outstanding − cost ≥ floor`, where `available` is what the
  kernel could reclaim now (the same number `pigtail servers` shows),
  `outstanding` is, per admitted lease, `max(0, cost − memory of its tree)`
  (physical footprint on darwin, VmRSS on Linux) —
  memory promised to commands that have not grown into it yet — and `floor`
  is `-admit-floor-mb` (default 6144);
- the free **disk** space (the tighter of the home and temp volumes) is at
  least `-admit-disk-floor-gb` (default 20; 0 = off). Unlike every gate above
  this one holds **an empty machine too**, and neither `-admit-max-starve-seconds`
  nor a long wait lifts it: a command started on a nearly full disk only fills
  it further (the 2026-10-31 outage began at 1.4 GB free). Only freed space
  does; `pigtail admit` that exhausts `--max-wait` exits 75 and names the
  disk. The reason reported is `disk`. A disk reading that fails counts as
  unknown, never as low: the gate stays open rather than stopping the machine
  on a broken `statfs`, and an old low reading is never reused;
- kernel memory pressure is below `critical` (4). At or above
  `-admit-pressure-level` (default 2 = warn; darwin's
  `kern.memorystatus_vm_pressure_level`, Linux PSI mapped onto it) pressure
  **brakes** rather than stops: the floor is multiplied by 1.5 and the swap
  gate below tightens to "any swapping at all". `warn` is a level a busy
  machine sits at for hours — 0.1 GiB free next to 13.5 GiB inactive is
  normal on macOS — so treating it as a stop sign strands the queue while
  memory is in fact available (measured 2026-09-24: four jobs queued behind
  one lease, 13 GiB available, zero swapping). The reason reported when only
  the raised floor blocks is `pressure`; when the plain floor does, `memory`;
- the swap-out rate over the last ten seconds is under
  `-admit-swapout-kb-per-sec` (default 4096), or under anything at all when
  the kernel is at `warn`;
- compressions + decompressions over the last ten seconds are under
  `-admit-compress-kb-per-sec` (default 16384); the threshold stays the same
  under `warn` (decompressions are never zero on a loaded machine), except
  for the heavy classes below. The reason reported is `compress`;
- the **heavy classes** — `compress_calm_classes` (default `build` and
  `vitest-browser`: every class whose prior is at least 4 GiB) — also need
  that same traffic under the threshold over the last
  `-admit-compress-calm-seconds` (default 60; 0 = off, they are then gated
  like any other class). They peak in a burst seconds after they start, so a
  lull of ten seconds between two bursts proves nothing. For them this
  replaces the ten-second `compress` gate: the reason reported is
  `compress_calm`. **A wait past `-admit-max-starve-seconds` does not lift
  it** (two builds released that way on 2026-10-27 landed in the burst); only
  `-admit-compress-calm-max-starve-seconds` > 0 (default 0 = never) does,
  after that wait. The anti-lockout rule is unchanged: with no lease held,
  the head is admitted;
- fewer than `-admit-max-jobs` leases are held (default: min(performance
  cores, physical memory / 10 GiB) — 6 on a 64 GB M5 Pro);
- fewer leases of the request's **class** are held than
  `-admit-class-limit=<class>:<n>` allows (repeatable; default
  `vitest-browser:1`, `go-test:2` and `build:1`: each worker of a browser-mode
  run is a live browser and memory alone does not measure that, two go-test
  runs at `-p 3` fit the cores where three at `-p 6` did not, and a JS build
  bursts into the compressor seconds after it starts). `<class>:0` turns a cap
  off. The reason reported is `class_limit`.

**With no lease held, the head is admitted whatever the gates say**: under
background pressure the controller serialises heavy commands, it never stops
them all. **A waiter past `-admit-max-starve-seconds` (default 180) is
admitted on the memory floor alone**, whatever the job ceiling, the class-free
gates and the pressure brake say — `critical` and the class caps excepted. The
longest wait is considered before the fairness pick, because the gates are
machine-wide: a waiter fairness did not choose is refused for a reason that is
not about it, and the queue stops there. That is the 2026-09-24 stall. Among the others, a **focus** request goes first: a caller whose session sits
in a cmux workspace that is pinned in an allowed window, or that belongs to an
open `pigtail focus`, is considered before every non-focus waiter. Focus
changes the order only; it opens no gate, and the starvation guarantee above
still holds (a non-focus waiter past max-starve beats a focus one). When the
focus head does not fit, the non-focus waiters behind it wait too (no
backfill), until the starvation guard releases them. Inside each side the
rule is unchanged: the next request served is the oldest one of the group
holding the fewest leases; if it does not fit, nobody behind it jumps ahead (no
backfill).

The one exception, and it is not backfill: a request held by `class_limit`
is **skipped for that pass**. A memory refusal is resolved by the queue in
front finishing; a class cap is not — it is resolved only by a lease of the
same class ending. Leaving it at the head would let one browser-mode run
block every `tsc` and `eslint` on the machine for its whole duration. FIFO
within the class is preserved, so no request of that class is starved.

Likewise a waiter held by `compress_calm` **and** past max-starve is set aside
for that pass, and from then on only waiters that are themselves past
max-starve may be admitted behind it: a fresh request passing it would feed
the very traffic it waits out. Other classes keep the starvation guarantee
above; the heavy one starts at the first durable calm.

The class is inferred client-side (`pigtail admit`, the Bash hook). `build`
covers `vite build`, `next build`, `bunchee`, `tsup`, `rollup` and `webpack`
(not its `serve`/`watch`/`info`-like subcommands; none with `--watch`,
`--help` or `--version`), and a `build[:*]` script judged by its name in a
workspace (not `build:watch`-like dev scripts; never `go build` or `make
build`). `test-storybook[:*]` scripts, which run Storybook's vitest addon in
browser mode, are `vitest-browser`.

`cost` is, per (class, repo), 1.2 × the EWMA of the peak memory (physical
footprint on darwin, VmRSS on Linux) observed on past runs once three are
known (never under half the class prior), and the class prior before that:
tsc 1.5 GiB, eslint 1.5, jest 3, vitest 2, nx 3, storybook-build 3, vitest-browser 4, build 8, generic 1.5.

Since 2026-10 the costs file is `{"version":2,"costs":{…}}`; an unversioned
file (RSS-based peaks) is discarded at load and logged. History lines written
before that date carry RSS `peak_bytes`.

**A daemon restart keeps the leases in flight and the queue's members.** The
leases on the books are written to `admission-leases.json` next to the
database (`{"version":1,"seq":n,"leases":[…]}`: per lease `lease_id`, `class`,
`repo`, `group`, `label`, `pid`, `cost_bytes`, `granted_at`, `enqueued_at`,
`waited_ms`) at every grant, release and revocation; a grant is written under
the controller's lock **before** its stream is told, so a daemon dying between
the two never forgets a run its client believes admitted. At start, before it
serves anything, the daemon reloads the leases whose client PID still lives
and still runs `pigtail` (`ps -o comm=`, base name), younger than the hold
limit; the rest are dropped. A reloaded lease counts in the class caps and the
reservations under its original id, ages from its real grant, and ends when
its client dies or its PID is reused by a program that is not `pigtail` (next
tick; its history line is written, the learned costs get nothing), when the
hold-limit revocation of the sweep reaches it, or when a request from the same PID arrives (the client reconnected;
never two places for one run). `seq` resumes above every id in the file. An
unreadable file, or one from a newer format, starts the controller empty and
is logged. Nothing of this is in the protocol: the client only reconnects
(`pigtail admit`: after a stream cut while queued, a new `POST` every 2 s for
at most 90 s from the first cut, inside its `--max-wait`; the window spent, it
runs unadmitted with a message; an `ADMISSION_DISABLED` answer stops it, silent).
A reconnection is a new enqueue: the client's place in line is the end, its
starvation clock restarts. A daemon that dies before its first line leaves the
client with nothing to reconnect from: it runs unadmitted, as before.

#### `POST /admission/leases`

Acquire one lease. **The connection carries the lease**, exactly as for CPU
slots. The response is NDJSON, one `{"data": AdmissionUpdate}` per line:

```
POST /admission/leases
     body: { "group": "fix-hovercard", "class": "tsc", "label": "npx tsc --noEmit",
             "repo": "/Users/me/dev/app", "pid": 4242, "cost_hint_bytes": 0,
             "cost_prior_bytes": 0, "workers": 0,
             "session_id": "…", "cmux_workspace_id": "…", "cwd": "/Users/me/dev/app",
             "parent_lease": "adm-16" }
     → 200 (streaming)
       {"data":{"state":"queued","position":2,"queued":3,"running":6,
                "available_bytes":…,"effective_bytes":…,"need_bytes":…,
                "floor_bytes":…,"reason":"memory"}}          — at once if it must wait,
                         (reason "disk" adds "disk_free_bytes","disk_floor_bytes")
                                                               then every 5 s
       {"data":{"state":"granted","lease_id":"adm-17","cost_bytes":…,
                "waited_ms":41200,"degraded":false}}          — then the connection stays open
     → 400 INVALID_REQUEST    — group is missing
     → 503 ADMISSION_DISABLED — the daemon runs with -admit-max-jobs=0
```

`reason` is set on the head of the queue only: `memory`, `pressure`,
`swapout`, `compress`, `compress_calm`, `max_jobs`, `class_limit` or `disk`. `pid` is the client process; the lease is charged
with the memory of its whole tree (physical footprint on darwin, VmRSS on
Linux), sampled every 2 s while leases exist.
`parent_lease` (optional) is the id of a held lease this command runs under
— the generic envelope of `pigtail admit -- git push`. The daemon does not
count that lease against the request (its remaining cost, the "nothing else
admitted" rule, MaxJobs); the class limit is unchanged. An unknown id is
ignored, the request is then an ordinary one.
Hanging up while queued withdraws the request. `degraded: true` means the
queue was at its ceiling (256) and the command runs uncounted.

A lease held longer than `-admit-max-hold-seconds` (default 1800) is revoked
from the accounting only, as for CPU slots.

#### `POST /admission/leases/{id}/exit`

Report the exit status of the command a lease admitted. The lease is the open
connection, which cannot say how the command ended, so the client calls this
**before hanging up**; the history line written at the release keeps it
(`exit_code`). Best effort for the client: it ignores any failure.

```
POST /admission/leases/adm-17/exit
     body: { "exit_code": 3 }
     → 200 { "data": { "recorded": true } }
     → 400 INVALID_REQUEST    — the body is not JSON, or has no exit_code
     → 404 NOT_FOUND          — no such lease (unknown, or already released or revoked)
     → 503 ADMISSION_DISABLED — the daemon runs with -admit-max-jobs=0
```

A lease released or revoked first answers 404 and its record keeps no exit.
The zero is a value: `{"exit_code":0}` is recorded.

#### `GET /admission`

```
GET /admission → 200 AdmissionStatus
```

```json
{
  "max_jobs": 6, "floor_bytes": 6442450944,
  "disk_free_bytes": 165191049216, "disk_known": true, "disk_floor_bytes": 21474836480,
  "pressure_gate": 2,
  "pressure_floor_factor": 1.5, "max_starve_seconds": 180,
  "swapout_gate_bytes_per_sec": 4194304,
  "compress_gate_bytes_per_sec": 16777216,
  "total_bytes": 68719476736, "available_bytes": 26198073344,
  "effective_bytes": 23086661632, "pressure": 1, "swapout_bytes_per_sec": 0,
  "compressor_bytes": 1073741824, "unused_bytes": 536870912,
  "compress_rate_bytes_per_sec": 0,
  "compress_calm_window_seconds": 60, "compress_calm_rate_bytes_per_sec": 2097152,
  "compress_calm_classes": ["build", "vitest-browser"],
  "compress_calm_max_starve_seconds": 0,
  "read_age_seconds": 1,
  "leases": [ { "id": "adm-17", "group": "fix-hovercard", "class": "tsc",
                "label": "npx tsc --noEmit", "cost_bytes": 1610612736,
                "rss_bytes": 1395864371, "peak_bytes": 1395864371,
                "age_seconds": 31, "waited_ms": 62000 } ],
  "queue":  [ { "group": "feat-rail", "class": "jest", "cost_bytes": 3221225472,
                "age_seconds": 12, "reason": "memory" } ],
  "costs":  [ { "class": "tsc", "repo": "/Users/me/dev/app", "ewma_bytes": 1363148800, "samples": 4 } ]
}
```

`max_jobs: 0` means admission is disabled. The four `compress_calm_*` fields
describe the heavy-class rule (window, current rate over it, classes, and the
wait that lifts it, 0 = never); they are filled only when the calm window is
positive and `compress_calm_classes` is non-empty, otherwise all four are
absent. Being `omitempty`, a filled field whose value is 0 (an idle compressor,
no starvation lift) is absent too. Serves
`pigtail slots`, which prints the rule as a `calm gate:` line.
`leases[].rss_bytes` is the lease's tree memory (physical footprint on darwin,
VmRSS on Linux); the key keeps its name for wire compatibility, and carried
`ps` RSS before 2026-10.

#### `GET /admission/history`

```
GET /admission/history?since=2026-09-22T00:00:00Z → 200 AdmissionHistory
```

The commands that ended at or after `since` (RFC3339; omitted = all),
oldest first, read from `admission-history.jsonl` next to the database (and
its rotated `.1`, renamed at startup past 20 MB). One line per command, written
when its lease is released or revoked, or when it gives up while queued:

```json
{
  "floor_bytes": 6442450944,
  "records": [ { "ts": "2026-09-29T10:04:12Z", "group": "fix-hovercard",
                 "repo": "/Users/me/dev/app", "class": "tsc", "label": "npx tsc --noEmit",
                 "outcome": "released", "exit_code": 0, "enqueued_at": "2026-09-29T10:01:40Z",
                 "waited_ms": 62000, "wait_by_reason_ms": { "max_jobs": 20000, "memory": 42000 },
                 "held_ms": 90000, "peak_bytes": 1395864371, "cost_bytes": 1610612736,
                 "avail_at_enqueue": 7516192768, "max_pressure": 2,
                 "swapout_bytes_during": 314572800,
                 "compress_bytes_during": 1073741824, "max_compress_rate": 20971520,
                 "admit_compress_rate": 1048576, "admit_compress_calm_rate": 2097152 } ]
}
```

`outcome` is `released`, `revoked` (held past max-hold: `held_ms` is a lower
bound) or `cancelled` (gave up while queued: `held_ms` 0).
`exit_code` is the command's exit status as its client reported it (see
`POST /admission/leases/{id}/exit`); absent when no client reported one — an
older client, a revoked or cancelled lease.
`swapout_bytes_during` and `compress_bytes_during` (bytes compressed plus
decompressed) are machine-wide, `max_compress_rate` is the highest bytes/s read
while it ran; `admit_compress_rate` and `admit_compress_calm_rate` are the
compressor traffic (bytes/s) read when it was admitted, over ten seconds and
over the calm window (0 for a `cancelled` command, and 0 for the second when
the calm window is 0), the data the compressor gates are recalibrated on; `degraded: true` marks a grant made by
the max-starve guarantee; `focus: true` marks a command that was a focus
caller when it queued. Serves `pigtail slots --history`.

#### `GET /cpu/slots/history`

```
GET /cpu/slots/history?since=2026-09-22T00:00:00Z → 200 CPUSlotHistory
```

The CPU slot holds that ended at or after `since` (RFC3339; omitted = all),
oldest first, from `cpu-slots-history.jsonl` next to the database (and its
rotated `.1`, renamed at startup past 20 MB). One line per holder:

```json
{ "records": [ { "ts": "2026-10-22T10:04:12Z", "group": "ws1:vitest",
                 "label": "Button.test.tsx", "enqueued_at": "2026-10-22T10:04:08Z",
                 "waited_ms": 1500, "held_ms": 4000, "outcome": "released" } ] }
```

`outcome` is `released`, `revoked` (held past max-hold; the later release of the
same holder adds no second line) or `cancelled` (gave up while queued: `held_ms`
0). Without an arbiter (`-cpu-slots=0`) the list is empty. `pigtail slots
--history` prints it after the admission summary; its `--json` adds a
`cpu_slots` array next to `floor_bytes` and `records`, left out (reason on
stderr) when the daemon predates this route.

### Session budget

pigtaild measures the live and busy Claude sessions (plan
`docs/superpowers/plans/2026-10-22-session-budget.md`). The **sampler** takes one
record every `-session-sample-seconds` (default 30, 0 = off) and appends it to
`session-samples.jsonl` in the state dir (`$XDG_STATE_HOME/pigtail/`, default
`~/.local/state/pigtail/`; renamed to `.1` at append past 32 MiB, so the two
files cover about five days). The **controller** grants launch slots from
those samples: n = the last sample's busy count plus the pending admissions
(admitted spawns and granted wakes not yet seen by a sample), m = the floor
(`-session-floor`, 6) under memory pressure (judged over `-session-window`,
5 min: any swap write, available memory under `-session-min-available`, or a
mean compressor rate over `-session-compress-kb-per-sec`), the ceiling
(`-session-ceiling`) otherwise. Every rule is the plan's « Table d'admission »
(`sessbudget.Decide`). The gate is off while the ceiling is 0 (the default):
every ask is admitted and nothing is flagged; pigtaild refuses to start with
the gate on and `-session-sample-seconds 0`. All `/session-budget*` routes are
scoped (`classScoped`, behind `scopeMW`).
`pigtail setup --session-budget` writes the six `-session-*` flags into the
launchd plist (floor 6, ceiling 20, window 5 min, min gap 40 s, compressor
16384 KiB/s, available memory 6144 MiB); a re-run without it carries them
forward, `--session-budget=false` writes ceiling 0. Client side, a launch
line the CLI types itself carries the one-command prefix
`PIGTAIL_GATE_PASSED=<spawn|relay|respawn|cmux-fix>` and
`PIGTAIL_SESSION_GATE=0` skips the gate; both are fail-open for the launcher.

#### `GET /session-budget`

```
GET /session-budget → 200 SessionBudgetState
```

```json
{ "enabled": true, "busy": 9, "budget": 20, "gate": "closed",
  "reason": "démarrage progressif, 40 s",
  "deferred_wakes": { "count": 2, "oldest_age_seconds": 180 } }
```

What an agent ask would get now (read-only). `enabled` is false when the
ceiling is 0. `reason` is a D8 label, empty while the gate is open.
`deferred_wakes` is the relay's last-pass gauge of the wakes it deferred for
budget (a display gauge, never a queue). Aggregate counts only: no session row.

#### `POST /session-budget/leases`

```
POST /session-budget/leases  { "kind": "agent", "origin": "spawn", "repo": "pigtail", "no_wait": true }
→ 200 { "admitted": true, "busy": 9, "budget": 20, "reason": "", "lease_id": "sl_…" }
→ 200 { "admitted": false, "busy": 20, "budget": 20, "reason": "budget plein" }
POST /session-budget/leases  { "kind": "human" }
→ 200 { "admitted": true, "busy": 12, "budget": 6, "reason": "compresseur 18 Mo/s", "over_budget": true }
```

One answer, admitted or refused at once: no stream, no queue, nothing stored
for a refusal. `kind` is `human` or `agent` (the relay's wakes ask in process,
not over HTTP); an `agent` ask without `no_wait` is a 400 (`no_wait required
for kind agent`). Every answer carries `admitted`, `busy`, `budget` and
`reason` (a D8 label, `""` when none); a `human` answer also carries
`over_budget` (admitted while n ≥ m) and never a lease. An admitted agent
lease counts in n until it is released, reported launched and then sampled, or
`PendingLeaseMax` (24 min) after admission, for a spawn killed mid-way.

#### `POST /session-budget/leases/release`

```
POST /session-budget/leases/release  { "lease_id": "sl_…" }                  → 204
POST /session-budget/leases/release  { "lease_id": "sl_…", "launched": true } → 204
```

Without `launched`: the spawn did not launch, the place is given back at once
and the progressive-start gap restored (unless another admission came after
it). With `launched: true`: the spawn launched Claude, the lease keeps counting
until the next sample. Idempotent: 204 for an unknown or released id, and for
a lease of another scope, which it leaves untouched.

#### `GET /session-budget/samples`

```
GET /session-budget/samples?since=2026-10-22T00:00:00Z → 200 SessionSamples
```

Scoped (`classScoped`, behind `scopeMW`). The records taken at or after `since`
(RFC3339; omitted = the last 24 h), oldest first, read from the rotated file
then the live one. A session counts, live or busy, only if its process is alive
and its native file still names it; the busy count reads the **native** status
(`busy|idle|waiting`, a native `shell` is recorded `idle`), never the stored
peer column.

```json
{ "samples": [ { "ts": "2026-10-22T12:00:00Z", "live": 2, "busy": 1,
                 "available_bytes": 8589934592, "compressor_bytes": 2147483648,
                 "compress_bytes_per_sec": 1048576, "swapout_bytes_per_sec": 0,
                 "sessions": [ { "session_id": "…", "pid": 4242, "cwd": "/wt/app",
                                 "scope": "perso", "repo": "pigtail", "subject_id": "sub_…",
                                 "status": "busy", "footprint_bytes": 314572800,
                                 "cpu_percent": 38.5 } ] } ] }
```

`compress_bytes_per_sec` and `swapout_bytes_per_sec` are averaged over the last
60 s (0 on the first sample). `footprint_bytes` and `cpu_percent` sum the
session's process tree (CPU is 0 on Linux). A non-master caller gets only the
rows of its own scope; `live`, `busy` and the memory levels stay (the
machine's). A session with no registered scope is the master's alone.
Serves `pigtail session cost`.

### Staffing

pigtaild's **staffing scheduler** (`internal/daemon/staffing`,
sub_x000000000000004) keeps up to N sessions working on the ready subjects of
every scope, in rank order. A **tour** runs once a minute, on every
`session.wait_changed`, `session.launch_changed`, `wait.*` and
`session.pause_changed` event, and after every subject write (creation,
update, phase, promoted captures, ranks). Each tour writes one line to the
decision journal. Three modes: `off` (no tour), `advise` (the default: the
tour journals what it would launch, the Pilote still launches) and `launch`
(the tour launches through the session launcher, `POST /subjects/{id}/launch`).

**Places.** A place is a live session that works, bound to a subject, or a
launch still `pending`. Working is read from the native session file at each
tour, never from the stored `peer_sessions.status`: `idle` and `waiting` (a
dialog waiting on the user) take no place but the session still holds its
subject (`held`); `busy`, `shell`, an unknown status and an unreadable file
count (fail-safe). A session whose launch is `pending`, or recorded less than
`LaunchGrace` (3 min) ago, counts whatever its status. `done` or `wrapped`
frees the place. A paused session holds no place. A live peer whose pid or config dir cannot be checked is
counted and flagged `phantom`.

**Ready queue.** Per scope, the subjects in `understanding`, `designing` or
`building`, by rank. A candidate is skipped, with its `skip`:
`held` (a live session already works it), `phantom` (a phantom session holds
it), `paused` (its session is paused), `pending` (a launch is in flight),
`waits_human` (an open wait addressed to the user; `detail` its type), `deps`
(blocked by an unclosed dependency; `detail` its id), `cooldown` (its last
launch failed or was refused less than 15 min ago; `detail` the hh:mm it may
be taken again), `over_margin` (the scope's head is over the accounts'
launch margin; an unmeasured margin is closed too, `unmeasured: …`) or
`over_budget` (the margin passes, but no account of the scope is within its
weekly token budget; a reading absent, older than 30 min or without a 5-hour
window is closed too, `unmeasured: …`). `detail` of these two is shown to the master only. The pick
is the best rank across scopes; at equal rank, the scope not launched last.

**Order of the gates.** A tour that cannot read its world stops on
`unreadable`. Then: `no_free_place` (places used ≥ N), `no_ready_subject`
(nothing to pick), `over_margin` (every head is refused, at least one by the
margin) or `over_budget` (every head is refused by the weekly token budget
alone, read after the margin; `reason` carries the nearest release or
`unmeasured: …`, for the master only). With a pick,
the gates are read in order, each closed when unreadable: `maintenance`,
`session_budget_off` (the session budget gate is off: `pigtail setup
--session-budget`), `session_budget_full`, `machine_busy` (a test run waits
for a CPU slot, or a command is queued for memory), `burst` (the launcher's
burst window is full). In `advise` the tour ends on `would_launch`. In
`launch` the scheduler asks the session budget for a place (only it does: a
launch by the user or by an agent is never held by this budget) and launches.

**Outcomes.** `idle` (a gate closed; `gate` and `reason` say which),
`would_launch` (advise), `launch_started` (the launcher answered `pending`;
`launch_id`), `launch_refused` (the launcher refused, or answered a
deduplicated launch; `cause`; the subject goes in `cooldown`), `launched` and
`launch_failed` (the outcome of the scheduler's own launch, from the
launcher's list), `settings_changed` (a POST changed the settings) and
`observed_launch` (a launch the scheduler did not make, `origin` the
launch's own `origin`, `reason` its state). A tour line identical to the journal's
last line is folded into it: `repeats` + 1 and `last_at`, no new event.

**Files.** Next to the database: `staffing.json` (`{"mode", "ceiling"}`, written
atomically; absent = `advise`, N = 3; unreadable = read as `advise` with
`settings_error` set, never as `launch`) and `staffing-decisions.jsonl`, one
`StaffingDecision` per line, renamed to `.1` at daemon start past 20 MiB.

**Scope.** All three routes are scoped (`classScoped`, behind `scopeMW`).
A non-master caller sees only its own scope's places (still counted in
`places_used`), queues and decisions; a decision of another scope keeps its
time, mode, counts, outcome and gate, its reason replaced by a fixed label.
An over-margin text (it names accounts and cells) is cleared for every
non-master caller. The settings are global: only the master writes them, with
the cross-scope opt-in (`--cross-scope`). `mode launch` is refused when the
request carries `X-Pigtail-Session` (the CLI of a Claude session sends it):
a guard against an agent's mistake, not a security boundary.

#### `GET /staffing`

```
GET /staffing → 200 StaffingState
```

```json
{ "mode": "advise", "ceiling": 3, "places_used": 2,
  "places": [ { "session_id": "…", "subject_id": "sub_…", "short_title": "…", "scope": "…",
                "label": "…", "wait": "review", "launch_id": "", "pending": false, "phantom": false } ],
  "queues": { "<scope>": [ { "subject_id": "sub_…", "short_title": "…", "scope": "…",
                             "status": "building", "rank": 1, "skip": "", "detail": "" } ] },
  "last": { "ts": "…", "outcome": "would_launch", "subject_id": "sub_…", "reason": "would launch sub_… (…)" },
  "checked_at": "…", "launcher": "pilot", "settings_error": "" }
```

`launcher` is `scheduler` in `launch` mode, `pilot` otherwise: who launches
the next session. Each queue shows its first 5 candidates. `places` and
`queues` are read at the request, in every mode; `last` and `checked_at` are
the last tour's (null before the first one).

#### `POST /staffing`

```
POST /staffing   body: { "mode"?: "off"|"advise"|"launch", "ceiling"?: 1..20 }
                 → 200 StaffingState
```

Behind the maintenance freeze (423 `MAINTENANCE_FROZEN`). An absent field
keeps its value. 403 `SCOPE_MISMATCH` unless the caller is the master with
`X-Pigtail-Cross-Scope: write`; 400 `INVALID_REQUEST` on an unknown mode or a
ceiling outside 1..20; 403 `UNAUTHORIZED` on `mode: launch` with
`X-Pigtail-Session`, its message the command to type from a bare terminal.
A change journals a `settings_changed` line and runs a tour.

#### `GET /staffing/decisions`

```
GET /staffing/decisions?since=<RFC 3339>&limit=<n> → 200 [StaffingDecision]
```

Defaults: the last 24 h, 200 lines, oldest first (the rotated file then the
live one). 400 on an invalid `since` or a `limit` under 1. A decision:

```json
{ "ts": "…", "last_at": "…", "repeats": 0, "mode": "launch", "places_used": 1,
  "ceiling": 3, "outcome": "launch_started", "gate": "", "reason": "launched sub_… (…)",
  "subject_id": "sub_…", "short_title": "…", "scope": "…", "launch_id": "ln_…",
  "origin": "", "cause": "", "skipped": [] }
```

Every route answers 503 `staffing off` when the daemon runs no scheduler (no
store or no launcher).

**Event.** `staffing.decision`, data a `StaffingDecision`, once per journal
line appended (a folded repeat publishes nothing). Scope: the decision's;
untagged, the master's subscribers only.

### Subjects & themes

The subject layer is a durable backlog: **themes** are handles onto vault
Projects/Areas that group **subjects** (prioritised units of work). These
endpoints are **vault-agnostic** — they update only the DB rows
(`themes.name`, `themes.vault_link`, `subjects.theme_id`, …). They never touch
vault files or Obsidian; relocating prose is the caller's job.

```
POST   /themes                       → create        body: { name, kind, vault_link, … }
GET    /themes                       → []Theme   ?scope=
PATCH  /themes/:id                   → update name, vault_link and/or space (DB only)
GET    /spaces                       → []Space  the spaces the caller may name, in folder order
DELETE /themes/:id                   → delete (refused if subjects are linked)

POST   /subjects                     → create
GET    /subjects                     → []Subject  ?theme=&status=&source=&scope=   // 400 on an unknown status (see Subject status)
GET    /subjects/next                → next actionable subject  ?scope=
POST   /subjects/suggest             body: SuggestRequest → 200 SuggestResult, 400   // a read (open under --freeze): candidate subjects for a pane
                                     //   signals pr|worktree (strong) and keyword (weak); a subject the caller may not read is only counted in elsewhere
                                     //   auto = the one strong, writable candidate not refused (« <sub_id> refusé » in the workspace's off-topic reason), else ""
                                     //   400: bad JSON, text/cwd/branch all empty, invalid workspace_id
POST   /subjects/find                body: SubjectFindRequest → 200 SubjectFindResult, 400, 500   // a search that records itself (open under --freeze): see § Subject find
POST   /subject-finds/:fid/pick      body: {subject_id} → 201 | 200 SubjectFindPick, 400, 403, 404, 409   // the subject a search led to (open under --freeze)
GET    /subjects/:id                 → Subject
POST   /subjects/:id                 → partial update (only the keys sent are written)
GET    /subjects/:id/worktree        → live Workspace linked by a phase
POST   /subjects/:id/phases          → create phase
GET    /subjects/:id/phases          → []SubjectPhase
POST   /phases/:id                   → update phase state/workspace
POST   /subjects/:id/log             → append log entry
GET    /subjects/:id/log             → []SubjectLogEntry newest-first  ?since=<unix ms>
POST   /subjects/:id/cmux-workspaces → bind a cmux workspace   body: AttachCmuxWorkspaceRequest (find_id optional: records the search's pick; the answer echoes find_id)
GET    /subjects/:id/cmux-workspaces → []SubjectCmuxBinding
DELETE /subjects/:id/cmux-workspaces/:wsid → unbind
GET    /cmux-workspaces/:wsid        → { binding, subject: {id, slug, short_title, scope} } ; 404 unbound, or bound elsewhere (details.bound_elsewhere)
POST   /cmux-workspaces/:wsid/retitle → hand the description back to pigtail → SubjectCmuxBinding
POST   /cmux-workspaces/{wsid}/trust-pending  body: { pid } → 204   // a spawned Claude waits on "trust this folder" (D14); 400 bad UUID or pid, 503 no sidebar loop
POST   /cmux-workspaces/{wsid}/respawn  body: CmuxRespawnRequest → 204, or 200 {data: CmuxRespawnNotice} for a final state with run_id   // a respawn run's state on its workspace (D16); 400 off the closed contract, 503 no sidebar loop (without run_id only)
DELETE /cmux-workspaces/{wsid}/respawn  → 204                      // drop it; 400 bad UUID, 503 no sidebar loop
GET    /cmux-workspaces/{wsid}/state      → CmuxWorkspaceState    // bound | bound-elsewhere | off-topic | unbound; a link the caller may not read is bound-elsewhere
POST   /cmux-workspaces/{wsid}/off-topic  body: DeclareOffTopicRequest → 201 | 200 WorkspaceOffTopic, 409 bound, 400 reason or space (a first declaration must resolve one: `space` of the scope, `"Inbox"` (the no-subject folder) as sentinel, or a `sub_…` in `reason` whose space is known; the 400 lists the scope's spaces then `Inbox`; the former label, `space.FormerNoSubject`, is no sentinel)
DELETE /cmux-workspaces/{wsid}/off-topic  [?session_id=<sid>] → 200 { cmux_workspace_id }, 404 none
PATCH  /cmux-workspaces/{wsid}/off-topic/space  body: { space } → 200 WorkspaceOffTopic, 403 SCOPE_MISMATCH (non-master, or a row of another scope without the cross-scope opt-in), 404 none, 400 undeclared space   // master only (A8): rewrites only `space` ("" = infer); outside the freeze like declare/lift
GET    /off-topic-workspaces              → []WorkspaceOffTopic   // the rows the caller may read
GET    /alerts                       → AlertsResponse            // open alerts (a respawn that gave up or failed), oldest first; § Alerts
POST   /alerts/{id}/ack              body: AlertAckRequest → 200 AlertResolution   // 404 unknown run, 409 session neither told nor on the subject, 403 cross-scope
POST   /alerts/resolve               body: AlertResolveRequest → 200 AlertsResolved  // a successful relaunch closes its subject's alerts
GET    /cmux/sidebar                 → CmuxSidebarState (the sidebar loop)
POST   /cmux/sidebar                 → switch the loop on/off   body: { enabled } → CmuxSidebarState
POST   /cmux/folders/toggle          → 200 { collapsed, folders }  // D12: fold every own cmux folder, or unfold them all when none is open, in every allowed window; 409 "folders idle"
GET    /board                        → HTML kanban (public)  ?scope=<name>|all
```

**Short title.** `Subject.short_title` (optional) is the label shown where the
full title does not fit — the cmux sidebar. `POST /subjects` and
`POST /subjects/:id` trim it and answer `400 INVALID_REQUEST` when it is longer
than 32 characters (runes) or not a single line — checked on the merged
subject, after the partial update below. Absent from the body, it is kept;
`null`, `""` or blank clears it: the field is then omitted from responses, and
readers derive the label from the title with `pigtail.ShortTitle` — the title
cut on a word boundary to at most 28 characters, followed by `…`, or the whole
title when it fits. The wire carries only the stored value, never the derived
one, so echoing a subject back cannot freeze a derived label into the row. CLI:
`pigtail subject create … --short-title "…"` at creation (validated by the
daemon: a refused one creates nothing), `pigtail subject set <id>
short_title="…"` afterwards; `subject show` prints the label
(`(derived)` when it is not set) and `show --json` adds it as top-level
`display_title` (`subject.short_title` stays the stored value).

**cmux workspace link.** A subject can be bound explicitly to one or more
cmux workspaces (table `subject_cmux_bindings`, migration `0012`); a
workspace carries **at most one** subject. The key is the workspace UUID as
`cmux workspace list` prints it, upper case: the daemon upper-cases what it
receives and answers `400 INVALID_REQUEST` for anything that is not a UUID
(a `workspace:N` ref is resolved by the CLI). It does not check that the
workspace exists.

```jsonc
POST /subjects/sub_42/cmux-workspaces
{ "cmux_workspace_id": "29664DF9-…", "source": "attach", "replace": false, "retitle": false }
→ 201 SubjectCmuxBinding   // new link, or a link moved here with replace
→ 200 SubjectCmuxBinding   // already bound to this subject (idempotent)
→ 409 CONFLICT             // bound to another subject
→ 400 INVALID_REQUEST      // not a UUID, source not attach|subject-work|ptnew
```

- **First link wins.** A workspace bound to another subject is `409
  CONFLICT` "already bound", whose body never names that subject. With
  `replace: true` the link moves only if the caller may also **write** the
  subject holding it (its scope, cross-scope opt-in included); otherwise the
  same `409`. A non-master caller thus learns that some link exists, never
  which, and cannot move one of another scope. Nothing is ever replaced silently.
- `source` records the writer (`attach` by default, `subject-work`, `ptnew`).
- Every link change appends a `note` to the subject log (`GET
  /subjects/:id/log`): a new link, a detach, and on replace one note on each
  subject (the one losing the workspace and the one taking it). An
  idempotent re-attach writes nothing. Best effort: a failed note never fails
  the link.
- `retitle: true` hands the workspace description back to pigtail
  (`description_owner` → `pigtail`; the title is always pigtail's since D5). The other binding fields
  (`prev_title`, `applied_title`, owners, `missing_ticks`) are the sidebar
  loop's memory; a replace keeps them — they describe the workspace.
- `DELETE /subjects/:id/cmux-workspaces/:wsid` → `200 { cmux_workspace_id }`;
  a workspace not bound to **this** subject is `404`.
- `GET /cmux-workspaces/:wsid` is `404 NOT_FOUND` for an unbound workspace
  (`no subject bound to this cmux workspace`) and for one bound to a subject
  the caller may not read (`cmux workspace bound to a subject this scope cannot
  read`, `details.bound_elsewhere: true`). The second names nothing of the
  subject — no id, slug, title or scope —; a work caller learns only that a
  link exists, as attach's 409 already tells it (D7, 2026-10-04). `session
  spawn` leaves such a calling tab's title alone. `subject.short_title` there
  is the effective label (stored or derived).
- `GET /subjects/:id` carries `cmux_workspaces` (UUIDs). It is read-only:
  `POST /subjects/:id` ignores it, like `id` or `scope`.
- `Subject.space` is the subject's own sidebar folder (migration `0033`, D5 of
  sub_x000000000000033): `""` (the default) means the theme's space; a name
  no longer in `spaces.yaml` reads as `""`. It is read-only: `POST
  /subjects/:id` ignores a `space` key, like `id` or `scope`. It has two write
  paths only: a hand drag of a bound session into a space folder (the sidebar
  loop, `cmuxsync/space_drag.go`), and the clear below on a theme change (D8).
  A drag writes `subjects.space`, one `subject_log` line (kind `note`, `space
  dragged: <from> → <to>`, `""` written `(theme: <space>)`) and one `journal`
  row (kind `subject-space-drag`, the subject's scope and `subject_id`, the
  same text behind the subject's short title), in one transaction; a drag
  that changes nothing writes nothing.
- `GET /subjects/:id` carries `blocked_by`, always present: the ids of the
  dependencies that do not unblock it (by the rule of `subject next`,
  `pigtail.SatisfiesDeps`; an unknown id counts), `[]` when none (D30).
  `GET /subjects` and the write answers never carry it; `pigtail subject show
  --json` lifts it to the top level, next to `subject` and `phases`. Read-only.
  The same route adds three more read-only keys, always present, sorted by id,
  deduplicated, never listing the subject itself, `[]` when none:
  `blocked_by_all` (every subject that blocks it, transitively: the closure of
  `deps` walked through unsatisfied subjects only; an unknown id or a subject
  of another scope counts when unsatisfied and is not walked), `blocks` (the
  subjects of the same scope, not satisfied by status, whose `deps` name it;
  `[]` when the subject itself satisfies deps) and `blocks_all` (the same,
  transitively through unsatisfied subjects). `pigtail subject show --json`
  lifts them to the top level too; the text form prints them when non-empty.
- **`session ls` SUBJECT.** The daemon stamps `peer_sessions.subject_id` on
  a session's upsert only when the session is proven alive (its own hook, or
  a row whose pid runs and still names it, a snapshot included): the subject
  its `cmux_workspace_id` is bound to, else the worktree deduction (§ Session
  messages), written as is, `""` included. A session not proven alive never
  changes a non-empty stamp. An unended session reads the binding first (a
  link written after it started shows at once, and the relay's scope
  redaction applies to it), then its stamp. An **ended** session with a stamp
  reads its stamp, even when its tab was rebound, unbound or closed since;
  `cmux_bound` then says the workspace is bound to the subject the row reads.
  A link made after the session started reaches the stamp at its next sign of
  life (a hook, or the sidebar loop's 30 s snapshot). Bind, unbind and
  rebind write no session row.
- No subject deletion route exists, so nothing cascades today; a closed
  workspace's link is cleaned by the sidebar loop: absent from every cmux
  window in two consecutive successful listings (≥ 30 s apart), the link is
  dropped and noted in the subject log. A failed listing never counts.
- `DELETE` hands the binding as it was to the sidebar loop, which gives the
  workspace its title and description back (below).

CLI: `pigtail subject attach <id> [--workspace <UUID|workspace:N>] [--replace]
[--retitle] [--source attach|subject-work|ptnew] [--cross-scope]` and
`pigtail subject detach <id> [--workspace …] [--cross-scope]`. Without
`--workspace`, the pane's `$CMUX_WORKSPACE_ID`; outside cmux it is an error.
A `workspace:N` ref resolves through `cmux workspace list --id-format both`
(the calling window's). `subject show` lists the bound workspaces.

**Off-topic workspaces.** A cmux workspace can instead be **declared
off-topic** (table `workspace_off_topic`, migration `0020`): the session there
works on no subject, by choice. One row per workspace: `scope` (the caller's),
`session_id` (the declaring Claude session, `""` when the CLI cannot tell),
`reason` (one line, 200 characters at most), `declared_at`, `space` (migration
`0024`: the explicit space given at declaration, `""` = none). A workspace is in
one of four states, in this order: `bound` (a link to a subject the caller may
read), `bound-elsewhere` (a link to a subject the caller may not read),
`off-topic` (a declaration), `unbound`.

```jsonc
GET /cmux-workspaces/29664DF9-…/state
→ 200 { "workspace_id": "29664DF9-…", "state": "off-topic", "off_topic": { … } }
POST /cmux-workspaces/29664DF9-…/off-topic   { "session_id": "…", "reason": "…", "space": "…" }
→ 201 WorkspaceOffTopic    // declared
→ 200 WorkspaceOffTopic    // already declared: row unchanged (first declaration kept), but its space (below)
→ 409 CONFLICT             // the workspace is bound (the subject is never named)
→ 400 INVALID_REQUEST      // not a UUID, reason with a line break or over 200 characters,
                           // "unknown space (known: …)": a space the caller's scope does not declare
DELETE /cmux-workspaces/29664DF9-…/off-topic?session_id=<sid>
→ 200 { "cmux_workspace_id": "29664DF9-…" }
→ 404 NOT_FOUND            // no row, a row the caller may not write, or (session_id) another session's row or one with none
```

- `state` answers `200` always (but `400` on a bad UUID, `500` on a database
  error). A link to a subject the caller may not read answers
  `bound-elsewhere`, naming nothing of the subject — the same exposure as the
  lookup's `404` with `details.bound_elsewhere` and attach's `409`; an orphan
  link (its subject gone) answers `unbound`. `subject` and `off_topic` are
  present only when the caller may read them.
- `POST … /off-topic` on a workspace already declared in a scope the caller
  may not read answers `200` with `cmux_workspace_id` and `declared_at` only.
  A creation writes one `journal` row (kind `workspace-off-topic`, the
  caller's scope); the idempotent `200` writes none.
- **Space (D1, sub_x000000000000019).** `space` in the request (trimmed) must
  be a space of the caller's own scope (`--cross-scope` does not widen it);
  otherwise `400 unknown space (known: <the caller's scope's spaces>)`, which
  never echoes the requested name. Redeclaring with a different non-empty
  `space` replaces the stored one (D6) — only on a row of the caller's own
  scope it may write; any other row is left unchanged — and nudges the
  sidebar loop; redeclaring without `space` keeps the stored one.
- **A first declaration needs a resolved space (D4).** Without `space` naming
  a space of the scope, `"Inbox"` (the sentinel for « file it in Inbox »), or
  a non-refused `sub_…` in `reason` whose effective space is readable, `POST`
  answers `400 INVALID_REQUEST` (`space: name one with --space (known: <the
  scope's spaces>, Inbox), or cite a sub_… whose space is known`) and writes
  no row, journal line or marker. A redeclaration on an existing row is never
  refused. A cited subject's space is its own `space` when known, else its
  theme's.
- **A hand drag rewrites `space` (P2, D2).** Dragging a declared workspace's
  tab into a space folder writes `workspace_off_topic.space` with that folder
  (the tab stays there; a later redeclaration can rewrite it) and one `journal`
  row (kind `workspace-off-topic-space-drag`, the declaration's scope, text
  `space dragged: <from> → <to>`). A space the declaration's scope cannot
  read, or a drop anywhere else, writes nothing and the tab goes back.
- **`resolved_space`** (`WorkspaceOffTopic`, computed on every read, never
  stored) is filled by `POST` (`201` and an unmasked `200`), `GET
  /off-topic-workspaces` and `GET …/state`: the explicit `space`, else the
  space of the first `sub_…` cited in `reason` (a whole word, in order) that
  the row's scope may read and whose theme has a space; kept only when the
  space table declares it. Declarations made before `0024` are thus filed
  without rewriting. A masked answer carries none. A store error while
  resolving is a `500`, never read as « no space »; a first declaration
  resolves before any write, so that `500` leaves no row, and a row lifted
  during a redeclaration is declared afresh (`201`). `resolved_space` is
  returned only when the caller may read the space's scope. The sidebar loop
  files the workspace in the folder of its resolved space, after the bound
  sessions there; without one, in `Inbox`. Only the main cmux window is
  arranged; a declaration in another allowed window keeps its place.
- `DELETE … /off-topic?session_id=<sid>` lifts only a row carrying exactly
  that session (the SessionEnd hook); any other row answers the same `404` as
  no row and is kept. Without the parameter the lift is unconditional. A
  master caller lifts another scope's row only with the cross-scope opt-in.
- A successful attach (`201` or `200`) drops the workspace's declaration and
  notes it in the subject log: the link always wins. The sidebar loop drops a
  declaration whose workspace two consecutive listings missed (as links), and
  shows a `pigtail-off` pill on a live declared, unbound workspace.
- The daemon keeps one marker file per bound or declared workspace in
  `<maintenance dir>/workspace-subject/<UUID>` (`bound\n` or `off-topic\n`),
  rewritten at start, every 30 s and after every write: a hook's fast path.

CLI: `pigtail subject off-topic declare [--workspace …] [--reason "…"] [--space <name>]`,
`lift [--workspace …] [--if-session <sid>] [--cross-scope]`, `ls [--json]`.
`declare` prints `✓ cmux workspace <id> declared off-topic (since 15:04) → folder <resolved_space|Inbox>`,
or `✓ cmux workspace <id> already declared off-topic elsewhere: folder unchanged`
on a masked answer, or when a given `--space` was not applied. `ls` has a
`FOLDER` column (`resolved_space` or `Inbox`, ` (inferred)` when inferred).
`pigtail subject which` keeps its text and exit code; `--json` adds
`"state"` (`bound`, `bound-elsewhere`, `off-topic`, `unbound`, or `unknown`
when the state route cannot be read), and an unbound workspace's error gains
` (bound to a subject this scope cannot read)`, ` (declared off-topic)` or
` (state unknown: <cause>)`.

**cmux sidebar loop.** `pigtaild` drives the cmux sidebar from
`internal/daemon/cmuxsync` — the one place the daemon executes the cmux CLI
(the app's embedded CLI under launchd). It is **off by default**:
`pigtaild --cmux-sidebar` switches it on at start, and `POST /cmux/sidebar
{"enabled": bool}` flips it until the next restart (not persisted). Off, it
makes no cmux call at all. `--cmux-sidebar-window <UUID>` (repeatable)
restricts its writes to those windows, for trying it on a throwaway window.

What it writes, and nothing else:

- the status pill `pigtail-attn` (priority 90): the most severe state of the
  workspace, one pill per workspace, in the colour of its gravity (one table,
  `internal/waittype/gravity.go`; the relay design notes § Gravity). Red —
  blocked: `Permission` (`lock.fill`), `Blocked` (`hand.raised.fill`; a
  spawned session stopped on "trust this folder" reads `Blocked · trust
  folder`, § trust-pending below; a respawn that failed reads
  `Blocked · respawn failed`, § respawn below), `Dead`
  (`xmark.octagon.fill`). Orange — your turn to answer: `Question`
  (`questionmark.bubble.fill`), `Decision` (`arrow.triangle.branch`),
  `Waiting for you` (`bell.fill`, untyped), `Respawn`
  (`arrow.clockwise.circle.fill`: a `pigtail session respawn` held by a
  draft, background work or an unreadable screen, or one that gave up (`Respawn · abandoned`), § respawn below), `Your
  turn` (`ellipsis.bubble.fill`: an idle session the loop saw busy and that
  declared nothing). Green — work ready: `Review` (`eye.fill`), `Done`
  (`checkmark.circle.fill`; `Done · <done command>` when no reason was
  given and `session.done_command` is set in `settings.yaml`, else `Done`). Grey —
  nothing to do: `Working` (`hourglass`: the turn ended, background work
  still runs), `Wrapped` (`archivebox.fill`, never with a reason). Order:
  Permission > Blocked > Dead > Question > Decision > Waiting for you >
  Respawn > Your turn > Review > Done > Working > Wrapped. The label is followed by ` ×n` when n > 1
  sessions share the winner's colour (a dead surface counts as one red), else
  by ` · <reason>` when the whole value fits in 36 characters. A session
  waits while its native status is `waiting`, or while it is `idle` with a
  declared wait or a finished turn; up at once, down after 2 s out of it (the
  state is held with it). `Dead` when the latest session of a surface died
  (pid gone, no SessionEnd), confirmed over two observations ≥ 1 s apart,
  while the live state keeps showing. A clean exit lights nothing. A dead
  session counts only while the last `cmux sessions list` lists it (or it
  registered since), so a long-closed pane never stays "dead".
- the status pill `pigtail-srv` (priority 60): the dev servers a workspace
  owns (`owner` = the `CMUX_WORKSPACE_ID` that ran `pigtail dev`), on **one
  line** sorted by the name shown — `dev :5173 up · sb :6006 dead` — with the
  icon of the worst state and the colour of its gravity (`dead` red
  `xmark.circle`, `up` grey `play.fill`, `stopped` grey `stop.fill`: green
  means work ready only). `up`: the port listens. `dead`:
  rammon recorded a death (the server vanished while no stop was announced)
  after the server was last seen up; shown until it listens again or the
  death leaves rammon's 30 min window. `stopped`: seen up, gone, and a rammon
  sweep made after it was seen gone recorded no death — a deliberate stop
  (`pigtail stop`, idle purge, eviction); shown 5 min, kept in memory only.
  Until that sweep (or 2 min if rammon stalls) the server keeps reading `up`,
  so a crash never reads `stopped`. With the RAM monitor off no sweep ever
  runs: a disappearance reads `stopped` at once and nothing reads `dead`.
  Names go through one table (`storybook` shows as `sb`); twins sharing a
  port are one entry showing a single name — the first of that table (`dev`
  before `sb`), else the shortest, alphabetical on a tie — so `dev+web` reads
  `dev` and `storybook+ui-styleguide` reads `sb`. The API and the event keep
  the real `dev+web`. No server, no pill.
- pill values are English and carry **no leading glyph**: cmux draws the
  icon (an SF Symbol tinted with the colour) in front of the value. A pill
  still showing an older text (the French labels with their `⏳` / `▶`) is
  rewritten by the first pass after the reconcile, which seeds what cmux
  really shows.
- `pigtail-inbox` and any other foreign key are never touched. `claude_code`
  is touched in one case only, by a **temporary patch** (to be removed once
  cmux ships manaflow-ai/cmux#15666 and #15603, which are in no release up to
  0.64.25). cmux leaves its own `claude_code` row on `Running` after a Stop it
  received but did not apply. When every live Claude session of a workspace
  has been idle for at least 10 s according to its native
  `sessions/<pid>.json`, and cmux still shows `claude_code=Running`, the loop
  rewrites that row to cmux's own Idle form (`Idle`, icon
  `pause.circle.fill`, colour `#8E8E93`, no priority). It never writes another
  value, never clears the row, and corrects nothing when the surfaces are
  unknown, a live session has no native file, or a session is busy, waiting
  or was idle less than 10 s ago. The check runs on slow passes only, so a
  row cmux sets back to `Running` is corrected at most once per slow pass
  (30 s). The switch is `pigtaild -cmux-claude-code-fix` (on by default):
  `=false` turns the patch off without turning the loop off. `pigtail setup`
  carries it, and `GET /cmux/sidebar` reports it as `claude_code_idle_fix`.
- on **bound** workspaces only: the title (the subject's short title) and the
  description `theme · <status> · #rank` (the subject's status, never its
  open phase, D6; the theme's name, read once per pass so
  a rename shows at the next pass; the subject's scope when the theme cannot be
  read). Before its first write on a field, the
  loop saves what the field showed (`prev_title`, `prev_title_custom`,
  `prev_description`); after each write it persists `applied_title` /
  `applied_description`. A bound workspace always carries its subject's short
  title (D5, 2026-10-09): a listing **started** ≥ 1.5 s after its last write
  **returned** that shows another custom title, or no custom title at all, is a
  **manual rename**, put back at that pass — the short title is renamed again,
  `prev_title` is kept (a detach still gives back the name from before the
  link), and one `note` goes to the subject log: « titre de l'onglet remis à
  « <court> » (renommé à la main en « <nom> ») — pour renommer, change le titre
  court » (« (nom effacé à la main) » for a cleared name), once per manual
  name. A breaker stops a fight: beyond 3 reverts on one workspace in 10 min,
  the loop stops putting that title back until the short title changes (or the
  daemon restarts) and notes it once. To rename a bound workspace, change its
  short title. `title_owner` stays in the schema but is always `pigtail`
  (migration 0021 gave back the titles taken by hand before D5). A
  **description** edited by hand is still a takeover: `description_owner`
  becomes `user`, one `note`, and the loop never writes it again until `POST
  /cmux-workspaces/:wsid/retitle` (`pigtail cmux retitle`) hands it back —
  owner → `pigtail`, applied text forgotten, so the user's description becomes
  the one given back later.
- given back on `DELETE` of the link, and on switch-off: a custom previous
  title is renamed back, an automatic one restored by `clear-name`; a previous
  description is set back, else cleared. A field the user took is left alone.
  Switch-off also clears every `pigtail-*` pill it posed; a daemon stop clears
  the pills only (2 s budget) — titles stay, the link remembers them.

Cadence: every 1 s, the native `sessions/<pid>.json` files (re-read when
their mtime moves); every 30 s, `list-windows`, `workspace list --window` for
each, and `cmux sessions list` (upserted into the session registry, never
reviving a session that ended cleanly); at start and every 10 min,
`list-status` per workspace, so a stale pill cmux persisted is cleared.
Between two of these slow reads (the listings, the reconcile's `list-status`
and the `claude_code` patch's), once the pills are due (a wait changed, 1 s since the last sync, or a 2 s hold ended), the loop syncs the pills
alone — `set-status` / `clear-status` only, from the listing it holds, never
inside the folder arrangement; a workspace whose pills such a checkpoint wrote
after the reconcile read its `list-status` keeps that written state. A wait
recorded or cleared (`session.wait_changed`, `wait.*`) wakes the loop: no
listing, and the next pill checkpoint runs at once. When the 2 s hold of
an attention pill ends between two ticks, the loop wakes at that instant and
syncs the pills alone (no listing, no other call), so the pill does not wait
for the next tick. The
servers are read on the slow tick (the `GET /servers` probe and rammon's
recent deaths) and again on the pass after a `resources.server_died` or
`resources.server_evicted`, without listing cmux. Writes
are serial, ≤ 10/s. cmux unreachable: nothing is written, retried after 5 s
doubling to 60 s, then a full listing and reconcile. A panic is recovered,
reported as `resources.warning` `{source: "cmuxsync"}`, and followed by 30 s
of rest; 5 within 10 minutes switch the loop off (pills cleared, titles given
back) until `POST /cmux/sidebar {"enabled": true}`. pigtail reorders the
sidebar only to arrange its folders (D2, sub_x000000000000041): with
`pigtaild --cmux-folders` (off by default; it acts only while the loop is on),
each pass files the workspaces of every allowed cmux window, each window on its
own with its own folders (D8 of sub_x000000000000007), into one folder per
space (D6 order, then « Inbox »), only while no cmux window, allowed or not,
is key (focus D11), re-checked before every write, never adding the selected
workspace to a collapsed folder, then settles 2 s.
With `--cmux-folder-sort=false` (on by default) the pass files without sorting:
no block move and no whole-window reorder (D39 of sub_x000000000000014:
cmux's `reorder-workspaces` puts the pinned rows first and glues each group
around them, so a folder holding a pinned member would rise, cmux probe P26);
each tab the bands or an entry move (a band change, an unpin, pigtail's add,
which goes to the bottom of its band, of its folder without bands: cmux's add
keeps the side it comes from; D11 of sub_x000000000000030) is
moved alone, `reorder-workspace` right after a neighbour of its own folder,
never above a pinned one; an existing folder moves only by hand (a
new folder is still placed once, at its creation); a workspace is added at the
bottom of its folder, except that a session born right under a member of its own folder stays there (P1 and D2 of sub_x000000000000040: ⌘N from a folder member with cmux's `workspaceGroups.newWorkspacePlacement` at `afterCurrent`, its default; `pigtail session spawn` passes `--group-placement afterCurrent --group-reference <caller>`, D1; D1 holds in both sort modes) — a created folder goes right before « Inbox » in the
current order (after the last block without it, on top when there is none;
« Standby » after the last block) and is never moved again. `folders` says whether it
is on, `folders_note` why it idles or stopped (a refused create).

`POST /cmux/folders/toggle` (D12, the user's key; CLI `pigtail cmux folders toggle
[--quiet]`) is the one write of a folder's folded state outside its creation.
No body. It acts on every allowed cmux window, each by its own state (D8 of
sub_x000000000000007): `folders` is the sum over the windows, `collapsed` the
state of the first window it wrote; a failure stops it there (the windows
already done keep their state) and is returned. Only pigtail's own folders count and move (a group made by hand, a
pinned group, a folder whose anchor hosts a session are left alone): at least
one expanded → every expanded one is collapsed; none → every one is expanded
(« Standby », time 2, excepted). It applies while cmux is the key window; the
arrangement never undoes it (the folded state is kept per folder name).
`200 {"collapsed": bool, "folders": n}` says the state it left and how many
folders it moved. The write runs on the loop's goroutine between two passes,
so the request waits for it (its own context bounds the wait: `499` /
`503` when the caller left or the loop is late, nothing written for a caller
that left). `409 "folders idle"` — nothing written — when the loop or
`--cmux-folders` is off (or the daemon runs no loop), the loop's window list is
older than two slow intervals or the last listing failed, no allowed window was
listed, or the folders were stopped. Behind `scopeMW` like
`POST /cmux/sidebar`; the answer names no subject.

```jsonc
GET /cmux/sidebar
→ 200 { "enabled": true, "claude_code_idle_fix": true,
        "disabled_by_panic": false, "cmux_available": true,
        "last_reconcile_at": "…", "errors_last_hour": 0, "windows": [],
        "folders": false, "folders_note": "",  // folder arrangement (--cmux-folders); note omitted when empty
        "folders_windows": ["…"],  // the windows the folders arrange: every allowed one (D8, D32); omitted when none
        "folders_wait": [ { "window": "…", "since": "…", "reason": "key" } ], // per window, a folder round owed, not yet applied; omitted when none
        "warnings": [],            // e.g. cmux sidebar.showCustomMetadata is false; in unsorted mode, workspaceGroups.newWorkspacePlacement other than afterCurrent, or a settings file pigtail cannot read
        "workspaces": [ { "cmux_workspace_id": "…", "window_id": "…",
                          "attention": "waiting"|"dead"|"",
                          "wait_type": "permission"|"blocked"|"limit"|"login"|"question"|"arbitrage"|"untyped"|"respawn"|"api_error"|"mac"|"your_turn"|"review"|"done"|"working"|"wrapped"|"",
                          "wait_reason": "PR #142 ready",  // one session of the winner's colour only
                          "waiting_count": 1, "title_owner": "pigtail", // bound workspaces only: the bound marker (always "pigtail" since D5)
                          "title": "Relance cmux sûre",    // the title the sidebar shows, status glyph removed
                          "display_name": "Relance cmux sûre", // the session's one name (D3/D5)
                          "servers": [ { "name": "dev", "port": 5173,
                                         "state": "up"|"stopped"|"dead"|"conflict" } ],
                          "subject": { "id", "short_title", "scope", "status", "rank" } | null, // status: the subject's (D28)
                          "cells": ["h1", "w1"] } ] }  // live sessions' cells, omitted when none
```

`folders_windows` and `folders_wait` (D3 of sub_x000000000000019; per window
since D32 of sub_x000000000000007, which replaces the main window of D2) are
omitted while the loop or `--cmux-folders` is off. `folders_windows` lists every
allowed window the folders arrange, in listing order. `folders_wait` lists, per
window, the folder round owed and not yet applied, read-only: one entry per
window that waits, in `folders_windows` order; `window` names it, and is
omitted only for `no-window` (the loop's own wait, listed first). A cause the
pass shares — `key` (any cmux window is key, focus D11), `focus-poll`,
`listing-down`, a store `read-error`, `stopped` — is set on every window it
holds back; a window's own cause — `settle`, `selected-collapsed`, the
`read-error` or `write-error` of its own cmux calls — on that window only, and
a window whose round applies clears its entry alone. `since` is the start of the wait,
`reason` one of `key` (cmux is the key window, or its state is unknown),
`settle` (the 2 s pause after a round), `focus-poll` (the focus is read at most
every 2 s), `selected-collapsed` (the selected workspace waits to enter a
collapsed folder), `listing-down`, `read-error`, `write-error`, `stopped` (a
created folder read back wrong) or `no-window` (no allowed window listed).
`settle` and `focus-poll` name a wait only when they open it: they never
replace another reason, and a later reason never moves `since` (each window
keeps its own). CLI: `pigtail cmux sidebar status` prints one line per entry,
`folders wait: key since 15:04:05 (3m12s)` — `folders wait 6A73C8EF: key since …`
(the window's first 8 characters) when the folders arrange several windows —,
then `folders window: <id>`, or `folders windows: <id>, <id>` for several.

Both `/cmux/sidebar` routes sit behind `scopeMW`: a bound subject the caller
may not read is `null` (attention stays — it does not reveal the subject), and
so is the `wait_reason` told by a session of a scope the caller may not read
(the `wait_type` stays). `cells` lists the cells of the workspace's live
sessions in the order its description shows them; a cell whose session is of a
scope the caller may not read is left out, and a cell with no scope is the
master's only, like a reason (D17).
`title` is the title the sidebar shows: for a bound workspace the subject's
short title pigtail keeps on it (D5, even while a manual rename is being put
back), for an unbound one cmux's title as shown — the name given by hand kept
whole (« #201 relance »), else cmux's own title without its status glyph.
`display_name` is the session's one name (`pigtail.SessionDisplayName`, D3):
the subject's short title when the workspace is bound and the subject readable,
else `title`; it is what `pigtail session ls` labels and agents cite. Both
follow the subject's redaction (D23): a bound workspace whose subject is `null`
for the caller — another scope, or a subject the loop could not read (the
master alone sees its `title`) — has neither; an unbound workspace with a cell
the caller may not read has neither.
`POST` without a loop in the daemon is `503`.

A server in `conflict` is a worktree whose port is in conflict (see
`port_conflicts` of `GET /servers`): the pill reads `dev :3013 conflict` in
orange (icon `exclamationmark.triangle`), under the cmux workspace of the
worktree in conflict — the server's owner marker when it has one, else every
cmux workspace hosting a live Claude session in that worktree; none of
either, no pill. It wins over `up` for the same port and is not retained.

`POST /cmux-workspaces/{wsid}/trust-pending` `{ "pid": <claude pid> }` →
`204`: `pigtail session spawn` saw the new session stopped on Claude's "trust
this folder" dialog (D14); since pre-trust (2026-10), only when trust could not
be inherited from a trusted ancestor in the cell — `session spawn --help` gives
the rule; the loop counts that workspace as one red session
`Blocked · trust folder`. Memory only; the pill shows until a session of the
workspace registers with its native file, the pid exits, or a listing after
it no longer shows the workspace. Behind `scopeMW` with the sidebar routes; it
names no subject. `400` on a bad UUID or a missing pid, `503` without a loop
in the daemon. With the loop switched off it still answers `204`, and no pill
shows; switching the loop off forgets the pending spawns.

`POST /cmux-workspaces/{wsid}/respawn` →
`204`: the state of a `pigtail session respawn` run on its workspace (D16),
posted by its worker on the run's workspace UUID, never the caller's
environment (#36). It replaces the workspace's last state; an unchanged one
(the worker re-posts a waiting state every minute, since a daemon restart
forgets it) asks for no extra pass. The body is a closed contract:

```
{ "state": "waiting",   "wait": "draft"|"background"|"screen", "surface_id": "<UUID>", "pid": <worker pid> }
{ "state": "abandoned"|"failed", "surface_id": "<UUID>" }   // surface_id optional
```

`waiting` counts as one orange `Respawn · draft in the input|background
work|screen unreadable` (the daemon's one table of those words) and stands
in for the computed `Your turn` of the run's surface (one wait, not two); it
drops once the worker's pid exits. `abandoned` counts as one orange
`Respawn · abandoned` (`wait_type` `respawn` in `GET /cmux/sidebar`) and
stands in for the computed `Your turn` of the run's surface like a waiting
one; it closed nothing, so a session that dies there reads a real `Dead`.
`failed` counts as one red `Blocked · respawn failed`, and the run's surface
reads dead to no one (a failed respawn closed that session itself). Both stay
until the user acts on that surface, read on a live session there: a Claude
started after the mark (a relaunch), or a session read `busy` after an `idle`
read seen since the mark, both on Claude's `statusUpdatedAt`. An `abandoned`
mark also drops at a typed prompt read in the transcript of a live session of
the surface, stamped after the mark: a `user` entry or an `attachment` of
type `queued_command` (a prompt typed during a turn), `origin.kind` `human`,
not `isMeta`, not pigtaild's wake line or `/compact`, not a turnless command
(`session.turnless_prompts` in `settings.yaml`); the one clear that holds while Claude keeps its native
file `busy` for a background task. It is read incrementally, only while an
abandoned mark stands. A `failed` mark ignores prompts: its fresh session was
launched with a prompt in argv, which would clear it. Any state drops once
a listing after it no longer shows the workspace, and when the loop is
switched off. Memory only, scope-free like trust-pending: it names no
subject. `DELETE` on the same path → `204` drops it (the worker stopped
waiting, or the respawn succeeded). `400` on a bad workspace or surface UUID,
an unknown state or wait, a waiting state without surface or pid, a final one
with a wait or a pid; `503` without a loop in the daemon.

**A final state carries its run** (subject « abandon respawn visible »,
D1/D2). `abandoned` and `failed` take four optional fields, refused (`400`) on
`waiting`:

```
{ "state": "abandoned"|"failed", "surface_id": "<UUID>",
  "run_id": "<run>", "subject_id": "sub_…", "session_id": "<the run's session, its current id>",
  "detail": "<why, as the worker's record says it>" }
```

`run_id` and `session_id` match `[A-Za-z0-9._:-]{1,100}`, `subject_id` the
shape of a subject id (`sub_[a-z0-9]+`); `detail` is flattened to one line
and cut at 1500 bytes. With `run_id`, the daemon tells the session and
answers `200 {data: CmuxRespawnNotice}` — with or without a sidebar loop:

```json
{"journal": "ok", "wait": "ok", "notice": "ok", "pill": "ok"}
```

Each field is `ok`, `duplicate` (this run's final state was handled
already), `skipped: <why>` or `failed: <error>`; `wait` is also `none` for
`abandoned`, which poses no wait. `pill` is reported apart:
`skipped: this daemon runs no cmux sidebar loop` when the loop is absent, off
(the default) or stopped — the session is told all the same. What the daemon
does, in this order:

- **journal** — one line `kind=respawn-<state>`, `id=respawn:<run>:<state>`,
  in the **subject's scope** (the caller's without a subject; a scope the
  caller may not write with its `--cross-scope` refuses the line, reported
  `failed:`), `subject_id` of the run, `source=pigtail:respawn/<run>`
  followed by `?session=<id>` when a session was told (the one that may ack
  the alert, § Alerts), summary `respawn <state> <run>: <detail>` (≤ 280
  runes), ending `— no live session to tell` when nobody was. The line is
  the alert, and its id the run's idempotence key: the same final state
  posted twice answers `duplicate` everywhere and writes, poses and sends
  nothing more.
- **wait** — `failed` only: on the session's current id, when it is alive and
  acts in the caller's scope: `type=blocked`, `source=auto` (it never holds
  the idle wake), reason `respawn failed: <detail>`. An `abandoned` run poses
  none (`wait: "none"`): it would overwrite the session's own Permission /
  Question.
- **notice** — one message `from=pigtail kind=info` that names the run, the
  state, the subject and the detail, and asks the session to tell the user then
  run `pigtail alerts ack <run>` (with `--cross-scope` when the line is not
  in the caller's scope); it goes through the existing wake paths.

A session that is gone, unknown or of another scope: `wait` and `notice`
answer `skipped: session <8> …`, the line still exists (and its alert shows).
Without `run_id` (an older worker) the route keeps its `204` and pill-only
behaviour, `503` without a loop. A detail of secret shape
(`relay.SecretShape`) is withheld everywhere.

`retitle` is a write on the bound
subject: `403 SCOPE_MISMATCH` from the master on another scope without the
cross-scope opt-in, and `404` alike for an unbound workspace and one bound to a subject
the caller may not read. CLI: `pigtail cmux sidebar [status|on|off] [--json]`,
`pigtail cmux retitle [--workspace <UUID|workspace:N>] [--cross-scope]` and
`pigtail cmux check [--fix] [--surface <UUID>]… [--json] [--last]` (no route
of its own: it reads the peers, servers and sidebar state through existing routes
and writes its report to `$XDG_STATE_HOME/pigtail/cmux-check/last.json`).

#### Session launch (sub_x000000000000015)

pigtaild opens a cmux tab running the `c` launcher on a subject, with no
calling session (the daemon's third cmux place, `internal/daemon/launch`),
then follows it until the session registers.

```
POST   /subjects/{id}/launch          → 202 {"data": SessionLaunch}  new launch, state "pending"
                                        200 {"data": SessionLaunch}  an API launch of the subject is under way: the same one, deduplicated: true
GET    /launches/{id}                 → 200 {"data": SessionLaunch}
```

**Request.** `POST /subjects/{id}/launch`, an empty body or `{}`. Auth as
every route (socket, bearer over HTTP, or the web front's cookie); scope from
the caller's `X-Pigtail-Scope` — a subject of the caller's own scope only,
except for the web front, which launches a subject of any scope
(§ Web (browser)); the route sits behind `FreezeMW` (`MAINTENANCE_FROZEN`).
The answer comes once the tab is created, within ~20 s (`new-workspace` is
bounded at 15 s).

**Placement.** The tab opens in the subject's live worktree when it has one
(pre-trusted in the cell), else in the scope's `launch_dir` (`scopes.yaml`).
Its cell is the launch margin guard's pick in the subject's scope, the
scope's lowest-rank cell when the guard cannot measure; its permission mode is
the cell's default (`bypassPermissions` refused). With the sidebar folders on,
the tab is added at the bottom of the subject's folder, else where cmux puts
it. The tab is bound to the subject (source `ptnew`).

**`SessionLaunch`.** Every field is always present (empty string = unknown):

```json
{
  "launch_id": "ln_1a2b3c4d5e6f7a8b",
  "subject_id": "sub_…",
  "state": "pending",
  "cause": "",
  "origin": "api",
  "workspace_id": "6F0C…UUID",
  "workspace_ref": "workspace:12",
  "cell": "h1",
  "permission_mode": "auto",
  "cwd": "/Users/…/home",
  "tab_title": "Web launch",
  "started_at": "2026-10-25T10:00:00Z",
  "updated_at": "2026-10-25T10:00:03Z",
  "deduplicated": false
}
```

`state` is `pending`, `launched` or `failed`; `cause` is filled for `failed`
only. `tab_title` is the name the launcher gives the cmux tab, the subject's
`ShortTitle`, on the `POST` answer (new or deduplicated); it is `""` on
`GET /launches/{id}` and on the event, which read the registry. A launch is `launched` once a live session registers on its tab
(`POST /peers` with its `cmux_workspace_id`, updated at or after the tab's
birth), `failed` when none has within 5 min of the tab's birth. A daemon
restart resumes the open API launches to their original deadline.

**Follow-up.** `GET /launches/{id}` reads the registry (`spawns.jsonl`) for
any launch, CLI ones included (`origin` `cli`). `404 NOT_FOUND` « no such
launch » for an unknown id, or a subject outside the caller's scope (same
answer; the web front reads a launch of any scope); a CLI launch with no subject has no scope: `404` for every caller
but the master. Before the `launch-armed` record (a few ms after the tab's
creation) `cell`, `cwd` and `permission_mode` are `""`; the launcher's 202
carries them.

**Event.** `session.launch_changed`, data a `SessionLaunch` without
`deduplicated`: published at `pending` (tab created), then at `launched` or
`failed`; tagged with the subject's scope (§ Events).

**Refusals.** Standard error body. `details` carries `subject_id` on the
launcher's refusals only (409/429/403/502/503 below); the refusals before the
launcher carry none (`404` from `authorizeSubject`, `LAUNCH_OVER_MARGIN`,
`MAINTENANCE_FROZEN`).

| HTTP | `error.code` | Cause | `details` besides |
|---|---|---|---|
| 404 | `NOT_FOUND` | unknown subject, or outside the caller's scope (same message « no such subject ») | — |
| 409 | `SESSION_LIVE` | a live session on a tab of the subject | `workspace_id`, `pids` |
| 409 | `LAUNCH_IN_PROGRESS` | a CLI launch of the subject under way | `launch_id`, `workspace_ref` |
| 429 | `LAUNCH_BURST` | more than 10 launches in 10 min | `recent`, `frees_at` |
| 409 | `NO_CELL` | no cell for the subject's scope | `scope` |
| 409 | `LAUNCH_OVER_MARGIN` | every cell of the scope over the margin; the message has no `--over-margin` advice (the API has no such flag) | — (`details` nil) |
| 403 | `BYPASS_REFUSED` | the cell's default mode is `bypassPermissions` | `cell` |
| 503 | `CELL_MODE_UNREADABLE` | the cell's `settings.json` unreadable, or its mode unknown | `cell`, `cause` |
| 409 | `NO_LAUNCH_DIR` | no live worktree, and no `launch_dir` declared or existing for the scope | `scope` |
| 503 | `CMUX_UNREACHABLE` | cmux does not answer (windows or tabs listing) | `cause` |
| 502 | `CMUX_CREATE_FAILED` | `new-workspace` failed, or its tab not found after | `cause` |
| 503 | `LIVE_UNKNOWN` | Claude processes unreadable: a duplicate cannot be ruled out | `pids` |
| 503 | `REGISTRY_UNREADABLE` | the launch registry unreadable | `cause` |
| 423 | `MAINTENANCE_FROZEN` | maintenance `--freeze` (`FreezeMW`) | as usual |
| 503 | `INTERNAL` | launcher off (« launcher off ») | — |

`GET /launches/{id}` answers `503 REGISTRY_UNREADABLE` when the registry
cannot be read.

**Idempotency.** Replaying the request while an API launch of the same
subject is under way returns that launch: `200`, `deduplicated: true`, the
same `launch_id` — never a second tab. A CLI launch under way is refused by
`LAUNCH_IN_PROGRESS`, with its `launch_id`. No `Idempotency-Key` header
(§ 7).

#### SDK sessions

A SDK session is a Claude session held by the Claude Agent SDK instead of a
cmux tab: one detached TypeScript host per session (`clients/sdkhost`, SDK
pinned) that keeps the session open between turns and takes answers over a
Unix socket. The host writes its state and journal to files; pigtaild reads
them (so a session survives a pigtaild restart) and serves scoped routes.
Launching one is behind the daemon flag `-sdk-launch`, **off by default**,
until the session hooks recognise the marker below; the routes themselves
need `-sdk-host` and `-sdk-node` (without them: `503 SDK_OFF`).

**Files.** One directory per session, `<state>/pigtail/sdk/<id>/`:

| File | Writer | Content |
|---|---|---|
| `meta.json` | pigtaild, before the host starts; rewritten once, atomically, to add `host_pid` after the spawn (0 before) | `{version:1, id, subject_id, cwd, config_dir, claude, mode, prompt, session_uuid, host_pid, created_at}` |
| `host.json` | the host alone, atomically (tmp + rename), when its loop starts then at every state change | `{version:1, host_pid, claude_pids, claude_session_id, state, turn, requests, updated_at}` |
| `events.jsonl` | the host, append only | one line per event: `{seq, at, kind, data}`, `seq` strictly increasing |
| `host.out`, `host.err` | pigtaild opens them for the host's stdout and stderr | host output |
| `host.sock` | the host | Unix socket, NDJSON commands `answer`, `say`, `end`, `status` |

`state` is one of `starting|running|waiting|idle|ended|failed|lost`; `idle`
means a turn is over and the session waits for a reply. `claude_session_id`
comes from claude's `init` message; `claude_pids` are the direct children of
the host process; `requests` are the pending requests (`rid`, `kind`
`question|permission`, `tool`, `input`, `opened_at`). A request has no
deadline: it stays pending, across a pigtaild restart, until answered.
`version` is 1; the shared fixture `clients/sdkhost/testdata/` (`meta.json`,
`host.json`, `events.jsonl`: a `waiting` session with one pending request) is
decoded by a Go test and a TypeScript test, so the two cannot drift.

**Routes.** All behind the scope middleware (the subject's scope decides; an
unknown id and another scope's id answer the same `404`), the writes behind the
maintenance freeze. Without `-sdk-host` and `-sdk-node` every one answers
`503 SDK_OFF` (the message carries the reason when pigtaild could not wire the
option, e.g. claude not found on `PATH`).

| Route | Answer |
|---|---|
| `POST /subjects/{id}/sdk-sessions` | `202 {id, state:"starting"}` once the session is reserved (the host starts after the answer); `409 SESSION_LIVE` (a TUI or SDK session of the subject is live, or a launch is under way; `details` names it); `403 BYPASS_REFUSED` (the cell's default mode is `auto`: a SDK session would run without permission cards); `503 SDK_OFF`; `503 SDK_LAUNCH_OFF` (pigtaild runs without `-sdk-launch`) |
| `GET /sdk-sessions[?subject=<id>]` | `{sessions:[Session], launch_enabled}`; without `subject`, only the sessions whose subject the caller may read (the master and the cookie read all); a `subject` the caller cannot read is a `404` |
| `GET /sdk-sessions/{id}` | `Session`; the subject's sessions are settled first (a dead host's session reads `lost` or `failed`, a proven orphan claude is killed) |
| `GET /sdk-sessions/{id}/events?after=<seq>` | `{events:[Event], last_seq}`; `last_seq` is the highest `seq` returned, else `after`; `after` must be a non-negative integer (`400`) |
| `POST /sdk-sessions/{id}/requests/{rid}/answer` | body `{behavior:"allow"\|"deny", answers?:{<question text>: <label> \| [<label>…]}, message?}`; `200 {answered}`; `400` bad body; `404` unknown `rid`; `409` already answered, or the host is unreachable or refused (the session moved on) |
| `POST /sdk-sessions/{id}/messages` | body `{text}`; `200 {sent}`; `400` empty text; `409 NOT_IDLE` unless the state is `idle`; `409` host unreachable |
| `POST /sdk-sessions/{id}/end` | `200 {ending}`; `409` when the session is already `ended`, `failed` or `lost` |

`Session` is `{id, subject_id, cwd, config_dir, claude_session_id, state,
reason?, resume_command?, host_pid, claude_pids, turn, pending:[Request], live,
started_at}`. `live` means a writer exists (the host, a claude, or a launch
under way): no `resume_command` is offered while it is true, and a TUI launch or
`pigtail session spawn|respawn` on the subject is refused. `reason` says why a
session is `failed` or `lost`. `resume_command` (`cd <cwd> && CLAUDE_CONFIG_DIR=<cell>
claude --resume <id>`) appears only once nothing writes the session any more.

`Event` is one `events.jsonl` line as is: `{seq, at, kind, data}`, `kind` among
`init`, `state`, `request_opened`, `request_answered`, … (`data` is the host's,
opaque to pigtaild).

**Writes take the cookie alone.** `answer`, `messages` and `end` are the web
front's: a request carrying `X-Pigtail-Session`, or one that does not come
through the web cookie (a bearer caller, the CLI), answers `403
UNAUTHORIZED` before anything is read. This reverses D2 of `POST
/waits/{id}/answer` ("answer it in the terminal") for SDK sessions only: a SDK
session has no terminal, and a permission card is the point. The launch is
open to the cookie and to a caller of the subject's own scope. The CLI reads
(`pigtail sdk ls|show [--json]`), it does not answer. Answering a question
clears the session's `auto` waits (`ClearSessionWaits(<claude session>, auto)`);
declared waits stay.

**Deployment.** `-sdk-host <dir>` is the `clients/sdkhost` directory and
`-sdk-node <path>` the node binary (22+); both, or neither (one alone is
refused at start). `make sdkhost` installs the host's pinned dependencies and is
run at post-merge, before restarting the daemon. claude is resolved at start on
a `PATH` cleaned of the cmux shim. `-sdk-launch` opens `POST
/subjects/{id}/sdk-sessions`; it needs both flags and stays **off** until the
session hooks recognise the marker below. `pigtail setup` carries
the three flags from the existing plist like `--gate-cmd`; none is a default.

**Marker contract (D12).** For a hook to know a session is a pigtail SDK
session bound to a subject:

1. pigtaild sets `PIGTAIL_SDK_SESSION=<id>` in claude's environment.
2. The session is registered with pigtaild: `pigtail sdk show <id> --json`
   returns `subject_id`, `claude_session_id` and `state`.
3. The proof a hook makes: `claude_session_id` equals the `session_id` of the
   hook's stdin. Equal: the session is bound to `subject_id`. Otherwise it is
   treated as an unbound session.

`CLAUDE_CODE_ENTRYPOINT` is `sdk-ts` under the SDK and cannot be forced: hooks
that guard on `cli` must use this contract instead.

**Binding invariant.** A SDK session is born bound to its subject and that
binding never changes: no route unbinds or rebinds it.

#### Journal (index of pointers)

A transversal journal of events: one row per event, a one-line summary and a
link to where the content lives — **never the content**. Sister of the subject
log (`subject_log` keeps a subject's lifecycle; an event may be in both).
Written by deterministic producers (a session-end hook, a gate tool on fire,
a meeting sync; the merge hook later), read across subjects.

```
POST /journal   → 201 {entry, created:true} | 200 {entry, created:false}
                  body: { id, kind, summary, source, ts?, subject_id?, scope? }
GET  /journal   → []JournalEntry newest first (ts, then id)
                  ?since=<ms, inclusive> &until=<ms, exclusive> &scope= &kind= &subject= &limit=<1..1000, default 200>
```

Fields and bounds: `id` ≤ 200 bytes, no whitespace, **required** — the
producer's deterministic id; `(scope, id)` is the key: re-posting it is a no-op
that echoes the stored row unchanged (first writer wins); an id present only in
the other scope is another row. `kind` `^[a-z][a-z0-9-]{0,63}$`. `summary` one
line, ≤ 280 characters. `source` one line, ≤ 1024 bytes, starting with a URI
scheme. `ts` RFC 3339, default the daemon's now. Violations → 400
`INVALID_REQUEST` naming the field.

Scope: with `subject_id`, the subject must be one the caller may write (else
the unknown-subject 404, or 403 `SCOPE_MISMATCH` for the master on another
scope without the opt-in); the row takes the subject's scope and a different
`scope` is 400. Without a subject, `scope` defaults to the caller's; a scope
writes itself only; the master writes another scope only with
`X-Pigtail-Cross-Scope: write`. `scope` is a declared name or alias, stored
canonical; a scope the caller cannot read is the 400 of an unknown name (D19).
Reads follow `scope.ListFilter`: a non-master caller only ever receives its
own scope's rows. No SSE event.

Conventions (producers):

| kind | id | source |
|---|---|---|
| `session-end` | `session-end:<subject>:<local date of the subject log line's ts>:<step>:t<round>` | `vault:<trace or index path>` |
| `gate-added`, `gate-fired`, `gate-closed` | `gate-<event>:<gate id>` | `file:<gates dir>/gates.jsonl#<gate id>` |
| `meeting` | `meeting:<note id>` | `vault:<vault-relative path>` |
| `delivery` (merged PR) | `delivery:<owner/repo>#<n>:merged` | the PR URL, or `pigtail:subjects/<id>/log#<log id>` |
| `decision-<type>` | `decision:<sha1-16 of the line without org>` | `file:<decisions dir>/decisions.jsonl#ts=<ts>` |
| `subject-find` | `subject-find:<fid>` | `pigtail:subject-find/<fid>` |
| `subject-find-pick` | `subject-find-pick:<fid>` | `pigtail:subject-find/<fid>` |
| `runtime-claim-expired` | `runtime-claim-expired:<rtc id>` | `pigtail:runtime/claim#<rtc id>` (scope: the holder's label scope, else the master; written by pigtaild's sweep only, never the label nor the reason) |
| `tmp-swept`, `tmp-would-sweep` | `<kind>:<unix nanoseconds>` | `proc:pigtaild` (scope: the master; summary `n=<k> bytes=<b> oldest=<age>`; written by pigtaild's tmp sweeper only, `-tmp-sweep on` / `observe`) |
| other subject-log kinds (back-fill) | `subject-log:<log id>` | `pigtail:subjects/<id>/log#<log id>` |

CLI: `pigtail journal add --id --kind --summary --source [--subject] [--entry-scope] [--ts] [--json] [--cross-scope]`,
`pigtail journal list [--since] [--until] [--kind] [--subject] [--limit] [--json]`,
`pigtail journal import [--cross-scope]` (NDJSON on stdin; exit 1 when a row is refused).

#### Subject find

A search that records itself: a ranked list of candidate subjects for a few words, one `subject_finds` row and one journal line per accepted search, and the choice the search led to. Promise (2026-10-29): the right subject is among the first 5 candidates in the large majority of cases; its real rank is read in use from the picks, not from a bench.

- **Corpus**: every subject of every scope, every status (done, parked, dropped, captured included). The idf weights do not depend on the caller; the partition decides what goes out (as `POST /subjects/suggest`).
- **Terms**: `suggest.Tokens` (lower case, French accents folded, stop words, < 3 letters and numbers dropped) plus the two-letter ASCII words that are not stop words (`DS`, `PR`, `UI`, `QA`). `suggest.Tokens` itself is not changed.
- **Match of a query term against a subject** (title + short title): exact term → weight `idf(df_exact)`; else a prefix in either direction (query ≥ 3 letters prefix of a term, or a term ≥ 4 letters prefix of the query: `notif` → `notifications`, `sujets` ↔ `sujet`) → `0.8 × idf(df_loose)`; a two-letter acronym matches exactly only; else the theme name (same match) → `0.5 × idf(df_theme)`. `idf(df) = ln((N+1)/(df+1))`.
- **Score** = sum of the weights × (matched terms / query terms); a closed subject (`pigtail.IsClosedStatus`) × 0.85 — open before closed at comparable relevance, a clearly better closed subject stays above. A subject matching no term is no candidate.
- **Order**: score desc, then last activity desc (no activity last), then id asc.
- **Recency** (`last_activity`): `max(subject_log.ts)` over kinds `phase`, `progress`, `handoff`, `delivery`, `decision`, one `GROUP BY`; absent when none.
- **Partition**: a candidate of a scope the caller may not read is counted in `elsewhere`, never named (body, stored row, journal line). `writable` = the caller may write it without the cross-scope opt-in.
- **Recording**: every accepted search writes one `subject_finds` row (query normalised — whitespace runs → one space —, readable candidates `[{subject_id, score}]` in order, the pane's cmux workspace) and one journal line, in one transaction; a refused request (400) records nothing; a record failure is a 500 and no result (never an unrecorded search).
- **Choice**: `subject attach --find <fid> <subject_id>` (link + choice) or `subject pick <fid> <subject_id>` (choice alone). It writes `picked_subject_id`, `picked_at` and one journal line `subject-find-pick` carrying the rank of the chosen subject among the candidates (0 = not among them) and the number of searches of that cmux workspace since its previous choice, this one included (at least 1: choosing a search older than the workspace's latest pick, whose count would be 0, reads 1; also 1 without a workspace). The same choice again changes nothing (200); another subject on a picked search is 409. A subject the search's own scope cannot read (`scope.CanRead(scope.Scope(find.Scope), subjectScope)` false) is refused before any write or link, in `pick` and in `attach --find` alike, with the same 404 as an unknown subject: a master with `--cross-scope` cannot tie a work search to a perso subject, so no id of another scope reaches a search's row or its journal line.

**CLI.**

```
pigtail subject find <words…> [--limit 1..20, default 5] [--json]      # scopedRead, no --cross-scope
pigtail subject pick <find_id> <subject_id> [--json] [--cross-scope]    # scopedWrite
pigtail subject attach <subject_id> --find <find_id> [existing flags]   # scopedWrite (unchanged annotation)
```

`--pick` is not a flag of `find`: § Subject scope (this document) makes every CLI write to a scoped route carry `--cross-scope` (`TestScopedCommandsDeclareAccess`), and `find` must stay a read without it — hence the separate `subject pick`; its `--cross-scope` lets the master pick another scope's search, like every other write.

`find` shows one label per candidate in the plain output (the short title, else the title); `--json` is the full form (title + short title + theme + recency, see Wire). Plain output: one tab-separated line per candidate on stdout — `<subject_id>\t<status>\t<short title or title>\t<theme>\t<YYYY-MM-DD of last activity, or ->` — then one line on stderr:
`find sf_…: 3 candidates (1 elsewhere) — link: pigtail subject attach --find sf_… <subject_id>`, or `find sf_…: no candidate (1 elsewhere)`; the `(… elsewhere)` part only when > 0. Exit 0 in both cases.
`pick` plain output: `✓ sf_… → sub_… (rank 1/3, 2 searches)`, `(not among the 3 candidates, 2 searches)` for rank 0, prefixed `=` instead of `✓` when the same pick was already recorded.

**Wire.**

`POST /subjects/find` body `{"query": "noms de sujets", "workspace_id": "<cmux UUID, optional>", "limit": 5}` → 200:

```json
{
  "find_id": "sf_3f9a0c2b7d1e4a56",
  "query": "noms de sujets",
  "candidates": [
    {
      "subject_id": "sub_x000000000000031",
      "short_title": "Trouver un sujet par mots",
      "title": "Résolution classée des sujets par les mots …",
      "theme_id": "thm_0123456789abcdef",
      "theme": "pigtail",
      "scope": "perso",
      "status": "understanding",
      "closed": false,
      "writable": true,
      "last_activity": "2026-10-29T09:12:44.12Z",
      "score": 4.12,
      "matched": ["noms", "sujets"]
    }
  ],
  "total": 1,
  "elsewhere": 0
}
```

`candidates` and `matched` are never `null`; `last_activity` is omitted when the subject has no owner log line; `score` is rounded to 2 decimals; `total` counts the readable candidates before `limit`; `short_title` is `pigtail.ShortTitle` (derived when empty).

`POST /subject-finds/{fid}/pick` body `{"subject_id": "sub_…"}` → 201 (new) / 200 (same pick again):

```json
{"find_id": "sf_…", "subject_id": "sub_…", "rank": 1, "candidates": 3, "searches": 2, "picked_at": "2026-10-29T09:13:02.5Z", "created": true}
```

`POST /subjects/{id}/cmux-workspaces` gains `"find_id"` in its body; its response (`SubjectCmuxBinding`) then carries `"find_id": "sf_…"` (absent otherwise).

Journal lines (scope = the search's scope, source `pigtail:subject-find/<fid>`, summary ≤ 280 runes — a contract the indicator recipe reads, pinned by tests):

| kind | id | summary |
|---|---|---|
| `subject-find` | `subject-find:<fid>` | (query clipped at 160 runes, `…`, in the summary only; the stored query stays whole) `recherche « <query> » : <total> candidat(s), tête <sub_id>` / `recherche « <query> » : aucun candidat`, + ` (+<n> ailleurs)` when elsewhere > 0 |
| `subject-find-pick` | `subject-find-pick:<fid>` | `recherche <fid> : <sub_id> choisi au rang <r>/<n> après <k> recherche(s)` / `… choisi hors liste (<n> candidat(s)) après <k> recherche(s)`; `subject_id` set when the subject's scope is the search's |

**Errors.**

| Case | Answer |
|---|---|
| bad JSON | 400 `INVALID_REQUEST` `invalid JSON: …` |
| empty query (after normalisation) | 400 `query is required` |
| query > 500 bytes | 400 `query too long (<n> bytes, max 500)` |
| only stop words / single letters | 400 `query has no searchable word (stop words and one-letter words are ignored)` |
| `limit` < 0 or > 20 (0 = default 5) | 400 `limit must be 1..20` ; CLI refuses before sending: `--limit must be 1..20 (got <n>)` |
| invalid `workspace_id` | 400 (`NormalizeCmuxWorkspaceID`'s message) |
| record failure | 500 `subject find not recorded: <err>` |
| pick: missing `subject_id` | 400 `subject_id is required` |
| pick / attach `find_id`: unknown, or another scope's search the caller may not read | 404 `no such subject find` (same body) ; master without the opt-in → 403 `SCOPE_MISMATCH` |
| pick / attach `find_id`: unknown subject, or one the search's scope may not read (even for a master with `--cross-scope`) | 404 `no such subject` — nothing written, nothing linked |
| pick / attach: search already picked for another subject | 409 `subject find already picked for another subject` — attach links nothing |
| attach linked but pick write failed | 500 `workspace linked, but the find pick was not recorded: <err>` |
| CLI against a daemon without the routes | `this pigtail daemon has no subject find (restart it after make install)` |
| CLI `attach --find` against a daemon that ignores `find_id` | error `the daemon ignored --find (restart it after make install), then: pigtail subject pick <fid> <subject_id>` — exit 1, the link stands |

**Reading the indicator.** Pick rank is read over picked finds; the count of unpicked finds is read separately (they leave no pick line):
`sqlite3 -readonly ~/.config/pigtail/pigtail.db "SELECT COUNT(*) FROM subject_finds WHERE picked_subject_id IS NULL"`. The journal lists are bounded by `--limit 1000` (default 200 rows).

```bash
pigtail journal list --kind subject-find-pick --since 14d --limit 1000 --json \
  | jq -r '.[].summary | capture("après (?<k>[0-9]+) recherche").k' | sort -n \
  | awk '{a[NR]=$1} END {if (!NR) {print "no pick"; exit 1}; i=int(NR*0.9); if (i<NR*0.9) i++; print "picks="NR, "p50="a[int((NR+1)/2)], "p90="a[i]}'
pigtail journal list --kind subject-find-pick --since 14d --limit 1000 --json \
  | jq '[.[].summary] | {picks: length, top5: map(select(test("au rang [1-5]/"))) | length, top1: map(select(test("au rang 1/"))) | length}'
```

`top5` is the promise (the right subject among the first five shown); `top1` is reported only.

Detail per search: `sqlite3 -readonly ~/.config/pigtail/pigtail.db "SELECT ts, query, candidates, picked_subject_id FROM subject_finds ORDER BY ts DESC LIMIT 20"`. No `--stats` command: the journal already carries rank and count (sober choice).

The journal summaries are a contract (`TestPickJournalSummaryFormat`, `TestFindRecordsRowAndJournal`).

#### Subject outputs

The registry of the artifacts produced for a subject — a screenshot, a mockup,
a report, a server, a PR, a file under `_outputs/<slug>/` — registered when
produced, a facet of the subject (no scope column: the scope is the
subject's). Kinds, a closed list: `capture html pdf mockup report server pr
clip link file`. Kind `capture` = capture d'écran (output), sans lien avec
`pigtail capture` / la table `captures` (idées du backlog).

```
POST /subjects/{id}/outputs        → 201 {output, created:true} | 200 {output, created:false}
     body: {kind, path?, url?, title?, ts?, step?, meta?, session_id?}
GET  /subjects/{id}/outputs        → []SubjectOutput, seq ASC, never null
     ?since=<seq, exclusive> &kind= &session= &limit=<1..1000, default 200>
GET  /subjects/{id}/outputs/groups → []OutputGroup, first registration order, never null
POST /subjects/{id}/outputs/purge  → 200 SubjectOutput | 404 NOT_FOUND "no such output"
     body: {key}
GET  /subjects/{id}                → + outputs_count, last_output_ts
```

- **Times** (`ts`, `created_at`, `purged_at`, `last_output_ts`) are integers,
  epoch ms. `ts` defaults to the daemon's now.
- **Key and id**: the key is `path` when set (absolute, `filepath.Clean`ed),
  else `url` (`^[a-z][a-z0-9+.-]*:\S+$`, ≤ 2048 bytes). `(subject_id, key)` is
  unique: re-posting a key is a no-op that echoes the stored row unchanged
  (first writer wins, `seq` unchanged). `id` = `out_` + the first 12 hex of
  `sha256(subject_id + "\n" + key)`.
- **Bounds**: `title` one line, 1..60 characters, refused above (the CLI cuts
  it to 60 before sending), default the basename of `path`, else the url
  without its scheme; `step` `^[a-z][a-z0-9-]{0,63}$`; `meta` a JSON object
  ≤ 4096 bytes; `session_id` ≤ 200 bytes, no whitespace. The `{id}` of the URL
  is the subject: a different `subject_id` in the body is 400. Violations → 400
  `INVALID_REQUEST` naming the field.
- **Listing**: purged rows included; each row carries `exists` (`os.Stat` of
  its path; true for a url-only row). `limit` outside 1..1000 → 400
  `limit must be 1..1000`.
- **`served_url`** (GET only, never stored, ignored on a write): the outputs
  server's URL of the row (§ Static servers, Outputs server) — its base +
  the escaped `path`, a round's `index.html` shortened to its folder
  (`…/t3/`). Set only for a row the outputs server opens — a served kind, a
  `path` that exists, not purged, and the server's own index opening that
  URL (a row in `$HOME`, a scope's `launch_dir` or a `.git` folder has
  none) — while its listener is bound; absent otherwise. `url` is never rewritten.
- **Groups** (`GET …/outputs/groups`, D2): the non-purged `mockup`,
  `report` and `html` rows with a `path`, grouped by round folder. A round is a folder
  named `^t(\d+)([-_].*)?$` (`t42-teams` → 42); its group's `key` is the
  parent of the round folders and its `name` the first ancestor that is not
  `mockup(s)`, `report(s)`, `round(s)` nor `_outputs`. A deliverable outside
  a round is a group of one tour (`n` 0, `label` = its file name, `key` = its
  path). Same authorization as the listing. Shape:

  ```json
  [{"key": "/…/_outputs/app/mockup", "name": "app", "kind": "mockup", "first_seq": 12,
    "tours": [{"n": 1, "label": "t1", "output_id": "out_…", "title": "index.html",
               "ts": 1761638400000, "path": "/…/t1/index.html",
               "served_url": "http://127.0.0.1:7801/…/t1/", "exists": true}]}]
  ```

  Tours are sorted by `n` ascending — the last one is the latest version;
  two tours of the same `n` are kept both, the most recent by `ts` last.
  `exists` and `served_url` follow the listing's rules (a round gone from
  the disk: `exists: false`, no `served_url`).
- **Purge**: sets `purged_at` once (the first date is kept); the row stays. A
  key starting with `/` is cleaned first. An unknown key on a visible subject
  answers 404 `NOT_FOUND` `no such output`, distinct from the `no such subject`
  of an unknown or invisible subject.
- **Scope**: the subject is authorized like every subject route — a subject the
  caller cannot read is `404 no such subject`; a write onto another scope's
  subject needs the master's `X-Pigtail-Cross-Scope: write`.
- **`GET /subjects/{id}`** carries `outputs_count` (non-purged rows) and, when
  it is above 0, `last_output_ts` (max `ts` of the non-purged rows); `GET
  /subjects` carries neither.
- **Event** `subject.output_added` `{subject_id, seq, id, kind, key}`, `Scope`
  = the subject's, published only when a row is created, after the write; a
  publication never changes the answer.

CLI: `pigtail subject output add [<subject_id>] --kind K (--path P | --url U)
[--title] [--step] [--meta] [--session] [--ts RFC3339] [--quiet-unbound]
[--json] [--cross-scope]`, `pigtail subject output ls [<subject_id>] [--since]
[--kind] [--session] [--limit] [--json]`, `pigtail subject output purge
<subject_id> --key <path|url> [--cross-scope]`, `pigtail subject output
backfill [--dir D] [--dry-run] [--cross-scope]` (registers the mockups and
reports of the claude-opens journals, through `POST /subjects/{id}/outputs`;
prints registered / skipped (no subject) / gone / already registered counts).
The subject defaults to the
pane's bound one (`$CMUX_WORKSPACE_ID`); none → exit 2, `no subject: pass
<subject_id> or bind the workspace`, and `--quiet-unbound` makes that case
alone a silent exit 0. `pigtail serve add` registers its server too (§ Static
servers).

#### Subject scope (N-ary scopes)

**The scope table.** Scopes are declared, not coded: `scopes.yaml` in the
pigtail config dir (`$XDG_CONFIG_HOME/pigtail`, else `~/.config/pigtail`),
beside `claude-cells.yaml`, read by the daemon **and** the CLI at start
(never next to `--socket`). Without the file, the built-in table applies —
`internal/scope/builtin_scopes.yaml`, a single `personal` scope that sees
everything. A two-scope table:

```yaml
default: globex
scopes:
  perso:  {vault_roots: [Perso/], sees_all: true, launch_dir: ~/code/home}
  globex: {vault_roots: [Globex/], aliases: [gx], launch_dir: ~/code/globex}
```

`launch_dir` (optional) is where pigtaild launches a session of the scope
whose subject has no live worktree (`POST /subjects/{id}/launch`, § Session
launch): absolute or starting with `~/`, no `..`, its existence checked at
launch (`NO_LAUNCH_DIR` otherwise).

Rules, checked at load: names `[a-z][a-z0-9-]{0,31}` (not `all`/`any`);
every scope owns ≥ 1 vault root ending in `/`, roots never overlap; exactly
one scope has `sees_all: true` (the **master**); `default` (the scope of a
caller that names none) is declared and is never the master — fail-closed;
`aliases` are read-only spellings, accepted wherever a scope enters and never
written. A broken file leaves the built-in table active, never a wider policy,
and says so: `ERROR scope table` in `daemon.log`, `/health.scopes.error`, a
warning on stderr at every CLI command (its detail from the master cell only).
A changed file takes effect when the daemon restarts.

In the table above, `gx` is the alias of `globex`: read everywhere (header,
`?scope=`, body, cell table), never written — the database, every answer and
every event say `globex`. The stored scope values are held by a shape check
(lowercase identifier, never an alias, never empty), not by a closed list.

Every theme belongs to one scope, the one owning its `vault_link` root (a link
no root claims is `400`). A subject has its theme's scope (`Subject.scope`,
read-only, joined in on read); a subject whose theme is gone is the master's.

The caller's scope travels in a header on every route above but `/board`:

| Header | Values | Absent |
|---|---|---|
| `X-Pigtail-Scope` | a declared scope name or alias (other → `400 INVALID_REQUEST`) | **the table's `default`** (`globex` in the table above) — a bare curl, a browser, an unconfigured profile never reads another scope |
| `X-Pigtail-Cross-Scope` | `write` | no opt-in |

The rule is asymmetric — the master sees everything, any other scope only
itself. With three scopes (`perso` master, `globex`, `acme`):

| Caller \ object | perso | globex | acme |
|---|---|---|---|
| **perso** (master) | read + write | read; write → **403 SCOPE_MISMATCH** unless `X-Pigtail-Cross-Scope: write` | same |
| **globex** | **404 NOT_FOUND**, byte-identical to an unknown id; absent from lists, `next` | read + write | 404 |
| **acme** | 404 | 404 | read + write |
| *(no header)* | as `globex` (the default) | | |

- `?scope=<name>|all` on `GET /themes`, `GET /subjects` and `GET /journal`
  only narrows: the master sees everything by default (ordered scope, then
  rank with unset last, then `created_at`); any other caller asking for
  `all` gets `403`.
- `GET /subjects/next` is per scope — the caller's own by default; the master
  may ask `?scope=<name>`; `?scope=all` is `400` (each backlog has its own
  rank order).
- **What a non-master caller is told (D19).** It learns no other scope's name:
  `/health.scopes` lists its own scope only (no master, default nor undeclared
  list), errors list only the names (or vault roots) it may use, and a
  declared scope it cannot read — in `?scope=`, a journal body's `scope` (with
  or without `subject_id`), or a theme's `vault_link` root — answers exactly
  like an unknown name: `400 INVALID_REQUEST`, same body once the echoed name
  is masked. The master gets the full lists and its `403`s.
- Write invariants: the master creating a theme under another scope's root
  without the opt-in is `403`; `PATCH /themes/:id` cannot move a theme to
  another scope's root (`400`) — a theme never changes scope. Creating a
  subject is a write on its theme (a theme the caller cannot see is
  `404 unknown theme_id`; another scope's theme from the master needs the
  opt-in). A subject never changes scope: reassigning it to a theme of
  another scope is `400` (`403` first without the opt-in). A newly added
  dependency must be a subject of the same scope (`400`). `POST /subjects`
  and `POST /subjects/:id` also refuse (`400 invalid_request`, the message
  being exactly the text, no prefix) a newly added dependency that is no
  subject (`unknown dependency <id>`), the subject itself (`a subject cannot
  depend on itself`) or one that closes a cycle (`dependency cycle: <id> → …
  → <id>`, ids only). Dependencies already stored are never re-judged: an
  update that leaves them untouched stays accepted. `POST /phases/:id`
  resolves phase → subject → theme.
- `/board` is public (no header in a browser) and is **the master's view**
  (D20): `?scope=` picks the view freely, `all` included, for any caller;
  absent = the default scope. Its title names the scope shown; its `400` for
  an unknown name echoes that name and lists no scope. This is a stated
  exception to the rules above: a localhost surface reserved to the user.
- `GET /health` gains `scopes`: `source` (`builtin` or the file's path),
  `names`, `master`, `default`, `undeclared` (scopes stored in the database
  that the table does not declare — their rows are the master's only) and
  `error` when the file failed to load. Read with the master's header for the
  whole object; any other caller gets its own view (D19).

**The isolation criterion** (decided by the maintainer with D20): « L'isolement des
scopes est un garde-fou contre l'erreur d'un agent, pas une frontière de
sécurité. Une route qu'un agent utilise dans son travail le respecte ; une vue
réservée à l'utilisateur (le board) peut tout montrer. Protéger une org contre un agent
qui la cherche demanderait une séparation au niveau du système (un utilisateur
macOS par org), hors de ce sujet. »

**Session and subject titles (D23)**: « Un titre de session ou de sujet
appartient à son scope. Toute sortie qu'un agent lit dans son travail normal
(réponse, erreur, avis, événement) ne le montre qu'à une cellule qui voit ce
scope ; sinon l'identifiant court. » Exception: the board (D20). The
maintenance notice, `pigtail maintenance status` and the maintenance refusals
apply it: the body of a `423 MAINTENANCE_FROZEN` names the owner session as
`X-Pigtail-Scope` may see it (absent = the default scope; invalid = the
default scope on the routes outside `scopeMW` — `/workspaces/new`,
`/dispatch` —, `scopeMW`'s `400` on the subject routes), else its 8-character
id.

**CLI.** The `pigtail` CLI sets `X-Pigtail-Scope` from the Claude cell it runs
under (`CLAUDE_CONFIG_DIR` matched against `claude-cells.yaml`; a cell's
`scope:` is read canonical: an alias is its scope). Inside a cell,
`--scope` / `PIGTAIL_SCOPE` never change that header: they only become the
`?scope=` view on listings (`subject list`, `journal list`…; a non-master cell
accepts only its own scope), and the master cell's writes onto another scope
still need `--cross-scope`. Outside any cell the header is the default scope
unless `--scope <name>|all` asks for another (`all` = the master).

`--cross-scope` sets `X-Pigtail-Cross-Scope: write` on every CLI write that
reaches a scoped route: `subject create|set|log|dep|attach|detach`, `handoff write`,
`theme create|set|delete`, and `journal add|import`. Without it the master
cell writing another scope gets `403 SCOPE_MISMATCH`; any other cell passing it
changes nothing (another scope's object stays `404`, another scope's root
stays the `400` of an unknown root).

**Beyond the subject routes.** The same caller scope filters what other
surfaces say about subjects:

- **Relay** — every `/peers` and `/messages` route takes `X-Pigtail-Scope`
  too (absent = the default scope, invalid = `400`).
  - *Reads.* A caller gets no row of a session of a scope it cannot read from
    `GET`/`POST /peers` (no session id, label, pid, inbox count…): knowing
    such a session id is what acting as it would take, and a session can
    never message another scope anyway. A visible session's row stays, with
    `subject_id`, `subject_slug` and the `label` built from them stripped when
    the subject is of a scope the caller cannot read, or unknown
    (fail-closed). A session with **no scope** (registered outside any cell),
    or with a scope the table does not declare, is seen by the master only
    (D17) — on `/peers`, `GET /messages` and the events alike. The caller
    sees only the conversations whose sessions all run on scopes it can read:
    others are absent from `GET /messages` (the filter applies before
    `limit`) and `404` on `GET /messages/:id`. What the registry stores is not
    redacted.
  - *Acting as a session.* `POST /messages` (as `from`), `POST
    /messages/claim`, `GET /messages/wait`, `POST /messages/:id/ack` and
    `POST /peers` with `end: true` act for the session id they name. The
    daemon does so only when the caller's scope **equals** the scope the
    registry holds for that session (a session with no scope acts as the
    default scope); otherwise it answers exactly as for an unknown session
    (`400` unknown sender, `404` on claim/wait, `404 no such message` on ack,
    end skipped) and nothing is sent, claimed, acked, superseded or ended.
    Equality, not "may read": the master does not consume another scope's
    session inbox either. Only the master changes a stored session scope on
    `POST /peers` (a session moved between cells); any other caller's upsert
    keeps the stored cell and scope. Stored scopes are canonical (an alias is
    written as its scope's name).
  - *Sending.* `POST /messages` sees sessions and subjects from the sender's
    scope: a sender gets no session of another scope as a candidate (such an
    address is `404 ADDRESS_UNRESOLVED`, and no ambiguity lists one), cannot
    address a peer by another scope's subject slug/id, and its message neither
    carries nor logs one. Only a `notice` crosses scopes — from the master,
    with `X-Pigtail-Cross-Scope: write`; it belongs to its recipient's scope,
    which reads it with its sender redacted (§ Session messages, Scope).
    Details: § Session relay.
- **`pigtail db list`** (and the table `db restore` prints) reads the database
  directly, daemon stopped; it counts subjects within the calling cell's
  scope: a non-master cell sees its own count alone (never the total), the
  master cell sees the total and one column per scope (the table's names,
  then any undeclared scope a generation holds). A generation from before
  0015 counts its alias rows under their scope's column. This hides the numbers
  on the pigtail surface; it is not access control over the database file.

- **`GET /events` (SSE)** reads `X-Pigtail-Scope` at subscribe time (absent =
  the default scope, invalid = `400`). The relay tags every `message.*` event
  with its conversation's scope — the master's when its sessions run on
  different scopes or one has none — and the sidebar loop tags `cmux.*`
  events with the bound subject's scope (default scope when unbound; the
  master's when the wait reason was told by a session of another scope or of
  none) and `wait.*` events with the scope of the wait (frozen at its
  declaration: the emitter's then, kept when another session receives the
  answer, D28), the subject outputs routes tag `subject.*` events with
  the subject's scope, and the session launcher tags `session.launch*`
  events with the subject's scope. Tags are daemon-side, never on the
  wire; a subscriber receives the
  events of the scopes it can read, replay included, and an untagged
  `message.*` / `cmux.*` / `wait.*` / `subject.*` / `session.launch*` event reaches the master's subscribers only
  (fail-closed). Details: § Events.

- **Waits** — `GET /waits`, `GET /waits/{id}`, `POST /waits/{id}/answer` and
  `POST /waits/{id}/snooze` are scoped by the scope of the wait's session at
  declaration (frozen): another scope's wait is `404`, byte-identical to an
  unknown id, and absent from the list; the two writes need `CanWrite` (the
  master on another scope: `403` without `--cross-scope`). The list drops the
  id and title of a subject the caller may not read (D23). Outside a cell the
  CLI sees the default scope only: `--scope all` / `PIGTAIL_SCOPE=all` gives
  the master's full view, `--cross-scope` writes outside the default scope.
  Details: § Waits — read, answer, snooze.

**Accepted, known residuals (not scoped, by decision).**

- The workspace layer: `GET /workspaces`, `pigtail list`, workspace events and
  a peer's `workspace_name` list every worktree whatever the caller. They serve
  port/server coordination across cells; worktree names and paths are **not**
  scoped, so a worktree named after another scope's subject shows that name.
- A peer session is identified by its registered scope, which the CLI
  derives from the cell table like the header: a caller that forges the
  header, or registers a new session id with a false scope before its owner
  does, is outside this model (it could read the database file too). Once a
  session is registered, only the master can change its scope.
- `pigtail session ls` still lists every Claude pane cmux shows, other scopes'
  included, with their session ids: that listing comes from cmux on the
  caller's machine, not from the daemon. From a non-master cell the relay
  columns of another scope's pane are empty.
- `/board` (D20, above).

**Guard.** `internal/daemon/scope_guard_test.go` walks the real router: every
route must be classified (scoped, stream-filtered, public-view,
session-bound, no-subject-data), a scoped or session-bound one must sit behind
`scopeMW`, every `GET` is probed as a non-master caller against seeded data of
the master's scope (its session ids included), and every session-bound route
is called as a non-master naming a master's session and must answer like an
unknown session, with nothing changed. A new route fails the test until it is
classified, and a new session-bound route until it has a probe. No Go file
outside the scope table names a scope or a vault root
(`internal/scope/hardcoded_test.go`).

`PATCH /themes/:id` accepts a partial body (name, vault_link, space, deadline
and/or carried; DB only); only the provided fields are written. `id`, `kind` and `created_at` are immutable.

```jsonc
PATCH /themes/thm_meeting
{ "name": "Meeting module", "vault_link": "Globex/Projects/meeting" }
→ 200 Theme              // the updated theme
→ 400 INVALID_REQUEST    // empty body (none of name, vault_link, space, deadline, carried)
→ 404 NOT_FOUND          // no such theme
```

Theme carries `deadline` (`""` or `YYYY-MM-DD`, a real date; any other value → 400) and `carried` (bool), both always present in the JSON (migration 0034).

A theme carries a `space` (`""` = none yet): the cmux sidebar folder of its
subjects' sessions (sub_x000000000000041, D1). Valid names are the rows of the
space table (`spaces.yaml` in the config dir, built-in fallback
`internal/space/builtin_spaces.yaml`), each bound to one scope; a theme takes
only a space of its own scope. `POST /themes` and `PATCH /themes/:id` refuse any
other value with `400 INVALID_REQUEST` (`unknown space (known: …)`, listing only
the spaces of the theme's scope; another scope's space answers like an unknown
one and is never echoed, D19). `PATCH {"space": ""}` clears it.
`GET /spaces` (behind the scope guard) returns `[{name, scope}]` in folder order
(D6): all for the master, its own scope's otherwise.

`DELETE /themes/:id` does **not** cascade. If any subject is still linked it is
refused with `409 CONFLICT` and the count; reassign the subjects first (see
below), then retry.

```jsonc
DELETE /themes/thm_meeting
→ 200 { "id": "thm_meeting" }                                   // had zero subjects
→ 409 CONFLICT  "cannot delete theme thm_meeting: 3 subject(s)
                 still linked — reassign them first"            // details: { theme_id, subjects }
→ 404 NOT_FOUND
```

**Subject status.** A subject's `status` is its maturity, one of eight words,
in lifecycle order: `inbox`, `understanding`, `designing`, `building` (the four
open ones, each a band of the cmux sidebar), then `done`, `parked`, `dropped`,
`captured` (closed; `done`, `captured` and `dropped` satisfy a dependency,
`parked` still blocks). `POST /subjects` (default `inbox`), `POST /subjects/:id`
and the `status` filter of `GET /subjects` accept only these words; any other
is refused, nothing written:

```jsonc
POST /subjects   { "theme_id": "thm_x", "title": "t", "status": "researching" }
POST /subjects/sub_42   { "status": "researching" }
GET  /subjects?status=researching
→ 400 INVALID_REQUEST  "unknown status \"researching\": use one of inbox, understanding,
                        designing, building, done, parked, dropped, captured"
```

Every change of status writes one `subject_log` line, in the same transaction
as the update: kind `status`, text `<old> → <new>`, at the update's
`updated_at`; rewriting the same status writes none, nor does creation.
Migration 0031 (the eight-word vocabulary) wrote the same line for each
subject it renamed, suffixed ` (migration)`: `triaged → inbox (migration)`.

**Subject update is partial.** `POST /subjects/:id` writes only the keys
present in the body; every other field keeps its value. A client that does not
know a field — an older `pigtail`, a skill's `curl` — therefore cannot erase
it. The CLI (`pigtail subject set k=v`, `subject dep`) sends only the fields it
was asked to change.

- **Absent key** → unchanged.
- **`null`**, or the field's zero value (`""`, `0`, `[]`) → reset (stored as
  NULL). `theme_id`, `slug` and `status` are never emptied: `null`/`""` keeps
  the current value. `title` is never emptied either, but loudly: `null` or a
  blank string (after trim) → `400`.
- **`id`, `scope`, `created_at`, `updated_at`** → ignored (server-owned), so
  echoing back a subject read with `GET` still works — as a partial update of
  every non-empty field it carries.
- **Unknown key, mistyped value, non-object body, or no writable key** →
  `400 INVALID_REQUEST`, nothing written.

```jsonc
POST /subjects/sub_42
{ "status": "building" }                 // board_card, rank, … untouched
{ "board_card": null, "impact": 0 }      // both reset
→ 200 Subject            // the subject after the update
→ 400 INVALID_REQUEST    // unknown key, wrong type, null/blank title, {} or read-only keys only
```

**Subject theme reassignment.** `POST /subjects/:id` accepts `theme_id` in the
body; setting it to a different theme reassigns the subject. The target theme
must exist, be visible to the caller and share the subject's scope — an
unknown id is rejected. The move is a DB link change only; no
vault files are moved.

```jsonc
POST /subjects/sub_42
{ "theme_id": "thm_other" }
→ 200 Subject            // reassigned
→ 404 NOT_FOUND          // unknown theme_id (details: { theme_id })
```

A reassignment that changes `theme_id` also resets `subjects.space` to `""`
(D8): the subject's sessions follow the new theme's space (a focus keeps its
priority). It happens in the transaction of the update, which leaves a
`subject_log` line (kind `note`, `space cleared (theme changed): <old> →
(theme)`) and a `journal` row (kind `subject-space-cleared`, the subject's
scope and `subject_id`, the same text behind the short title); a subject
without an own space writes nothing more, and a `POST` that keeps the theme
never clears it. The answer is the subject read back after the update, so it
shows the cleared `space`.

#### Captures (backlog ideas under a project)

A capture (A3) is a lightweight idea filed under a theme (a project), not yet a
subject, and not a screenshot. Scope derives from the theme (JOIN), never
stored; same scope middleware as `/subjects`. A work caller addressing a perso
theme gets 404 (no-probe); the perso master writing a work theme needs
`--cross-scope` (403 `SCOPE_MISMATCH` otherwise). Writes sit behind the
maintenance freeze (423), the read does not. Captures never appear on `/board`.

```
POST   /captures          {theme_id, text, from_subject_id?} → 201 Capture   // from_subject_id: same theme, the subject becomes `captured` (one transaction, log line)
GET    /captures?theme=<id>&open=1[&scope=] → []Capture
POST   /captures/promote  {capture_ids[], title, short_title?} → 201 {subject, captures}  // open captures of one theme -> one inbox subject; 400 mixed themes
DELETE /captures/{id}     → 200 {capture, subject?, note?}   // undo: restores from_prev_status if the subject is still `captured`; a promoted capture is 400
```

`DELETE /themes/{id}` answers 409 while the theme carries open captures.

#### Focus (temporary folders)

A focus (sub_x000000000000007) is a temporary cmux sidebar folder that gathers
the sessions of some themes and subjects, an overlay on `themes.space` (which
it never rewrites). Behind the scope guard; writes behind `FreezeMW`.

```
POST   /focus                                  → 201 Focus (+ note)
       body: { name, ends_at?, members: [{kind: "theme"|"subject", id}] }
GET    /focus[?all=1]                          → 200 { focus: []Focus, note? }
POST   /focus/{id}/members  { kind, id }       → 200 Focus
DELETE /focus/{id}/members/{kind}/{member}     → 200 Focus
POST   /focus/{id}/end                         → 200 Focus (end_reason manual)
```

`Focus`: `{id (fcs_…), name, scope, created_at, ends_at?, ended_at?,
end_reason? ("deadline"|"manual"), members: [{kind, id, added_at, missing?}],
note?}`. `missing: true` marks a member theme or subject deleted since. `GET
/focus` lists the focus the caller may read, oldest first; without `all=1`, the
open ones only.

**End judged at read.** A focus counts while it is not ended and its
`ends_at`, if any, has not passed. An expired focus reads `ended_at = ends_at`,
`end_reason: "deadline"` at once; the daemon seals it (writes `ended_at` and
one `note` line `focus "<name>" ended (deadline)` in each concerned subject's
log, dated the deadline) at the first read or write after the deadline, once.
A manual end writes `… ended (by hand)` dated now. Concerned subjects: the
subject members and the subjects not `done` of the theme members, once each.
Under `maintenance on --freeze` nothing is sealed (the read still shows the
focus ended); the seal happens at the first read after the lift. A failed seal
is logged (`focus: seal: …`) and the reads are still served; a write then
answers `500` `focus: seal: …` without writing anything.

**Scope (D6).** A focus takes its creator's identity scope and holds that
scope's themes and subjects only — the master included, even with
`X-Pigtail-Cross-Scope: write` (which only lets it write another scope's
focus). Refusals:

| Case | Answer |
|---|---|
| member the caller cannot read, or unknown | `404 NOT_FOUND` `no such theme` / `no such subject` (one body) |
| member of another scope than the focus | `403 SCOPE_MISMATCH` `a focus holds its own scope's themes and subjects only` |
| focus of a scope the caller cannot read, or unknown | `404 NOT_FOUND` `no such focus` |
| name invalid, `Inbox`, `Standby` or `Prio` | `400 INVALID_REQUEST` |
| name of a space (any scope) or of an open focus (any scope; under a freeze, also an expired focus not sealed yet) | `409 CONFLICT` `focus name unavailable` — the same body whoever holds the name, no details |
| `ends_at` not in the future | `400 INVALID_REQUEST` `--until is in the past` |
| member already in another open focus | `409 CONFLICT` `<kind> <id> is already in an open focus` (`details.focus_id`, `focus_name` when the caller may read that focus) |
| write on an ended focus | `409 CONFLICT` `focus already ended` |
| member not in the focus (`DELETE`) | `404 NOT_FOUND` `no such focus member` |

**`note`.** Why no folder will show: `folders: sidebar loop not running` (no
loop in the daemon), `folders: idle (pigtail cmux sidebar on, --cmux-folders)`
(loop off or folders off), `folders: <folders_note>` (the loop's own note);
absent when folders run. Set on `POST /focus` and once in the `GET /focus`
envelope.

### Session messages (relay)

Short coordination messages between two Claude Code sessions, relayed by the
daemon.  Ids: `msg_…` (message),
`thr_…` (thread).

```
POST   /peers                → upsert sessions      body: { peers: [PeerSession], end? }
GET    /peers                → []PeerSession (live) ?all=1 includes ended/dead
POST   /messages             → send                 body: SendMessageRequest → 201 SendMessageResponse
POST   /messages/broadcast   → notice to all        body: BroadcastRequest → 200 BroadcastResponse
GET    /messages             → []Message            ?to=&from=&session=&state=&thread=&subject=&since=&limit=
GET    /messages/:id         → Message
POST   /messages/claim       → deliver pending      body: { session, via } → { messages }; 404 unknown session
GET    /messages/wait        → long poll            ?session=&timeout=<s, ≤300> → { messages, superseded? }; 404 unknown session
POST   /messages/:id/ack     → acknowledge          body: { session } (the recipient only)
```

A session in Standby is never served: `GET /messages/wait` claims nothing for
it and `pigtail hook claude-inbox` delivers nothing, until the prompt the user
types (never on the `/compact` pigtail types, nor on a tool call) — the inbox
waits for the user's prompt (D16, § Standby).

**Registry.** `PeerSession` rows are derived: the CLI joins
`cmux sessions list --agent claude --json`, `<config-dir>/sessions/<pid>.json`,
the cell table and the cmux workspace titles, and POSTs the snapshot; the
SessionStart hook posts the session itself, SessionEnd posts `end: true`.
A session's own SessionStart (`source: "hook"`) on a surface where another
session is in Standby ends that Standby (`session.standby_changed`,
`standby: false`): another conversation took the surface (`/clear`, a fresh
`claude`, a respawn). A relaunch resumes the same id and keeps it; a snapshot
never ends one (D24, § Standby).
While the cmux sidebar loop runs (§ Subjects, cmux sidebar loop), the daemon
also takes that snapshot itself every 30 s — `cmux sessions list` is then the
one cmux call the registry depends on daemon-side; a row that ended cleanly
is not revived by it unless its process runs again. Upserts merge: an empty field never erases a known
one (the subject of a session proven alive is the exception: it is written as
given). On the way in, the daemon fills `workspace_id`/`workspace_name` (deepest
pigtail workspace containing `cwd`) and `subject_id` (the subject whose phase
runs there); for a session proven alive, a subject explicitly bound to its
cmux workspace (§ Subjects, cmux workspace link) is written instead of that
deduction. On read, for an unended session only, the live binding takes
precedence over the stamp; an ended session reads its stamp. `GET /peers` adds `label`, `alive` (claude pid alive **and** its
`sessions/<pid>.json` still naming the session) and `inbox` (pending count).

`GET /peers` also reads, never stored on the row: `subject_short_title` (the
short title of the session's subject, `subject_id`; absent when it has none)
and `cmux_bound` (true when the session's cmux workspace carries an explicit
link to that subject, § Subjects, cmux workspace link — a subject deduced from
the cwd does not set it). A bound tab shows the subject's short title, and
the daemon's `label` uses it. Both are omitted when empty/false. Redaction:
for a caller who may not read the subject (other scope, or unknown), the row
loses `subject_id`, `subject_slug` and `subject_short_title`, and, when the
tab is bound to it, `cmux_bound` and `cmux_title` too (the title shown by a
bound tab is the subject's short title, D23); the live title of the sidebar
loop is never put back on such a row.

`GET /peers` also adds `paused_at` (RFC 3339) on a session pigtaild paused
idle (`session_pause` row in state `paused`, § pigtail
pauses a session idle past the threshold); omitted otherwise. The row is ended
(`ended_at`), so it shows only with `?all=1`; `pigtail session resume` reads it
there, and `session ls` shows the tab as `paused`.

`last_worktree` / `last_worktree_at` (epoch ms in the store, RFC 3339 on the
wire) are the worktree a session last worked in, from its newest presence
marker (`$TMPDIR/claude-wt-*`, written by the session-presence hook),
read CLI-side with no TTL and attached to the snapshot. The newest always
wins: an older `last_worktree_at` or an empty `last_worktree` erases nothing.
The path may name a directory deleted since — the session left its previous
worktree for one that is gone; consumers hide it (the daemon for addressing,
the CLI for the `session ls` WORKTREE column). They never feed
`workspace_id`/`subject_id` nor the label.
See § Worktree from presence markers.

**Scope.** Every relay route answers for the `X-Pigtail-Scope` caller
(absent = the default scope, invalid = `400`). Reads: a caller gets no row of a
session of a scope it cannot read (a session with no scope is the master's,
D17), `subject_id`/`subject_slug` (and the label part built from them)
stripped from visible peers whose subject is of another scope or unknown, and
only conversations between sessions of its scope (others absent, or `404`);
the master sees everything. Send (as `from`), claim, wait, ack and `end` act for a
session only when the caller's scope equals its registered scope, and answer
as for an unknown session otherwise — see § Subject scope. `POST /messages`
resolves `to` among the sessions the sender's scope sees and attaches a
subject from that scope.

A `notice` belongs to its **recipient's** scope: that scope sees it in `GET
/messages`, `GET /messages/:id`, claim, wait, ack and the SSE stream (its
events are tagged with the recipient's scope). For a caller that cannot read
the sender's scope, `from_session` reads `pigtail`, `from_label` reads
`pigtail · master cell` (a constant naming no scope, D19), and `to_label` was
built from the recipient's own view at send time (D23); the store keeps the
real sender (rate limits, the master's view). A `GET /messages` filter
(`from`, `to`, `session`) naming a session the caller cannot read answers `[]`,
exactly like an unknown session.

`msg send` takes `--all` (`POST /messages/broadcast`), `--kind notice` and
`--cross-scope` (the `X-Pigtail-Cross-Scope: write` header); it acts as the
session (`sessionClient`), the opt-in added on top.

The CLI sends the header everywhere: `session ls` and `msg inbox --all|show|
thread|log` send the cell scope (`newScopedClient`); every call acting as a
session — the hooks (`pigtail hook claude-session|claude-inbox|
claude-inbox-wait`) and `msg send|reply|ack|inbox` — sends the scope of the
session's own cell (`sessionClient`: the cell of `CLAUDE_CONFIG_DIR`, looked
up like the registry does, the default scope outside any cell; never `--scope`). A session
the daemon does not know (`404` on claim/ack) registers its own row and
retries once; the Stop watcher re-registers on every `404` retry.

**Addressing.** `to` is resolved against live peers, first level with a match
wins, several matches fail `409 ADDRESS_AMBIGUOUS` with the candidates:
full session id → label form (`<primary> · <handle> (<cell>)`, whole or
partial) → subject slug or `sub_` id →
worktree name → last worktree (exact basename of the newest presence marker;
only for a session without a cwd worktree; ignored if the directory is gone)
→ native session name → cmux title (case-insensitive substring)
→ session-id prefix (≥ 4). `details.candidates` lists each candidate as
`<label> [<session id>]`. `label` (and `from_label`/`to_label`, stamped at
send time) is unique per session: the native name, else an id prefix, always
follows the primary.
A reply (`in_reply_to`) ignores `to` and goes back to the original sender, in
the same thread. `SendMessageResponse.matched_by` names the level that resolved
`to` (`"worktree"`, `"last worktree"`, …); empty for a reply.

`SendMessageResponse.delivery` says how the recipient will see the message,
computed at send time: `"waiter"` (a Stop watcher long-polls `/messages/wait`
for it, or closed a poll less than 15 s ago), `"busy"` (native status `busy`
or `waiting`: mid-turn, its PostToolUse / prompt hook reads the inbox),
`"none"` (native status `idle` and no watcher: it will **not** wake on its
own), `"unknown"` (no watcher and no usable status), `"wake"` (idle with no
watcher, its pane verified: pigtaild types "lis ta boîte pigtail" at its next
sweep, under the screen guard, and **possibly later**: when the session budget
(`GET /session-budget`) is full, the sweep types nothing and the message stays
pending; the relay asks again at its next pass (at most a minute) or as soon as
a sample shows a session freed. A send never asks the budget, so `"wake"` means
« typed at a pass the budget allows ». The sweep tries the recipient waiting
longest first; an answer from the user in person skips the budget). `"wake"` replaces `"none"` or `"unknown"` when
the recipient is wakeable at send time — native status `idle` or `shell`, no
blocking wait, not frozen, and cmux sees this session with this pid active on
its surface; the cmux listing of that check is bounded at 3 s, and its failure
leaves `"none"` / `"unknown"`. The send succeeds in every case (`201`, message
`pending`). Absent from a daemon older than the field; clients then say
nothing. `pigtail msg send` / `msg reply` print a warning on stdout for `none`
and `unknown`, and for `wake` the line `⏰ <label> is idle: pigtail will wake it
(screen guard).` instead of the warning. The same
values appear in `BroadcastResult.delivery`.

**Limits (daemon-enforced).**

| Rule | Response |
|---|---|
| sender unknown | `400 INVALID_REQUEST` |
| sender of another scope than the caller | `400 INVALID_REQUEST` (same as unknown) |
| across scopes, kind other than `notice` (from another scope: the recipient is no candidate, `404 ADDRESS_UNRESOLVED`) | `403 SCOPE_MISMATCH` |
| `notice` from the master to another scope without `X-Pigtail-Cross-Scope: write`; a scope unknown | `403 SCOPE_MISMATCH` |
| body empty or > 2048 bytes | `422 RELAY_REFUSED` |
| body matches a secret shape (`ghp_`, `github_pat_`, `sk-`, `xox[bp]-`, `AKIA`, `-----BEGIN … KEY`, `Bearer …`) | `422 RELAY_REFUSED` |
| recipient already has 5 pending | `422 RELAY_REFUSED` |
| 5 messages from the same sender to the same recipient in 10 min | `422 RELAY_REFUSED` |
| thread already holds 6 messages ("escalate to the user") | `422 RELAY_REFUSED` |
| message to oneself, reply to one's own message, reply to a pigtail notice, reply to a `notice` ("a notice takes no reply") | `422 RELAY_REFUSED` |
| new message kind not `info`/`request`/`notice`, reply kind not `reply`/`ack` | `400 INVALID_REQUEST` |

**Kinds.** A new message is `info` (default), `request` or `notice`; a reply
is `reply` or `ack`. A `notice` is a one-way service notice: no `in_reply_to`
may point at it (`422 RELAY_REFUSED`, "a notice takes no reply" — ack it
instead), it carries no `subject_id`, logs only the sender's `→` line (no `←`
line on the recipient's subject), and expires without an expiry notice to its
sender. It is the only kind that crosses scopes (master only, with the
opt-in). Migration `0018` adds it to the `kind` CHECK.

TTL defaults to 24 h (max 7 days). A pending message past its TTL becomes
`expired` and its sender receives an `info` notice from `pigtail` (no reply
possible) — except for a `notice`, whose sender learnt each outcome at send
time. The countdown is
suspended while the recipient is in Standby and resumes at its return: the
message keeps the time it had left when the Standby began (one received during
it, its whole TTL).

#### POST /messages/broadcast

One `notice` to every live session the sender reaches, the sender excluded:
the live sessions of the sender's scope, or of every scope for the master with
`X-Pigtail-Cross-Scope: write`. Session-bound, like `POST /messages` (the
caller acts as `from`).

```jsonc
POST /messages/broadcast  { "from": "<session id>", "body": "reboot at six", "ttl_seconds": 3600 }
→ 200 { "results": [ { "to": PeerSession, "message_id": "msg_…", "pending": 1, "delivery": "none" },
                     { "to": PeerSession, "refused": "RELAY_REFUSED", "reason": "… already has 5 unread messages …" } ],
        "sent": 1, "refused": 1, "other_scopes_skipped": 3 }
```

Request-level refusals refuse the whole call and send nothing: sender unknown
or of another scope than the caller `400 INVALID_REQUEST`;
`X-Pigtail-Cross-Scope` from a non-master `403 SCOPE_MISMATCH`; body empty,
too long or secret-shaped `422 RELAY_REFUSED`. Past them, each recipient gets
its own message and thread under the limits of a single send, and may be
refused alone (its result carries `refused` and `reason`). The answer is `200`
even when nothing was sent — `sent` says it; `pigtail msg send --all` exits 1
when `sent` is 0. `other_scopes_skipped` (omitted at 0) counts the live
sessions of other scopes a master broadcast **without** the opt-in did not
reach; a non-master never receives it (D19).

**Delivery.** `pending → delivered` (`delivered_via`: `prompt`, `tool`,
`rewake`, `cli`) `→ acked`, or `pending → expired`. `claim` and `wait` take
only rows still pending, so two claimers never get the same message. `wait`
admits one waiter per session: a new one makes the old return
`superseded: true`.

**Inbox markers.** While a session has pending messages the daemon keeps a
file `<socket dir>/inbox/<session_id>` holding the count; the prompt/tool hook
tests it before calling the daemon. A test daemon on its own socket therefore
has its own marker dir.

#### Session waits

What a session waits for from its user (spec "Sidebar pigtail" §4.8), shown
by the sidebar loop's `pigtail-attn` pill. Two session-bound routes of the
relay, behind `scopeMW`: the caller acts as the session (`actingAs`), so its
scope must equal the session's registered scope — else, as for an unknown
session, `404 NOT_FOUND`.

```jsonc
POST   /peers/{session}/wait   { "type": "arbitrage", "reason": "Sidebar pigtail",
                                 "source": "declared",      // "auto" or "held"; default declared
                                 "holder_pid": 4242,        // held only: required
                                 "until": "2026-10-05T15:00:00Z" } // optional: limit (auto) and mac (held)
→ 200 PeerSession              // with "waits": [ { type, reason, source, since, until?, holder_pid? } ]
DELETE /peers/{session}/wait   ?source=auto|declared|held|all[&holder_pid=N]
                               // or ?keep=subject alone: the turn's waits, the subject's wait kept (done command)
→ 200 PeerSession              // no source = the turn's waits (auto, declared)
```

| Rule | Response |
|---|---|
| `type` not one of `permission`, `question`, `arbitrage` (`decision` accepted), `review`, `blocked`, `done`, `working`, `wrapped` — plus, for `source=auto` only, the hook-typed failure states `limit`, `login`, `api_error`; `mac` is not declarable (`waittype.Parse` refuses it), only `source=held` takes it (`waittype.ParseHeld`), and `source=held` takes nothing else | `400 INVALID_REQUEST` |
| `source` not `auto`/`declared`/`held` (POST), not `auto`/`declared`/`held`/`all` (DELETE) | `400 INVALID_REQUEST` |
| `source=held` without a positive `holder_pid` (POST); `holder_pid` on a DELETE that is not `source=held`, or not a positive integer | `400 INVALID_REQUEST` |
| `keep` other than `subject`, or with `source` or `holder_pid` (DELETE) | `400 INVALID_REQUEST` |
| `reason` (one line once whitespace is folded) > 200 bytes, or a secret shape (same list as messages) | `422 RELAY_REFUSED` |

One row per session and source (`session_waits`, migration `0013`): a new
declaration replaces the previous one (last told wins) — for `source=auto`, a
`permission` without a reason, or the same type without a reason, does not
replace a standing question or review (D3, 2026-10-04); the response is `200`
with the unchanged waits. The automatic wait sits next to the declared one.
Clearing what is not there is `200`. `GET /peers` rows carry `waits` too.

`POST /peers` (not `end`) also restores: a declared wait closed by a session
end (`wait_records.closed_by = 'session_end'`, migration `0030`; the end hook or
the sidebar judging the session gone) comes back when the same session id
registers again alive — `source: "hook"`, or a snapshot whose pid runs and
names the session. The same record reopens; `session.wait_changed` and
`wait.opened` fire as for a declaration. `done`, `wrapped`, `working` (D7: its
background job died with the restart, so the session is closable after an idle
hour), auto and held waits are never restored. A record still `open` that its
subject owns (kept by the done command's `keep=subject` clear or at a session
end) gets only its row back, and only when `closed_by = 'session_end'`; that
branch resets `closed_by` to NULL. An end that finds a `done`, `wrapped` or
`working` row stamps nothing, so a kept-open record is never restored as the
`review` it was before the done command.

`until` (RFC 3339, migration `0016`, column `until_ms`) is when an automatic
wait ends on its own: the usage limit's reset, read from the transcript's
`quotaLimits.resetsAt` (subject `sub_x000000000000032`, A1/A2). It is kept for
`type=limit` only and ignored on any other type; a later wait of the same
source replaces the row whole, `until` included. The same holds for `type=mac` of a
held wait: `until` is when the hold ends on its own (`pigtail session wait mac
--for D`).

**Held waits** (`source=held`, migration `0019`, columns `source` and
`holder_pid` of `session_waits`): a wait that a process keeps and that ends
with it, not with a turn. Only `mac` is held: the wait-for-Mac helper (`--wait`) poses it while
the session waits for the Mac, the sidebar reads "Waiting for Mac" (orange,
just above Your turn in the gravity order; with no reason given, the pill
names `mac.release_command` from `settings.yaml` when it is set — the
user's own command that releases the Mac). `holder_pid` is the process that
keeps it.
A held wait sits next to the auto and declared ones (one row per session and
source); the pill shows the gravest of the declared and held types. It ends
at its `until`, with the session, or when its holder pid is dead, judged by
the sidebar loop.

- **Typed content and records** (sub_x000000000000026, migration `0027`
  `wait_records`). `POST /peers/{session}/wait` takes an optional `details`
  object, the typed content of the wait (§ Waits — read, answer, snooze lists
  the fields per type); the daemon validates it and a refusal is `400
  INVALID_REQUEST` naming the field (`details.options[1].reco: …`), with
  nothing written. A declared wait, and the `auto` wait of a Claude Code
  dialog (`permission`, `question`), open a **record** beside their
  `session_waits` row, with a stable id `wait_…`: `waits[].id` in the
  `PeerSession` answers and in `GET /peers` (empty for the other `auto` types
  — `limit`, `login`, `api_error` — and for `held`, which have no record). The
  row and the pill are unchanged; the old form `pigtail session wait <type>
  "<reason>"` gives the same output, and prints the id only with `--details`. A record is `open` until its
  source's row goes, or until the wait of a subject is answered: `replaced`
  (a newer wait of the same owner and source, or of the same session and
  source), `cleared` (a prompt or a turn end of its emitter, the end of a
  session for a wait it owns) or `answered` (§ Waits). At most **one open
  record per owner and source and one per session and source** (`declared`,
  `auto`), the owner being the subject or the session (next paragraph): a
  declaration and a dialog that came after are two true facts at once, and
  one never closes the other.
- **The wait of a subject** (D28, D29 of sub_x000000000000026). A record has
  an **owner**: `subject:<scope>:<subject_id>` for a declared wait of an
  answerable type (`arbitrage`, `question`, `review`, `blocked`) frozen on a
  subject, else `session:<session_id>` (a `CHECK` of `0027` holds the rule).
  Two uniquenesses on open records: one per owner and source — a declaration
  on the subject by another session **replaces** the previous one (D29-B,
  `wait.closed` `replaced` told under the session that emitted it) — and
  one per session and source. A subject's wait **outlives its session**
  (D29-A): the session's end, or the sidebar loop judging it **gone** (ended,
  or its pid dead, confirmed `WaitGoneConfirm` apart), drops its
  `session_waits` row and its pill, never the record. A clear closes only a
  record the clearing session emitted, and only with its own row: a prompt or
  a turn end of the emitter while it lives closes its subject's wait as
  before — except the user's **done command** (`session.done_command` in
  `settings.yaml`, unset by default): a prompt that is that command, alone or
  followed by a space and arguments (`waittype.DonePrompt`, the exact prefix
  only), clears with `DELETE /peers/{session}/wait?keep=subject`, the
  turn's rows and the records the session owns go, its subject's wait stays
  open, and the `done` or `wrapped` the emitter then declares replaces
  nothing of it (its pill reads the new type; an answer drops the emitter's
  row only while it is still that wait's): the wait outlives the session and
  its answer reaches the subject's next session (D29-A, D29-C). A session
  that inherited a wait (same subject, emitted by another) never closes it —
  a `working`, `done` or `wrapped` it declares replaces nothing of the
  subject's. An inherited wait has no pill; `pigtail wait ls` shows it. A dialog reported twice (`PermissionRequest`,
  then its `permission_prompt` notification, same type and reason) is one
  record, its `cmd` kept. The details of a dialog are what the hook payload
  carries (`waittype.DialogDetails`): for a permission the `cmd` and `where`
  (the cwd), for a question its header, text and options; invalid details of
  an `auto` wait are dropped and logged, the wait is still recorded. A dialog
  record is readable, **never answerable** outside the terminal (D2). A
  record's `scope` and `subject_id` are frozen at declaration: a subject bound
  to the session afterwards does not appear on it, nor on the answer's trace
  (the subject log, the journal pointer, the message). The subject is frozen
  only if the session's scope may read it (D23, the rule of `POST /journal`): a
  work session working in a perso subject's worktree records a wait with none,
  and the answer's journal pointer carries none either. **Retention**: a closed
  record is kept 30 days, then purged — at the relay's start and at each tick
  of its sweep; an open record is never purged.
- **Sources.** `auto`: a Claude Code hook typed a blocking dialog (`pigtail
  hook claude-wait`: `PermissionRequest`, `Notification` `permission_prompt` →
  permission, except for `AskUserQuestion` (→ question · header) and
  `ExitPlanMode` (→ review · Plan), whose permission is the dialog itself;
  `Notification` `elicitation_dialog`/`elicitation_url_dialog`,
  `PreToolUse` `AskUserQuestion` → question; `PreToolUse` `ExitPlanMode` →
  review; `idle_prompt`, `agent_needs_input` and the other notifications →
  nothing). `declared`: `pigtail session wait <type> "<reason>"`. `held`:
  `pigtail session wait mac --holder-pid N [--for D]`, run by the wait-for-Mac helper
  (an agent never declares `mac`).
- **Shown** (by the loop, from the native status): an automatic wait only
  while the status is `waiting` and it was recorded at most 10 s before that
  wait began; else the native `waitingFor` when telling (`sandbox request` →
  permission, `goal proposal` → review); else the declared wait; else
  untyped. While `idle`, the declared wait; with none, `your_turn` when the
  loop observed the session `busy` since it started or resumed (in memory,
  per session id, pid and native `startedAt`; forgotten at a daemon restart),
  else nothing. While `busy`, nothing.
- **Cleared**: the prompt hook (`UserPromptSubmit` → `DELETE`, only when the
  marker exists; a bare `DELETE` clears `auto` and `declared` and spares a
  held wait, and `all` adds held); SessionEnd (`POST /peers {end}`, which
  clears all three sources); a held wait by its holder — `DELETE
  ?source=held&holder_pid=N` is conditional, it clears the held wait only if
  its holder is N (a newer helper run keeps its own), also the path the loop
  takes (`Relay.ClearHeldWait`: row, marker and event) when the holder is dead
  or `until` has passed; and by the loop — an
  automatic wait once the native status leaves `waiting` after it was
  recorded, a declared one at the next turn (idle, then busy again, both
  after the declaration, confirmed by a turn-opening entry in the transcript:
  a background notification that runs no turn does not end it; no readable
  transcript = the status alone), both when the session is gone (two observations
  2 s apart). No expiry by time: a decision waits as long as it takes.
- **Markers.** While a session has a wait the daemon keeps
  `<socket dir>/wait/<session_id>`; the prompt hook stats it and calls the
  daemon only when it exists. Rebuilt from the store at start.
- **Event** `session.wait_changed` `{session_id, cmux_workspace_id, type,
  source}` on every record and every clear (`type` empty), Scope = the
  session's. Never the reason. On a clear, `source` is the list of the
  sources actually cleared, comma-separated (`auto,declared`, `held`, ...).

#### Standby — a session parked by `/standby` (sub_x000000000000041)

Two session-bound writes (`actingAs`, as `/wait`): the `/standby` skill parks
its own session (`pigtail session standby`); the next prompt the user types ends
it (`pigtail hook claude-wait`), as does `pigtail session standby --clear`.

```jsonc
PUT    /peers/{session}/standby   → 200 { "session_id": "…", "since": "…" }
DELETE /peers/{session}/standby   → 200 { "session_id": "…", "cleared": true }   // false: was not in Standby
```

| Rule | Response |
|---|---|
| caller scope ≠ the session's registered scope, or unknown session | `404 NOT_FOUND`, as an unknown session |

- **Rows.** Migration `0023`: `session_standby`, one row per session; a new
  `PUT` starts over (no compaction state). The row outlives `SessionEnd`
  (`POST /peers {end:true}`, D24): a relaunch resumes the same session id and
  finds its Standby.
- **Markers.** While a session is in Standby the daemon keeps
  `<socket dir>/standby/<session_id>`, the hooks' and the CLI's reading of
  the same state (a marker that cannot be read counts as in Standby). Kept on
  `SessionEnd`, rebuilt from the table at start.
- **Event** `session.standby_changed` `{session_id, cmux_workspace_id,
  standby}` on every entry and every actual exit (a `DELETE` of nothing
  publishes nothing), Scope = the session's. The Standby also ends,
  with the same event, when its cmux workspace is gone: two successful cmux
  listings in a row without it (a finished session whose workspace is still
  open keeps its Standby).
- **Compaction.** At each relay sweep, after the idle wake and with its cmux
  calls and guards (eligible and idle, surface verified, pane unfocused, input
  empty, no running `cmux check`), pigtaild types `/compact` in a session in
  Standby. One guard differs from the wake: the session's open Stop watcher
  (`GET /messages/wait`) does not hold the compaction — during the Standby it
  claims nothing, so it cannot wake the session against the `/compact`; the
  Standby is re-read right before typing instead (ended or unreadable →
  nothing typed). Proof: the session's native status time moves on the pid it was
  sent to; not proven after 2 min → sent again, 3 sends at most. A Standby
  compacted or given up is never typed in again, a relaunch included.
- **One exception (D1).** A user answer (`POST /waits/{id}/answer`, § Waits
  — read, answer, snooze) lifts the Standby **of the session it is delivered
  to**, then wakes it: the only thing besides his prompt that ends a Standby
  and delivers its inbox. A kept answer too (D29-C): at the registration of
  the subject's next session, the message is written and the Standby lifted,
  the wake comes at the relay's tick; at the tick, the Standby is lifted then
  the session woken. Nothing else changes: the idle
  wake still skips a session in Standby and its inbox is still not claimed
  before his prompt (D16).
- **Event** `session.standby_compact_failed` `{session_id, cmux_workspace_id,
  tries, reason}`: the `/compact` pigtail types in a standby session was not
  proven after its last send (or was left in the input). Scope = the
  session's. The Standby stays.
- **Event** `session.pause_changed` `{session_id, cmux_workspace_id,
  paused}`: pigtaild paused a Claude session idle past
  `-idle-session-pause-seconds` (`paused: true`, its claude pid exited after
  SIGTERM), or the pause ended (`paused: false`: the same session registered
  alive again, or another session took its surface). Scope = the session's.
  Journal kinds `session-pause-paused` (`session-pause:<session>:<pid>:paused`)
  and `session-pause-resumed` (`…:<new pid>:resumed`).
- **Event** `session.pause_failed` `{session_id, cmux_workspace_id, reason}`:
  pigtaild gave a pause up (SIGTERM refused, or the pid still alive 2 minutes
  after it); every write of the pause was undone. Scope = the session's. Also
  the journal kind `session-pause-failed` (`…:<pid>:failed`), which holds that
  pid, daemon restart included.

- **Journal** kinds `idle-wrap-sent` (`idle-wrap:<session>:<pid>:sent`) and
  `idle-wrap-failed` (`…:<pid>:failed`, summary `send failed` or `stayed open
  after <done command>`): pigtaild typed the user's done command
  (`session.done_command` in `settings.yaml`; unset, nothing is typed) into an
  idle session whose declared wait is `done`, once per pid, instead of pausing it; the sent line is the
  durable once-only marker, and either line keeps that pid out of the pause.
  Scope = the session's. No event.

- **Journal** kinds of the automatic retry (`relay_retry.go`), all with the
  session's scope and no event (the pill reason travels with the wait, and
  `session.wait_changed` never carries it):
  `api-retry-sent` (`api-retry:<session>:<pid>:<anchorMs>:<try>:<class>:<otherTry>`,
  the once-only marker written right before the `continue`), `api-retry-held`
  (`…:<try>:held:<hash>`, a refusal, once per reason), `api-retry-exhausted`
  (`…:<anchorMs>:exhausted`); `limit-resume-sent`, `limit-resume-native` and
  `limit-resume-held` (`limit-resume:<session>:<pid>:<untilUnix>:sent|native|held[:<hash>]`);
  and from the rotation worker `rotate-retry`
  (`rotate-retry:<session>:<untilUnix>:<k>`, summary `<short>: limit · retry <k> after: <why>`)
  and `rotate-retry-ended` (`rotate-retry:<session>:<untilUnix>:ended`, summary
  `<short>: limit · try <k> · window over|limit wait gone|reset passed|standby|refused: <why>`;
  `<short>` is the session id's first 8 characters).

#### Prio — cmux's own pin on the session's tab (sub_x000000000000014, D3, D18, D19, D25, D28, D31)

No route. The prio is cmux's native pin, read by the sidebar loop in every
`workspace list` (`pinned`) and written by the CLI from inside the session:
`pigtail session prio` pins the tab of `CMUX_WORKSPACE_ID`,
`pigtail session prio --clear` unpins it (a user's own slash commands may run
them). A pin by hand in cmux is a prio too; the pin belongs to the tab, so
`/clear`, a fresh `claude` and a fresh session in the same tab keep it. cmux puts a pinned tab at the top of
its folder, above the maturity bands; it is not reordered by
pigtail, and it is filed in its area folder when its workspace is bound to a
subject. There is no « Prio » folder any more (the name stays reserved for a
focus, D24).

- **Writes (CLI only).** `cmux workspace-action --action pin|unpin --workspace
  <CMUX_WORKSPACE_ID>` (`cmux.FoldersCLI.SetPinned`, the one place pigtail
  writes a pin), aimed by UUID, never by `CMUX_SURFACE_ID`; every other
  `CMUX_*` stripped, `CMUX_SOCKET_PATH` kept (a live pass on NIGHTLY).
- **Read first (D31).** The CLI finds the tab with `list-windows` then
  `workspace list --window <UUID>` on each window (the window never comes
  from the env) and reads `pinned`: an already pinned tab gets no pin, an
  unpinned tab no unpin (`"changed": false`). A tab absent from every window
  → `workspace <id> not found in cmux`, non-zero exit, no write.
- **Output.** `prio: pinned`, `prio: unpinned`, `prio: not pinned`; `--json`
  → `{"session_id","workspace_id","pinned","changed","warning"}` (`warning`
  omitted when empty). Without `CMUX_WORKSPACE_ID`: `not in a cmux workspace:
  nothing pinned`, non-zero exit. `--clear` needs `CMUX_WORKSPACE_ID` only,
  never the session.
- **Prio and Standby: the last gesture wins.** A pin lifts the session's
  Standby (`DELETE /peers/{session}/standby`; 404 ignored); the daemon down
  (D25) → the pin stays, exit 0, `warning: daemon unreachable: standby not
  cleared`. `pigtail session standby` unpins a pinned tab **before** writing
  the Standby; a failed read or unpin writes no Standby (`standby: not set —
  unpin failed: <err>` on stderr, non-zero exit). A pin by hand on a tab in
  Standby lifts the Standby at the loop's next pass (D29).
- **Read by another process (`prio-check`, CLI only).** `pigtail session
  prio-check --session S [--workspace W] --json` →
  `{"prio":bool,"available":bool}` (maintenance window, C2): `prio` is the
  pin of tab `W` read with the same `workspacePinned`, in every window; an
  empty or absent `W` is `prio:false`, cmux unreachable `available:false`
  (both exit 0); neither flag exits 2. It needs no `CMUX_*` variable (the
  caller may be a detached runner: `CMUX_SOCKET_PATH` is honoured when set,
  else cmux's default socket) and no daemon route.
- **Gone.** `PUT`/`DELETE /peers/{session}/prio`, the `session_prio` reads
  and writes, `prio_ended` on the Standby answer and the
  `session.prio_changed` event. Migration `0026`'s `session_prio` table stays
  in the base, unread (a rollback to the previous binary still works).

#### Handbacks — turns ended without a declared wait (D11/D12)

Measure of the turns a session handed back without saying what it waits for,
credited to the session's last skill (subject `sub_x000000000000032`, Task 11).
Two session-bound writes (`actingAs`, as `/wait`) fed by `pigtail hook
claude-wait`, and one scoped read.

```jsonc
POST /peers/{session}/skill  { "skill": "plan" }                    → 204
POST /peers/{session}/turn   { "turn_id": "<transcript promptId>",
                               "turn_start": "2026-10-05T09:00:00Z",
                               "event": "stop",                             // or "failure"
                               "working": true, "reason": "la CI tourne" }  → 204  // stop only, optional
GET  /session-handbacks?since=<RFC 3339>    // default: now − 7 days
→ 200 { "since": "…", "skills": [ { "skill": "plan", "count": 2, "last_at": "…" } ] }
```

| Rule | Response |
|---|---|
| `skill` not a skill name (`[A-Za-z0-9][A-Za-z0-9:_.-]{0,99}`, as `waittype.ValidSkillName`) | `400 INVALID_REQUEST` |
| `event` not `stop`/`failure`; `turn_id` empty, > 128 bytes or with a `/`; `turn_start` missing | `400 INVALID_REQUEST` |
| `since` not RFC 3339 | `400 INVALID_REQUEST` |
| caller scope ≠ the session's registered scope (writes) | `404 NOT_FOUND`, as an unknown session |

`working` (the hook read a closing `🔄` line in `last_assistant_message`): when
the session declared nothing since `turn_start`, the daemon declares a
`working` wait with `reason` (source `declared`, cleared like one); without it,
an undeclared stop ends a declared `working` older than the turn. The turn is
recorded first, so it still counts as undeclared.

A session in Standby (§ Standby) infers no `working` wait, and a `stop`
recorded while it has its `session_standby` row is `declared`: the `/standby`
turn hands back without a wait and is not a missed hand-back
(sub_x000000000000041).

Migration `0017`: `session_last_skill` (one row per session, last told wins),
`session_last_declared` (written by `POST /wait` with `source=declared`, in
the same transaction), `session_turn_ends` (one row per `(session_id,
turn_id)`, with the session's scope). A `stop` inserts once — two `Stop`s of
a turn count once — with `declared` = a declaration at or after `turn_start`
and `skill` = the session's last skill (`""` before any); a `failure` sets
`failed=1`, whichever comes first. A turn counts when `failed=0` and
`declared=0`. The read groups the counted turns of `at >= since` by skill,
ordered by skill, over the scopes the caller can read (`scope.CanRead`: a
scope reads its own turns, the master — `sees_all` in the scope table — every
scope's; a session with no scope records under the master, D17). `skills` is
`[]` when nothing counts.

#### Reveal — bring a session's cmux tab to the front (sub_x000000000000025)

« Aller à la session » on a native dialog's card in the reader (a permission,
a Claude Code dialog: it is answered in the terminal). One scoped write of the
relay (`classScoped`, behind `scopeMW`, not acting as the session: no
`actingAs`), allowed to the web cookie. It writes nothing in the store.

```jsonc
POST /peers/{session}/reveal   (no body)
→ 200 { "session_id": "…", "cmux_workspace_id": "<UUID>" }
```

pigtaild runs, under one 3 s deadline and with no `CMUX_*` variable in the
environment but `CMUX_SOCKET_PATH` of `-cmux-socket`: `cmux tree --all --json
--id-format both` (the window holding the workspace), `cmux select-workspace
--workspace <UUID> --window <UUID>`, then `open -b com.cmuxterm.app` (a safety
net: the select alone brought cmux to the front, probe of 2026-10-29).

| Rule | Response |
|---|---|
| unknown session, or one whose scope the caller may not read (`scope.CanRead`) | `404 NOT_FOUND` « no such session », the same body; cmux never called |
| the request carries `X-Pigtail-Session` (an agent session) | `403 UNAUTHORIZED`; cmux never called |
| the session has no `cmux_workspace_id` | `409 CONFLICT` |
| cmux has no tab with that workspace (closed since) | `409 CONFLICT` « its cmux tab is not open » |
| cmux does not answer (no CLI, socket down, the 3 s passed) | `503 CMUX_UNREACHABLE` |

**Audit.** The `messages` table is the trail. A message whose sender maps to a
subject appends `subject_log` `kind=message` (`→ <recipient> [kind msg_…]: first
line`) there; the recipient's subject gets the `←` line. Messages with no
subject on either side stay in the table only.

#### Waits — read, answer, snooze (sub_x000000000000026)

The user lists, answers and snoozes what their sessions wait for from their
terminal (`pigtail wait ls|show|answer|snooze`). Four routes of the relay
behind `scopeMW`, all `classScoped`; `GET`s read under the scope of the
wait's session at declaration, the two writes also need `scope.CanWrite`.

```jsonc
GET  /waits[?all=1][&subject=<id>][&session=<id>][&closed=<0..5>]
                               → 200 []Wait      // open waits the caller may read, oldest first; all=1 (or true) adds the snoozed ones;
                                                 // then up to `closed` closed ones, most recently closed first
GET  /waits/{id}               → 200 Wait        // open or closed, with its answer
POST /waits/{id}/answer        { "choice": "A" | "reco", "text": "…", "items": ["1=ok","2=ko:note"], "seen": true, "reason": "…" }
                               → 200 { "wait": Wait, "message_id": "msg_…", "delivery": "…", "standby_ended": true,
                                       "delivered_to": "<session>", "delivered_to_label": "…" }
POST /waits/{id}/snooze        { "until": "2026-10-20T15:00:00Z" }
                               → 200 Wait
```

`Wait`: `id`, `session_id`, `session_label`, `session_ended`, `subject_id`,
`subject_short_title`, `type`, `reason`, `source` (`declared`|`auto`), `state`
(`open`|`replaced`|`cleared`|`answered`), `opened_at`, `closed_at`,
`snoozed_until`, `details`, `answer`, `delivered_to`, `delivered_to_label`,
`delivered_at`. `session_ended` tells the emitter is gone (ended, or its pid
dead): a subject's wait outlives it (D29-A). `delivered_to` names the
session the answer was delivered to, `delivered_to_label` its label under
the caller's scope (D23); both are empty while an answer is kept.

**Envelope** (D28). `details` and `answer` carry `kind` (the wait's type) and
`schema` (`1`, `pigtail.WaitDetailsSchema` / `WaitAnswerSchema`). Both are
optional on a declaration and filled by the daemon; a `kind` that is not the
wait's type is `400` `details.kind: "<x>" does not match the wait type
"<t>"`, a `schema` other than `1` is `400` `details.schema: unsupported
version <n> (this pigtaild reads 1)`. A record read back is always
normalized (`kind`, `schema` set). Details by type (`title`, `summary`,
`step`, `url`, `shots[{src,caption}]` are common; a field a type does not take
is refused, 16 KiB at most):

| Type | Takes beyond the common fields |
|---|---|
| `arbitrage` | `options` (2 to 10: `id`, `label`, `detail`, `url`, one `reco`), `why` |
| `question` | `options` (up to 10, no `reco`), `other` |
| `review` | `items` (up to 30: `label`, `detail`, `url`, `shots`), or `url` with its `lead` — not both |
| `permission` | `cmd`, `where` — shown only (D2) |
| `blocked`, `done`, `working`, `wrapped` | nothing |

A string that looks like a secret (the shapes `POST /messages` refuses) is
refused in any field. `shots[].src` is an `http(s)://` or `file://` URL or an
absolute path.

**Filters and history** (sub_x000000000000025). `subject=<id>` keeps the
waits of that subject, `session=<id>` those its session emitted; both apply
to the open waits and to the history. `closed=<n>` (0..5, default 0) appends
up to `n` closed waits (`answered`, `cleared`, `replaced`), the most
recently closed first, each with its `answer`; the history is read in the
caller's scope, so `n` counts readable waits only. `closed` outside 0..5 →
`400 INVALID_REQUEST` `closed must be 0..5`.

**Scope.** A wait of a scope the caller may not read answers exactly like an
unknown id (`404 NOT_FOUND`, `no such wait`) on all four routes, and is absent
from `GET /waits`. The same list drops, for a subject of a scope the caller
may not read, the subject's id and short title, and shows the session by its
own name (D23). A write on a readable wait of another scope (the master
without `--cross-scope`) is `403 SCOPE_MISMATCH`. Outside a cell the CLI reads
the default scope only; the full view is `--scope all` / `PIGTAIL_SCOPE=all`,
and `--cross-scope` (header `X-Pigtail-Cross-Scope: write`) writes outside
that scope.

**Answer.** Order of the checks; each refusal writes nothing.

| Rule | Response |
|---|---|
| the request carries `X-Pigtail-Session` (an agent session) | `403 UNAUTHORIZED` |
| unknown wait, or of a scope the caller may not read | `404 NOT_FOUND` |
| master on another scope's wait without the opt-in | `403 SCOPE_MISMATCH` |
| the wait is not `open` (or its race was lost) | `409 CONFLICT` `wait <id> is <state>` |
| the wait is a `permission`, or any `auto` record (a dialog) | `422 RELAY_REFUSED` `answer it in the terminal (D2)` |
| invalid JSON; not exactly one form; a form the type does not take; unknown option; a review item unjudged, judged twice or a `ko` without a note; a secret in `text`, a note or `reason` (the field is named); a `done`, `wrapped` or `working` wait | `400 INVALID_REQUEST` |
| store error | `500 INTERNAL`, nothing published |

Forms by type: `arbitrage` → `choice` (an option id, or `reco` for the
recommended one) or `text`; `question` → `choice` when it has options, or
`text`; `review` with items → `items` (`n=ok|ko[:note]`, every item once);
`review` without items and `blocked` → `seen` or `text`. `reason` adds a note to
any form. Only these four types are answerable (`waittype.AnswerableTypes`):
any other is `400` `a <type> wait takes no answer`.

**Recipient** (D28, D29-C). The answer goes to the emitter while it is not
gone; else, for a wait of a subject, to the most recent session not gone
bound to the same subject **in the wait's scope** (a work session in a perso
subject's tab never receives it); else nobody runs on the subject and the
answer is **kept**: stored, no message, `delivery` `kept: no live session on
its subject` (`pigtail.DeliveryKept`), `message_id` and `delivered_to`
empty. A kept answer reaches the subject's next session once (the delivery
is conditional), by two paths: at that session's registration (`POST
/peers`, once its subject is known: message written, Standby lifted, no
wake and no cmux call in the hook's request) or at the relay's tick (at
start and at each sweep, before the idle wake: Standby lifted, then the
session woken). The recipient chosen and ended before the write (a race):
the answer is kept for a wait of a subject; for a wait **without a
subject**, `409 CONFLICT` `wait <id> is closed: its session ended` — never
kept, it would be lost in silence. A wait without a subject answers its
emitter only, as before (a crashed emitter not yet ended still receives
it). The recipient ending with its answer still unread (message pending),
or judged **gone** by the sidebar loop (a crash without SessionEnd, its
pid dead, confirmed `WaitGoneConfirm` apart) or by the relay's tick (a
recipient of an unread answer not `Alive` on two sightings 2 s apart — it
has no wait row left for the loop to judge; no cmux call): the message
expires and the answer is kept again for the subject's next session, which
the same tick hands it to, once. The recipient ending
between the answer's write and its delivery: the response says `delivery:
kept`, without `message_id`, `delivered_to` or a `wait.delivered_to`, and
the trace reads « kept for the subject's next session ». A kept answer whose subject never runs again is purged with the
closed waits at 30 days, with one log line `relay: purged kept answer <id>
never delivered (subject <sub>)`.

The stored `answer` value is the source of truth; the message body is its
rendering (`waitdetail.RenderAnswer`), for the agent to read — never to be
parsed.

The answer is **one transaction**: the wait becomes `answered`, its `declared`
row goes, and a relay message to the recipient is inserted (`from_session` `human`, label
the configured human name (`settings.yaml` `human.name`, default `the user`), `thread_id` = the wait id, kind `info`, body « Answer from <name> to your
wait … », clipped to the body limit with a pointer to `pigtail wait show
<id>`, TTL = the maximum). A failure of the insert leaves the wait `open`,
publishes nothing. After the commit: the recipient's Standby is lifted (D1,
§ Standby) with `session.standby_changed` `standby:false`, then the recipient
is woken.
`delivery` is what `POST /messages` says (`waiter`, `wake`, …); `standby_ended`
tells the Standby was lifted. When the Standby cannot be read (or not lifted),
the answer is still stored and sent, the Standby stays, and `delivery` is
`held: <reason>` (`pigtail.DeliveryHeld`): the answer waits for the user's next
prompt. Traces, written once at the answer, kept or not: when the wait has a
subject, a `subject_log` line `kind=wait` (`<name> → <label> [answer <wait
id>]: …`, the label `kept for the subject's next session` for a kept answer), and a journal entry `kind=wait-answer`
with the deterministic id `wait-answer-<wait id>` (a wait is answered once),
`source` `pigtail:wait/<wait id>`, a one-line summary; a failed trace is logged
and the answer stays delivered.

**Snooze.** The user's, from their terminal, like the answer (D3): a request that
carries `X-Pigtail-Session` (an agent session) is refused first, `403
UNAUTHORIZED` `snooze from your terminal, not from an agent session`, and
writes nothing; `pigtail wait snooze` refuses locally when
`CLAUDE_CODE_SESSION_ID` is set. `until` must be in the future (`400 INVALID_REQUEST`, also for a
zero time); an already snoozed wait takes the new time; a wait that is not
`open` is `409 CONFLICT`; the scope rules are the answer's. The wait leaves
`GET /waits` until `until` and comes back by itself: there is **no timer**, the
list compares `snoozed_until` with the clock at each read, so a daemon
stopped over the deadline still returns it. The session is not touched: no
message, no marker, no Standby change, the `session_waits` row stays.

**Events.** `wait.*`, data `{wait_id, session_id, state}` — never the reason
nor the content —, Scope = the wait's (frozen at declaration), delivered only
to subscribers that may read it (§ Subject scope):

- `wait.opened` — a record was opened (`state` `open`);
- `wait.closed` — a record closed without an answer: `state` `replaced` or
  `cleared`;
- `wait.answered` — `state` `answered`, `session_id` the emitter. An answer
  never publishes `wait.closed` with it; an immediate delivery publishes it
  **followed by** `wait.delivered`;
- `wait.delivered` — `state` `answered`, `session_id` the recipient: the
  answer's message was written for it, at the answer or later for a kept
  answer;
- `wait.snoozed` — `state` `open`: only the event, the session's marker and
  `session.wait_changed` do not move.

A delivery also publishes `message.created`; an answer publishes
`session.wait_changed` (`source` `declared`, type empty) for its emitter
while it is not gone.

### Profiles & recipes

```
GET    /profiles                      → []Profile
GET    /profiles/:id
POST   /profiles                      → register
PATCH  /profiles/:id
DELETE /profiles/:id

GET    /recipes                       → []Recipe (across all projects)
       ?project_id=
GET    /recipes/:id
```

Profiles are user-level (not project-scoped). Recipes are
project-scoped.

### Events (SSE stream)

```
GET    /events                        → Server-Sent Events stream
       query: ?since=<event_id>&types=workspace.*,agent.*
```

The subscriber's scope is `X-Pigtail-Scope` at subscribe time (absent = the
default scope). Events carrying subject-layer data (`message.*`, `cmux.*`, `wait.*`, `subject.*`, `session.launch*`, `staffing.*`) are
delivered only to subscribers that may read their scope — see
§ Subject scope; untagged, they reach the master's subscribers only. Every event outside these six families goes to every subscriber.

Right after the headers the daemon writes and flushes the comment line
`: connected` (then a blank line), before any replayed event: a client that
waits for the first data learns at once that its subscription exists. It
then sends `: ping` every 15 s. Clients ignore comment lines (`:`-prefixed),
per the SSE format. At daemon shutdown the stream ends normally after the
queued events (`daemon.shutting_down` included).

Each event:

```
id: evt_abc
event: workspace.state_changed
data: { "workspace_id": "ws_h7s9", "from": "planning", "to": "building", "timestamp": "..." }
```

#### Event types (stable, additive)

```
project.created
project.updated
project.deleted

workspace.created
workspace.state_changed
workspace.activity      — agent edits, commits, etc.
workspace.cleanup_started
workspace.cleanup_done
workspace.cleanup_failed — teardown failed; terminates cleanup_started
workspace.deleted

agent.started
agent.message            — agent emits output
agent.paused             — agent asks for input
agent.resumed
agent.done
agent.killed
agent.crashed

delivery.created
delivery.updated         — CI changed, review added, etc.
delivery.merged
delivery.cancelled

backlog.item_added
backlog.item_updated
backlog.item_deleted
backlog.item_dispatched

resources.warning        — RAM, port exhausted, etc.
resources.server_evicted — pigtail stopped a dev server (RAM, battery, cap)
resources.server_died    — a dev server vanished unannounced (crash, OOM,
                           kill from outside pigtail); one event per
                           process, payload:
                           {workspace_id, server, servers, port, at_unix}
                           where servers lists every name on the port
                           (sorted) and server is servers[0]
cpu.slot_revoked         — a CPU slot held past -cpu-slot-max-hold was
                           dropped from the arbiter's books and redistributed.
                           Nothing was killed and the client was not told;
                           payload: {group, label, held_seconds}
disk.low                 — the free disk space crossed under -disk-alert-gb;
                           payload: {free_bytes, threshold_bytes,
                           floor_bytes}. Visible to every scope (machine-wide,
                           no scope tag); one per crossing, none on a restart
                           under the threshold. Mirrors the disk-low journal
                           line and the open `disk` alert of GET /alerts
disk.ok                  — back above the threshold + 10 GB; same payload
admission.granted        — a heavy command was admitted; payload:
                           {lease_id, group, class, label, cost_bytes,
                            waited_ms} — waited_ms is how long it spent in
                           the queue, 0 when it started at once
admission.released       — an admitted command ended; same payload plus
                           {peak_bytes, held_seconds} — peak_bytes is the
                           peak memory of its process tree (physical
                           footprint on darwin, VmRSS on Linux), what costs
                           are learned from
admission.revoked        — a lease held past -admit-max-hold-seconds was
                           dropped from the books; nothing was killed;
                           payload adds {held_seconds}
message.created          — a relay message was accepted (or pigtail sent
                           an expiry notice); payload: {id, thread_id, kind,
                           state, from_session, to_session, from_label,
                           to_label, subject_id} — never the body
message.delivered        — claimed by the recipient (same payload)
message.acked            — acknowledged by the recipient
message.expired          — its TTL passed unread; the sender is notified
cmux.check_notice        — pigtaild wrote the end-of-pass notice of a cmux
                           launch (GET /cmux/check/notice); no payload
cmux.sidebar_changed     — the sidebar loop's wanted state of a workspace
                           moved; payload: {cmux_workspace_id, attention,
                           wait_type, wait_reason, waiting_count, servers,
                           subject_id} — servers = [{name, port, state}], as in
                           GET /cmux/sidebar; never a title. Scope: the bound
                           subject's, the default scope when unbound — the
                           master's when the wait_reason was told by a
                           session of another scope or of none (D17);
                           pigtail's own reasons (trust folder, respawn)
                           keep the workspace's
session.wait_changed     — a session's wait was recorded or cleared;
                           payload: {session_id, cmux_workspace_id, type,
                           source} (type "" = cleared) — never the reason.
                           Scope: the session's (the master's for a session
                           with no scope, D17)
wait.opened              — a wait record was opened (a declared wait, or the
                           auto wait of a dialog); payload: {wait_id,
                           session_id, state} — never the reason nor the
                           content. Scope: the wait's, frozen at its
                           declaration (all wait.* alike)
wait.closed              — a record closed without an answer; state =
                           replaced | cleared
wait.answered            — the user answered it (never a wait.closed with it);
                           session_id = the emitter
wait.delivered           — its answer's message was written for a session
                           (at the answer, or later for a kept answer);
                           session_id = the recipient
wait.snoozed             — the user snoozed it; state stays open
session.standby_changed  — a session entered or left Standby; payload:
                           {session_id, cmux_workspace_id, standby}. Scope:
                           the session's
session.standby_compact_failed — the /compact typed in a standby session
                           was not proven; payload: {session_id,
                           cmux_workspace_id, tries, reason}. Scope: the
                           session's
session.pause_changed    — an idle session was paused or its pause ended;
                           payload: {session_id, cmux_workspace_id, paused}.
                           Scope: the session's
session.pause_failed     — a pause was given up and undone; payload:
                           {session_id, cmux_workspace_id, reason}. Scope:
                           the session's
lid.changed              — Travel Mode state changed (POST /lid, auto-off
                           deadline/battery/thermal (lid closed)/external,
                           failed auto-off);
                           payload: {on, reason}. Unscoped
subject.output_added     — an output was registered on a subject (POST
                           /subjects/{id}/outputs, only when the row is
                           created); payload: {subject_id, seq, id, kind,
                           key}. Scope: the subject's
staffing.decision        — a line the staffing scheduler appended to its
                           journal; payload: a StaffingDecision. Scope: the
                           decision's (§ Staffing)
session.launch_changed   — a session launch (POST /subjects/{id}/launch)
                           changed state: pending (tab created), launched
                           (its session registered) or failed; payload: a
                           SessionLaunch without deduplicated. Scope: the
                           subject's

daemon.started
daemon.shutting_down
```

Clients subscribe to all events or filter via `types` query.
Reconnection: clients send `Last-Event-ID` header; daemon replays from
that point (events retained for ~24h or until `MAX_EVENTS` configured
in daemon).

### Web (browser)

The web front shows the backlog of every scope and acts on it through an
exact list of writes (reorder, Standby / resume, capture, launch), served by
pigtaild itself (an embedded single-page app, `internal/webui/dist`). A
browser cannot present the bearer token, and must never see it: it signs in
once with a single-use code and then carries a cookie, which reads and writes
on an exact allowlist.

| Route | Transport | Answer |
|---|---|---|
| `POST /web/login-code` | socket only (HTTP, even with the Bearer → 403) | `{code, http_addr}`; HTTP off → 409 |
| `GET /web/login?code=` | HTTP, Host checked | code valid → `Set-Cookie` + 303 to `/`; used, expired or unknown → the « Lien expiré » page, 410 |
| `GET /` | HTTP, Host checked | cookie valid → the embedded `index.html`; else the « Lien expiré » page, 200; both `Cache-Control: no-store` |
| `GET /assets/*` | HTTP, Host checked | cookie valid → the hashed asset, `Cache-Control: public, max-age=31536000, immutable`; else 401 |

**Flow.** `pigtail web` asks a code on the socket and prints
`http://<http_addr>/web/login?code=<code>`; it never opens a browser. A
code is 256 random bits, single-use, valid 2 minutes, held in memory only (a
daemon restart voids it; at most 16 wait at once). Trading it sets
`pigtail_web=<secret>; Path=/; Max-Age=31536000; HttpOnly;
SameSite=Strict`, host-only (no `Domain`). The secret is one per daemon,
in `<config dir>/web-session` (0600, created at first start with HTTP
on; the file of the former web front is renamed to it at load, which keeps the secret only: the cookie changed name, so browsers and the app are signed out once after the upgrade); a restart keeps every browser signed in. **Revoking**: delete that file
and restart pigtaild — every browser is signed out.

**The native app (Pigtail, macOS).** It asks its codes on the socket
exactly like `pigtail web` (`POST /web/login-code`, no scope header;
the answer is read from the usual `data` envelope), loads
`/web/login?code=` in its web view and keeps the cookie. It probes its
session with `GET /assets/<unknown name>` (it uses `/assets/_probe`): **401
without a valid cookie, 404 with one, 403 on a refused Host**. That 401 is
now a contract the app relies on — `GET /` cannot tell, it answers 200 either
way. A probe with no HTTP answer makes the app ask a code on the socket to
tell a stopped daemon (socket error) from HTTP off (409) or a moved
`http_addr` (200, used at once for the login). At most two codes per
attempt.

**The cookie path in `authMW`**, tried only after the socket and a valid
Bearer, which do not change:

1. the cookie matches the secret (constant-time compare);
2. the method and the decoded path are on one of two exact allowlists —
   reads (`GET`): `/health`, `/version`, `/subjects`, `/subjects/{id}`,
   `/subjects/{id}/log`, `/subjects/{id}/outputs`,
   `/subjects/{id}/outputs/groups`, `/themes`, `/spaces`, `/peers`,
   `GET /captures`, `GET /launches/{id}`, `GET /waits` (with its query:
   `subject=`, `session=`, `closed=`; never `GET /waits/{id}`),
   `GET /sdk-sessions`, `GET /sdk-sessions/{id}`,
   `GET /sdk-sessions/{id}/events`; writes:
   `POST /subjects/ranks`, `POST /subjects/{id}` (one segment, not `next`,
   `suggest`, `find` nor `ranks`), `POST /subjects/{id}/launch`, `POST /captures`,
   `DELETE /captures/{id}`, `POST /waits/{id}/answer` (one segment; the
   answer reads as the user's — step 4 drops the session header — and the handler
   still refuses a permission or a Claude Code dialog, « answer it in the
   terminal (D2) »), `POST /peers/{session}/reveal` (one segment; § Reveal),
   `POST /subjects/{id}/sdk-sessions`,
   `POST /sdk-sessions/{id}/requests/{rid}/answer`,
   `POST /sdk-sessions/{id}/messages`, `POST /sdk-sessions/{id}/end` —
   nothing else (`/subjects/next`,
   `/subjects/{id}/worktree`, `/subjects/{id}/phases`, `/backup`, `/events`,
   `/workspaces`, `POST /themes`, `POST /subjects/{id}/log`,
   `POST /captures/promote`, any other write all answer 401); an escaped path
   (`%2F`) is refused;
3. `Host` is the daemon's own address (`--http`) or `localhost:<its port>`
   (DNS rebinding); `Origin` is `http://` + that allowed host (never the
   request's own `Host`) — `Origin` required on every write, tolerated
   absent on a read; `Sec-Fetch-Site`, when sent, is `same-origin` or
   `none` — else 403;
4. the server sets `X-Pigtail-Scope` to the master scope, drops the
   client's `X-Pigtail-Cross-Scope` and `X-Pigtail-Session`, and on a write
   sets `X-Pigtail-Cross-Scope: write` itself: the browser never picks its
   scope nor its opt-in (D1, one web front acts on every scope), and never
   passes for a maintenance's exempt session (a freeze holds its writes,
   423);
5. the request is marked as the web front's (`handlers.WithWeb`), and the
   handlers narrow what it may do:
   - `POST /subjects/{id}`: the body's one key is `status`, an open status
     (`inbox`, `understanding`, `designing`, `building`) or `parked`, and the
     subject's current status is open or `parked`; anything else — another
     field, `done`, `dropped`, `captured`, a closed subject reopened — is
     `403 FORBIDDEN_FOR_WEB`, the subject untouched. The web front never
     writes `captured` nor `done`;
   - `POST /captures` with a non-empty `from_subject_id` (it would close the
     subject, `captured`) and `DELETE /captures/{id}` of a capture born of a
     subject (it would put back its `from_prev_status`, `done` included):
     `403 FORBIDDEN_FOR_WEB`, nothing written; a plain capture is allowed;
   - `POST /subjects/{id}/launch` and `GET /launches/{id}`: a subject of
     any scope (the CLI keeps its own-scope rule, § Session launch).

A failure at 1–2 answers 401 like a request without credentials, at 3 403.
No page and no URL carries the API token (`TestTokenNeverInServedHTML`).

**Rank changes.** `POST /subjects/ranks` applies a swap, an insertion or its
undo in one transaction (any caller; the web front is its first user):

```
POST   /subjects/ranks   {"changes":[{"id":"sub_…","from":5,"to":6}, …]}
                         → 200 {"data": [Subject, …]}   the changed subjects, in the order of changes
```

1 to 500 changes, ids unique, ranks ≥ 0 (`0` = unranked, stored NULL), else
400. Each `from` is checked against the subject's current rank first: one
stale `from` answers `409 RANK_CHANGED` and nothing is written (someone
moved it since the read). Each id is authorised as a write: `404 NOT_FOUND`
(unknown, or not visible), `403 SCOPE_MISMATCH` (another scope without the
opt-in). Behind `FreezeMW` (423). `updated_at` is not touched.

**Accepted risk.** A host-only cookie is sent to every port of `127.0.0.1`.
Any local process able to receive it already runs as the same macOS user and
can read `~/.config/pigtail/token` (0600) directly, so the cookie adds no
exposure to local code. The threats the cookie path stops are remote web
pages: CSRF (`SameSite=Strict`, plus an `Origin` equal to the daemon's own
required on every write, and `Sec-Fetch-Site`) and DNS rebinding (`Host`).
The writes themselves are bounded by the allowlist and the web-only
refusals above: a forged request still cannot close a subject.

### Webhooks (inbound)

```
POST   /webhooks/github               → GitHub webhook receiver
```

Public-facing endpoint (HTTP only, never on Unix socket). Configured per
project via `gh api`-managed webhooks pointing at this URL (typically via
a ngrok-like tunnel in dev, or a stable URL via `cloudflared` for live
setups). Validates HMAC signature.

---

## 5. Authentication & security

### Socket

No explicit auth; relies on:
- Filesystem permissions (`mode 0600` on the socket)
- Unix uid check at accept time
- `XDG_RUNTIME_DIR` semantics on Linux

### HTTP

Bearer token in `Authorization: Bearer <token>` header. Token is generated
at daemon start, written to `~/.config/pigtail/token` (mode 0600), and
rotated on `pigtail rotate-token`.

The web front's session cookie is the one other credential: it reads and
writes on exact allowlists (an `Origin` equal to the daemon's own required
on every write), with `Host` checks, the master scope and the cross-scope
opt-in set by the server: § Web (browser).

### Webhook endpoint

HMAC-SHA256 signature validation against `X-Hub-Signature-256` header.
Secret stored per-project in keychain via `pass-cli` integration (or env
var fallback).

---

## 6. Pagination & filtering

List endpoints support:

```
?limit=50            (default 100, max 500)
?cursor=<opaque>     (cursor-based pagination)
```

Response includes `meta.next_cursor` if more results exist.

Filtering via specific query params per endpoint (documented inline).

---

## 7. Idempotency

There is no `Idempotency-Key` header: the daemon never read one. Idempotency
is per route, stated with each route. `POST /subjects/{id}/launch` is
idempotent per subject: a replay while an API launch of the subject is under
way returns that launch (`200`, `deduplicated: true`), never a second tab
(§ Session launch).

---

## 8. Example end-to-end flow

### Dispatch a feature, watch it ship

```
1. Client → POST /dispatch
   { project_id: "prj_t", intent: "fix date picker", recipe_id: "rec_fix_ui" }
   ← 201 { workspace_id: "ws_h7s9", agent_session_id: "agt_42" }

2. Client → GET /events (SSE)
   ← workspace.created
   ← agent.started
   ← agent.message (claude proposes a plan)
   ← workspace.state_changed (planning → planning still, plan refined)
   ← agent.paused (waits for user "go")

3. Client → POST /agents/agt_42/reply { content: "go" }
   ← 202
   ← agent.resumed
   ← workspace.state_changed (planning → building)
   ← agent.message (edits, commits)
   ← agent.done

4. Client → POST /workspaces/ws_h7s9/ship
   ← 201 { delivery_id: "del_1503" }
   ← delivery.created

5. Daemon receives webhook from GitHub (PR opened, then reviews, then CI)
   ← delivery.updated (ci_status: passing)
   ← delivery.updated (review approved)
   ← delivery.merged
   ← workspace.state_changed (delivery_open → shipped)
   ← workspace.cleanup_started
   ← workspace.cleanup_done
   ← workspace.deleted
```

### Live triage refresh

```
1. Client → GET /triage             → 13 items
2. Client → GET /events?types=workspace.*,agent.paused,delivery.updated
3. On every event, client either:
   - Re-fetches GET /triage (simple)
   - Or applies the event diff client-side (optimized)
```

---

## 9. Stability & deprecation

- All endpoints documented here are **stable** in API v1.
- Additive changes (new fields, new optional params, new events) are
  not breaking.
- Breaking changes bump the major (v1 → v2). v1 is supported for at
  least 12 months after v2 is released.
- Endpoint deprecation: 6-month notice, response header
  `X-Pigtail-Deprecation: <date> <reason>`.

---

## 10. Reference: Go client snippet

```go
client := pigtail.NewClient(pigtail.DefaultSocket)

ws, err := client.Dispatch(ctx, pigtail.DispatchRequest{
    ProjectID: "prj_globex",
    Intent:    "fix the date picker locale",
    RecipeID:  "rec_fix_ui",
})

events, _ := client.Events(ctx, pigtail.EventsRequest{
    Types: []string{"workspace.*", "agent.paused", "delivery.updated"},
})
for ev := range events {
    fmt.Println(ev.Type, ev.Data)
}
```

A reference Go client lives at `pkg/pigtail/client.go` and is the
canonical implementation. The CLI, TUI, and MCP server all use it.

---

## 11. Open questions

- WebSocket as an alternative to SSE for events? Pros: bidir. Cons: more
  complex client. Current bias: SSE for v1.
- Bulk endpoints (e.g. `POST /workspaces/bulk-cleanup`) — needed at scale,
  defer to V1+.
- gRPC alternative for high-throughput clients (web UI rendering 1000s
  of events/s)? Not for MVP.
- Token rotation policy and refresh semantics for HTTP clients.

These are not blockers for the v1 build.
