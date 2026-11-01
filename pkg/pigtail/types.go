package pigtail

import (
	"fmt"
	"strings"
	"time"
)

// ID prefixes used throughout the wire protocol.
const (
	PrefixProject   = "prj_"
	PrefixWorkspace = "ws_"
	PrefixAgent     = "agt_"
	PrefixDelivery  = "del_"
	PrefixBacklog   = "bli_"
	PrefixProfile   = "prf_"
	PrefixRecipe    = "rec_"
	PrefixEvent     = "evt_"
	PrefixTheme     = "thm_"
	PrefixSubject   = "sub_"
	PrefixPhase     = "phs_"
	PrefixLog       = "log_"
	PrefixCapture   = "cap_"

	PrefixStaticServer = "ss_"
)

// WorkspaceState represents the lifecycle state of a workspace.
type WorkspaceState string

const (
	StatePlanning      WorkspaceState = "planning"
	StateBuilding      WorkspaceState = "building"
	StateReadyToReview WorkspaceState = "ready_to_review"
	StateReviewing     WorkspaceState = "reviewing"
	StateReadyToShip   WorkspaceState = "ready_to_ship"
	StateDeliveryOpen  WorkspaceState = "delivery_open"
	StateShipped       WorkspaceState = "shipped"
	StateNeedsCleanup  WorkspaceState = "needs_cleanup"
	StateArchived      WorkspaceState = "archived"
)

// AgentState represents the lifecycle state of an agent session.
type AgentState string

const (
	AgentStateRunning     AgentState = "running"
	AgentStatePausedInput AgentState = "paused_waiting_input"
	AgentStatePausedPlan  AgentState = "paused_waiting_plan_approval"
	AgentStateDone        AgentState = "done"
	AgentStateKilled      AgentState = "killed"
	AgentStateCrashed     AgentState = "crashed"
)

// EventType identifies the kind of event emitted by the daemon.
type EventType string

const (
	EventProjectCreated EventType = "project.created"
	EventProjectUpdated EventType = "project.updated"
	EventProjectDeleted EventType = "project.deleted"

	EventWorkspaceCreated      EventType = "workspace.created"
	EventWorkspaceStateChanged EventType = "workspace.state_changed"
	EventWorkspaceActivity     EventType = "workspace.activity"
	EventWorkspaceCleanupStart EventType = "workspace.cleanup_started"
	EventWorkspaceCleanupDone  EventType = "workspace.cleanup_done"
	// EventWorkspaceCleanupFailed terminates a cleanup_started that did not
	// reach cleanup_done. Without it a failed teardown leaves subscribers
	// waiting on an event that never comes.
	EventWorkspaceCleanupFailed EventType = "workspace.cleanup_failed"
	EventWorkspaceDeleted       EventType = "workspace.deleted"
	EventWorkspaceRenamed       EventType = "workspace.renamed"

	EventAgentStarted EventType = "agent.started"
	EventAgentMessage EventType = "agent.message"
	EventAgentPaused  EventType = "agent.paused"
	EventAgentResumed EventType = "agent.resumed"
	EventAgentDone    EventType = "agent.done"
	EventAgentKilled  EventType = "agent.killed"
	EventAgentCrashed EventType = "agent.crashed"

	EventDeliveryCreated   EventType = "delivery.created"
	EventDeliveryUpdated   EventType = "delivery.updated"
	EventDeliveryMerged    EventType = "delivery.merged"
	EventDeliveryCancelled EventType = "delivery.cancelled"

	EventBacklogItemAdded      EventType = "backlog.item_added"
	EventBacklogItemUpdated    EventType = "backlog.item_updated"
	EventBacklogItemDeleted    EventType = "backlog.item_deleted"
	EventBacklogItemDispatched EventType = "backlog.item_dispatched"

	EventResourcesWarning EventType = "resources.warning"
	EventServerEvicted    EventType = "resources.server_evicted"
	// EventServerDied announces a dev server that vanished without anyone
	// announcing a stop — a crash, an OOM kill, or a kill from outside
	// pigtail. Distinct from EventServerEvicted, which pigtail caused.
	EventServerDied EventType = "resources.server_died"

	// EventCPUSlotRevoked is published when a CPU slot held past the
	// -cpu-slot-max-hold ceiling is dropped from the arbiter's books. The
	// client is never told and is never interrupted: only the accounting
	// changes, so a wedged test cannot eat a slot forever.
	EventCPUSlotRevoked EventType = "cpu.slot_revoked"

	// EventAdmissionGranted is published when a heavy command is admitted
	// by the memory admission controller.
	EventAdmissionGranted EventType = "admission.granted"
	// EventAdmissionReleased is published when an admitted command ends; it
	// carries the peak memory (physical footprint on darwin) its process tree
	// reached — what the controller learns costs from.
	EventAdmissionReleased EventType = "admission.released"
	// EventAdmissionRevoked is published when a lease held past
	// -admit-max-hold-seconds is dropped from the books. As with CPU slots,
	// the command is neither told nor stopped.
	EventAdmissionRevoked EventType = "admission.revoked"

	// EventDiskLow is published when the free disk space crosses under the
	// alert threshold (-disk-alert-gb): payload {free_bytes, threshold_bytes,
	// floor_bytes}. EventDiskOK follows once it is back above the re-arm
	// level. Machine-wide: visible to every scope, naming none.
	EventDiskLow EventType = "disk.low"
	EventDiskOK  EventType = "disk.ok"

	EventDaemonStarted      EventType = "daemon.started"
	EventDaemonShuttingDown EventType = "daemon.shutting_down"
)

// ErrorCode is the machine-readable error identifier in error responses.
type ErrorCode string

const (
	ErrInternal          ErrorCode = "INTERNAL"
	ErrInvalidRequest    ErrorCode = "INVALID_REQUEST"
	ErrUnauthorized      ErrorCode = "UNAUTHORIZED"
	ErrNotFound          ErrorCode = "NOT_FOUND"
	ErrWorkspaceNotFound ErrorCode = "WORKSPACE_NOT_FOUND"
	ErrAgentNotFound     ErrorCode = "AGENT_NOT_FOUND"
	ErrProjectNotFound   ErrorCode = "PROJECT_NOT_FOUND"
	ErrProjectNotInit    ErrorCode = "PROJECT_NOT_INITIALIZED"
	ErrDeliveryNotFound  ErrorCode = "DELIVERY_NOT_FOUND"
	ErrInvalidRecipe     ErrorCode = "INVALID_RECIPE"
	ErrInvalidProfile    ErrorCode = "INVALID_PROFILE"
	// ErrLaunchOverMargin: every account of the profile's scope is over the
	// usage margin (HTTP 409); over_margin launches anyway.
	ErrLaunchOverMargin  ErrorCode = "LAUNCH_OVER_MARGIN"
	ErrPortExhausted     ErrorCode = "PORT_EXHAUSTED"
	ErrBackendError      ErrorCode = "BACKEND_ERROR"
	ErrTmuxNotAvailable  ErrorCode = "TMUX_NOT_AVAILABLE"
	ErrGHAuthRequired    ErrorCode = "GH_AUTH_REQUIRED"
	ErrAgentSpawnFailed  ErrorCode = "AGENT_SPAWN_FAILED"
	ErrDaemonBusy        ErrorCode = "DAEMON_BUSY"
	ErrConflict          ErrorCode = "CONFLICT"
	ErrCPUSlotsDisabled  ErrorCode = "CPU_SLOTS_DISABLED"
	ErrAdmissionDisabled ErrorCode = "ADMISSION_DISABLED"
	ErrMaintenanceFrozen ErrorCode = "MAINTENANCE_FROZEN"
	// ErrForbiddenForWeb: a write the web front's cookie may not make
	// (HTTP 403) — a subject field other than an open or parked status, a
	// closed subject reopened, a capture that closes or reopens a subject
	// (docs/wire-protocol.md § Web (browser)).
	ErrForbiddenForWeb ErrorCode = "FORBIDDEN_FOR_WEB"
	// ErrRankChanged: POST /subjects/ranks found a subject whose current
	// rank is not the change's from (HTTP 409); nothing is written.
	ErrRankChanged ErrorCode = "RANK_CHANGED"
	// ErrDevServerRunning: a dev server of the workspace survived the stop (or
	// could not be attributed); a clean refuses and removes nothing.
	ErrDevServerRunning ErrorCode = "DEV_SERVER_RUNNING"

	// Session launch refusals (POST /subjects/{id}/launch,
	// docs/wire-protocol.md § Session launch).
	ErrSessionLive        ErrorCode = "SESSION_LIVE"
	ErrLaunchInProgress   ErrorCode = "LAUNCH_IN_PROGRESS"
	ErrLaunchBurst        ErrorCode = "LAUNCH_BURST"
	ErrNoCell             ErrorCode = "NO_CELL"
	ErrBypassRefused      ErrorCode = "BYPASS_REFUSED"
	ErrCellModeUnreadable ErrorCode = "CELL_MODE_UNREADABLE"
	ErrNoLaunchDir        ErrorCode = "NO_LAUNCH_DIR"
	ErrCmuxUnreachable    ErrorCode = "CMUX_UNREACHABLE"
	ErrCmuxCreateFailed   ErrorCode = "CMUX_CREATE_FAILED"
	ErrLiveUnknown        ErrorCode = "LIVE_UNKNOWN"
	ErrRegistryUnreadable ErrorCode = "REGISTRY_UNREADABLE"
)

// Remote describes the VCS remote for a project.
type Remote struct {
	Kind  string `json:"kind"`
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

// PortRange is an inclusive [min, max] port range.
type PortRange [2]int

// Project represents a registered project (one per pigtail.yaml).
type Project struct {
	ID               string               `json:"id"`
	Name             string               `json:"name"`
	RootPath         string               `json:"root_path"`
	MainWorktree     string               `json:"main_worktree,omitempty"`
	Remote           *Remote              `json:"remote,omitempty"`
	DefaultProfileID string               `json:"default_profile_id,omitempty"`
	DefaultBackend   string               `json:"default_backend"`
	PortRanges       map[string]PortRange `json:"port_ranges"`
	// PortEnvs maps a port kind to the env var name written into .env.local.
	// When a kind has no entry, callers fall back to DefaultPortEnv(kind).
	PortEnvs map[string]string `json:"port_envs,omitempty"`
	// Defaults are project-level dispatch defaults (agent, base_ref,
	// branch_prefix). nil if pigtail.yaml has no `defaults:` block.
	Defaults *ProjectDefaults `json:"defaults,omitempty"`
}

// DiffSummary is a compact diff stat (lines / files changed).
type DiffSummary struct {
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	Files     int `json:"files"`
}

// HooksStatus is where a workspace's post-create hooks stand.
type HooksStatus string

const (
	HooksRunning HooksStatus = "running"
	HooksOK      HooksStatus = "ok"
	HooksFailed  HooksStatus = "failed"
)

// HooksResult is the recorded outcome of a workspace's post-create hooks.
// A nil *HooksResult means unknown (a workspace created before it was
// recorded, or a project without hooks) — never a failure.
type HooksResult struct {
	Status HooksStatus `json:"status"`
	Hook   string      `json:"hook,omitempty"`  // the hook that stopped (failed only)
	Error  string      `json:"error,omitempty"` // why it stopped (failed only)
	Log    string      `json:"log,omitempty"`   // the worktree's hooks.log
	At     time.Time   `json:"at"`
}

// Workspace is the central resource: one isolated unit of agent work.
type Workspace struct {
	ID          string                 `json:"id"`
	ProjectID   string                 `json:"project_id"`
	Intent      string                 `json:"intent"`
	State       WorkspaceState         `json:"state"`
	Path        string                 `json:"path"`
	Ports       map[string]int         `json:"ports"`
	RecipeID    string                 `json:"recipe_id,omitempty"`
	BackendKind string                 `json:"backend_kind"`
	BackendData map[string]interface{} `json:"backend_data,omitempty"`
	// DisplayRef is the backend-computed human label of the work ref (git
	// branch name; Intent when absent). Populated server-side by the list/get
	// handlers via backend.DisplayRef so clients never read BackendData
	// (architecture.md §8). Not persisted — recomputed per response.
	DisplayRef string `json:"display_ref,omitempty"`
	// MainRefresh reports what the create path did with the project's main
	// worktree on the way in (nil when it did not look). Like DisplayRef:
	// stamped on the create response, never persisted — see MainRefreshNote.
	MainRefresh      *MainRefreshNote `json:"main_refresh,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	LastActivityAt   time.Time        `json:"last_activity_at"`
	SnoozedUntil     *time.Time       `json:"snoozed_until,omitempty"`
	AgentSessions    []AgentSession   `json:"agent_sessions,omitempty"`
	ActiveDeliveryID string           `json:"active_delivery_id,omitempty"`
	DiffSummary      *DiffSummary     `json:"diff_summary,omitempty"`
	// LastRunawayAt/LastRunawayReason record the most recent runaway
	// dev-server kill by the watchdog (nil/"" when none).
	LastRunawayAt     *time.Time `json:"last_runaway_at,omitempty"`
	LastRunawayReason string     `json:"last_runaway_reason,omitempty"`
	// Hooks is the post-create hooks outcome; nil = unknown.
	Hooks *HooksResult `json:"hooks,omitempty"`
}

// StaticServer is a named static HTTP server that serves an arbitrary
// directory of files, NOT tied to any git worktree. The record is durable;
// its running state is derived live (no PID/running field). Command is empty
// for the built-in Go file server (the default) and is reserved for a future
// override.
type StaticServer struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Dir       string    `json:"dir"`
	Port      int       `json:"port"`
	Command   string    `json:"command,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// StaticServerView is a StaticServer record annotated with live running state.
// It mirrors the daemon handler's view shape: the StaticServer fields inlined
// plus running/running_port. RunningPort equals the record Port when up.
type StaticServerView struct {
	StaticServer
	Running     bool `json:"running"`
	RunningPort int  `json:"running_port,omitempty"`
}

// PinUntilUnpinned is the `duration` value of POST /servers/{ws}/{server}/pin
// that pins a server until it is unpinned.
const PinUntilUnpinned = "until-unpinned"

// ServerView is one running dev server's resource snapshot, as exposed by the
// read-only GET /servers introspection endpoint.
type ServerView struct {
	WorkspaceID string `json:"workspace_id"`
	Server      string `json:"server"`
	Branch      string `json:"branch,omitempty"`
	Port        int    `json:"port"`
	// RSSBytes is the server's process-tree memory: physical footprint on
	// darwin (top's MEM, compressed pages included), VmRSS on Linux. Named
	// and keyed rss_bytes for wire compatibility (Pigtail, older CLIs); it
	// held ps RSS before 2026-10.
	RSSBytes    uint64 `json:"rss_bytes"`
	IdleSeconds int    `json:"idle_seconds"`
	// PinnedUnixSeconds is a timed pin's expiry in Unix seconds; 0 (omitted)
	// when not pinned or pinned until unpinned.
	PinnedUnixSeconds int64 `json:"pinned_until,omitempty"`
	// PinnedDurable reports a pin "until unpinned": it never expires and
	// survives daemon restarts. Additive: a client that predates it reads such
	// a server as unpinned (display only — the daemon still honours the pin).
	PinnedDurable bool `json:"pinned_durable,omitempty"`
	// Owner is the session claiming this server ("" = unclaimed). A
	// sid:<n>@<host> value is derived from the POSIX session; anything else
	// is an opaque identity (cmux workspace id, PIGTAIL_OWNER).
	Owner string `json:"owner,omitempty"`
	// OwnerAlive is only meaningful when Owner != "": an unclaimed server
	// (Owner == "") always carries the zero value false here too, but that is
	// NOT a dead-owner verdict — there is no owner to be dead. Once Owner is
	// set, OwnerAlive is false only for a derived identity whose session
	// leader is gone — the case where `pigtail stop --all` may reclaim the
	// server. Always true for an opaque identity, which cannot be
	// interrogated. `omitempty` means the wire form drops a false value
	// either way; see docs/wire-protocol.md for how a client tells the two
	// "absent" cases apart.
	OwnerAlive bool `json:"owner_alive,omitempty"`
}

// DeadServerView is a dev server that vanished on its own — a crash, an OOM
// kill, or a kill from outside pigtail — as exposed by GET /servers. Distinct
// from a deliberate stop, which never appears here.
//
// One entry is one process. When a project lists that process under several
// names sharing a port (a large monorepo: `dev` and `web`), Servers names them all,
// sorted, and Server is Servers[0] — kept for clients that only read it.
type DeadServerView struct {
	WorkspaceID string   `json:"workspace_id"`
	Server      string   `json:"server"`
	Servers     []string `json:"servers,omitempty"`
	Port        int      `json:"port"`
	// AgoSeconds is how long ago the death was detected, in seconds.
	AgoSeconds int `json:"ago_seconds"`
}

// ServersResponse is the body of GET /servers: the running dev servers plus
// system headroom (available vs. the eviction floor).
type ServersResponse struct {
	AvailableBytes uint64       `json:"available_bytes"`
	FloorBytes     uint64       `json:"floor_bytes"`
	Servers        []ServerView `json:"servers"`
	// RecentDeaths lists dev servers that died on their own inside the
	// detector's reporting window (empty when none — never a placeholder).
	RecentDeaths []DeadServerView `json:"recent_deaths,omitempty"`

	// MainWorktrees reports how fresh each project's main checkout is, as of
	// the daemon's last refresh pass. Empty when the daemon has no verdict yet
	// (just started, refresh disabled, no project) — which a client must
	// render as silence, never as "fresh".
	MainWorktrees []MainWorktreeView `json:"main_worktrees,omitempty"`

	// PortConflicts lists the workspaces whose port is held by another
	// worktree's process, or whose .env.local names another port than the
	// registry — each with the command that fixes it. Empty when none.
	PortConflicts []PortConflictView `json:"port_conflicts,omitempty"`
}

// Reasons of a PortConflictView.
const (
	PortConflictForeignListener = "foreign_listener"
	PortConflictEnvDiverges     = "env_diverges"
)

// PortConflictView is one port conflict of GET /servers.
type PortConflictView struct {
	WorkspaceID       string `json:"workspace_id"`
	Server            string `json:"server"`
	Kind              string `json:"kind"`
	Port              int    `json:"port"`            // what .env.local says
	RegisteredPort    int    `json:"registered_port"` // what the registry says
	Reason            string `json:"reason"`          // "foreign_listener" | "env_diverges"
	HolderPath        string `json:"holder_path,omitempty"`
	HolderWorkspaceID string `json:"holder_workspace_id,omitempty"`
	Fix               string `json:"fix"` // the command to run
}

// MainRefreshAction is the three-valued verdict of a main-worktree refresh, as
// it travels on the wire: "updated", "up-to-date" or "skipped". It mirrors
// internal/backend's own type — the daemon converts, so clients never import
// daemon internals.
type MainRefreshAction = string

// Wire values of MainRefreshAction.
const (
	MainRefreshUpdated  = "updated"
	MainRefreshUpToDate = "up-to-date"
	MainRefreshSkipped  = "skipped"
)

// MainRefreshNote annotates a create response with what happened to the
// project's main worktree on the way in.
//
// Response-only: it is stamped after the workspace is persisted, exactly like
// DisplayRef, and never stored. It describes an event, not the workspace.
type MainRefreshNote struct {
	Action MainRefreshAction `json:"action"`
	Path   string            `json:"path,omitempty"`
	Base   string            `json:"base,omitempty"`
	// Reason explains an abstention, empty otherwise.
	Reason string `json:"reason,omitempty"`
	// Behind is how many commits the checkout was missing before the pass.
	// 0 means level *or* unmeasurable — never read it as proven freshness.
	Behind int `json:"behind,omitempty"`
	// From/To are the short SHAs of a move, set only when Action is "updated".
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// MainWorktreeView is one project's main-checkout freshness in GET /servers.
//
// It is served from the daemon's last known verdict, never computed on the
// request: the listing must stay a fast read, and a display path that shelled
// out to git would make the command people use to check freshness the slowest
// one they own.
type MainWorktreeView struct {
	ProjectID   string            `json:"project_id"`
	ProjectName string            `json:"project_name,omitempty"`
	Path        string            `json:"path,omitempty"`
	Base        string            `json:"base,omitempty"`
	Action      MainRefreshAction `json:"action"`
	Reason      string            `json:"reason,omitempty"`
	Behind      int               `json:"behind,omitempty"`
	// CheckedAgoSeconds is how long ago the verdict was produced, computed
	// fresh on every call. A verdict from an hour ago is a claim about an hour
	// ago — and the daemon may have stopped sweeping since.
	CheckedAgoSeconds int `json:"checked_ago_seconds"`
}

// OffsiteView is the daemon's classification of the offsite backup
// destination, as exposed by GET /backup — the same verdict `pigtail db
// list` renders, read from the same file, so a live client and the CLI with
// the daemon down cannot disagree about it.
type OffsiteView struct {
	DestDir string `json:"dest_dir"`
	// Status is a stable discriminant a client can switch on without parsing
	// Detail's prose: one of the OffsiteStatus wire values ("ok", "failing",
	// "stuck", "in_progress", "stale", "never_succeeded", "no_attempt",
	// "unconfigured").
	Status OffsiteStatus `json:"status"`
	// Severity drives a client-side glyph: "ok", "warn", or "alarm".
	Severity OffsiteSeverity `json:"severity"`
	// Detail is the phrase `pigtail db list` has always shown for this state,
	// rendered once by store.RenderOffsiteDetail and reused verbatim here so
	// the CLI and this endpoint cannot drift into saying different things
	// about the same state.
	Detail string `json:"detail"`

	// LastOKAgeSeconds is how long ago the last successful copy landed. Nil
	// when no copy has ever succeeded — an age of 0 would otherwise be
	// indistinguishable from "just now".
	LastOKAgeSeconds *int `json:"last_ok_age_seconds,omitempty"`
	// LastTryAgeSeconds is how long ago the daemon last attempted a copy,
	// successful or not. Nil when it has never tried.
	LastTryAgeSeconds *int `json:"last_try_age_seconds,omitempty"`

	// ConsecutiveFailures counts attempts that failed back-to-back, reset by
	// any success — the fact that raises an alarm on a repeated failure even
	// while other signals (a recent last_ok, nothing stuck) stay quiet.
	ConsecutiveFailures int `json:"consecutive_failures,omitempty"`
	// SameDevice reports that the destination shares a filesystem with the
	// database: accepted, but not the guarantee it looks like. No omitempty:
	// this is a bool a client branches on, and an absent key must not be
	// confused with an older daemon that never sent the field at all — unlike
	// ConsecutiveFailures, where absent unambiguously means zero.
	SameDevice bool `json:"same_device"`
	// LastErr is the most recent failure, verbatim, empty when the last
	// attempt succeeded.
	LastErr string `json:"last_err,omitempty"`

	// Fallback is the redundant route's own classification
	// (store.ClassifyOffsiteFallback), nil — the "fallback" key absent from
	// the wire form entirely, never null or an empty object — when no
	// fallback destination is configured. The same absence rule this type's
	// own doc comment describes for "offsite" itself, one level down. See
	// docs/wire-protocol.md's "fallback" subsection of GET /backup for the
	// client contract this field carries: a client driving a single glyph
	// must take the max severity rank of this view and Fallback's, and
	// Fallback.Severity never reaches OffsiteSeverityAlarm.
	Fallback *OffsiteFallbackView `json:"fallback,omitempty"`
}

// OffsiteFallbackView is the daemon's classification of the offsite
// *fallback* route — the redundant destination a fallback exercise probes
// independently of whatever path the primary backup actually took, since an
// "ok" primary only proves the path that was used, never the one held in
// reserve. Nested under OffsiteView.Fallback; see that field's doc comment
// and docs/wire-protocol.md for the absence rule and the client contract.
type OffsiteFallbackView struct {
	// Dest is the raw fallback destination alias, unparsed, exactly as
	// declared.
	Dest string `json:"dest"`
	// Status is a stable discriminant a client can switch on without parsing
	// Detail's prose: one of the OffsiteFallbackStatus wire values ("ok",
	// "failing", "not_yet_exercised", "misconfigured"). Independent of the
	// enclosing OffsiteView.Status — the two routes are never the same fact.
	Status OffsiteFallbackStatus `json:"status"`
	// Severity drives a client-side glyph on the same "ok"/"warn"/"alarm"
	// scale as OffsiteView.Severity, with one guarantee the primary severity
	// does not carry: it never reaches OffsiteSeverityAlarm — see this
	// package's ClassifyOffsiteFallback caller and docs/wire-protocol.md.
	Severity OffsiteSeverity `json:"severity"`
	// Detail is the phrase rendered once by store.RenderOffsiteFallbackDetail
	// and reused verbatim here, on the same model as OffsiteView.Detail.
	Detail string `json:"detail"`

	// LastOKAgeSeconds is how long ago the fallback last exercised
	// successfully. Nil when it never has.
	LastOKAgeSeconds *int `json:"last_ok_age_seconds,omitempty"`
	// LastTryAgeSeconds is how long ago the fallback was last exercised,
	// successful or not. Nil when it has never been tried — including the
	// not_yet_exercised status itself: Detail's prose may name how long the
	// fallback has sat idle since it was declared, but that borrowed
	// duration is for the sentence only and is not repeated here.
	LastTryAgeSeconds *int `json:"last_try_age_seconds,omitempty"`

	// ConsecutiveFailures counts fallback exercises that failed
	// back-to-back, reset by any success, counted independently of the
	// enclosing view's own ConsecutiveFailures.
	ConsecutiveFailures int `json:"consecutive_failures,omitempty"`
	// LastErr is the fallback's own most recent failure, verbatim, empty
	// when the last exercise succeeded.
	LastErr string `json:"last_err,omitempty"`
	// Via is the source address the destination reported seeing on the
	// fallback's last successful exercise. See docs/wire-protocol.md's
	// "fallback" subsection: on a host running its VPN in userspace
	// networking, "127.0.0.1" is the *healthy* reading for a probe that
	// genuinely took the pinned route, not a bug — and Via is the one field
	// that lets a human notice two aliases that actually share the same
	// underlying route, which this classification cannot detect on its own.
	Via string `json:"via,omitempty"`
}

// BackupResponse is the body of GET /backup. Offsite is nil — the "offsite"
// key absent from the wire form entirely, never a null or an empty object —
// when no destination is configured, mirroring formatOffsite's own rule of
// printing nothing at all in that case.
type BackupResponse struct {
	Offsite *OffsiteView `json:"offsite,omitempty"`
}

// BackupRunResponse is the body of POST /backup/offsite/run — the daemon's
// acknowledgment that it started (or declined to start) an immediate backup
// run, never a fresh verdict: the copy and the exercise are both
// asynchronous, so a verdict returned at this point would invite reading it
// as this run's own result when it is really whatever the last cycle left
// behind.
//
// No omitempty on any of the three fields, unlike every other bool or
// counter in this file (see OffsiteView.SameDevice's own doc comment for the
// same rule stated once already). This is the one place in this package
// where omitting it is actively dangerous rather than merely inconsistent:
// the client-side Swift BackupRunResult is the
// single type in its backup wire file that keeps Swift's synthesized Decodable
// conformance, with three non-optional Bool fields, deliberately — every
// other wire type there hand-writes init(from:) precisely so an absent key
// decodes to a safe default instead of throwing. A synthesized decoder
// throws on any missing key. Add omitempty to OffsiteStarted, FallbackStarted
// or FallbackConfigured here and the day one of them is false, the key
// disappears from the payload, BackupRunResult.init(from:) throws, the whole
// decode fails, and the retry button goes silently, permanently inert — no
// error surfaces anywhere, because the failure happens inside a decode the
// caller already treats as "nothing new to report". See docs/wire-protocol.md's
// POST /backup/offsite/run section for the full contract.
type BackupRunResponse struct {
	// OffsiteStarted reports whether this call actually started an immediate
	// primary copy. False both when a copy was already in flight and when no
	// primary destination is configured (though the latter is instead a 400
	// — see the handler's own doc comment).
	OffsiteStarted bool `json:"offsite_started"`
	// FallbackStarted reports whether this call actually started an
	// immediate fallback exercise, bypassing its own 6h due-check but never
	// its correction guards (an empty, malformed, or same-as-primary
	// destination, or no file-backed state to persist to). False whenever
	// FallbackConfigured is false, and also false when an exercise was
	// already in flight or a guard refused it.
	FallbackStarted bool `json:"fallback_started"`
	// FallbackConfigured reports whether a fallback destination string is
	// set at all, independent of whether it is valid — the same "set, not
	// necessarily usable" meaning store.OffsiteFallbackHealth.Configured
	// carries on the read side (store/offsite_fallback.go). False here is
	// the "no fallback configured" case of the handler's response table,
	// distinct from a configured-but-guarded destination, where this reads
	// true and FallbackStarted reads false.
	FallbackConfigured bool `json:"fallback_configured"`
}

// BatteryServerView is one running server's battery-eviction status, as
// exposed by GET /battery.
type BatteryServerView struct {
	WorkspaceID    string `json:"workspace_id"`
	Server         string `json:"server"`
	Port           int    `json:"port"`
	IdleSeconds    int    `json:"idle_seconds"`
	EvictInSeconds int    `json:"evict_in_seconds"`
	Spared         bool   `json:"spared"`
}

// BatteryResponse is the body of GET /battery and POST /battery: power state,
// mode config, and per-server idle countdown to eviction.
type BatteryResponse struct {
	OnBattery    bool                `json:"on_battery"`
	Enabled      bool                `json:"enabled"`
	GraceSeconds int                 `json:"grace_seconds"`
	Servers      []BatteryServerView `json:"servers"`
}

// StopIdleServerView is one server's outcome in POST /servers/stop-idle: every
// running server is reported, stopped or not, and a kept server always says
// why (never silent about what it spared).
type StopIdleServerView struct {
	WorkspaceID string `json:"workspace_id"`
	Server      string `json:"server"`
	Port        int    `json:"port"`
	IdleSeconds int    `json:"idle_seconds"`
	Stopped     bool   `json:"stopped"`
	Reason      string `json:"reason,omitempty"`
}

// StopIdleResponse is the body of POST /servers/stop-idle.
type StopIdleResponse struct {
	Servers []StopIdleServerView `json:"servers"`
}

// PurgeResponse is the body of GET/POST /purge: the always-on idle-purge
// threshold in seconds (0 = off).
type PurgeResponse struct {
	GraceSeconds int `json:"grace_seconds"`
}

// CPUSlotRequest is the body of POST /cpu/slots: which run is asking, and for
// what unit of work.
type CPUSlotRequest struct {
	// Group is the equity unit — every request sharing a group shares one
	// fair share. Conventionally "<workspace>:vitest".
	Group string `json:"group"`
	// Label names the unit of work (a test file). Diagnostics only.
	Label string `json:"label,omitempty"`
	// Weight is reserved for a future weighted policy and is ignored in v1.
	Weight int `json:"weight,omitempty"`
}

// CPUSlotGrant is what POST /cpu/slots streams once the slot is granted. The
// connection then stays open: it *is* the slot, and closing it releases.
type CPUSlotGrant struct {
	Granted  bool `json:"granted"`
	WaitedMS int  `json:"waited_ms"`
	// Degraded reports a slot handed out without real arbitration (the
	// daemon's queue was at its ceiling). The work runs regardless.
	Degraded bool `json:"degraded"`
}

// CPUSlotGroupView is one group's line in CPUSlotsResponse.
type CPUSlotGroupView struct {
	Group   string `json:"group"`
	Held    int    `json:"held"`
	Waiting int    `json:"waiting"`
	Share   int    `json:"share"`
}

// CPUSlotsResponse is the body of GET /cpu/slots: the arbiter's whole state.
// Budget 0 means the arbiter is disabled (-cpu-slots=0).
type CPUSlotsResponse struct {
	Budget  int                `json:"budget"`
	Held    int                `json:"held"`
	Waiting int                `json:"waiting"`
	Groups  []CPUSlotGroupView `json:"groups"`
}

// AgentSession represents one AI agent running inside a workspace.
type AgentSession struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	AgentKind   string `json:"agent_kind"`
	// Intent is the prompt the session was spawned with. Surfaced per-agent
	// in the triage TUI when multiple agents share a workspace.
	Intent            string     `json:"intent,omitempty"`
	ProfileID         string     `json:"profile_id,omitempty"`
	State             AgentState `json:"state"`
	TmuxSessionName   string     `json:"tmux_session_name,omitempty"`
	StartedAt         time.Time  `json:"started_at"`
	LastEventAt       time.Time  `json:"last_event_at"`
	ExternalSessionID string     `json:"external_session_id,omitempty"`
	MessageCount      int        `json:"message_count"`
}

// Event is an SSE event emitted by the daemon.
type Event struct {
	ID        string                 `json:"id"`
	Type      EventType              `json:"type"`
	Timestamp time.Time              `json:"timestamp"`
	Data      map[string]interface{} `json:"data"`

	// Scope is daemon-side only, never on the wire: the globex/perso scope an
	// event belongs to, set by its publisher when the event carries
	// subject-layer data (message.* today). GET /events delivers such an
	// event to a subscriber only if that subscriber may read the scope; an
	// untagged one of those types reaches perso subscribers only.
	Scope string `json:"-"`
}

// Profile bundles auth + config for one Claude account / agent identity.
type Profile struct {
	ID           string            `json:"id" yaml:"id,omitempty"`
	Name         string            `json:"name" yaml:"name,omitempty"`
	AgentKind    string            `json:"agent_kind" yaml:"agent_kind,omitempty"`
	ConfigDir    string            `json:"config_dir,omitempty" yaml:"config_dir,omitempty"`
	EnvOverrides map[string]string `json:"env_overrides,omitempty" yaml:"env_overrides,omitempty"`
}

// Recipe is a templated set of dispatch parameters scoped to a project.
type Recipe struct {
	ID        string            `json:"id"`
	ProjectID string            `json:"project_id"`
	Name      string            `json:"name"`
	Backend   string            `json:"backend,omitempty"`
	Delivery  string            `json:"delivery,omitempty"`
	AgentKind string            `json:"agent_kind,omitempty"`
	PlanFirst bool              `json:"plan_first"`
	ProfileID string            `json:"profile_id,omitempty"`
	Defaults  map[string]string `json:"defaults,omitempty"`
}

// ExternalRef identifies a delivery in an external system (e.g. GitHub PR).
type ExternalRef struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

// Reviewer is a requested reviewer on a delivery.
type Reviewer struct {
	Login string `json:"login"`
	State string `json:"state"`
}

// DeliveryComment is one review comment on a delivery.
type DeliveryComment struct {
	ID       string `json:"id"`
	Author   string `json:"author"`
	Resolved bool   `json:"resolved"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Body     string `json:"body"`
}

// Delivery represents a handoff artifact (e.g. a GitHub PR).
type Delivery struct {
	ID                 string            `json:"id"`
	WorkspaceID        string            `json:"workspace_id"`
	Kind               string            `json:"kind"`
	ExternalRef        *ExternalRef      `json:"external_ref,omitempty"`
	URL                string            `json:"url,omitempty"`
	State              string            `json:"state"`
	CIStatus           string            `json:"ci_status"`
	Reviewers          []Reviewer        `json:"reviewers,omitempty"`
	Comments           []DeliveryComment `json:"comments,omitempty"`
	UnresolvedComments int               `json:"unresolved_comments"`
	Mergeable          bool              `json:"mergeable"`
	AutoMerge          bool              `json:"auto_merge"`
	LastSyncedAt       time.Time         `json:"last_synced_at"`
}

// BacklogItem is an idea captured before being dispatched.
type BacklogItem struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	Title        string    `json:"title"`
	Description  string    `json:"description,omitempty"`
	Tags         []string  `json:"tags,omitempty"`
	SizeEstimate string    `json:"size_estimate,omitempty"`
	Area         []string  `json:"area,omitempty"`
	Priority     int       `json:"priority"`
	State        string    `json:"state"`
	CreatedAt    time.Time `json:"created_at"`
}

// TriageSummary is the compact summary shown in the triage view.
type TriageSummary struct {
	Intent     string `json:"intent"`
	AgentState string `json:"agent_state"`
	Diff       string `json:"diff"`
}

// TriageItem is a server-computed item for the triage list.
type TriageItem struct {
	WorkspaceID   string        `json:"workspace_id"`
	Category      string        `json:"category"`
	Reason        string        `json:"reason"`
	PriorityScore float64       `json:"priority_score"`
	AgeSeconds    int64         `json:"age_seconds"`
	Summary       TriageSummary `json:"summary"`
}

// DispatchRequest is the body of POST /dispatch.
type DispatchRequest struct {
	ProjectID      string `json:"project_id"`
	Intent         string `json:"intent"`
	RecipeID       string `json:"recipe_id,omitempty"`
	ProfileID      string `json:"profile_id,omitempty"`
	AgentKind      string `json:"agent_kind,omitempty"`
	BranchOverride string `json:"branch_override,omitempty"`
	// BaseRef overrides the ref the new branch starts from. Empty falls back
	// to the project's defaults.base_ref (then "main").
	BaseRef string `json:"base_ref,omitempty"`
	// NoFetch skips the auto-fetch of the base ref's remote before branching.
	NoFetch bool `json:"no_fetch,omitempty"`

	// PlanContent is the raw bytes of a reviewed spec/plan to hand the agent.
	// When set, the daemon writes it into the workspace and the agent is told
	// to execute it instead of reinterpreting the intent.
	PlanContent string `json:"plan_content,omitempty"`
	// PlanFilename is the basename used when writing PlanContent into the
	// workspace (under .pigtail/). Sanitised by the daemon.
	PlanFilename string `json:"plan_filename,omitempty"`
	// PlanFirst forces (true) or forbids (false) plan mode. nil = auto:
	// plan mode unless a PlanContent is provided.
	PlanFirst *bool `json:"plan_first,omitempty"`
	// Force bypasses the anti-collision guard.
	Force bool `json:"force,omitempty"`
	// OverMargin launches even when every account of the profile's scope is
	// over the usage margin (decision forced).
	OverMargin bool `json:"over_margin,omitempty"`
}

// NewWorkspaceRequest is the body of POST /workspaces/new: create a worktree
// (and ports) without spawning any agent.
type NewWorkspaceRequest struct {
	ProjectID      string `json:"project_id"`
	Intent         string `json:"intent,omitempty"`
	BranchOverride string `json:"branch_override,omitempty"`
	RecipeID       string `json:"recipe_id,omitempty"`
	// BaseRef overrides the ref the new branch starts from. Empty falls back
	// to the project's defaults.base_ref (then "main").
	BaseRef string `json:"base_ref,omitempty"`
	// NoFetch skips the auto-fetch of the base ref's remote before branching.
	NoFetch bool `json:"no_fetch,omitempty"`
	// Force skips the pre-existence collision guard (branch/worktree already exists).
	Force bool `json:"force,omitempty"`
}

// DispatchResponse is the 201 response body of POST /dispatch.
type DispatchResponse struct {
	WorkspaceID    string `json:"workspace_id"`
	AgentSessionID string `json:"agent_session_id"`
	// Margin is the launch margin guard's decision when it is not ok
	// (switched, forced, unmeasured); absent otherwise.
	Margin *LaunchMargin `json:"margin,omitempty"`
}

// LaunchMargin is the launch margin guard's decision on a dispatch or a run
// : Decision is switched|forced|unmeasured, Requested
// and Final the profile's cell and the cell launched on (ids only), Warning
// the line the CLI prints (bounded: cell ids and release time only).
type LaunchMargin struct {
	Decision  string `json:"decision"`
	Requested string `json:"requested,omitempty"`
	Final     string `json:"final,omitempty"`
	Warning   string `json:"warning,omitempty"`
}

// RunRequest is the body of POST /workspaces/{id}/run: spawn an additional
// agent session inside an existing workspace without creating a worktree.
type RunRequest struct {
	Intent       string `json:"intent"`
	ProfileID    string `json:"profile_id,omitempty"`
	AgentKind    string `json:"agent_kind,omitempty"`
	PlanContent  string `json:"plan_content,omitempty"`
	PlanFilename string `json:"plan_filename,omitempty"`
	PlanFirst    *bool  `json:"plan_first,omitempty"`
	// OverMargin launches even when every account of the profile's scope is
	// over the usage margin (decision forced).
	OverMargin bool `json:"over_margin,omitempty"`
}

// RunResponse is the 201 response body of POST /workspaces/{id}/run.
type RunResponse struct {
	WorkspaceID    string        `json:"workspace_id"`
	AgentSessionID string        `json:"agent_session_id"`
	Margin         *LaunchMargin `json:"margin,omitempty"`
}

// RenameRequest is the body of POST /workspaces/{id}/rename. NewSlug is the
// new directory name under the project root (a single path segment).
type RenameRequest struct {
	NewSlug      string `json:"new_slug"`
	Force        bool   `json:"force,omitempty"`
	RenameBranch bool   `json:"rename_branch,omitempty"`
}

// RenameResponse is the 200 response body of POST /workspaces/{id}/rename.
type RenameResponse struct {
	WorkspaceID string `json:"workspace_id"`
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	NewBranch   string `json:"new_branch,omitempty"`
}

// Modes of PATCH /workspaces/{id}/ports.
const (
	PortsModeResync   = "resync"   // registry ← .env.local
	PortsModeReassign = "reassign" // fresh port → .env.local → registry
)

// PatchPortsRequest is the body of PATCH /workspaces/{id}/ports.
type PatchPortsRequest struct {
	Mode  string   `json:"mode"`            // "resync" | "reassign"
	Kinds []string `json:"kinds,omitempty"` // reassign only; empty = every kind of the project
}

// PortChange is one kind whose port the route changed.
type PortChange struct {
	Kind string `json:"kind"`
	From int    `json:"from"`
	To   int    `json:"to"`
}

// PatchPortsResponse is the 200 response body of PATCH /workspaces/{id}/ports.
type PatchPortsResponse struct {
	WorkspaceID string         `json:"workspace_id"`
	Ports       map[string]int `json:"ports"`
	Changed     []PortChange   `json:"changed"` // empty when nothing differed
}

// GCRequest is the body of POST /workspaces/gc.
type GCRequest struct {
	DryRun bool `json:"dry_run,omitempty"`
}

// GCResponse is the 200 response body of POST /workspaces/gc. DeletedIDs
// lists the workspaces removed (or, in dry-run mode, the candidates).
type GCResponse struct {
	DeletedIDs []string `json:"deleted_ids"`
	DryRun     bool     `json:"dry_run"`
}

// APIError is the error shape returned by the daemon on failure.
type APIError struct {
	Code    ErrorCode              `json:"code"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details,omitempty"`
}

func (e *APIError) Error() string { return string(e.Code) + ": " + e.Message }

// Response is the standard envelope for successful API responses.
type Response[T any] struct {
	Data T        `json:"data"`
	Meta MetaInfo `json:"meta"`
}

// ErrorResponse is the standard envelope for error API responses.
type ErrorResponse struct {
	Error APIError `json:"error"`
	Meta  MetaInfo `json:"meta"`
}

// MetaInfo carries request-level metadata in every response.
type MetaInfo struct {
	RequestID  string `json:"request_id"`
	TookMs     int64  `json:"took_ms,omitempty"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// PreflightGit describes the git state of a workspace for the preflight check.
type PreflightGit struct {
	Clean  bool `json:"clean"`
	Ahead  int  `json:"ahead"`
	Behind int  `json:"behind"`

	// Merged is the narrow, historical answer: the branch tip is an ancestor
	// of the base ref. It misses every squash-merge. Prefer MergeStatus.
	Merged bool `json:"merged"`

	// MergeStatus is the three-valued containment verdict: "contained" (the
	// work is provably in the base ref), "absent" (every check ran and found
	// nothing) or "unknown" (the question could not be answered). Empty when
	// the backend offers no containment check at all.
	//
	// Three values, not two, so a caller sweeping for merged workspaces can
	// report "we could not classify N of them" instead of silently counting
	// them as unmerged (issue #54).
	MergeStatus string `json:"merge_status,omitempty"`

	// MergedHow names the detection path when MergeStatus is "contained":
	// "merged" (ancestor), "squashed" (patch-equivalent commits) or
	// "squash-merged" (net change carried by one base commit).
	MergedHow string `json:"merged_how,omitempty"`

	// MergeBaseRef is the ref the containment check actually compared
	// against, which may be fresher than the workspace's recorded base.
	MergeBaseRef string `json:"merge_base_ref,omitempty"`

	// MergeNote is the evidence when contained, or the reason when unknown.
	MergeNote string `json:"merge_note,omitempty"`

	ModifiedFiles []string `json:"modified_files,omitempty"`
	Commits       []string `json:"commits,omitempty"`
}

// PreflightDelivery describes the delivery (PR) state for the preflight check.
type PreflightDelivery struct {
	URL      string `json:"url"`
	State    string `json:"state"`
	CIStatus string `json:"ci_status"`
}

// PreflightResponse is the response of GET /workspaces/{id}/preflight.
type PreflightResponse struct {
	WorkspaceID string             `json:"workspace_id"`
	Branch      string             `json:"branch"`
	Git         PreflightGit       `json:"git"`
	Delivery    *PreflightDelivery `json:"delivery,omitempty"`
	Verdict     string             `json:"verdict"`
}

// SubjectStatuses is the subject status vocabulary, in lifecycle order:
// the only values the CLI, the daemon and the database accept.
var SubjectStatuses = []string{
	StatusInbox, StatusUnderstanding, StatusDesigning, StatusBuilding,
	StatusDone, StatusParked, StatusDropped, StatusCaptured,
}

// ValidateSubjectStatus returns nil when s is one of SubjectStatuses, else the
// refusal every writer shows.
func ValidateSubjectStatus(s string) error {
	for _, v := range SubjectStatuses {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("unknown status %q: use one of %s", s, strings.Join(SubjectStatuses, ", "))
}

// IsClosedStatus reports whether a subject in status s is closed: out of
// `subject next`, the keyword suggestions and the board's active columns.
func IsClosedStatus(s string) bool {
	return s == StatusDone || s == StatusParked || s == StatusDropped || s == StatusCaptured
}

// SatisfiesDeps reports whether a subject in status s unblocks the subjects
// that depend on it: done, captured, and dropped (an abandoned subject no longer holds anything). Closed is not
// enough: a parked subject still blocks.
func SatisfiesDeps(s string) bool {
	return s == StatusDone || s == StatusCaptured || s == StatusDropped
}

// Subject status vocabulary: the subject's maturity, independent of the
// workflow step. SubjectStatuses lists them in lifecycle order.
const (
	StatusInbox         = "inbox"
	StatusUnderstanding = "understanding"
	StatusDesigning     = "designing"
	StatusBuilding      = "building"
	StatusDone          = "done"
	StatusParked        = "parked"
	StatusDropped       = "dropped"  // abandoned: closed
	StatusCaptured      = "captured" // converted into a capture: closed, satisfies dependencies

	// Subject source discriminator: local subjects have no Globex card;
	// board subjects mirror an ingested board card.
	SourceLocal = "local"
	SourceBoard = "board"
)

// Phase kinds and states.
const (
	PhaseResearch  = "research"
	PhaseSpec      = "spec"
	PhaseImplement = "implement"
	PhaseCustom    = "custom"

	PhasePending    = "pending"
	PhaseDispatched = "dispatched"
	PhaseActive     = "active"
	PhaseDone       = "done"
)

// Subject log entry kinds.
const (
	LogIdea        = "idea"
	LogNote        = "note"
	LogProgress    = "progress"
	LogStatus      = "status"
	LogPhase       = "phase"
	LogDelivery    = "delivery"
	LogHandoff     = "handoff"
	LogHandoffAuto = "handoff-auto"
)

// Theme kinds. A theme is a project in the backlog taxonomy; "ongoing" is a
// continuous project (formerly kind "area").
const (
	ThemeKindProject = "project" // a project: an outcome, it closes
	ThemeKindOngoing = "ongoing" // a continuous project: never closes
)

// ValidThemeDeadline checks a theme deadline: "" (none) or exactly YYYY-MM-DD,
// a real calendar date. Shared by the daemon (the authority) and the CLI.
func ValidThemeDeadline(s string) error {
	if s == "" {
		return nil
	}
	if len(s) == len(time.DateOnly) {
		if _, err := time.Parse(time.DateOnly, s); err == nil {
			return nil
		}
	}
	return fmt.Errorf("deadline must be a date YYYY-MM-DD, or empty to clear (got %q)", s)
}

// NormalizeThemeKind maps the historical "area" input to "ongoing"; ok=false for anything else.
func NormalizeThemeKind(k string) (string, bool) {
	switch k {
	case ThemeKindProject, ThemeKindOngoing:
		return k, true
	case "area":
		return ThemeKindOngoing, true
	}
	return "", false
}

// Theme is a handle onto an existing vault project (a continuous one when
// Kind is "ongoing"). It is a grouping of subjects, distinct from a pigtail
// Project (a code repo).
type Theme struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"` // "project" | "ongoing" (historical "area" is normalized)
	VaultLink     string `json:"vault_link"`
	CodeProjectID string `json:"code_project_id,omitempty"`
	Archived      bool   `json:"archived"`
	// Scope is a declared scope name (e.g. "globex" or "perso"), derived by the daemon from VaultLink's
	// vault-root prefix at creation. Read-only: it never changes.
	Scope string `json:"scope,omitempty"`
	// Space is the cmux sidebar folder of the theme's subjects' sessions
	// (spaces.yaml); "" = none yet (« Inbox »).
	Space string `json:"space,omitempty"`
	// Deadline: "" = none; else YYYY-MM-DD, checked by ValidThemeDeadline.
	Deadline string `json:"deadline"`
	// Carried: a project the user carries himself.
	Carried   bool      `json:"carried"`
	CreatedAt time.Time `json:"created_at"`
}

// Space is one row of the space table (GET /spaces).
type Space struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
}

// ThemeUpdate is a partial PATCH /themes/{id}: a nil field is left unchanged.
type ThemeUpdate struct {
	Name, VaultLink, Space, Deadline *string
	Carried                          *bool
}

// Capture is a lightweight idea filed under a project (theme), not yet a
// subject (A3). It may replace a subject (FromSubjectID) and is closed by
// promotion into a subject. Not a screenshot.
type Capture struct {
	ID                string     `json:"id"`
	ThemeID           string     `json:"theme_id"`
	Text              string     `json:"text"`
	FromSubjectID     string     `json:"from_subject_id,omitempty"`
	FromPrevStatus    string     `json:"from_prev_status,omitempty"`
	PromotedSubjectID string     `json:"promoted_subject_id,omitempty"`
	Scope             string     `json:"scope"` // derived from the theme (JOIN), never stored
	CreatedAt         time.Time  `json:"created_at"`
	ClosedAt          *time.Time `json:"closed_at,omitempty"`
}

// Subject is the durable, prioritisable, dispatchable unit of work.
// Impact/Effort/Urgency/Rank use 0 to mean "unset" (stored as SQL NULL).
type Subject struct {
	ID            string   `json:"id"`
	ThemeID       string   `json:"theme_id"`
	Title         string   `json:"title"`
	Slug          string   `json:"slug"`
	Status        string   `json:"status"`
	Impact        int      `json:"impact,omitempty"`
	Effort        int      `json:"effort,omitempty"`
	Urgency       int      `json:"urgency,omitempty"`
	Rank          int      `json:"rank,omitempty"`
	Deps          []string `json:"deps,omitempty"`
	VaultPath     string   `json:"vault_path,omitempty"`
	CodeProjectID string   `json:"code_project_id,omitempty"`
	Source        string   `json:"source"`             // "local" | "board"
	ShortID       string   `json:"short_id,omitempty"` // board card shortId (ABC-…) when source=board
	BoardID       string   `json:"board_id,omitempty"`
	Section       string   `json:"section,omitempty"`
	BoardCard     string   `json:"board_card,omitempty"` // linked board card shortId (ABC-…), any source
	// ShortTitle is the optional label shown where Title does not fit (the
	// cmux sidebar). Empty = derived from Title; read it through ShortTitle().
	ShortTitle string `json:"short_title,omitempty"`
	// Scope is the theme's scope ("gx" | "perso"), joined in on read.
	// Read-only: a subject changes scope only by changing theme, which the
	// daemon refuses across scopes.
	Scope string `json:"scope,omitempty"`
	// Space is the subject's own cmux sidebar space (migration 0033), written by a hand drag of one of its sessions into
	// a space folder; it beats its theme's space. "" = its theme's. Read-only
	// on the wire: no route writes it but the drag and the theme-change clear.
	Space string `json:"space,omitempty"`
	// CmuxWorkspaces lists the cmux workspaces bound to the subject (UUIDs,
	// subject_cmux_bindings). Filled by GET /subjects/:id only; read-only —
	// the link is written through /subjects/:id/cmux-workspaces.
	CmuxWorkspaces []string `json:"cmux_workspaces,omitempty"`
	// OutputsCount and LastOutputTS (epoch ms) sum up the subject's
	// non-purged outputs (subject_outputs). Filled by GET /subjects/:id only;
	// LastOutputTS only when OutputsCount > 0.
	OutputsCount *int   `json:"outputs_count,omitempty"`
	LastOutputTS *int64 `json:"last_output_ts,omitempty"`
	// BlockedBy lists the dependency ids SatisfiesDeps does not satisfy (an
	// unknown id included), [] when none. Filled by GET /subjects/:id
	// only, always pointing at a non-nil slice there; nil (omitted) elsewhere.
	BlockedBy *[]string `json:"blocked_by,omitempty"`
	// BlockedByAll, Blocks and BlocksAll are computed, read-only, GET
	// /subjects/:id only (non-nil there, [] when none; no column): every
	// subject blocking this one transitively, the unsatisfied subjects of the
	// same scope naming it in their deps, and those transitively.
	BlockedByAll *[]string `json:"blocked_by_all,omitempty"`
	Blocks       *[]string `json:"blocks,omitempty"`
	BlocksAll    *[]string `json:"blocks_all,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// SubjectPhase links a subject phase to the workspace executing it.
type SubjectPhase struct {
	ID          string    `json:"id"`
	SubjectID   string    `json:"subject_id"`
	Kind        string    `json:"kind"`  // research|spec|implement|custom
	State       string    `json:"state"` // pending|dispatched|active|done
	WorkspaceID string    `json:"workspace_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// SubjectLogEntry is one timestamped line in a subject's activity log; the
// reporting layer reads these.
type SubjectLogEntry struct {
	ID        string    `json:"id"`
	SubjectID string    `json:"subject_id"`
	Timestamp time.Time `json:"ts"`
	Kind      string    `json:"kind"` // idea|note|progress|status|phase|delivery|handoff
	Text      string    `json:"text"`
}

// SuggestRequest is the body of POST /subjects/suggest: what a pane brings to
// find the subject it works on. At least one of Text, Cwd and Branch is set.
type SuggestRequest struct {
	Text          string `json:"text"`
	Cwd           string `json:"cwd,omitempty"`
	Branch        string `json:"branch,omitempty"`
	DefaultBranch string `json:"default_branch,omitempty"`
	Repo          string `json:"repo,omitempty"`
	// WorkspaceID is the pane's cmux workspace: its off-topic reason, if any,
	// is added to Text and read for refusals (« <sub_id> refusé »).
	WorkspaceID string `json:"workspace_id,omitempty"`
	Limit       int    `json:"limit,omitempty"` // 1..10, default 3
}

// SuggestCandidate is one ranked subject the caller may read.
type SuggestCandidate struct {
	SubjectID  string `json:"subject_id"`
	ShortTitle string `json:"short_title"`
	Title      string `json:"title"`
	Scope      string `json:"scope"`
	Status     string `json:"status"`
	Signal     string `json:"signal"`   // pr | worktree | keyword
	Strength   string `json:"strength"` // strong | weak
	Evidence   string `json:"evidence"` // internal, never copied into the announcement
	PR         int    `json:"pr,omitempty"`
	Writable   bool   `json:"writable"` // false: readable but of another scope (relaunch in the right cell)
}

// SuggestElsewhere counts the candidates of a scope the caller may not read,
// naming none of them.
type SuggestElsewhere struct {
	Strong int `json:"strong"`
	Weak   int `json:"weak"`
}

// SuggestResult answers POST /subjects/suggest.
type SuggestResult struct {
	Candidates []SuggestCandidate `json:"candidates"` // never null
	// Auto is the id of the strong candidate when exactly one non-refused
	// strong candidate exists across all scopes and it is writable; "" otherwise.
	Auto      string           `json:"auto"`
	Elsewhere SuggestElsewhere `json:"elsewhere"`
}
