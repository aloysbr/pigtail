package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Scope isolation of the subject routes. The policy itself is pure
// (internal/scope); this file reads the caller's scope off the request and
// turns the policy's answers into responses:
//
//   - not visible → 404 with the exact body an unknown id gets, so a default-scope
//     caller cannot probe for perso ids;
//   - cross-scope → 403 SCOPE_MISMATCH;
//   - invalid scope, filter or vault link → 400 INVALID_REQUEST.

type callerKey struct{}

type webKey struct{}

// WithWeb marks ctx as the web front's: the daemon's cookie path sets
// it (docs/wire-protocol.md § Web (browser)), never a header the caller sends.
func WithWeb(ctx context.Context) context.Context {
	return context.WithValue(ctx, webKey{}, true)
}

// IsWeb reports whether ctx carries the web front mark (WithWeb).
func IsWeb(ctx context.Context) bool {
	v, _ := ctx.Value(webKey{}).(bool)
	return v
}

// callerScope is what scopeMW learnt about the request's sender.
type callerScope struct {
	scope scope.Scope
	cross bool // X-Pigtail-Cross-Scope: write
	web   bool // the web front's cookie (WithWeb)
}

// scopeMW reads X-Pigtail-Scope (absent = the default scope), X-Pigtail-Cross-Scope and
// the web front mark of the context into the request context. An unreadable scope is refused up front: guessing
// one would either leak perso or hide it for a reason the caller cannot see.
func scopeMW(resp Responder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sc, err := scope.FromHeader(r.Header.Get(pigtail.HeaderScope))
			if err != nil {
				resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest,
					pigtail.HeaderScope+": "+err.Error(), nil)
				return
			}
			c := callerScope{scope: sc, cross: r.Header.Get(pigtail.HeaderCrossScope) == pigtail.CrossScopeWrite,
				web: IsWeb(r.Context())}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, c)))
		})
	}
}

// IsScopeMW reports whether mw is a scopeMW: run on a probe request that
// names the master, it must hand the next handler a context carrying that
// caller. Identified by behaviour, not by function identity — the compiler
// inlines scopeMW, so two instances need not share a code pointer. The
// daemon's route guard (TestRoutesExposingSubjectsAreScoped) uses it to check,
// from the router itself, that a route classified as scoped sits behind it.
func IsScopeMW(mw func(http.Handler) http.Handler) bool {
	var seen bool
	probe := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		c, ok := r.Context().Value(callerKey{}).(callerScope)
		seen = ok && c.scope == scope.Master()
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(pigtail.HeaderScope, string(scope.Master()))
	mw(probe).ServeHTTP(httptest.NewRecorder(), req)
	return seen
}

// callerOf returns the request's caller scope. A request that did not go
// through scopeMW is the default scope, like one without the header.
func callerOf(r *http.Request) callerScope {
	if c, ok := r.Context().Value(callerKey{}).(callerScope); ok {
		return c
	}
	return callerScope{scope: scope.Default()}
}

// scopeError writes the response for a policy refusal. notFoundMsg is the
// message an unknown id gets on this route, reused verbatim for ErrNotVisible.
func scopeError(w http.ResponseWriter, r *http.Request, resp Responder, err error, notFoundMsg string) {
	switch {
	case errors.Is(err, scope.ErrNotVisible):
		resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, notFoundMsg, nil)
	case errors.Is(err, scope.ErrCrossScope):
		resp.Error(w, r, http.StatusForbidden, pigtail.ErrScopeMismatch, err.Error(), nil)
	case errors.Is(err, scope.ErrInvalid):
		resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, err.Error(), nil)
	default:
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
	}
}

// authorize checks the caller against an object of scope target, for a read
// or a write.
func authorize(c callerScope, target scope.Scope, write bool) error {
	if write {
		return scope.CanWrite(c.scope, target, c.cross)
	}
	if !scope.CanRead(c.scope, target) {
		return scope.ErrNotVisible
	}
	return nil
}

// authorizeSubject loads a subject the caller may read (or write) and writes
// the error response otherwise. ok=false means the response is written.
func authorizeSubject(w http.ResponseWriter, r *http.Request, svc *Services, resp Responder, id string, write bool) (*pigtail.Subject, bool) {
	const msg = "no such subject"
	sub, err := svc.Store.GetSubject(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, msg, nil)
			return nil, false
		}
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
		return nil, false
	}
	if err := authorize(callerOf(r), scope.Scope(sub.Scope), write); err != nil {
		scopeError(w, r, resp, err, msg)
		return nil, false
	}
	return sub, true
}

// authorizeTheme is authorizeSubject for a theme. notFoundMsg lets the
// subject routes keep their own wording for an unknown theme_id.
func authorizeTheme(w http.ResponseWriter, r *http.Request, svc *Services, resp Responder, id string, write bool, notFoundMsg string, details map[string]interface{}) (*pigtail.Theme, bool) {
	th, err := svc.Store.GetTheme(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, notFoundMsg, details)
			return nil, false
		}
		resp.Error(w, r, http.StatusInternalServerError, pigtail.ErrInternal, err.Error(), nil)
		return nil, false
	}
	err = authorize(callerOf(r), scope.Scope(th.Scope), write)
	if errors.Is(err, scope.ErrNotVisible) {
		resp.Error(w, r, http.StatusNotFound, pigtail.ErrNotFound, notFoundMsg, details)
		return nil, false
	}
	if err != nil {
		scopeError(w, r, resp, err, notFoundMsg)
		return nil, false
	}
	return th, true
}

// depError is a refusal of a newly added dependency (unknown id, self, cycle).
// Its text is the whole wire message, so it is never wrapped in scope.ErrInvalid
// (whose scopeError would prefix "invalid scope: ").
type depError struct{ msg string }

func (e *depError) Error() string { return e.msg }

// depFailure answers a checkDeps error: a *depError as 400 INVALID_REQUEST with
// its exact text, anything else (the cross-scope refusal, a store error)
// through scopeError.
func depFailure(w http.ResponseWriter, r *http.Request, resp Responder, err error) {
	var de *depError
	if errors.As(err, &de) {
		resp.Error(w, r, http.StatusBadRequest, pigtail.ErrInvalidRequest, de.msg, nil)
		return
	}
	scopeError(w, r, resp, err, "")
}

// checkDeps refuses, for each newly added dependency d of subject subjectID
// (empty on create: the self and cycle checks are then skipped): the subject's
// own id, an id that is no subject, a dependency from which the subject is
// reachable through deps (a cycle, named by its path of ids), and a subject of
// another scope (ranks and "next" are per scope, so a cross-scope edge could
// never be read coherently). Only newly added deps are checked: a stored one is
// grandfathered, so a legacy edge never blocks an unrelated edit.
func checkDeps(svc *Services, own scope.Scope, subjectID string, deps, existing []string) error {
	had := make(map[string]bool, len(existing))
	for _, d := range existing {
		had[d] = true
	}
	for _, d := range deps {
		if had[d] {
			continue
		}
		if subjectID != "" && d == subjectID {
			return &depError{msg: "a subject cannot depend on itself"}
		}
		sc, err := svc.Store.SubjectScope(d)
		if errors.Is(err, store.ErrNotFound) {
			return &depError{msg: "unknown dependency " + d}
		}
		if err != nil {
			return err
		}
		if sc != own {
			return fmt.Errorf("%w: dependency %s is not in this subject's scope (%s)", scope.ErrInvalid, d, own)
		}
		if subjectID == "" {
			continue
		}
		path, err := svc.Store.SubjectDepPath(d, subjectID)
		if err != nil {
			return err
		}
		if path != nil {
			return &depError{msg: "dependency cycle: " + subjectID + " → " + strings.Join(path, " → ")}
		}
	}
	return nil
}
