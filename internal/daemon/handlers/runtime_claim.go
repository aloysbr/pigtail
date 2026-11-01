package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/runtimeclaim"
	"github.com/aloysbr/pigtail/pkg/pigtail"
	"github.com/go-chi/chi/v5"
)

// RegisterRuntimeClaimRoutes mounts the lease on the installed runtime (D3 of
// sub_x000000000000005). The file is the truth: every call reads and writes
// it under its flock, nothing is kept in memory, so a claim the CLI wrote
// directly while the daemon was down is the one served after the restart.
// No route returns the holder's label nor the reason (D23).
func RegisterRuntimeClaimRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Get("/runtime/claim", GetRuntimeClaim(svc, resp))
	r.Post("/runtime/claim", PostRuntimeClaim(svc, resp))
	r.Post("/runtime/release", PostRuntimeRelease(svc, resp))
}

func runtimeView(dir string, st runtimeclaim.State, alive runtimeclaim.AliveFunc) pigtail.RuntimeClaimView {
	return pigtail.RuntimeClaimView{Dir: dir, Present: true, ID: st.ID, Session: st.Session, Branch: st.Branch, SHA: st.SHA,
		Dirty: st.Dirty, PR: st.PR, Since: st.Since, Liveness: string(st.Liveness(alive)), ExpiresAt: st.Deadline()}
}

func heldDetails(cur runtimeclaim.State, lv runtimeclaim.Liveness) map[string]interface{} {
	return map[string]interface{}{"id": cur.ID, "session": cur.Session, "since": cur.Since, "liveness": string(lv)}
}

// runtimeError answers an error of runtimeclaim.Claim/Release: 409 for a
// lease held by another session, 503 for a busy lock, 500 otherwise (an
// unreadable file: the cause and the path, never overwritten).
func runtimeError(svc *Services, w http.ResponseWriter, r *http.Request, resp Responder, err error) {
	var held *runtimeclaim.HeldError
	var nh *runtimeclaim.NotHolderError
	switch {
	case errors.As(err, &held):
		resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "runtime held by another session", heldDetails(held.Cur, held.Liveness))
	case errors.As(err, &nh):
		resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, "runtime held by another session", heldDetails(nh.Cur, nh.Liveness))
	case errors.Is(err, runtimeclaim.ErrLockTimeout):
		resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrDaemonBusy, err.Error(), nil)
	default:
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal,
			"runtime claim "+filepath.Join(svc.RuntimeClaimDir, runtimeclaim.FileName)+": "+err.Error(), nil)
	}
}

func runtimeWired(svc *Services, w http.ResponseWriter, r *http.Request, resp Responder) bool {
	if svc.RuntimeClaimDir == "" {
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "runtime claim not wired", nil)
		return false
	}
	return true
}

// GetRuntimeClaim: GET /runtime/claim.
func GetRuntimeClaim(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !runtimeWired(svc, w, r, resp) {
			return
		}
		dir := svc.RuntimeClaimDir
		// A read that finds the lease past its deadline lifts it, but never
		// waits for the lock: busy or failing, it only reads.
		if exp, lifted, _ := runtimeclaim.ExpireNoWait(dir, time.Now()); lifted {
			svc.logf("runtime claim %s expired, lifted", exp.Lease.ID)
		}
		st, ok, err := runtimeclaim.Load(dir)
		switch {
		case err != nil:
			resp.JSON(w, r, http.StatusOK, pigtail.RuntimeClaimView{Dir: dir, Present: true,
				Unreadable: filepath.Join(dir, runtimeclaim.FileName) + ": " + err.Error()})
		case !ok:
			resp.JSON(w, r, http.StatusOK, pigtail.RuntimeClaimView{Dir: dir})
		default:
			resp.JSON(w, r, http.StatusOK, runtimeView(dir, st, svc.Alive))
		}
	}
}

// PostRuntimeClaim: POST /runtime/claim.
func PostRuntimeClaim(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !runtimeWired(svc, w, r, resp) {
			return
		}
		var req pigtail.RuntimeClaimRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON body", nil)
			return
		}
		if req.Session != "" && !maintenance.ValidSession(req.Session) {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "session is not a session id", nil)
			return
		}
		if req.Reason == "" || len(req.Reason) > runtimeclaim.MaxReasonBytes {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "reason is required (≤ 500 bytes)", nil)
			return
		}
		st, err := runtimeclaim.Claim(svc.RuntimeClaimDir, runtimeclaim.State{Reason: req.Reason, PR: req.PR, Session: req.Session,
			Label: req.Label, LabelScope: req.LabelScope, PID: req.PID, ConfigDir: req.ConfigDir,
			Branch: req.Branch, SHA: req.SHA, Dirty: req.Dirty}, req.TakeOver, svc.Alive, time.Now())
		if err != nil {
			runtimeError(svc, w, r, resp, err)
			return
		}
		resp.JSON(w, r, http.StatusOK, runtimeView(svc.RuntimeClaimDir, st, svc.Alive))
	}
}

// PostRuntimeRelease: POST /runtime/release.
func PostRuntimeRelease(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !runtimeWired(svc, w, r, resp) {
			return
		}
		var req pigtail.RuntimeReleaseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON body", nil)
			return
		}
		if req.Session != "" && !maintenance.ValidSession(req.Session) {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "session is not a session id", nil)
			return
		}
		st, ok, err := runtimeclaim.Release(svc.RuntimeClaimDir, req.Session, req.Force, svc.Alive, time.Now())
		if err != nil {
			runtimeError(svc, w, r, resp, err)
			return
		}
		out := pigtail.RuntimeReleaseResponse{}
		if ok {
			out.Released = &st.ID
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}
