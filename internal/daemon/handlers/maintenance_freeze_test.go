package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
	"github.com/go-chi/chi/v5"
)

func osWriteFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }

func frozenRouter(t *testing.T, st maintenance.State) (*chi.Mux, string) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	dir := t.TempDir()
	if st.ID != "" {
		_ = maintenance.Save(dir, st)
	}
	svc := &Services{Store: db, Maintenance: maintenance.NewWatch(dir)}
	r := chi.NewRouter()
	RegisterSubjectRoutes(r, svc, testResponder{})
	r.With(FreezeMW(svc, testResponder{})).Post("/workspaces/new", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(299) })
	r.With(FreezeMW(svc, testResponder{})).Post("/dispatch", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(299) })
	return r, dir
}

func req(r http.Handler, method, path, session string) *httptest.ResponseRecorder {
	q := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	q.Header.Set(pigtail.HeaderScope, "perso")
	if session != "" {
		q.Header.Set(pigtail.HeaderSession, session)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, q)
	return rec
}

var frozenWrites = [][2]string{
	{"POST", "/themes"}, {"PATCH", "/themes/thm_x"}, {"DELETE", "/themes/thm_x"},
	{"POST", "/subjects"}, {"POST", "/subjects/sub_x"}, {"POST", "/subjects/sub_x/phases"},
	{"POST", "/phases/phs_x"}, {"POST", "/subjects/sub_x/log"}, {"POST", "/journal"}, {"POST", "/workspaces/new"},
	{"POST", "/dispatch"}, // D10
}

func frozen() maintenance.State {
	return maintenance.State{ID: "mnt_a", On: true, Freeze: true, Reason: "bascule", Exempt: "lead", Since: time.Now()}
}

func TestFreezeRefusesEveryWrite(t *testing.T) {
	t.Parallel()
	r, _ := frozenRouter(t, frozen())
	for _, w := range frozenWrites {
		rec := req(r, w[0], w[1], "other")
		if rec.Code != http.StatusLocked || !strings.Contains(rec.Body.String(), "MAINTENANCE_FROZEN") ||
			!strings.Contains(rec.Body.String(), "bascule") || !strings.Contains(rec.Body.String(), "pigtail maintenance off") {
			t.Fatalf("%s %s: %d %s", w[0], w[1], rec.Code, rec.Body)
		}
	}
	if rec := req(r, "GET", "/subjects", "other"); rec.Code == http.StatusLocked {
		t.Fatal("reads must pass")
	}
}

func TestFreezeExemptSessionPasses(t *testing.T) {
	t.Parallel()
	r, _ := frozenRouter(t, frozen())
	for _, w := range frozenWrites {
		if rec := req(r, w[0], w[1], "lead"); rec.Code == http.StatusLocked {
			t.Fatalf("exempt refused on %s %s", w[0], w[1])
		}
	}
}

func TestNoFreezeFlagNoFreeze(t *testing.T) {
	t.Parallel()
	st := frozen()
	st.Freeze = false
	r, _ := frozenRouter(t, st)
	if rec := req(r, "POST", "/workspaces/new", "other"); rec.Code != 299 {
		t.Fatalf("got %d", rec.Code)
	}
}

// D8: closed everywhere, the exempt session included, reads untouched.
func TestFreezeCorruptStateClosed(t *testing.T) {
	t.Parallel()
	r, dir := frozenRouter(t, frozen())
	_ = osWriteFile(dir+"/"+maintenance.FileName, "{tronqué")
	for _, sess := range []string{"other", "lead"} {
		for _, w := range frozenWrites {
			rec := req(r, w[0], w[1], sess)
			b := rec.Body.String()
			if rec.Code != http.StatusLocked || !strings.Contains(b, "unreadable") ||
				!strings.Contains(b, dir+"/maintenance.json") || !strings.Contains(b, "pigtail maintenance off") {
				t.Fatalf("%s %s as %s: %d %s", w[0], w[1], sess, rec.Code, b)
			}
		}
	}
	if rec := req(r, "GET", "/subjects", "other"); rec.Code == http.StatusLocked {
		t.Fatal("reads must pass")
	}
}

// Lifecycle: `off` deletes the state file; nothing stays frozen after it.
func TestFreezeAfterOffNotFrozen(t *testing.T) {
	t.Parallel()
	r, dir := frozenRouter(t, frozen())
	if err := maintenance.Lift(dir, "mnt_a", time.Now(), "", "", ""); err != nil {
		t.Fatal(err)
	}
	if rec := req(r, "POST", "/workspaces/new", "other"); rec.Code != 299 {
		t.Fatalf("after off: %d", rec.Code)
	}
}

// One log line per distinct error, not one per refused request.
func TestFreezeUnreadableLogsOncePerError(t *testing.T) {
	testutil.SerialOnly(t) // resets a package variable
	unreadableLog = distinctLog{}
	var logged []string
	db, _ := store.Open(":memory:")
	defer db.Close()
	dir := t.TempDir()
	_ = maintenance.Save(dir, frozen())
	svc := &Services{Store: db, Maintenance: maintenance.NewWatch(dir),
		Logf: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }}
	r := chi.NewRouter()
	r.With(FreezeMW(svc, testResponder{})).Post("/w", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(299) })
	_ = osWriteFile(dir+"/"+maintenance.FileName, "{")
	for i := 0; i < 5; i++ {
		req(r, "POST", "/w", "other")
	}
	if len(logged) != 1 {
		t.Fatalf("same error logged %d times", len(logged))
	}
	_ = osWriteFile(dir+"/"+maintenance.FileName, "[1]") // another error
	req(r, "POST", "/w", "other")
	if len(logged) != 2 {
		t.Fatalf("a new error must log once more, got %d", len(logged))
	}
}

func TestFreezeAbsentFileNotFrozen(t *testing.T) {
	t.Parallel()
	r, _ := frozenRouter(t, maintenance.State{}) // ID vide : rien n'est écrit
	if rec := req(r, "POST", "/workspaces/new", "other"); rec.Code != 299 {
		t.Fatalf("no file = no maintenance, got %d", rec.Code)
	}
}

func TestFreezeUnwiredIsLoud(t *testing.T) {
	testutil.SerialOnly(t) // resets a package variable
	unwiredOnce = sync.Once{}
	var logged []string
	svc := &Services{Logf: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }}
	r := chi.NewRouter()
	r.With(FreezeMW(svc, testResponder{})).Post("/workspaces/new", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(299) })
	for i := 0; i < 2; i++ {
		rec := req(r, "POST", "/workspaces/new", "other")
		if rec.Code != 299 || rec.Header().Get("X-Pigtail-Freeze") != "unwired" {
			t.Fatalf("unwired: %d %v", rec.Code, rec.Header())
		}
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "writes are NOT guarded") {
		t.Fatalf("log once, got %q", logged)
	}
}

func TestIsFreezeMW(t *testing.T) {
	t.Parallel()
	if !IsFreezeMW(FreezeMW(&Services{}, testResponder{})) || IsFreezeMW(scopeMW(testResponder{})) {
		t.Fatal("IsFreezeMW")
	}
}

// A good read in between resets the once-per-error log: the same error that
// comes back after a repair is a new incident and logs again.
func TestFreezeUnreadableLogResetsAfterGoodRead(t *testing.T) {
	testutil.SerialOnly(t) // resets a package variable
	unreadableLog = distinctLog{}
	var logged []string
	db, _ := store.Open(":memory:")
	defer db.Close()
	dir := t.TempDir()
	_ = maintenance.Save(dir, frozen())
	svc := &Services{Store: db, Maintenance: maintenance.NewWatch(dir),
		Logf: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }}
	r := chi.NewRouter()
	r.With(FreezeMW(svc, testResponder{})).Post("/w", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(299) })
	_ = osWriteFile(dir+"/"+maintenance.FileName, "{")
	req(r, "POST", "/w", "other")
	_ = maintenance.Save(dir, frozen()) // repaired: a readable state
	req(r, "POST", "/w", "other")
	_ = osWriteFile(dir+"/"+maintenance.FileName, "{") // the same error again
	req(r, "POST", "/w", "other")
	if len(logged) != 2 {
		t.Fatalf("the same error after a good read must log again, got %d: %q", len(logged), logged)
	}
}

func TestFreezeOwnerSessionPasses(t *testing.T) {
	t.Parallel()
	st := frozen()
	st.Owner = "owner"
	r, _ := frozenRouter(t, st)
	for _, w := range frozenWrites {
		if rec := req(r, w[0], w[1], "owner"); rec.Code == http.StatusLocked {
			t.Fatalf("owner refused on %s %s", w[0], w[1])
		}
		if rec := req(r, w[0], w[1], "other"); rec.Code != http.StatusLocked || !strings.Contains(rec.Body.String(), "pigtail maintenance off") {
			t.Fatalf("other passed on %s %s", w[0], w[1])
		}
	}
}

// D4: a session the freeze refuses gets a marker the sidebar reads as
// "Blocked · freeze"; the owner, the exempt session, a header-less caller
// and an unreadable state leave none (D10).
func TestFreezeMarksTheRefusedSession(t *testing.T) {
	t.Parallel()
	st := maintenance.State{ID: "mnt_t", On: true, Freeze: true, Reason: "bascule", Exempt: "lead", Owner: "own", Since: time.Now()}
	r, dir := frozenRouter(t, st)
	marker := func(s string) string {
		raw, err := os.ReadFile(dir + "/" + maintenance.FrozenDirName + "/" + s)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(raw))
	}
	if rec := req(r, "POST", "/workspaces/new", "s1"); rec.Code != http.StatusLocked || marker("s1") != "mnt_t" {
		t.Fatalf("refused session: %d, marker %q", rec.Code, marker("s1"))
	}
	if rec := req(r, "POST", "/workspaces/new", "lead"); rec.Code == http.StatusLocked || marker("lead") != "" {
		t.Fatalf("exempt: %d, marker %q", rec.Code, marker("lead"))
	}
	if rec := req(r, "POST", "/workspaces/new", "own"); rec.Code == http.StatusLocked || marker("own") != "" {
		t.Fatalf("owner: %d, marker %q", rec.Code, marker("own"))
	}
	if got := maintenance.FrozenSessions(dir, st); len(got) != 1 || !got["s1"] {
		t.Fatalf("header-less caller left a marker: %v", got)
	}
	if rec := req(r, "POST", "/workspaces/new", ""); rec.Code != http.StatusLocked {
		t.Fatalf("header-less: %d", rec.Code)
	}
	if got := maintenance.FrozenSessions(dir, st); len(got) != 1 {
		t.Fatalf("header-less caller left a marker: %v", got)
	}

	if err := osWriteFile(dir+"/"+maintenance.FileName, "{"); err != nil {
		t.Fatal(err)
	}
	if rec := req(r, "POST", "/workspaces/new", "s9"); rec.Code != http.StatusLocked || marker("s9") != "" {
		t.Fatalf("unreadable state: %d, marker %q", rec.Code, marker("s9"))
	}
}

// D23: the 423 names the owner as its caller may see it; an absent or
// invalid header reads with the default scope, except on the subject routes
// where scopeMW answers 400 first.
func TestFreezeRefusalNamesOwnerPerCaller(t *testing.T) {
	scopetest.Three(t)
	st := frozen()
	st.Exempt = ""
	st.Owner, st.OwnerLabel, st.OwnerScope = "0ae10000-0000-4000-8000-000000000001", "chantier racine", "perso"
	r, _ := frozenRouter(t, st)
	as := func(method, path, header string) *httptest.ResponseRecorder {
		q := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		if header != "" {
			q.Header.Set(pigtail.HeaderScope, header)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, q)
		return rec
	}
	for _, c := range []struct{ name, method, path, header string }{
		{"globex", "POST", "/themes", "globex"},
		{"no header", "POST", "/themes", ""},
		{"invalid header off scopeMW", "POST", "/workspaces/new", "nope"},
	} {
		rec := as(c.method, c.path, c.header)
		if rec.Code != http.StatusLocked || !strings.Contains(rec.Body.String(), "0ae10000") || strings.Contains(rec.Body.String(), "chantier racine") {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
	if rec := as("POST", "/themes", "nope"); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid header on a subject route = %d, want scopeMW's 400", rec.Code)
	}
	if rec := as("POST", "/themes", "perso"); rec.Code != http.StatusLocked || !strings.Contains(rec.Body.String(), "chantier racine") {
		t.Errorf("master: %d %s", rec.Code, rec.Body.String())
	}
}
