package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/rammon"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// RegisterBatteryRoutes wires the /battery endpoints onto r.
func RegisterBatteryRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Get("/battery", GetBattery(svc, resp))
	r.Post("/battery", SetBattery(svc, resp))
}

// GetBattery handles GET /battery — battery-mode status + per-server countdown.
func GetBattery(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := pigtail.BatteryResponse{Servers: []pigtail.BatteryServerView{}}
		if svc.RAMMonitor != nil {
			out = toBatteryResponse(svc.RAMMonitor.BatteryStatus())
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// setBatteryRequest is the body of POST /battery.
type setBatteryRequest struct {
	Enabled bool `json:"enabled"`
}

// SetBattery handles POST /battery — toggle battery mode for the running
// daemon session and return the fresh status.
func SetBattery(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req setBatteryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		out := pigtail.BatteryResponse{Servers: []pigtail.BatteryServerView{}}
		if svc.RAMMonitor != nil {
			svc.RAMMonitor.SetBatteryEnabled(req.Enabled)
			out = toBatteryResponse(svc.RAMMonitor.BatteryStatus())
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}

// toBatteryResponse maps the rammon view to the wire type 1:1.
func toBatteryResponse(v rammon.BatteryView) pigtail.BatteryResponse {
	out := pigtail.BatteryResponse{
		OnBattery:    v.OnBattery,
		Enabled:      v.Enabled,
		GraceSeconds: v.GraceSeconds,
		Servers:      make([]pigtail.BatteryServerView, 0, len(v.Servers)),
	}
	for _, s := range v.Servers {
		out.Servers = append(out.Servers, pigtail.BatteryServerView{
			WorkspaceID:    s.WorkspaceID,
			Server:         s.Server,
			Port:           s.Port,
			IdleSeconds:    s.IdleSeconds,
			EvictInSeconds: s.EvictInSeconds,
			Spared:         s.Spared,
		})
	}
	return out
}
