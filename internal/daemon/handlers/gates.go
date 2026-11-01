package handlers

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/gates"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// RegisterGateRoutes mounts GET /gates behind scopeMW: a gate carries a title
// and a subject id, subject-layer data (D19). Pigtail reads it as the
// master (the machine's single view, SI-D7).
func RegisterGateRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Group(func(r chi.Router) {
		r.Use(scopeMW(resp))
		r.Get("/gates", ListGates(svc, resp))
	})
}

// ListGates handles GET /gates. Always 200: an unreadable gate source is the
// source's state, reported in `error` with no `counts` — never a 0 nobody can
// tell from "nothing fired" (docs/wire-protocol.md § GET /gates).
func ListGates(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		caller := callerOf(r).scope
		out := pigtail.GatesResponse{Complete: scope.SeesAll(caller),
			Fired: []pigtail.GateView{}, Open: []pigtail.GateView{}, ReadAt: now}
		if svc.Gates == nil || svc.Gates.Cmd == "" {
			resp.JSON(w, r, http.StatusOK, out)
			return
		}
		gs, gerr := svc.Gates.Read(r.Context())
		if gerr != nil {
			out.Configured, out.Error = true, gerr
			resp.JSON(w, r, http.StatusOK, out)
			return
		}
		resp.JSON(w, r, http.StatusOK, gates.Build(gs,
			func(s scope.Scope) bool { return scope.CanRead(caller, s) }, scope.SeesAll(caller), now))
	}
}
