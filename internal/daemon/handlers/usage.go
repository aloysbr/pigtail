package handlers

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// RegisterUsageRoutes wires GET /usage/overview, behind scopeMW: the view
// names every account of every cell, so only the master scope reads it.
func RegisterUsageRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Group(func(r chi.Router) {
		r.Use(scopeMW(resp))
		r.Get("/usage/overview", GetUsageOverview(svc, resp))
	})
}

// GetUsageOverview handles GET /usage/overview: 403 for a caller that is not
// the master scope, 501 when not wired, 503 when the readings cannot be read.
func GetUsageOverview(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !scope.SeesAll(callerOf(r).scope) {
			resp.Error(w, r, http.StatusForbidden, pigtail.ErrScopeMismatch, "usage overview: master scope only", nil)
			return
		}
		if svc.UsageOverview == nil {
			resp.Error(w, r, http.StatusNotImplemented, pigtail.ErrUsageUnavailable, "usage overview: not wired", nil)
			return
		}
		ov, err := svc.UsageOverview(time.Now())
		if err != nil {
			resp.Error(w, r, http.StatusServiceUnavailable, pigtail.ErrUsageUnavailable, "usage overview: "+err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, ov)
	}
}
