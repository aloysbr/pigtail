package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/backend"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/mainfresh"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// refreshingBackend is a Backend whose Create reports a scripted main-worktree
// verdict through the hook, the way the git backend does after fast-forwarding
// the checkout it is about to branch from.
type refreshingBackend struct {
	sentinelBackend
	verdict backend.MainRefresh
	called  bool
}

func (b *refreshingBackend) Create(_ context.Context, p *pigtail.Project, intent backend.CreateIntent) (*pigtail.Workspace, error) {
	if intent.OnMainRefresh != nil {
		b.called = true
		intent.OnMainRefresh(b.verdict)
	}
	now := time.Now().UTC()
	return &pigtail.Workspace{
		ID: "ws_new", ProjectID: p.ID, Intent: intent.Intent,
		State: pigtail.StateBuilding, Path: "/tmp/ws_new", BackendKind: "sentinel",
		CreatedAt: now, LastActivityAt: now,
	}, nil
}

func newCreateRouter(t *testing.T, bk backend.Backend, reg *mainfresh.Registry) (*chi.Mux, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateProject(&pigtail.Project{ID: "prj_1", Name: "globex", RootPath: "/tmp/ts"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	svc := &Services{Store: st, Backend: bk, Bus: events.New(), MainFresh: reg}
	r := chi.NewRouter()
	r.Post("/workspaces/new", NewWorkspace(svc, testResponder{}))
	return r, st
}

func createWorkspace(t *testing.T, r http.Handler) pigtail.Workspace {
	t.Helper()
	rec := doJSON(t, r, "POST", "/workspaces/new", pigtail.NewWorkspaceRequest{
		ProjectID: "prj_1", Intent: "an intent",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out pigtail.Workspace
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// TestNewWorkspaceReportsTheAbstentionItWasToldAbout: the create path knows
// the main checkout is 23 commits behind and that it declined to move it. That
// verdict used to end in daemon.log, where the person who typed `pigtail new`
// never saw it — which is how two months passed under a fix believed applied.
func TestNewWorkspaceReportsTheAbstentionItWasToldAbout(t *testing.T) {
	t.Parallel()
	bk := &refreshingBackend{verdict: backend.MainRefresh{
		Path: "/tmp/ts/main", Base: "origin/main", Behind: 23,
		Action: backend.MainRefreshSkipped,
		Reason: `checked out on "feat/x", not "main"`,
	}}
	r, _ := newCreateRouter(t, bk, mainfresh.New())

	ws := createWorkspace(t, r)

	if !bk.called {
		t.Fatal("the create path was given no way to report what it did to main")
	}
	if ws.MainRefresh == nil {
		t.Fatal("the create response carries no verdict")
	}
	if ws.MainRefresh.Behind != 23 || ws.MainRefresh.Action != pigtail.MainRefreshSkipped {
		t.Fatalf("verdict = %+v, want the abstention and its 23 commits of drift", ws.MainRefresh)
	}
	if ws.MainRefresh.Reason == "" {
		t.Error("an abstention without its reason is an unanswerable 'why is main behind?'")
	}
}

// TestNewWorkspaceDoesNotPersistTheVerdict: it describes a working tree at an
// instant, not the workspace. Storing it would let a claim about a checkout
// outlive the checkout's state — and a stale claim of freshness is worse than
// none. Same rule as DisplayRef: stamped after the write.
func TestNewWorkspaceDoesNotPersistTheVerdict(t *testing.T) {
	t.Parallel()
	bk := &refreshingBackend{verdict: backend.MainRefresh{
		Base: "origin/main", Action: backend.MainRefreshUpdated,
		Behind: 23, From: "aaaaaaa", To: "bbbbbbb",
	}}
	r, st := newCreateRouter(t, bk, mainfresh.New())

	created := createWorkspace(t, r)
	if created.MainRefresh == nil {
		t.Fatal("the response should carry the verdict")
	}

	stored, err := st.GetWorkspace("ws_new")
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if stored.MainRefresh != nil {
		t.Fatalf("the verdict was persisted: %+v", stored.MainRefresh)
	}
}

// TestNewWorkspacePublishesToTheFreshnessRegistry: a create measures the main
// checkout for real. `pigtail servers` should answer from that measurement
// rather than from the last sweep tick, which may be ten minutes old.
func TestNewWorkspacePublishesToTheFreshnessRegistry(t *testing.T) {
	t.Parallel()
	reg := mainfresh.New()
	bk := &refreshingBackend{verdict: backend.MainRefresh{
		Path: "/tmp/ts/main", Base: "origin/main", Behind: 23,
		Action: backend.MainRefreshSkipped, Reason: "working tree not pristine (1 uncommitted or untracked entry)",
	}}
	r, _ := newCreateRouter(t, bk, reg)

	createWorkspace(t, r)

	got := reg.Snapshot()
	if len(got) != 1 {
		t.Fatalf("registry holds %d entries after a create, want 1", len(got))
	}
	if got[0].ProjectName != "globex" || got[0].Behind != 23 {
		t.Fatalf("published %+v, want the verdict against the named project", got[0])
	}
}

// TestNewWorkspaceWithoutAVerdictCarriesNoNote: a backend with no shared
// checkout, or a create told not to fetch, must produce no note at all. An
// empty one would read as "checked, nothing to say".
func TestNewWorkspaceWithoutAVerdictCarriesNoNote(t *testing.T) {
	t.Parallel()
	r, _ := newCreateRouter(t, &refreshingBackend{}, mainfresh.New())

	if ws := createWorkspace(t, r); ws.MainRefresh != nil {
		t.Fatalf("a create that measured nothing reported %+v", ws.MainRefresh)
	}
}

// TestNewWorkspaceSurvivesAMissingRegistry: MainFresh is optional wiring.
func TestNewWorkspaceSurvivesAMissingRegistry(t *testing.T) {
	t.Parallel()
	bk := &refreshingBackend{verdict: backend.MainRefresh{
		Base: "origin/main", Action: backend.MainRefreshUpToDate,
	}}
	r, _ := newCreateRouter(t, bk, nil)

	if ws := createWorkspace(t, r); ws.MainRefresh == nil {
		t.Fatal("a nil registry must not stop the note from reaching the response")
	}
}
