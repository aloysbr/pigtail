package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// maxRankChanges bounds one POST /subjects/ranks.
const maxRankChanges = 500

type rankChangeBody struct {
	Changes []struct {
		ID   string `json:"id"`
		From int    `json:"from"`
		To   int    `json:"to"`
	} `json:"changes"`
}

// SetRanks handles POST /subjects/ranks {"changes":[{"id","from","to"}]}: a
// swap, an insertion or its undo in one transaction, each rank checked
// against the caller's from (docs/wire-protocol.md § Web (browser)). 200 with the
// updated subjects, in the order of the changes; 409 RANK_CHANGED when a
// from is stale (nothing written); 404 / 403 per id as POST /subjects/{id}.
func SetRanks(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b rankChangeBody
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&b); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if n := len(b.Changes); n == 0 || n > maxRankChanges {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
				fmt.Sprintf("changes: 1 to %d, got %d", maxRankChanges, n), nil)
			return
		}
		seen := make(map[string]bool, len(b.Changes))
		changes := make([]store.RankChange, 0, len(b.Changes))
		for _, c := range b.Changes {
			switch {
			case c.ID == "":
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "changes: every change needs an id", nil)
				return
			case seen[c.ID]:
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "changes: subject "+c.ID+" is listed twice", nil)
				return
			case c.From < 0 || c.To < 0:
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "changes: ranks are ≥ 0 (0 = unranked)", nil)
				return
			}
			seen[c.ID] = true
			changes = append(changes, store.RankChange{ID: c.ID, From: c.From, To: c.To})
		}
		for _, c := range changes {
			if _, ok := authorizeSubject(w, r, svc, resp, c.ID, true); !ok {
				return
			}
		}
		if err := svc.Store.SetRanks(changes); err != nil {
			switch {
			case errors.Is(err, store.ErrRankChanged):
				resp.Error(w, r, http.StatusConflict, pigtail.ErrRankChanged, err.Error(), nil)
			case errors.Is(err, store.ErrNotFound):
				resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, "no such subject", nil)
			default:
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			}
			return
		}
		svc.subjectsChanged()
		out := make([]pigtail.Subject, 0, len(changes))
		for _, c := range changes {
			sub, err := svc.Store.GetSubject(c.ID)
			if err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
			out = append(out, *sub)
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}
