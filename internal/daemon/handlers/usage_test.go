package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func usageRouter(fn func(time.Time) (pigtail.UsageOverview, error)) *chi.Mux {
	r := chi.NewRouter()
	RegisterUsageRoutes(r, &Services{UsageOverview: fn}, testResponder{})
	return r
}

func getUsage(r http.Handler, scopeHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/usage/overview", nil)
	if scopeHeader != "" {
		req.Header.Set(pigtail.HeaderScope, scopeHeader)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestUsageRouteMasterOnly(t *testing.T) {
	t.Parallel()
	called := false
	r := usageRouter(func(now time.Time) (pigtail.UsageOverview, error) {
		called = true
		return pigtail.UsageOverview{GeneratedAt: now, Accounts: []pigtail.UsageAccount{{Label: "m"}}}, nil
	})
	for _, h := range []string{"gx", ""} { // the default scope is not the master
		rec := getUsage(r, h)
		if rec.Code != http.StatusForbidden || errCode(rec) != string(pigtail.ErrScopeMismatch) ||
			!strings.Contains(rec.Body.String(), "master scope only") {
			t.Fatalf("scope %q: %d %s, want 403 SCOPE_MISMATCH", h, rec.Code, rec.Body)
		}
	}
	if called {
		t.Fatal("the source was read for a non-master caller")
	}
	rec := getUsage(r, string(scope.Master()))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"label":"m"`) {
		t.Fatalf("master: %d %s", rec.Code, rec.Body)
	}
}

func TestUsageRouteUnwired(t *testing.T) {
	t.Parallel()
	rec := getUsage(usageRouter(nil), string(scope.Master()))
	if rec.Code != http.StatusNotImplemented || errCode(rec) != string(pigtail.ErrUsageUnavailable) {
		t.Fatalf("%d %s, want 501 USAGE_UNAVAILABLE", rec.Code, rec.Body)
	}
}

func TestUsageRouteSourceError(t *testing.T) {
	t.Parallel()
	r := usageRouter(func(time.Time) (pigtail.UsageOverview, error) {
		return pigtail.UsageOverview{}, errors.New("cell table: no claude-cells.yaml")
	})
	rec := getUsage(r, string(scope.Master()))
	if rec.Code != http.StatusServiceUnavailable || errCode(rec) != string(pigtail.ErrUsageUnavailable) ||
		!strings.Contains(rec.Body.String(), "claude-cells.yaml") {
		t.Fatalf("%d %s, want 503 USAGE_UNAVAILABLE with the reason", rec.Code, rec.Body)
	}
}
