package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// La friction que ces tests épinglent (mesurée le 2026-09-04) : ré-enregistrer un
// projet connu était un no-op qui répondait « registered ». Le daemon lisant ses
// réglages en base, éditer `pigtail.yaml` après le premier enregistrement ne
// changeait donc RIEN — un `base_ref: origin/main` a dormi deux mois dans un yaml
// pendant que les worktrees continuaient de brancher 27, 47 puis 53 commits
// derrière un `main` local périmé.

func newProjectServices(t *testing.T) *Services {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Services{Store: st, Bus: events.New()}
}

func postProject(t *testing.T, svc *Services, body string) (*httptest.ResponseRecorder, pigtail.Project) {
	t.Helper()
	h := CreateProject(svc, testResponder{})
	req := httptest.NewRequest("POST", "/projects", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h(rec, req)
	var got pigtail.Project
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	return rec, got
}

// TestReregisterRefreshesConfigOwnedFields est le test de régression : un second
// enregistrement doit écrire les réglages que le fichier de config porte.
func TestReregisterRefreshesConfigOwnedFields(t *testing.T) {
	t.Parallel()
	svc := newProjectServices(t)

	rec, _ := postProject(t, svc, `{"id":"prj_x","name":"x","root_path":"/tmp/x",
		"main_worktree":"main","default_backend":"git",
		"port_ranges":{"dev":[3000,3099]},
		"defaults":{"agent":"claude","base_ref":"main","branch_prefix":"feat/"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("premier enregistrement : status %d", rec.Code)
	}

	// Le yaml a changé : la base doit suivre.
	rec, got := postProject(t, svc, `{"id":"prj_x","name":"x","root_path":"/tmp/x",
		"main_worktree":"main","default_backend":"git",
		"port_ranges":{"dev":[3000,3099]},
		"defaults":{"agent":"claude","base_ref":"origin/main","branch_prefix":"feat/"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("ré-enregistrement : status %d", rec.Code)
	}
	if got.Defaults == nil || got.Defaults.BaseRef != "origin/main" {
		t.Errorf("réponse : base_ref = %+v, want origin/main", got.Defaults)
	}

	stored, err := svc.Store.GetProject("prj_x")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Defaults == nil || stored.Defaults.BaseRef != "origin/main" {
		t.Errorf("en base : base_ref = %+v, want origin/main — le yaml est resté décoratif",
			stored.Defaults)
	}
}

// TestReregisterPreservesDaemonOwnedFields garde l'autre bord : le fichier de
// config ne connaît ni le profil d'agent ni le remote, donc un ré-enregistrement
// ne doit pas les effacer au passage.
func TestReregisterPreservesDaemonOwnedFields(t *testing.T) {
	t.Parallel()
	svc := newProjectServices(t)

	if err := svc.Store.CreateProject(&pigtail.Project{
		ID: "prj_y", Name: "y", RootPath: "/tmp/y", MainWorktree: "main",
		DefaultBackend: "git", DefaultProfileID: "prof_abc",
		Remote:     &pigtail.Remote{Kind: "github", Owner: "acme", Repo: "y"},
		PortRanges: map[string]pigtail.PortRange{"dev": {3000, 3099}},
		Defaults:   &pigtail.ProjectDefaults{BaseRef: "main"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Un payload issu du yaml : il ne porte ni profil ni remote.
	postProject(t, svc, `{"id":"prj_y","name":"y","root_path":"/tmp/y",
		"main_worktree":"main","default_backend":"git",
		"port_ranges":{"dev":[3000,3099]},
		"defaults":{"base_ref":"origin/main"}}`)

	stored, err := svc.Store.GetProject("prj_y")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.DefaultProfileID != "prof_abc" {
		t.Errorf("default_profile_id effacé : %q", stored.DefaultProfileID)
	}
	if stored.Remote == nil || stored.Remote.Owner != "acme" {
		t.Errorf("remote effacé : %+v", stored.Remote)
	}
	if stored.Defaults == nil || stored.Defaults.BaseRef != "origin/main" {
		t.Errorf("base_ref non rafraîchi : %+v", stored.Defaults)
	}
}

// TestUpdateProjectConfigUnknownProject : mettre à jour un projet inconnu doit
// se dire, pas passer inaperçu.
func TestUpdateProjectConfigUnknownProject(t *testing.T) {
	t.Parallel()
	svc := newProjectServices(t)
	err := svc.Store.UpdateProjectConfig(&pigtail.Project{ID: "prj_absent", Name: "n"})
	if err == nil {
		t.Fatal("un projet inconnu doit remonter une erreur, pas un succès silencieux")
	}
}
