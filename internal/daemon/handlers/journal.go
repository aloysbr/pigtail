package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// System journal routes (docs/wire-protocol.md § Journal). Registered in the
// subject-layer group, behind scopeMW: a row's scope follows the same rules
// as a subject's — a scope writes itself, the master writes another scope
// only with the opt-in, a subject the caller cannot see answers like an
// unknown one.

// journalWriteRefusal is the 403 message of a refused journal write. The
// --cross-scope hint goes to the master caller only: the opt-in never widens a
// non-master caller (scope.CanWrite), so suggesting it there would mislead.
func journalWriteRefusal(caller, target scope.Scope) string {
	msg := fmt.Sprintf("a %s caller cannot write a %s journal entry", caller, target)
	if scope.SeesAll(caller) {
		msg += " (a master caller writing another scope: --cross-scope)"
	}
	return msg
}

// AddJournal handles POST /journal. 201 created, 200 already present (the
// stored row echoed, unchanged).
func AddJournal(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var e pigtail.JournalEntry
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "invalid JSON: "+err.Error(), nil)
			return
		}
		c := callerOf(r)
		// The alias and any case come in; only the canonical name goes
		// on: every comparison below is between canonical values.
		if strings.TrimSpace(e.Scope) != "" {
			// ParseFor: a scope this caller cannot read answers like an
			// unknown one, in both branches below (D19).
			sc, err := scope.ParseFor(c.scope, e.Scope)
			if err != nil {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "scope: "+err.Error(), nil)
				return
			}
			e.Scope = string(sc)
		}
		if err := e.Validate(); err != nil {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
			return
		}
		if e.SubjectID != "" {
			sub, ok := authorizeSubject(w, r, svc, resp, e.SubjectID, true)
			if !ok {
				return
			}
			subScope := sub.Scope
			if subScope == "" {
				subScope = string(scope.Master()) // themeless subject: fail-closed
			}
			if e.Scope == "" {
				e.Scope = subScope
			} else if e.Scope != subScope {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
					fmt.Sprintf("scope %s differs from the subject's scope %s", e.Scope, subScope), nil)
				return
			}
		} else {
			if e.Scope == "" {
				e.Scope = string(c.scope)
			}
			if err := scope.CanWrite(c.scope, scope.Scope(e.Scope), c.cross); err != nil {
				resp.Error(w, r, http.StatusForbidden, pigtail.ErrScopeMismatch,
					journalWriteRefusal(c.scope, scope.Scope(e.Scope)), nil)
				return
			}
		}
		if e.Timestamp.IsZero() {
			e.Timestamp = time.Now().UTC()
		}
		created, err := svc.Store.AddJournal(&e)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		resp.JSON(w, r, status, pigtail.JournalAddResult{Entry: e, Created: created})
	}
}

// ListJournal handles GET /journal?since=&until=&scope=&kind=&subject=&limit=.
// since/until are unix ms (since inclusive, until exclusive); scope only
// narrows what the caller may read (scope.ListFilter).
func ListJournal(svc *Services, resp Responder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		sc, err := scope.ListFilter(callerOf(r).scope, q.Get("scope"))
		if err != nil {
			scopeError(w, r, resp, err, "")
			return
		}
		f := store.JournalFilter{Scope: sc, Kind: q.Get("kind"), SubjectID: q.Get("subject"), Limit: pigtail.JournalListDefault}
		for _, p := range []struct {
			name string
			dst  *time.Time
		}{{"since", &f.Since}, {"until", &f.Until}} {
			v := q.Get(p.name)
			if v == "" {
				continue
			}
			ms, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, p.name+" must be unix millis", nil)
				return
			}
			*p.dst = time.UnixMilli(ms).UTC()
		}
		if !f.Since.IsZero() && !f.Until.IsZero() && !f.Until.After(f.Since) {
			resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, "until must be after since", nil)
			return
		}
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > pigtail.JournalListMax {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
					fmt.Sprintf("limit must be 1..%d", pigtail.JournalListMax), nil)
				return
			}
			f.Limit = n
		}
		out, err := svc.Store.ListJournal(f)
		if err != nil {
			resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
			return
		}
		resp.JSON(w, r, http.StatusOK, out)
	}
}
