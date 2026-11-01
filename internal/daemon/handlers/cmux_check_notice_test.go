package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func noticeRouter(seam func() (pigtail.CmuxCheckNotice, error)) *chi.Mux {
	r := chi.NewRouter()
	RegisterCmuxCheckNoticeRoutes(r, &Services{CmuxCheckNotice: seam}, testResponder{})
	return r
}

func getNotice(r http.Handler, scopeHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/cmux/check/notice", nil)
	if scopeHeader != "" {
		req.Header.Set(pigtail.HeaderScope, scopeHeader)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestCmuxCheckNoticeRoute(t *testing.T) {
	line := func() (pigtail.CmuxCheckNotice, error) { return pigtail.CmuxCheckNotice{LaunchPID: 7, Line: "x"}, nil }
	none := func() (pigtail.CmuxCheckNotice, error) { return pigtail.CmuxCheckNotice{}, nil }
	boom := func() (pigtail.CmuxCheckNotice, error) { return pigtail.CmuxCheckNotice{}, errors.New("boom") }
	cases := []struct {
		name   string
		seam   func() (pigtail.CmuxCheckNotice, error)
		header string
		code   int
		body   string // substring of the body
	}{
		{"master reads the line", line, string(scope.Master()), 200, `"line":"x"`},
		{"none", none, string(scope.Master()), 200, `"line":""`},
		{"a globex caller is refused", line, string(scope.Default()), 403, "master"},
		{"no header = the default scope = refused", line, "", 403, "master"},
		{"unreadable", boom, string(scope.Master()), 500, "boom"},
	}
	for _, c := range cases {
		rec := getNotice(noticeRouter(c.seam), c.header)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.body) {
			t.Errorf("%s: %d %s, want %d containing %q", c.name, rec.Code, rec.Body, c.code, c.body)
		}
		if c.code == 403 && errCode(rec) != string(pigtail.ErrScopeMismatch) {
			t.Errorf("%s: code %q, want SCOPE_MISMATCH", c.name, errCode(rec))
		}
		if c.code == 403 && strings.Contains(rec.Body.String(), `"x"`) {
			t.Errorf("%s: the line leaked to a refused caller", c.name)
		}
	}
}
