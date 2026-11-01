package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// foldersCtl is the sidebar loop stand-in that can also toggle the folders.
type foldersCtl struct {
	*fakeSidebarCtl
	res     pigtail.CmuxFoldersToggle
	err     error
	calls   int
	waitCtx bool // like the runner: no answer before the caller's context ends
}

func (f *foldersCtl) ToggleFolders(ctx context.Context) (pigtail.CmuxFoldersToggle, error) {
	f.calls++
	if f.waitCtx {
		<-ctx.Done()
		return pigtail.CmuxFoldersToggle{}, ctx.Err()
	}
	return f.res, f.err
}

func foldersRouter(t *testing.T, ctl CmuxSidebarControl) *chi.Mux {
	t.Helper()
	_, st := scopedFixture(t)
	r := chi.NewRouter()
	svc := &Services{Store: st}
	if ctl != nil {
		svc.CmuxSidebar = ctl
	}
	RegisterSubjectRoutes(r, svc, testResponder{})
	return r
}

func TestCmuxFoldersToggle(t *testing.T) {
	t.Parallel()
	ctl := &foldersCtl{fakeSidebarCtl: &fakeSidebarCtl{}, res: pigtail.CmuxFoldersToggle{Collapsed: true, Folders: 3}}
	rec := doAs(t, foldersRouter(t, ctl), none, "POST", "/cmux/folders/toggle", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"collapsed":true,"folders":3}` {
		t.Fatalf("body = %s", got)
	}
	if ctl.calls != 1 {
		t.Fatalf("calls = %d", ctl.calls)
	}
}

func TestCmuxFoldersToggleIdleIs409(t *testing.T) {
	t.Parallel()
	idle := &foldersCtl{fakeSidebarCtl: &fakeSidebarCtl{}, err: pigtail.ErrCmuxFoldersIdle}
	for name, ctl := range map[string]CmuxSidebarControl{
		"runner says idle":   idle,
		"no loop in daemon":  nil,
		"loop cannot toggle": &fakeSidebarCtl{}, // an older stand-in without ToggleFolders
	} {
		rec := doAs(t, foldersRouter(t, ctl), none, "POST", "/cmux/folders/toggle", nil)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "folders idle") {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestCmuxFoldersToggleCanceledRequestIsNot200(t *testing.T) {
	t.Parallel()
	ctl := &foldersCtl{fakeSidebarCtl: &fakeSidebarCtl{}, waitCtx: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, "POST", "/cmux/folders/toggle", nil)
	rec := httptest.NewRecorder()
	foldersRouter(t, ctl).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable && rec.Code != 499 {
		t.Fatalf("a canceled request answered %d %s, want 503 or 499", rec.Code, rec.Body)
	}
}
