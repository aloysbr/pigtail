package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/lid"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// LidService is the lid manager as the handlers see it (*lid.Manager).
type LidService interface {
	On(hours int) (lid.Status, error)
	Off() (lid.Status, error)
	Status() (lid.Status, error)
	Probe() (lid.ProbeResult, error)
}

// RegisterLidRoutes wires the /lid endpoints onto r.
func RegisterLidRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Get("/lid", GetLid(svc, resp))
	r.Post("/lid", SetLid(svc, resp))
	r.Post("/lid/probe", ProbeLid(svc, resp))
}

const lidUnavailableMsg = "lid: sleep control is only available on macOS"

// GetLid handles GET /lid — the real sleep state plus pigtail's timer.
func GetLid(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.Lid == nil {
			resp.Error(w, r, http.StatusNotImplemented, pigtail.ErrLidUnavailable, lidUnavailableMsg, nil)
			return
		}
		st, err := svc.Lid.Status()
		if err != nil {
			lidError(w, r, resp, err)
			return
		}
		resp.JSON(w, r, http.StatusOK, toLidResponse(st))
	}
}

// SetLid handles POST /lid — on (re-arms the timer when already on, D5) or off.
func SetLid(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.Lid == nil {
			resp.Error(w, r, http.StatusNotImplemented, pigtail.ErrLidUnavailable, lidUnavailableMsg, nil)
			return
		}
		var req pigtail.LidRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		var st lid.Status
		var err error
		if req.On {
			hours := req.Hours
			if hours == 0 {
				hours = lid.DefaultHours
			}
			st, err = svc.Lid.On(hours)
		} else {
			st, err = svc.Lid.Off()
		}
		if err != nil {
			lidError(w, r, resp, err)
			return
		}
		resp.JSON(w, r, http.StatusOK, toLidResponse(st))
	}
}

// ProbeLid handles POST /lid/probe — the D7 sudo probe, run by pigtaild under
// the manager lock. rule_ok=false is a 200 answer, not an error.
func ProbeLid(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc.Lid == nil {
			resp.Error(w, r, http.StatusNotImplemented, pigtail.ErrLidUnavailable, lidUnavailableMsg, nil)
			return
		}
		p, err := svc.Lid.Probe()
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, pigtail.LidProbeResponse{RuleOK: p.RuleOK, Warning: p.Warning})
	}
}

func lidError(w http.ResponseWriter, r *http.Request, resp Responder, err error) {
	switch {
	case errors.Is(err, lid.ErrNotSetUp):
		resp.Error(w, r, http.StatusPreconditionFailed, pigtail.ErrLidNotSetUp, lid.ErrNotSetUp.Error(), nil)
	case errors.Is(err, lid.ErrHours):
		resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
	default:
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
	}
}

func toLidResponse(st lid.Status) pigtail.LidResponse {
	out := pigtail.LidResponse{
		SleepDisabled: st.SleepDisabled,
		BatteryFloor:  lid.MinBattery,
		LastError:     st.LastError,
		GuardError:    st.GuardError,
		ThermalState:  st.ThermalState,
	}
	if st.LidClosed != nil {
		c := *st.LidClosed
		out.LidClosed = &c
	}
	if !st.Deadline.IsZero() {
		d := st.Deadline
		out.Deadline = &d
	}
	if st.LastOff != nil {
		out.LastOff = &pigtail.LidOff{At: st.LastOff.At, Reason: st.LastOff.Reason}
	}
	return out
}
