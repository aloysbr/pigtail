package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// RegisterPurgeRoutes wires the /purge endpoints onto r.
func RegisterPurgeRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Get("/purge", GetPurge(svc, resp))
	r.Post("/purge", SetPurge(svc, resp))
}

// GetPurge handles GET /purge — the current always-on idle-purge threshold.
func GetPurge(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := pigtail.PurgeResponse{}
		if svc.RAMMonitor != nil {
			out.GraceSeconds = int(svc.RAMMonitor.IdlePurgeGrace().Seconds())
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// setPurgeRequest is the body of POST /purge.
type setPurgeRequest struct {
	GraceSeconds int `json:"grace_seconds"`
}

// SetPurge handles POST /purge — set the idle-purge threshold for the running
// daemon session and return the fresh value.
func SetPurge(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req setPurgeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if req.GraceSeconds < 0 {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "grace_seconds must be >= 0", nil)
			return
		}
		out := pigtail.PurgeResponse{}
		if svc.RAMMonitor != nil {
			svc.RAMMonitor.SetIdlePurgeGrace(time.Duration(req.GraceSeconds) * time.Second)
			out.GraceSeconds = int(svc.RAMMonitor.IdlePurgeGrace().Seconds())
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}
