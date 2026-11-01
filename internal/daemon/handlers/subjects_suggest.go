package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/space"
	"github.com/aloysbr/pigtail/internal/suggest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const (
	suggestTextMax      = 4096
	suggestLimitDefault = 3
	suggestLimitMax     = 10
)

// suggestKinds are the only log kinds subject suggest reads: the ownership
// tiers of internal/suggest. message, idea and every other kind are ignored.
var suggestKinds = []string{"delivery", "phase", "handoff", "progress", "note", "decision"}

// cutUTF8 cuts s to at most max bytes on a rune boundary.
func cutUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// SuggestSubjects handles POST /subjects/suggest: the subjects a pane's
// request may belong to, ranked by internal/suggest and partitioned by the
// caller's scope — a subject the caller may not read is only counted in
// elsewhere, never named. A read: it stays open under maintenance --freeze.
//
//	200 SuggestResult; 400 bad JSON, nothing to match on (text, cwd and
//	branch all empty), or an invalid workspace_id.
func SuggestSubjects(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.SuggestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		req.Text = cutUTF8(req.Text, suggestTextMax)
		if strings.TrimSpace(req.Text) == "" && req.Cwd == "" && req.Branch == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "text, cwd or branch is required", nil)
			return
		}
		if req.Limit < 1 || req.Limit > suggestLimitMax {
			req.Limit = suggestLimitDefault
		}
		c := callerOf(r)

		// The workspace's off-topic reason joins the text and carries the
		// refusals — only when the caller may read that declaration (the
		// guard of GET /cmux-workspaces/{wsid}/state).
		text := req.Text
		refused := map[string]bool{}
		if req.WorkspaceID != "" {
			wsID, err := pigtail.NormalizeCmuxWorkspaceID(req.WorkspaceID)
			if err != nil {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
				return
			}
			o, err := svc.Store.GetOffTopic(wsID)
			switch {
			case err == nil:
				if authorize(c, scope.Scope(o.Scope), false) == nil {
					text += "\n" + o.Reason
					for _, id := range space.RefusedIDs(o.Reason) {
						refused[id] = true
					}
				}
			case !errors.Is(err, store.ErrNotFound):
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
		}

		q := suggest.Query{Text: text, Cwd: req.Cwd, Branch: req.Branch, DefaultBranch: req.DefaultBranch, Repo: req.Repo, Limit: req.Limit}
		var needles []string
		for _, ref := range suggest.ExtractPRRefs(text) {
			needles = append(needles, strconv.Itoa(ref.Number))
		}
		if q.Branch != "" && !suggest.IsDefaultBranch(q.Branch, q.DefaultBranch) {
			needles = append(needles, q.Branch)
		}
		logs, err := svc.Store.SuggestLogLines(needles, suggestKinds)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		var linked []suggest.Linked
		if q.Cwd != "" {
			if linked, err = svc.Store.SuggestLinkedWorktrees(); err != nil {
				resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
				return
			}
		}
		// The corpus is every subject of every scope: the idf weights do not
		// depend on who asks; the partition below decides what goes out.
		all, err := svc.Store.ListSubjects(store.SubjectFilter{Scope: scope.Any})
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		subjects := make([]suggest.Subject, 0, len(all))
		for _, s := range all {
			subjects = append(subjects, suggest.Subject{ID: s.ID, ShortTitle: pigtail.ShortTitle(s), Title: s.Title, Scope: s.Scope, Status: s.Status})
		}

		out := pigtail.SuggestResult{Candidates: []pigtail.SuggestCandidate{}}
		strong, autoID := 0, ""
		for _, cand := range suggest.Rank(q, subjects, logs, linked) {
			sc := scope.Scope(cand.Scope)
			isStrong := cand.Strength == suggest.Strong
			if !scope.CanRead(c.scope, sc) {
				if isStrong {
					out.Elsewhere.Strong++
					strong++
				} else {
					out.Elsewhere.Weak++
				}
				continue
			}
			sc2 := pigtail.SuggestCandidate{
				SubjectID: cand.SubjectID, ShortTitle: cand.ShortTitle, Title: cand.Title, Scope: cand.Scope, Status: cand.Status,
				Signal: cand.Signal, Strength: cand.Strength, Evidence: cand.Evidence, PR: cand.PR,
				Writable: scope.CanWrite(c.scope, sc, false) == nil,
			}
			if isStrong && !refused[cand.SubjectID] {
				strong++
				if sc2.Writable {
					autoID = cand.SubjectID
				} else {
					autoID = ""
				}
			}
			out.Candidates = append(out.Candidates, sc2)
		}
		// One strong candidate in every scope, and it is writable and not
		// refused: the only case the link is made without a question.
		if strong == 1 {
			out.Auto = autoID
		}
		if len(out.Candidates) > req.Limit {
			out.Candidates = out.Candidates[:req.Limit]
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}
