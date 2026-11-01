package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/relay"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// CmuxSidebarControl is the HTTP side of the sidebar loop
// (internal/daemon/cmuxsync.Runner), kept abstract so handlers do not import
// the loop. Nil in Services means no loop: GET answers "off", POST 503.
type CmuxSidebarControl interface {
	State() pigtail.CmuxSidebarState
	SetEnabled(on bool)
	Nudge()
	Released(b pigtail.SubjectCmuxBinding)
	// TrustPending records a spawned Claude (pid) stopped on "trust this
	// folder" in workspace wsID (D14).
	TrustPending(wsID string, pid int)
	// RespawnMark records a respawn run's state on workspace wsID (D16),
	// replacing its last one; RespawnClear drops it.
	RespawnMark(wsID string, req pigtail.CmuxRespawnRequest)
	RespawnClear(wsID string)
}

// Registered with the subject routes, behind scopeMW: the state names bound
// subjects, and retitle writes one; trust-pending and respawn name only a
// workspace, a surface and a pid.
func registerCmuxSidebarRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Get("/cmux/sidebar", GetCmuxSidebar(svc, resp))
	r.Post("/cmux/sidebar", SetCmuxSidebar(svc, resp))
	r.Post("/cmux/folders/toggle", ToggleCmuxFolders(svc, resp))
	r.Post("/cmux-workspaces/{wsid}/retitle", RetitleCmuxWorkspace(svc, resp))
	r.Post("/cmux-workspaces/{wsid}/trust-pending", MarkCmuxTrustPending(svc, resp))
	r.Post("/cmux-workspaces/{wsid}/respawn", MarkCmuxRespawn(svc, resp))
	r.Delete("/cmux-workspaces/{wsid}/respawn", ClearCmuxRespawn(svc, resp))
}

// GetCmuxSidebar handles GET /cmux/sidebar: the loop's switch, cmux's
// reachability, warnings and the wanted state of every workspace. A bound
// subject the caller may not read shows as null, and so does the wait reason
// of a session of a scope it may not read; attention and the wait type stay,
// they reveal neither.
func GetCmuxSidebar(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp.JSON(w, r, http.StatusOK, cmuxSidebarState(svc, callerOf(r)))
	}
}

// SetCmuxSidebar handles POST /cmux/sidebar {enabled}: switch the loop on or
// off until the daemon restarts. Off clears pigtail's pills and gives back the
// titles and descriptions it posed, on the loop's next pass.
func SetCmuxSidebar(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.CmuxSidebarToggle
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if svc.CmuxSidebar == nil {
			resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrInternal, "this daemon runs no cmux sidebar loop", nil)
			return
		}
		svc.CmuxSidebar.SetEnabled(req.Enabled)
		st := cmuxSidebarState(svc, callerOf(r))
		resp.JSON(w, r, http.StatusOK, st)
	}
}

func cmuxSidebarState(svc *Services, c callerScope) pigtail.CmuxSidebarState {
	if svc.CmuxSidebar == nil {
		return pigtail.CmuxSidebarState{Warnings: []string{}, Workspaces: []pigtail.CmuxSidebarWorkspace{}}
	}
	st := svc.CmuxSidebar.State()
	for i := range st.Workspaces {
		w := &st.Workspaces[i]
		if s := w.Subject; s != nil && authorize(c, scope.Scope(s.Scope), false) != nil {
			w.Subject = nil
		}
		// A reason told by a session: a caller who may not see that session
		// keeps the type (attention), not the words. A session with no scope
		// is seen by the master only (D17). pigtail's own reasons (trust
		// folder, respawn; WaitFromSession false) name no session: shown as
		// before.
		if w.WaitReason != "" && w.WaitFromSession && authorize(c, sessionScope(pigtail.PeerSession{Scope: w.WaitScope}), false) != nil {
			w.WaitReason = ""
		}
		var cells, scopes []string
		for i, id := range w.Cells {
			sc := ""
			if i < len(w.CellScopes) {
				sc = w.CellScopes[i]
			}
			// A cell's scope is its sessions': none = the master's only (D17).
			if authorize(c, sessionScope(pigtail.PeerSession{Scope: sc}), false) != nil {
				continue
			}
			cells, scopes = append(cells, id), append(scopes, sc)
		}
		// The title follows the subject's redaction (D23): a bound
		// workspace shows its subject's title; an unbound one's title may
		// name a session the caller may not see.
		hidden := false
		if w.TitleOwner != "" { // bound: the title is the subject's
			// Subject nil: redacted above, or a subject the loop could not
			// read (cmuxsync publishChanges) — its scope unknown, so hidden
			// from all but the master, who reads every scope.
			hidden = w.Subject == nil && !scope.SeesAll(c.scope)
		} else {
			hidden = len(cells) < len(w.Cells) // a cell the caller may not see
		}
		w.Cells, w.CellScopes = cells, scopes
		if hidden {
			w.Title = ""
		} else {
			short := ""
			if w.Subject != nil {
				short = w.Subject.ShortTitle
			}
			w.DisplayName = pigtail.SessionDisplayName(pigtail.DisplayNameInput{Shown: w.Title, ShortTitle: short, Bound: w.TitleOwner != ""})
		}
	}
	return st
}

// RetitleCmuxWorkspace handles POST /cmux-workspaces/{wsid}/retitle: hand a
// bound workspace's description back to pigtail after a manual edit (the
// title is always pigtail's since D5: a manual rename is put back). A write on the bound subject: an unbound workspace and one bound to
// a subject the caller may not read get the same 404.
func RetitleCmuxWorkspace(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, err := pigtail.NormalizeCmuxWorkspaceID(chi.URLParam(r, "wsid"))
		if err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		b, err := svc.Store.GetCmuxBinding(wsID)
		if errors.Is(err, store.ErrNotFound) {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, msgCmuxNotBound, nil)
			return
		}
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		sc, err := svc.Store.SubjectScope(b.SubjectID)
		c := callerOf(r)
		if err != nil || authorize(c, sc, false) != nil {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, msgCmuxNotBound, nil)
			return
		}
		if err := authorize(c, sc, true); err != nil {
			scopeError(w, r, resp, err, msgCmuxNotBound)
			return
		}
		if err := svc.Store.ResetCmuxOwners(wsID); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		logCmuxLink(svc, b.SubjectID, "description cmux de "+wsID+" rendue à pigtail (retitle)")
		if svc.CmuxSidebar != nil {
			svc.CmuxSidebar.Nudge()
		}
		writeBinding(w, r, svc, resp, wsID, http.StatusOK)
	}
}

// MarkCmuxTrustPending handles POST /cmux-workspaces/{wsid}/trust-pending:
// a spawned session waits on Claude's "trust this folder" dialog (D14). It
// names a workspace and a pid, nothing of the subject layer.
func MarkCmuxTrustPending(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, err := pigtail.NormalizeCmuxWorkspaceID(chi.URLParam(r, "wsid"))
		if err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		var req pigtail.CmuxTrustPendingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PID <= 0 {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "body must be {\"pid\": <claude pid>}", nil)
			return
		}
		if svc.CmuxSidebar == nil {
			resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrInternal, "this daemon runs no cmux sidebar loop", nil)
			return
		}
		svc.CmuxSidebar.TrustPending(wsID, req.PID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// MarkCmuxRespawn handles POST /cmux-workspaces/{wsid}/respawn: the state of
// a `pigtail session respawn` run on its workspace (D16). A closed contract:
// state waiting|abandoned|failed; waiting takes a wait from the closed list,
// the run's surface and the worker's pid; a final state takes the surface
// alone, or nothing, plus its run (run_id, subject_id, session_id, detail —
// the detail flattened to one line and cut at 1500 bytes). Workspace and
// surface are UUIDs, handed on upper case.
//
// A final state carrying run_id goes to the RespawnNotifier (journal line,
// session wait, message) and answers 200 with its report
// (pigtail.CmuxRespawnNotice) — with or without a running sidebar loop, whose
// pill the report's Pill tells apart (a disabled loop is not posted to).
// Without run_id (an older worker): the pill alone, 204 (a disabled loop
// included, as before); 503 without a loop controller.
func MarkCmuxRespawn(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, err := pigtail.NormalizeCmuxWorkspaceID(chi.URLParam(r, "wsid"))
		if err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		var req pigtail.CmuxRespawnRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if msg := normalizeRespawnRequest(&req); msg != "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, msg, nil)
			return
		}
		var notice *pigtail.CmuxRespawnNotice
		if req.RunID != "" { // a final state: normalize refuses a run on waiting
			n := pigtail.CmuxRespawnNotice{Journal: "skipped: no notifier", Wait: "skipped: no notifier", Notice: "skipped: no notifier"}
			if svc.RespawnNotifier != nil {
				c := callerOf(r)
				n = svc.RespawnNotifier.NotifyRespawnEnd(c.scope, c.cross, wsID, req)
			} else {
				svc.logf("respawn %s %s: no notifier wired — nothing journaled, nobody told", req.RunID, req.State)
			}
			notice = &n
		}
		if notice != nil && !sidebarLoopRuns(svc.CmuxSidebar) {
			// The notifier ran: say what it did, and that no pill was posted.
			// The loop is always constructed in pigtaild; --cmux-sidebar off
			// (the default) leaves it disabled, not nil (CA8b).
			notice.Pill = "skipped: this daemon runs no cmux sidebar loop"
			resp.JSON(w, r, http.StatusOK, notice)
			return
		}
		if svc.CmuxSidebar == nil {
			resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrInternal, "this daemon runs no cmux sidebar loop", nil)
			return
		}
		svc.CmuxSidebar.RespawnMark(wsID, req)
		if notice != nil {
			notice.Pill = pigtail.CmuxRespawnNoticeOK
			resp.JSON(w, r, http.StatusOK, notice)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// sidebarLoopRuns reports a sidebar loop that would post a pill: present,
// enabled and not stopped by a panic.
func sidebarLoopRuns(c CmuxSidebarControl) bool {
	if c == nil {
		return false
	}
	st := c.State()
	return st.Enabled && !st.DisabledByPanic
}

// respawnIDRe is the shape of a respawn run id and of a session id on the
// respawn route: one token, no blank, no slash.
var respawnIDRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,100}$`)

// subjectIDRe is the shape of a subject id (genID(pigtail.PrefixSubject)):
// the respawn route and POST /alerts/resolve refuse any other (400).
var subjectIDRe = regexp.MustCompile(`^sub_[a-z0-9]{1,64}$`)

// respawnDetailMax caps a final state's detail once flattened: the record
// keeps it whole, the daemon needs one line.
const respawnDetailMax = 1500

// normalizeRespawnRequest checks req against the closed contract and puts
// its surface in upper case; the answer is the 400's message, "" when valid.
func normalizeRespawnRequest(req *pigtail.CmuxRespawnRequest) string {
	if req.SurfaceID != "" {
		id, err := pigtail.NormalizeCmuxWorkspaceID(req.SurfaceID)
		if err != nil {
			return fmt.Sprintf("surface_id must be a cmux surface UUID, got %q", req.SurfaceID)
		}
		req.SurfaceID = id
	}
	switch req.State {
	case pigtail.CmuxRespawnWaiting:
		if req.RunID != "" || req.SubjectID != "" || req.SessionID != "" || req.Detail != "" {
			return "run_id, subject_id, session_id and detail go with a final state only"
		}
		if !slices.Contains(pigtail.CmuxRespawnWaits, req.Wait) {
			return fmt.Sprintf("wait must be one of %s, got %q", strings.Join(pigtail.CmuxRespawnWaits, ", "), req.Wait)
		}
		if req.SurfaceID == "" || req.PID <= 0 {
			return "a waiting respawn needs surface_id and the worker's pid"
		}
	case pigtail.CmuxRespawnAbandoned, pigtail.CmuxRespawnFailed:
		if req.Wait != "" || req.PID != 0 {
			return "wait and pid go with state waiting only"
		}
		if req.RunID != "" && !respawnIDRe.MatchString(req.RunID) {
			return fmt.Sprintf("run_id must match %s, got %q", respawnIDRe, req.RunID)
		}
		if req.SessionID != "" && !respawnIDRe.MatchString(req.SessionID) {
			return fmt.Sprintf("session_id must match %s, got %q", respawnIDRe, req.SessionID)
		}
		if req.SubjectID != "" && !subjectIDRe.MatchString(req.SubjectID) {
			return fmt.Sprintf("subject_id must match %s, got %q", subjectIDRe, req.SubjectID)
		}
		// The secret test runs on the whole detail, before the clip: a token
		// straddling the cut would otherwise leave as a partial prefix.
		req.Detail = strings.Join(strings.Fields(req.Detail), " ")
		if name := relay.SecretShape(req.Detail); name != "" {
			req.Detail = withheldDetail(name)
		}
		req.Detail = clipBytes(req.Detail, respawnDetailMax)
	default:
		return fmt.Sprintf("state must be waiting, abandoned or failed, got %q", req.State)
	}
	return ""
}

// ClearCmuxRespawn handles DELETE /cmux-workspaces/{wsid}/respawn: the run
// stopped waiting, or succeeded — its workspace shows no respawn state.
func ClearCmuxRespawn(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsID, err := pigtail.NormalizeCmuxWorkspaceID(chi.URLParam(r, "wsid"))
		if err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		if svc.CmuxSidebar == nil {
			resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrInternal, "this daemon runs no cmux sidebar loop", nil)
			return
		}
		svc.CmuxSidebar.RespawnClear(wsID)
		w.WriteHeader(http.StatusNoContent)
	}
}
