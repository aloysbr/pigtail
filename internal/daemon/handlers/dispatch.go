// Package handlers contains HTTP handler implementations for the daemon endpoints.
package handlers

import (
	"encoding/json"
	"errors"
	"github.com/aloysbr/pigtail/internal/usersettings"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/agent"
	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/daemon/activity"
	"github.com/aloysbr/pigtail/internal/daemon/admission"
	"github.com/aloysbr/pigtail/internal/daemon/cpuslots"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/focuspri"
	"github.com/aloysbr/pigtail/internal/daemon/rammon"
	"github.com/aloysbr/pigtail/internal/daemon/sessbudget"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/devserver"
	"github.com/aloysbr/pigtail/internal/gates"
	"github.com/aloysbr/pigtail/internal/launchmargin"
	"github.com/aloysbr/pigtail/internal/mainfresh"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/ports"
	"github.com/aloysbr/pigtail/internal/runtimeclaim"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/internal/spawnreg"
	"github.com/aloysbr/pigtail/internal/staticsrv"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// FocusResolver is the part of focuspri.Resolver the admission route needs.
type FocusResolver interface{ IsFocus(focuspri.Caller) bool }

// Services bundles the dependencies handlers need. The daemon constructs
// one Services at startup and passes it into the router.
type Services struct {
	Store *store.Store
	// HumanName is how agents see the human (settings.yaml human.name);
	// empty = usersettings.DefaultHumanName.
	HumanName string
	// RoleSnapshot is the role sweep's last verdicts and whether the watch is
	// on (GET /roles/status). nil = the watch is off.
	RoleSnapshot func() ([]pigtail.RoleStatus, bool)
	// OutputsURLs returns, once per request, the served_url of a path: the
	// outputs server's URL when its own index opens it, "" otherwise. nil, or
	// a nil result, = no served_url.
	OutputsURLs func() func(path string) string
	Bus         *events.Bus
	Backend     backend.Backend
	Agent       agent.Agent
	Watcher     *activity.Watcher
	// Spaces is the space table (spaces.yaml): the sidebar folders a theme
	// may name. nil = none declared (every non-empty space is refused).
	Spaces *space.Table

	// CreateWorkspace persists a created workspace. nil = Store.CreateWorkspace;
	// tests use it to observe the allocator at the instant of persistence.
	CreateWorkspace func(*pigtail.Workspace) error
	// ListAgentSessions lists a workspace's agent sessions for the dispatch
	// anti-collision guard. nil = Store.ListAgentSessionsByWorkspace.
	ListAgentSessions func(workspaceID string) ([]pigtail.AgentSession, error)

	// PortBusy, ProcessCwds and KillPIDs are the listener probes and the kill
	// the handlers use (rename --force, the ports route). nil =
	// devserver.IsPortBusy / devserver.ProcessCwds / devserver.KillPIDs.
	PortBusy    func(port int) (bool, []int, error)
	ProcessCwds func(pids []int) (map[int]string, error)
	KillPIDs    func(pids []int)
	// ProcessParents (pid → ppid), ProcessAlive and ServerStopGrace are the
	// seams of the dev-server stop that DELETE /workspaces/:id runs first.
	// nil/0 = devserver.ProcessParents / devserver.ProcessAlive /
	// devserver.DefaultStopGrace.
	ProcessParents  func() (map[int]int, error)
	ProcessAlive    func(pid int) bool
	ServerStopGrace time.Duration
	// UpdateWorkspacePorts records a workspace's new ports (ports route).
	// nil = Store.UpdateWorkspacePorts; tests make it fail.
	UpdateWorkspacePorts func(id string, ports map[string]int) error

	// Ports allocates daemon-global ports (used by static servers). May be nil
	// in tests that don't exercise static-server creation.
	Ports *ports.Allocator
	// StaticMgr owns the in-daemon file servers. May be nil in such tests.
	StaticMgr *staticsrv.Manager
	// StaticPortRange is the daemon-global range static servers allocate from.
	StaticPortRange pigtail.PortRange

	// RAMMonitor enumerates running dev servers for the read-only /servers
	// endpoint. Always wired by the daemon (even when eviction is disabled);
	// may be nil in tests that don't exercise /servers.
	RAMMonitor *rammon.RAMMonitor

	// Lid is the lid sleep-control manager (pigtail lid). nil = not wired
	// (not macOS, or a test server): the /lid routes answer 501.
	Lid LidService

	// CPUSlots arbitrates the machine-wide CPU slot budget for the /cpu/slots
	// endpoints. Always wired by the daemon (even at budget 0, where it
	// answers "disabled"); may be nil in tests that don't exercise them.
	CPUSlots *cpuslots.Arbiter
	// Admission holds heavy commands back while memory is short, for the
	// /admission endpoints. nil answers "disabled".
	Admission *admission.Controller
	// SessionSampler measures the live and busy sessions; nil when off.
	SessionSampler *sessbudget.Sampler
	// SessionBudget grants launch slots from the samples (always set by the
	// daemon; its gate is off when the ceiling is 0).
	SessionBudget *sessbudget.Controller
	// FocusPri says whether an admission caller is in focus, so its request
	// is considered before the others. nil = no priority. It answers from
	// memory and never errors (internal/daemon/focuspri).
	FocusPri FocusResolver
	// Gates reads the workflow gates (GET /gates). nil = not configured.
	Gates *gates.Source
	// UsageOverview builds GET /usage/overview (the Claude accounts, for
	// Pigtail). nil = not wired: 501.
	UsageOverview func(now time.Time) (pigtail.UsageOverview, error)
	// CmuxCheckNotice reads the end-of-pass notice of the running cmux launch
	// for GET /cmux/check/notice (D2, sub_x000000000000037). nil = none.
	CmuxCheckNotice func() (pigtail.CmuxCheckNotice, error)

	// MainFresh holds the latest main-worktree verdict per project, written by
	// the daemon's refresh sweep and by every create path, read by /servers.
	// Nil-safe: a nil registry makes both sides no-ops.
	MainFresh *mainfresh.Registry

	// Offsite triggers an immediate offsite backup run for POST
	// /backup/offsite/run (offsite_run.go). Nil in tests that don't exercise
	// that route — RunOffsiteBackup treats a nil Offsite the same as a nil
	// Store: nothing started, no panic.
	Offsite OffsiteRunner
	// OffsiteDestDir / OffsiteFallbackDest mirror the daemon's own
	// cfg.DBOffsiteDir / cfg.DBOffsiteFallbackDest, wired through by name
	// rather than the whole Config: this package must not import daemon's
	// own Config type (that would be the import cycle Services exists to
	// avoid), and destDir/fallbackDest are the only two fields
	// RunOffsiteBackup needs from it.
	OffsiteDestDir      string
	OffsiteFallbackDest string

	// CmuxSidebar is the cmux sidebar loop (cmuxsync.Runner): GET/POST
	// /cmux/sidebar, retitle, and the link routes nudge it. Nil = no loop.
	CmuxSidebar CmuxSidebarControl

	// RespawnNotifier tells a respawn run's session that it gave up or
	// failed (relay_respawn.go). Nil = no notice: the route logs it.
	RespawnNotifier RespawnNotifier

	// Maintenance reads the system maintenance state (maintenance.json next
	// to the socket, or $PIGTAIL_MAINTENANCE_DIR): GET /maintenance reports
	// it. Nil in tests that don't exercise it.
	Maintenance *maintenance.Watch

	// RuntimeClaimDir holds runtime-claim.json (same directory as the
	// maintenance state): /runtime/* read and write it. "" = not wired.
	RuntimeClaimDir string
	// Alive says whether a claim holder's Claude process still hosts its
	// session (claudesession.SessionAlive in the daemon).
	Alive runtimeclaim.AliveFunc

	// MarginDeps builds the launch margin guard's deps for one dispatch or
	// run (sub_x000000000000011); nil = the real ones (marginDeps).
	MarginDeps func() launchmargin.Deps
	// MarginSpawns is where the guard's decisions are traced; nil = the
	// spawn registry's default path.
	MarginSpawns *spawnreg.Registry

	// Launch is the session launcher (POST /subjects/{id}/launch, GET
	// /launches/{id}); nil = launcher off (503).
	Launch SessionLauncher
	// Staffing is the staffing scheduler (GET/POST /staffing, GET
	// /staffing/decisions); nil = staffing off (503).
	Staffing StaffingService
	// SubjectsChanged asks the scheduler for a tour after a subject write
	// that publishes no event (creation, rank, status, phase, promotion); nil = none.
	SubjectsChanged func()

	// SDK holds the SDK sessions (nil: the option is off, every route 503s).
	SDK *SDKService
	// SDKOffReason says why SDK is nil when pigtaild was asked for it (the 503 SDK_OFF message).
	SDKOffReason string

	// Logf is the diagnostic sink. Nil means log.Printf — see logf().
	Logf func(format string, args ...any)

	// SyncWorkspaceMarkers rewrites the workspace-subject markers (wsmarker) after a link or off-topic write; nil = none.
	SyncWorkspaceMarkers func()
	// Hooks runs post-create hooks on the daemon's lifetime; nil = context.Background(), untracked (handler tests).
	Hooks *HookRunner

	// cleanups serialises workspace teardowns. Zero value ready to use;
	// see cleanup_inflight.go.
	cleanups cleanupInflight
}

// Responder is the contract handlers use to write JSON/error responses.
// The daemon package owns the concrete impl; we keep it abstract here so
// the handlers don't depend on daemon internals.
type Responder interface {
	JSON(w http.ResponseWriter, r *http.Request, status int, data interface{})
	Error(w http.ResponseWriter, r *http.Request, status int, code pigtail.ErrorCode, msg string, details map[string]interface{})
}

// Dispatch handles POST /dispatch:
//  1. validate body
//  2. load project
//  3. Backend.Create  → Workspace
//  4. persist + emit workspace.created
//  5. Agent.Spawn     → AgentSession
//  6. persist + emit agent.started
//  7. return { workspace_id, agent_session_id }
func Dispatch(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.DispatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if req.ProjectID == "" || req.Intent == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "project_id and intent are required", nil)
			return
		}

		proj, err := svc.Store.GetProject(req.ProjectID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrProjectNotFound, "unknown project_id", map[string]interface{}{"project_id": req.ProjectID})
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}

		// The profile and the launch margin guard come first: a refusal
		// creates no workspace, no worktree, runs no hook.
		lp, ok := resolveLaunchProfile(w, r, resp, proj, req.ProfileID)
		if !ok {
			return
		}
		if err := guardLaunch(svc, &lp, req.OverMargin); err != nil {
			refuseOverMargin(w, r, resp, err)
			return
		}

		ctx := r.Context()
		ws, err := svc.Backend.Create(ctx, proj, backend.CreateIntent{
			Intent:         req.Intent,
			BranchOverride: req.BranchOverride,
			BaseRef:        req.BaseRef,
			NoFetch:        req.NoFetch,
			RecipeID:       req.RecipeID,
			// Dispatch has no field to carry a note back (its response is two
			// ids), but the verdict is a real measurement of the main checkout
			// and belongs in the registry `pigtail servers` reads.
			OnMainRefresh: onMainRefresh(svc, proj, nil),
		})
		if err != nil {
			var apiErr *pigtail.APIError
			if errors.As(err, &apiErr) {
				resp.Error(w, r, statusForCode(apiErr.Code), apiErr.Code, apiErr.Message, apiErr.Details)
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrBackendError, err.Error(), nil)
			return
		}
		persisted := false
		defer func() { settlePorts(svc, ws, persisted) }()
		// --- anti-collision guard (needs ws.ID, before CreateWorkspace) ---
		// Its 500 and its 409 release the ports through the deferred settlePorts.
		if !req.Force {
			sessions, lerr := listAgentSessions(svc, ws.ID)
			if lerr != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "list sessions: "+lerr.Error(), nil)
				return
			}
			for _, s := range sessions {
				if isLiveAgentState(s.State) {
					// NOTE: Backend.Create already made a worktree on disk; its
					// ports are released by the deferred settlePorts. We intentionally do NOT call Backend.Cleanup
					// here: if Create returned a pre-existing workspace (the very one
					// holding the live agent), cleaning up would destroy in-progress
					// work. Safely reclaiming a truly-orphaned worktree requires
					// knowing whether this call created it — deferred to a follow-up.
					resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict,
						"an agent is already active on this workspace; pass force to override",
						map[string]interface{}{"workspace_id": ws.ID, "agent_session_id": s.ID})
					return
				}
			}
		}

		if err := persistWorkspace(svc, ws); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "persist workspace: "+err.Error(), nil)
			return
		}
		persisted = true
		svc.Bus.Publish(pigtail.Event{
			Type: pigtail.EventWorkspaceCreated,
			Data: workspaceEvent(ws),
		})

		// Run post-create hooks (npm install, etc.) before spawning the agent.
		if cfg, err := pigtail.LoadProjectConfig(proj.RootPath); err == nil && len(cfg.Hooks) > 0 {
			select {
			case err := <-runPostCreateHooks(svc, ws, cfg.Hooks):
				if err != nil {
					resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrBackendError,
						"post-create hook: "+err.Error()+" (log: "+devserver.LogPath(ws.Path, "hooks")+")", nil)
					return
				}
			case <-r.Context().Done():
				// The client left. The hooks run on to their end on the
				// daemon's context; their result is recorded on the workspace.
				return
			}
		}

		// --- write reviewed plan into the workspace -------------------
		var planPath string
		if req.PlanContent != "" {
			pp, werr := writePlanFile(ws.Path, req.PlanFilename, req.PlanContent)
			if werr != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "write plan: "+werr.Error(), nil)
				return
			}
			planPath = pp
		}

		sess, ok := spawnAgentInto(w, r, svc, resp, proj, ws, spawnParams{
			Intent:    req.Intent,
			ProfileID: req.ProfileID,
			AgentKind: req.AgentKind,
			PlanFirst: resolvePlanFirst(&req),
			PlanPath:  planPath,
			Profile:   &lp,
		})
		if !ok {
			return
		}

		resp.JSON(w, r, http.StatusCreated, pigtail.DispatchResponse{
			WorkspaceID:    ws.ID,
			AgentSessionID: sess.ID,
			Margin:         lp.Margin,
		})
	}
}

// spawnParams bundles the already-resolved inputs spawnAgentInto needs.
// Intent is the prompt; ProfileID/AgentKind are the raw request values
// (defaults are applied inside the helper); PlanFirst/PlanPath are resolved
// by the caller.
type spawnParams struct {
	Intent    string
	ProfileID string
	AgentKind string
	PlanFirst bool
	PlanPath  string
	// Profile is the profile already resolved (and guarded) by the caller;
	// nil = resolve it here from ProfileID.
	Profile *launchProfile
}

// spawnAgentInto resolves the profile env, spawns an agent inside ws,
// persists the session, publishes agent.started, and starts the activity
// watcher. On any failure it writes the error response itself and returns
// ok=false. Shared by Dispatch (new workspace) and RunAgent (existing one).
func spawnAgentInto(w http.ResponseWriter, r *http.Request, svc *Services, resp Responder,
	proj *pigtail.Project, ws *pigtail.Workspace, p spawnParams) (*pigtail.AgentSession, bool) {
	// --- resolve profile env (source: pigtail.yaml) ---------------
	lp := p.Profile
	if lp == nil {
		resolved, ok := resolveLaunchProfile(w, r, resp, proj, p.ProfileID)
		if !ok {
			return nil, false
		}
		lp = &resolved
	}
	profileID, profileEnv := lp.ID, lp.Env

	agentKind := p.AgentKind
	if agentKind == "" && proj.Defaults != nil {
		agentKind = proj.Defaults.Agent
	}
	// Folder trust before Claude starts: inherited from a trusted ancestor in
	// the profile's cell, or nothing (reported in the daemon log).
	preTrustAgent(profileEnv, ws, svc.logf)
	sess, err := svc.Agent.Spawn(r.Context(), ws, agent.SpawnIntent{
		AgentKind: agentKind,
		Intent:    p.Intent,
		ProfileID: profileID,
		PlanFirst: p.PlanFirst,
		PlanPath:  p.PlanPath,
		ExtraEnv:  profileEnv,
	})
	if err != nil {
		var apiErr *pigtail.APIError
		if errors.As(err, &apiErr) {
			resp.Error(w, r, statusForCode(apiErr.Code), apiErr.Code, apiErr.Message, apiErr.Details)
			return nil, false
		}
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrAgentSpawnFailed, err.Error(), nil)
		return nil, false
	}
	// Record the spawn intent on the session so the TUI can show per-agent
	// intent excerpts. The tmux backend already sets this, but be defensive
	// for backends that don't.
	if sess.Intent == "" {
		sess.Intent = p.Intent
	}
	if err := svc.Store.CreateAgentSession(sess); err != nil {
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "persist agent session: "+err.Error(), nil)
		return nil, false
	}
	svc.Bus.Publish(pigtail.Event{
		Type: pigtail.EventAgentStarted,
		Data: agentEvent(sess),
	})
	if svc.Watcher != nil {
		svc.Watcher.Start(*sess)
	}
	return sess, true
}

// AdoptWorkspace handles POST /workspaces — registers a pre-existing
// worktree as a workspace without creating it or spawning an agent.
func AdoptWorkspace(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var ws pigtail.Workspace
		if err := json.NewDecoder(r.Body).Decode(&ws); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if ws.ID == "" || ws.ProjectID == "" || ws.Path == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "id, project_id, and path are required", nil)
			return
		}
		if err := svc.Store.CreateWorkspace(&ws); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "persist workspace: "+err.Error(), nil)
			return
		}
		svc.Bus.Publish(pigtail.Event{
			Type: pigtail.EventWorkspaceCreated,
			Data: workspaceEvent(&ws),
		})
		resp.JSON(w, r, http.StatusCreated, ws)
	}
}

// NewWorkspace handles POST /workspaces/new — create a worktree (and ports)
// without spawning an agent. Steps 1-4 of Dispatch, minus the agent spawn.
func NewWorkspace(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.NewWorkspaceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if req.ProjectID == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "project_id is required", nil)
			return
		}
		if req.Intent == "" && req.BranchOverride == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "intent or branch_override is required", nil)
			return
		}

		proj, err := svc.Store.GetProject(req.ProjectID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrProjectNotFound, "unknown project_id", map[string]interface{}{"project_id": req.ProjectID})
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}

		ctx := r.Context()
		var refreshed backend.MainRefresh
		ws, err := svc.Backend.Create(ctx, proj, backend.CreateIntent{
			Intent:         req.Intent,
			BranchOverride: req.BranchOverride,
			BaseRef:        req.BaseRef,
			NoFetch:        req.NoFetch,
			RecipeID:       req.RecipeID,
			Force:          req.Force,
			OnMainRefresh:  onMainRefresh(svc, proj, &refreshed),
		})
		if err != nil {
			var apiErr *pigtail.APIError
			if errors.As(err, &apiErr) {
				resp.Error(w, r, statusForCode(apiErr.Code), apiErr.Code, apiErr.Message, apiErr.Details)
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrBackendError, err.Error(), nil)
			return
		}
		persisted := false
		defer func() { settlePorts(svc, ws, persisted) }()
		if err := persistWorkspace(svc, ws); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "persist workspace: "+err.Error(), nil)
			return
		}
		persisted = true
		svc.Bus.Publish(pigtail.Event{Type: pigtail.EventWorkspaceCreated, Data: workspaceEvent(ws)})

		if cfg, err := pigtail.LoadProjectConfig(proj.RootPath); err == nil && len(cfg.Hooks) > 0 {
			select {
			case err := <-runPostCreateHooks(svc, ws, cfg.Hooks):
				if err != nil {
					resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrBackendError,
						"post-create hook: "+err.Error()+" (log: "+devserver.LogPath(ws.Path, "hooks")+")", nil)
					return
				}
			case <-r.Context().Done():
				// The client left. The hooks run on to their end on the
				// daemon's context; their result is recorded on the workspace.
				return
			}
		}

		// Response-only annotations, stamped on a COPY. The store caches the
		// very pointer it was handed (store.cacheWorkspace), so writing them
		// onto ws would leave them on the cached workspace — and a note saying
		// "main was 23 commits behind" would then be served, unchanged, by
		// every later GET of this workspace, long after it stopped being true.
		// The note describes an event during this create, not a property of
		// the thing created.
		out := *ws
		if svc.Backend != nil {
			out.DisplayRef = svc.Backend.DisplayRef(ws)
		}
		out.MainRefresh = mainRefreshNote(refreshed)
		resp.JSON(w, r, http.StatusCreated, out)
	}
}

// ListWorkspaces handles GET /workspaces.
func ListWorkspaces(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state := pigtail.WorkspaceState(r.URL.Query().Get("state"))
		out, err := svc.Store.ListWorkspaces(state)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		// Attach sessions and the backend-agnostic work-ref label so clients
		// never read BackendData (architecture.md §8).
		for i := range out {
			sessions, _ := svc.Store.ListAgentSessionsByWorkspace(out[i].ID)
			out[i].AgentSessions = sessions
			if svc.Backend != nil {
				out[i].DisplayRef = svc.Backend.DisplayRef(&out[i])
			}
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// GetWorkspace handles GET /workspaces/:id.
func GetWorkspace(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		ws, err := svc.Store.GetWorkspace(id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrWorkspaceNotFound, "no such workspace", map[string]interface{}{"id": id})
				return
			}
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		if svc.Backend != nil {
			ws.DisplayRef = svc.Backend.DisplayRef(ws)
		}
		resp.JSON(w, r, http.StatusOK, ws)
	}
}

// --- helpers --------------------------------------------------------------

// resolvePlanFirst implements the auto rule: an explicit PlanFirst wins;
// otherwise enable plan mode only when no reviewed plan was provided.
func resolvePlanFirst(req *pigtail.DispatchRequest) bool {
	if req.PlanFirst != nil {
		return *req.PlanFirst
	}
	return req.PlanContent == ""
}

// writePlanFile writes reviewed plan content into the workspace under
// .pigtail/<sanitised basename> and returns the workspace-relative path.
func writePlanFile(wsPath, filename, content string) (string, error) {
	base := filepath.Base(filename)
	if base == "" || base == "." || base == "/" {
		base = "plan.md"
	}
	dir := filepath.Join(wsPath, ".pigtail")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, base), []byte(content), 0o644); err != nil {
		return "", err
	}
	return filepath.Join(".pigtail", base), nil
}

// isLiveAgentState reports whether a session is still occupying the workspace
// (running or paused waiting on the user) vs terminal (done/killed/crashed).
func isLiveAgentState(s pigtail.AgentState) bool {
	switch s {
	case pigtail.AgentStateRunning, pigtail.AgentStatePausedInput, pigtail.AgentStatePausedPlan:
		return true
	default:
		return false
	}
}

func statusForCode(c pigtail.ErrorCode) int {
	switch c {
	case pigtail.ErrInvalidRequest, pigtail.ErrInvalidRecipe, pigtail.ErrInvalidProfile:
		return http.StatusBadRequest
	case pigtail.ErrUnauthorized:
		return http.StatusUnauthorized
	case pigtail.ErrNotFound, pigtail.ErrWorkspaceNotFound, pigtail.ErrAgentNotFound,
		pigtail.ErrProjectNotFound, pigtail.ErrDeliveryNotFound:
		return http.StatusNotFound
	case pigtail.ErrConflict, pigtail.ErrPortExhausted, pigtail.ErrProjectNotInit, pigtail.ErrLaunchOverMargin, pigtail.ErrDevServerRunning:
		return http.StatusConflict
	case pigtail.ErrTmuxNotAvailable, pigtail.ErrGHAuthRequired, pigtail.ErrAgentSpawnFailed,
		pigtail.ErrBackendError, pigtail.ErrDaemonBusy:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

func workspaceEvent(ws *pigtail.Workspace) map[string]interface{} {
	return map[string]interface{}{
		"workspace_id": ws.ID,
		"project_id":   ws.ProjectID,
		"intent":       ws.Intent,
		"state":        string(ws.State),
		"path":         ws.Path,
		"ports":        ws.Ports,
		"backend_kind": ws.BackendKind,
	}
}

func agentEvent(sess *pigtail.AgentSession) map[string]interface{} {
	return map[string]interface{}{
		"agent_session_id":  sess.ID,
		"workspace_id":      sess.WorkspaceID,
		"agent_kind":        sess.AgentKind,
		"tmux_session_name": sess.TmuxSessionName,
	}
}

// settlePorts ends the in-flight life of a created workspace's ports:
// Confirm once its row is persisted, Release when the create is abandoned.
// Same order as static.go (allocate → persist → Confirm): confirming before
// the row exists let a concurrent create see the port in neither place.
func settlePorts(svc *Services, ws *pigtail.Workspace, persisted bool) {
	if svc.Ports == nil || ws == nil {
		return
	}
	for kind, port := range ws.Ports {
		if persisted {
			svc.Ports.Confirm(kind, port)
		} else {
			svc.Ports.Release(kind, port)
		}
	}
}

func persistWorkspace(svc *Services, ws *pigtail.Workspace) error {
	if svc.CreateWorkspace != nil {
		return svc.CreateWorkspace(ws)
	}
	return svc.Store.CreateWorkspace(ws)
}

func listAgentSessions(svc *Services, workspaceID string) ([]pigtail.AgentSession, error) {
	if svc.ListAgentSessions != nil {
		return svc.ListAgentSessions(workspaceID)
	}
	return svc.Store.ListAgentSessionsByWorkspace(workspaceID)
}

// humanName is the configured name of the human, else the neutral default.
func (s *Services) humanName() string {
	if s == nil || s.HumanName == "" {
		return usersettings.DefaultHumanName
	}
	return s.HumanName
}
