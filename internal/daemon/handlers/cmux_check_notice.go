package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// RegisterCmuxCheckNoticeRoutes wires GET /cmux/check/notice behind scopeMW.
func RegisterCmuxCheckNoticeRoutes(r chi.Router, svc *Services, resp Responder) {
	r.Group(func(r chi.Router) {
		r.Use(scopeMW(resp))
		r.Get("/cmux/check/notice", GetCmuxCheckNotice(svc, resp))
	})
}

// GetCmuxCheckNotice handles GET /cmux/check/notice (D2,
// sub_x000000000000037): the end-of-pass line the Pigtail app posts. Master only:
// the line names sessions of every scope.
func GetCmuxCheckNotice(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !scope.SeesAll(callerOf(r).scope) {
			resp.Error(w, r, http.StatusForbidden, pigtail.ErrScopeMismatch, "cmux check notice: master scope only", nil)
			return
		}
		if svc.CmuxCheckNotice == nil {
			resp.JSON(w, r, http.StatusOK, pigtail.CmuxCheckNotice{})
			return
		}
		n, err := svc.CmuxCheckNotice()
		if err != nil {
			svc.logf("cmux check notice unreadable: %v", err)
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, n)
	}
}
