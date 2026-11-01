package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func TestThemeScopeCheckConstraint(t *testing.T) {
	s := newTestStore(t)
	// The closed list lives in the scope table now: the schema checks the
	// shape only, and refuses ''.
	for _, bad := range []string{"", "Two Words"} {
		_, err := s.db.Exec(`INSERT INTO themes (id, name, kind, vault_link, archived, created_at, scope) VALUES ('thm_bad', 'b', 'area', 'Globex/x', 0, 1, ?)`, bad)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "check") {
			t.Fatalf("scope %q must violate the CHECK, got %v", bad, err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO themes (id, name, kind, vault_link, archived, created_at, scope) VALUES ('thm_work', 'w', 'area', 'WORK/x', 0, 1, 'work')`); err != nil {
		t.Fatalf("a well-formed scope name fits the schema: %v", err)
	}
}

// seedScoped creates one work theme (thm_t) and one perso theme (thm_p).
func seedScoped(t *testing.T, s *Store) {
	t.Helper()
	for _, th := range []*pigtail.Theme{
		{ID: "thm_t", Name: "T", Kind: "project", VaultLink: "Globex/Projects/t", CreatedAt: time.UnixMilli(1).UTC()},
		{ID: "thm_p", Name: "P", Kind: "area", VaultLink: "Perso/Areas/p", CreatedAt: time.UnixMilli(2).UTC()},
	} {
		if err := s.CreateTheme(th); err != nil {
			t.Fatalf("seed theme %s: %v", th.ID, err)
		}
	}
}

func TestCreateThemeDerivesScope(t *testing.T) {
	s := newTestStore(t)
	seedScoped(t, s)
	if th, _ := s.GetTheme("thm_t"); th.Scope != "globex" {
		t.Fatalf("work theme scope = %q", th.Scope)
	}
	if th, _ := s.GetTheme("thm_p"); th.Scope != "perso" {
		t.Fatalf("perso theme scope = %q", th.Scope)
	}
	work, err := s.ListThemes(scope.Scope("globex"))
	if err != nil || len(work) != 1 || work[0].ID != "thm_t" {
		t.Fatalf("ListThemes(work) = %+v, %v", work, err)
	}
	perso, _ := s.ListThemes(scope.Scope("perso"))
	if len(perso) != 1 || perso[0].ID != "thm_p" {
		t.Fatalf("ListThemes(perso) = %+v", perso)
	}
	all, _ := s.ListThemes(scope.Any)
	if len(all) != 2 {
		t.Fatalf("ListThemes(any) = %d, want 2", len(all))
	}
}

func TestSubjectScopeFilters(t *testing.T) {
	s := newTestStore(t)
	seedScoped(t, s)
	for _, sub := range []*pigtail.Subject{
		mkSubject("t1", "thm_t", pigtail.StatusInbox, 2),
		mkSubject("t2", "thm_t", pigtail.StatusInbox, 1),
		mkSubject("p1", "thm_p", pigtail.StatusInbox, 0),
		mkSubject("p2", "thm_p", pigtail.StatusBuilding, 1),
	} {
		if err := s.CreateSubject(sub); err != nil {
			t.Fatalf("create %s: %v", sub.ID, err)
		}
	}

	got, _ := s.GetSubject("p1")
	if got.Scope != "perso" {
		t.Fatalf("GetSubject scope = %q, want perso (derived from theme)", got.Scope)
	}
	if sc, err := s.SubjectScope("t1"); err != nil || sc != scope.Scope("globex") {
		t.Fatalf("SubjectScope(t1) = %q, %v", sc, err)
	}
	if _, err := s.SubjectScope("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SubjectScope(unknown) = %v, want ErrNotFound", err)
	}

	ids := func(subs []pigtail.Subject) string {
		var out []string
		for _, s := range subs {
			out = append(out, s.ID)
		}
		return strings.Join(out, ",")
	}
	work, _ := s.ListSubjects(SubjectFilter{Scope: scope.Scope("globex")})
	if ids(work) != "t2,t1" {
		t.Fatalf("work list = %s, want t2,t1", ids(work))
	}
	perso, _ := s.ListSubjects(SubjectFilter{Scope: scope.Scope("perso")})
	if ids(perso) != "p2,p1" {
		t.Fatalf("perso list = %s, want p2,p1", ids(perso))
	}
	// Any: grouped by scope, then rank (unset last), then created_at.
	all, _ := s.ListSubjects(SubjectFilter{})
	if ids(all) != "t2,t1,p2,p1" {
		t.Fatalf("all list = %s, want t2,t1,p2,p1", ids(all))
	}
	for _, sub := range all {
		if sub.Scope == "" {
			t.Fatalf("list must carry the derived scope: %+v", sub)
		}
	}
	inbox, _ := s.ListSubjects(SubjectFilter{Scope: scope.Scope("perso"), Status: pigtail.StatusInbox})
	if ids(inbox) != "p1" {
		t.Fatalf("perso inbox = %s, want p1", ids(inbox))
	}
}

func TestNextSubjectIgnoresOtherScope(t *testing.T) {
	s := newTestStore(t)
	seedScoped(t, s)
	for _, sub := range []*pigtail.Subject{
		mkSubject("p_top", "thm_p", pigtail.StatusInbox, 1), // better rank, other scope
		mkSubject("t_mid", "thm_t", pigtail.StatusInbox, 5),
	} {
		if err := s.CreateSubject(sub); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.NextSubject(scope.Scope("globex"))
	if err != nil || got.ID != "t_mid" {
		t.Fatalf("NextSubject(work) = %+v, %v; want t_mid", got, err)
	}
	got, err = s.NextSubject(scope.Scope("perso"))
	if err != nil || got.ID != "p_top" {
		t.Fatalf("NextSubject(perso) = %+v, %v; want p_top", got, err)
	}
}

func TestGetPhase(t *testing.T) {
	s := newTestStore(t)
	seedScoped(t, s)
	if err := s.CreateSubject(mkSubject("p1", "thm_p", pigtail.StatusInbox, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePhase(&pigtail.SubjectPhase{ID: "phs_1", SubjectID: "p1", Kind: "research", State: "pending", CreatedAt: time.UnixMilli(3)}); err != nil {
		t.Fatal(err)
	}
	ph, err := s.GetPhase("phs_1")
	if err != nil || ph.SubjectID != "p1" {
		t.Fatalf("GetPhase = %+v, %v", ph, err)
	}
	if _, err := s.GetPhase("phs_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetPhase(unknown) = %v, want ErrNotFound", err)
	}
}
