package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func rolesRouter(t *testing.T, snap func() ([]pigtail.RoleStatus, bool)) (http.Handler, *store.Store) {
	t.Helper()
	r, st, svc := scopedFixtureSvc(t)
	svc.RoleSnapshot = snap
	RegisterRoleRoutes(r, svc, testResponder{})
	RegisterAlertRoutes(r, svc, testResponder{})
	return r, st
}

func addRole(t *testing.T, r http.Handler, c caller, name, subject string) *httptest.ResponseRecorder {
	t.Helper()
	return doAs(t, r, c, "POST", "/roles", pigtail.RoleRequest{Name: name, SubjectID: subject, CadenceMS: (30 * time.Minute).Milliseconds()})
}

func TestRolesAddListRemove(t *testing.T) {
	t.Parallel()
	r, _ := rolesRouter(t, nil)
	if rec := addRole(t, r, perso, "pilote", "sub_p"); rec.Code != 200 {
		t.Fatalf("add = %d %s", rec.Code, rec.Body)
	}
	rec := doAs(t, r, perso, "GET", "/roles", nil)
	var got pigtail.RolesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Roles) != 1 {
		t.Fatalf("list = %s %v", rec.Body, err)
	}
	ro := got.Roles[0]
	if ro.Name != "pilote" || ro.Scope != "perso" || ro.Cadence != 30*time.Minute || ro.Grace != 10*time.Minute || ro.Stall != 20*time.Minute {
		t.Fatalf("role = %+v", ro)
	}
	if rec := doAs(t, r, perso, "DELETE", "/roles/pilote", nil); rec.Code != 200 {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if rec := doAs(t, r, perso, "DELETE", "/roles/pilote", nil); rec.Code != 404 {
		t.Fatalf("second delete = %d", rec.Code)
	}
	rec = doAs(t, r, perso, "GET", "/roles", nil)
	if !strings.Contains(rec.Body.String(), `"roles":[]`) {
		t.Fatalf("empty list must be [], got %s", rec.Body)
	}
}

func TestRolesAddScopeIsSubjects(t *testing.T) {
	t.Parallel()
	r, st := rolesRouter(t, nil)
	// The master caller writing a work subject's role needs the opt-in; with it the role is the subject's.
	rec := doAs(t, r, caller{scope: "perso", cross: true}, "POST", "/roles",
		pigtail.RoleRequest{Name: "tr", SubjectID: "sub_t", CadenceMS: 60000})
	if rec.Code != 200 {
		t.Fatalf("add = %d %s", rec.Code, rec.Body)
	}
	rs, err := st.ListRoles(scope.Any)
	if err != nil || len(rs) != 1 || rs[0].Scope == "perso" {
		t.Fatalf("roles = %+v, %v (scope must be the subject's, not the caller's)", rs, err)
	}
	if addRole(t, r, perso, "pr", "sub_p").Code != 200 {
		t.Fatal("perso role on perso subject")
	}
	rs, _ = st.ListRoles("perso")
	if len(rs) != 1 || rs[0].Name != "pr" {
		t.Fatalf("perso roles = %+v", rs)
	}
}

func TestRolesAddRefusesForeignSubject(t *testing.T) {
	t.Parallel()
	r, _ := rolesRouter(t, nil)
	foreign := doAs(t, r, work, "POST", "/roles", pigtail.RoleRequest{Name: "x", SubjectID: "sub_p", CadenceMS: 60000})
	unknown := doAs(t, r, work, "POST", "/roles", pigtail.RoleRequest{Name: "x", SubjectID: "sub_nope", CadenceMS: 60000})
	if foreign.Code != 404 || unknown.Code != 404 || foreign.Body.String() != unknown.Body.String() {
		t.Fatalf("foreign = %d %s, unknown = %d %s", foreign.Code, foreign.Body, unknown.Code, unknown.Body)
	}
}

func TestRolesAddValidates(t *testing.T) {
	t.Parallel()
	r, _ := rolesRouter(t, nil)
	for name, req := range map[string]pigtail.RoleRequest{
		"bad name":     {Name: "Bad Name", SubjectID: "sub_p", CadenceMS: 60000},
		"no cadence":   {Name: "ok", SubjectID: "sub_p"},
		"neg grace":    {Name: "ok", SubjectID: "sub_p", CadenceMS: 1000, GraceMS: -1},
		"bad subject":  {Name: "ok", SubjectID: "no", CadenceMS: 1000},
		"name too big": {Name: strings.Repeat("a", 33), SubjectID: "sub_p", CadenceMS: 1000},
	} {
		if rec := doAs(t, r, perso, "POST", "/roles", req); rec.Code != 400 {
			t.Errorf("%s = %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
}

func TestRolesWritesFrozen(t *testing.T) {
	t.Parallel()
	r, _ := frozenRouterWith(t, frozen())
	for _, w := range [][2]string{{"POST", "/roles"}, {"DELETE", "/roles/pilote"}} {
		rec := req(r, w[0], w[1], "other")
		if rec.Code != http.StatusLocked || !strings.Contains(rec.Body.String(), "MAINTENANCE_FROZEN") {
			t.Fatalf("%s %s under freeze: %d %s, want 423", w[0], w[1], rec.Code, rec.Body)
		}
	}
	if rec := req(r, "GET", "/roles", "other"); rec.Code == http.StatusLocked {
		t.Fatal("GET /roles must be served under a freeze")
	}
}

func frozenRouterWith(t *testing.T, st maintenance.State) (*chi.Mux, string) {
	t.Helper()
	r, dir := frozenRouter(t, st)
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	RegisterRoleRoutes(r, &Services{Store: db, Maintenance: maintenance.NewWatch(dir)}, testResponder{})
	return r, dir
}

func TestRolesRemoveClosesEpisodes(t *testing.T) {
	t.Parallel()
	r, st := rolesRouter(t, nil)
	addRole(t, r, perso, "pilote", "sub_p")
	key := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	id := seedRoleAlert(t, st, "perso", "pilote", "sub_p", key, key, "silent")
	if rec := doAs(t, r, perso, "DELETE", "/roles/pilote", nil); rec.Code != 200 {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	un, err := st.UnrecoveredRoleAlerts(scope.Any)
	if err != nil || len(un) != 0 {
		t.Fatalf("unrecovered after rm = %+v, %v", un, err)
	}
	keys, _ := st.RecoveredRoleKeys("perso", "pilote")
	if len(keys) != 1 || !keys[0].Equal(key) {
		t.Fatalf("recovered keys = %v", keys)
	}
	es, _ := st.ListJournal(store.JournalFilter{Scope: scope.Any, Kind: "alert-resolved"})
	if len(es) != 1 || es[0].ID != "alert-resolved:"+id {
		t.Fatalf("resolutions = %+v", es)
	}
}

func rolesStatusOf(t *testing.T, r http.Handler, c caller) pigtail.RolesStatusResponse {
	t.Helper()
	rec := doAs(t, r, c, "GET", "/roles/status", nil)
	var out pigtail.RolesStatusResponse
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	return out
}

func TestRolesStatusWatchOff(t *testing.T) {
	t.Parallel()
	r, _ := rolesRouter(t, nil)
	addRole(t, r, perso, "pilote", "sub_p")
	out := rolesStatusOf(t, r, perso)
	if out.Watch || len(out.Roles) != 1 || out.Roles[0].State != "unknown" || !strings.Contains(out.Roles[0].Reason, "watch is off") {
		t.Fatalf("status = %+v", out)
	}
}

func TestRolesStatusNotJudgedYetAndJudged(t *testing.T) {
	t.Parallel()
	var snap []pigtail.RoleStatus
	r, _ := rolesRouter(t, func() ([]pigtail.RoleStatus, bool) { return snap, true })
	addRole(t, r, perso, "pilote", "sub_p")
	out := rolesStatusOf(t, r, perso)
	if !out.Watch || out.Roles[0].State != "unknown" || out.Roles[0].Reason != "not judged yet" {
		t.Fatalf("before a pass = %+v", out)
	}
	snap = []pigtail.RoleStatus{{Role: pigtail.Role{Scope: "perso", Name: "pilote"}, State: "silent", Reason: "wakeup due 11:31 never fired"}}
	out = rolesStatusOf(t, r, perso)
	if out.Roles[0].State != "silent" || out.Roles[0].Role.SubjectID != "sub_p" {
		t.Fatalf("after a pass = %+v", out)
	}
}

func TestRolesStatusScoped(t *testing.T) {
	t.Parallel()
	r, _ := rolesRouter(t, func() ([]pigtail.RoleStatus, bool) { return nil, true })
	addRole(t, r, perso, "pilote", "sub_p")
	doAs(t, r, caller{scope: "perso", cross: true}, "POST", "/roles", pigtail.RoleRequest{Name: "tr", SubjectID: "sub_t", CadenceMS: 60000})
	if got := rolesStatusOf(t, r, work); len(got.Roles) != 1 || got.Roles[0].Role.Name != "tr" {
		t.Fatalf("work sees %+v", got)
	}
	if got := rolesStatusOf(t, r, perso); len(got.Roles) != 2 {
		t.Fatalf("master sees %+v", got)
	}
}
