package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/subjectfind"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// findJournalQueryRunes clips the query part of a journal summary only: the tail
// (« <total> candidat(s), tête … ») always stays whole, so a long query never
// breaks the summary format (and 160 + the tail stays under JournalSummaryMax).
const findJournalQueryRunes = 160

// findKinds are the log kinds whose last line is a subject's recency (T2):
// subjects.updated_at and status lines are written in batches.
var findKinds = []string{"phase", "progress", "handoff", "delivery", "decision"}

// findJournal is the pointer line of one search, in the search's scope.
func findJournal(f *pigtail.SubjectFind, res pigtail.SubjectFindResult) *pigtail.JournalEntry {
	sum := "recherche « " + clipRunes(f.Query, findJournalQueryRunes) + " » : "
	if len(res.Candidates) == 0 {
		sum += "aucun candidat"
	} else {
		sum += fmt.Sprintf("%d candidat(s), tête %s", res.Total, res.Candidates[0].SubjectID)
	}
	if res.Elsewhere > 0 {
		sum += fmt.Sprintf(" (+%d ailleurs)", res.Elsewhere)
	}
	return &pigtail.JournalEntry{ID: pigtail.JournalKindSubjectFind + ":" + f.ID, Timestamp: f.Timestamp,
		Kind: pigtail.JournalKindSubjectFind, Scope: f.Scope, Summary: sum, Source: pigtail.SubjectFindSource(f.ID)}
}

// FindSubjects handles POST /subjects/find: the subjects a few words name,
// ranked by internal/subjectfind over every subject of every scope and
// partitioned by the caller's scope — a subject the caller may not read is
// only counted in elsewhere. Every accepted search is recorded (subject_finds
// + a journal line, one transaction); a refused one records nothing. Out of
// the freeze, like suggest.
//
//	200 SubjectFindResult; 400 bad JSON, empty or too long query, no
//	searchable word, limit out of 1..20, invalid workspace_id; 500 not recorded.
func FindSubjects(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.SubjectFindRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		q := pigtail.NormalizeFindQuery(req.Query)
		bad := func(msg string) { resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, msg, nil) }
		switch {
		case q == "":
			bad("query is required")
			return
		case len(q) > pigtail.SubjectFindQueryMax:
			bad(fmt.Sprintf("query too long (%d bytes, max %d)", len(q), pigtail.SubjectFindQueryMax))
			return
		case len(subjectfind.Terms(q)) == 0:
			bad("query has no searchable word (stop words and one-letter words are ignored)")
			return
		case req.Limit < 0 || req.Limit > pigtail.SubjectFindLimitMax:
			bad(fmt.Sprintf("limit must be 1..%d", pigtail.SubjectFindLimitMax))
			return
		}
		if req.Limit == 0 {
			req.Limit = pigtail.SubjectFindLimitDefault
		}
		ws := ""
		if req.WorkspaceID != "" {
			var err error
			if ws, err = pigtail.NormalizeCmuxWorkspaceID(req.WorkspaceID); err != nil {
				bad(err.Error())
				return
			}
		}
		internal := func(err error) {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
		}
		all, err := svc.Store.ListSubjects(store.SubjectFilter{Scope: scope.Any})
		if err != nil {
			internal(err)
			return
		}
		themes, err := svc.Store.ListThemes(scope.Any)
		if err != nil {
			internal(err)
			return
		}
		last, err := svc.Store.SubjectLastActivity(findKinds)
		if err != nil {
			internal(err)
			return
		}
		themeName := make(map[string]string, len(themes))
		for _, th := range themes {
			themeName[th.ID] = th.Name
		}
		byID := make(map[string]pigtail.Subject, len(all))
		corpus := make([]subjectfind.Subject, 0, len(all))
		for _, s := range all {
			byID[s.ID] = s
			corpus = append(corpus, subjectfind.Subject{ID: s.ID, Title: s.Title, ShortTitle: pigtail.ShortTitle(s),
				Theme: themeName[s.ThemeID], Scope: s.Scope, Status: s.Status, LastActivity: last[s.ID]})
		}

		c := callerOf(r)
		res := pigtail.SubjectFindResult{Query: q, Candidates: []pigtail.SubjectFindCandidate{}}
		hits := []pigtail.SubjectFindHit{}
		for _, cand := range subjectfind.Rank(q, corpus) {
			sc := scope.Scope(cand.Scope)
			if !scope.CanRead(c.scope, sc) {
				res.Elsewhere++
				continue
			}
			res.Total++
			if len(res.Candidates) == req.Limit {
				continue
			}
			score := math.Round(cand.Score*100) / 100
			out := pigtail.SubjectFindCandidate{
				SubjectID: cand.ID, ShortTitle: cand.ShortTitle, Title: cand.Title,
				ThemeID: byID[cand.ID].ThemeID, Theme: cand.Theme, Scope: cand.Scope, Status: cand.Status,
				Closed: pigtail.IsClosedStatus(cand.Status), Writable: scope.CanWrite(c.scope, sc, false) == nil,
				Score: score, Matched: cand.Matched,
			}
			if cand.LastActivity > 0 {
				at := time.UnixMilli(cand.LastActivity).UTC()
				out.LastActivity = &at
			}
			res.Candidates = append(res.Candidates, out)
			hits = append(hits, pigtail.SubjectFindHit{SubjectID: cand.ID, Score: score})
		}
		f := &pigtail.SubjectFind{ID: genID(pigtail.PrefixSubjectFind), Scope: string(c.scope), Timestamp: time.Now().UTC(),
			Query: q, Candidates: hits, CmuxWorkspaceID: ws}
		res.FindID = f.ID
		if err := svc.Store.CreateSubjectFind(f, findJournal(f, res)); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "subject find not recorded: "+err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, res)
	}
}

const (
	msgFindMissing    = "no such subject find"
	msgSubjectMissing = "no such subject"
	msgFindPicked     = "subject find already picked for another subject"
)

// pickSummary is the pick line's summary — the indicator's contract.
func pickSummary(p pigtail.SubjectFindPick) string {
	place := fmt.Sprintf("au rang %d/%d", p.Rank, p.Candidates)
	if p.Rank == 0 {
		place = fmt.Sprintf("hors liste (%d candidat(s))", p.Candidates)
	}
	return fmt.Sprintf("recherche %s : %s choisi %s après %d recherche(s)", p.FindID, p.SubjectID, place, p.Searches)
}

// findForPick checks that the caller may write search fid, that the search's
// own scope may read subjectID, and that it is not picked for another subject:
// 0 when it may be picked for subjectID, else the status, API code and message
// to answer. A search of a scope the caller may not read is the unknown one's
// 404; so is a subject the search's scope may not read (a master with
// --cross-scope included): nothing of another scope is tied to the search.
func findForPick(svc *Services, c callerScope, fid, subjectID string) (code int, apiCode pigtail.ErrorCode, msg string) {
	f, err := svc.Store.GetSubjectFind(fid)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, pigtail.ErrNotFound, msgFindMissing
	case err != nil:
		return http.StatusInternalServerError, pigtail.ErrInternal, err.Error()
	}
	if err := authorize(c, scope.Scope(f.Scope), true); err != nil {
		switch {
		case errors.Is(err, scope.ErrNotVisible):
			return http.StatusNotFound, pigtail.ErrNotFound, msgFindMissing
		case errors.Is(err, scope.ErrCrossScope):
			return http.StatusForbidden, pigtail.ErrScopeMismatch, err.Error()
		}
		return http.StatusInternalServerError, pigtail.ErrInternal, err.Error()
	}
	subScope, err := svc.Store.SubjectScope(subjectID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, pigtail.ErrNotFound, msgSubjectMissing
	case err != nil:
		return http.StatusInternalServerError, pigtail.ErrInternal, err.Error()
	}
	if !scope.CanRead(scope.Scope(f.Scope), subScope) {
		return http.StatusNotFound, pigtail.ErrNotFound, msgSubjectMissing
	}
	if f.PickedSubjectID != "" && f.PickedSubjectID != subjectID {
		return http.StatusConflict, pigtail.ErrConflict, msgFindPicked
	}
	return 0, "", ""
}

// recordPick writes the pick and its journal line; the line carries the
// subject_id only when the subject is of the search's scope (never another
// scope's id on a row).
func recordPick(svc *Services, fid, subjectID string) (*pigtail.SubjectFindPick, error) {
	subScope, err := svc.Store.SubjectScope(subjectID)
	if err != nil {
		return nil, err
	}
	return svc.Store.PickSubjectFind(fid, subjectID, time.Now().UTC(), func(p pigtail.SubjectFindPick, f *pigtail.SubjectFind) *pigtail.JournalEntry {
		e := &pigtail.JournalEntry{ID: pigtail.JournalKindSubjectFindPick + ":" + f.ID, Timestamp: p.PickedAt,
			Kind: pigtail.JournalKindSubjectFindPick, Scope: f.Scope, Summary: clipRunes(pickSummary(p), pigtail.JournalSummaryMax), Source: pigtail.SubjectFindSource(f.ID)}
		if string(subScope) == f.Scope {
			e.SubjectID = p.SubjectID
		}
		return e
	})
}

// PickSubjectFind handles POST /subject-finds/{fid}/pick: the subject a
// search led to, when no attach carries it. Out of the freeze, like the link.
//
//	201 new pick; 200 the same pick again; 400 bad JSON or no subject_id;
//	404 unknown (or unreadable) search or subject; 403 master without the
//	opt-in; 409 picked for another subject.
func PickSubjectFind(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req pigtail.SubjectFindPickRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		if req.SubjectID == "" {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "subject_id is required", nil)
			return
		}
		fid := chi.URLParam(r, "fid")
		if code, apiCode, msg := findForPick(svc, callerOf(r), fid, req.SubjectID); code != 0 {
			resp.Error(w, r, code, apiCode, msg, nil)
			return
		}
		if _, ok := authorizeSubject(w, r, svc, resp, req.SubjectID, false); !ok {
			return
		}
		p, err := recordPick(svc, fid, req.SubjectID)
		switch {
		case errors.Is(err, store.ErrFindPicked):
			resp.Error(w, r, http.StatusConflict, pigtail.ErrConflict, msgFindPicked, nil)
			return
		case err != nil:
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		code := http.StatusOK
		if p.Created {
			code = http.StatusCreated
		}
		resp.JSON(w, r, code, p)
	}
}

// finishAttach ends a successful attach: it records the pick when the attach
// carries a find_id, then answers the binding (with find_id echoed).
func finishAttach(w http.ResponseWriter, r *http.Request, svc *Services, resp Responder, wsID string, code int, subjectID, findID string) {
	if findID != "" {
		if _, err := recordPick(svc, findID, subjectID); err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, "workspace linked, but the find pick was not recorded: "+err.Error(), nil)
			return
		}
	}
	b, err := svc.Store.GetCmuxBinding(wsID)
	if err != nil {
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
		return
	}
	b.FindID = findID
	resp.JSON(w, r, code, b)
}
