package daemon

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/runtimeclaim"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func runtimeClaimServer(t *testing.T) (*Server, string) {
	t.Helper()
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	st, _ := store.Open(":memory:")
	t.Cleanup(func() { _ = st.Close() })
	sock := testSocketPath(t)
	srv, err := New(Config{SocketPath: sock}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return srv, filepath.Dir(sock)
}

func serve(t *testing.T, srv *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.RemoteAddr = ""
	rec := httptest.NewRecorder()
	srv.buildRouter().ServeHTTP(rec, req)
	return rec
}

// Review Focus 2: a claim written while the daemon is down (the very moment
// of a bootout) is the one the restarted daemon serves and enforces.
func TestDaemonReloadsClaimWrittenWhileDown(t *testing.T) {
	t.Setenv("PIGTAIL_MAINTENANCE_DIR", "")
	sock := testSocketPath(t)
	dir := filepath.Dir(sock)
	held, err := runtimeclaim.Claim(dir, runtimeclaim.State{Session: "sess-A", Reason: "acceptance", Branch: "feat/a", SHA: strings.Repeat("a", 40)},
		false, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(":memory:")
	defer st.Close()
	srv, err := New(Config{SocketPath: sock}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(t, srv, "GET", "/runtime/claim", nil)
	var env struct {
		Data pigtail.RuntimeClaimView `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || rec.Code != 200 || !env.Data.Present || env.Data.ID != held.ID || env.Data.Session != "sess-A" {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(t, srv, "POST", "/runtime/claim", pigtail.RuntimeClaimRequest{Session: "sess-B", Reason: "post-merge"}); rec.Code != 409 {
		t.Fatalf("POST by another session = %d %s, want 409", rec.Code, rec.Body.String())
	}
}

// D23: a claim recorded with the holder's label (a perso subject title) and
// its reason never goes out through GET /runtime/claim.
func TestRuntimeClaimNoLabelLeak(t *testing.T) {
	srv, dir := runtimeClaimServer(t)
	if _, err := runtimeclaim.Claim(dir, runtimeclaim.State{Session: "s-perso-a", Label: probeSubjectTitle, LabelScope: "perso",
		Reason: "zqx perso reason", Branch: "feat/a", SHA: strings.Repeat("a", 40)}, false, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, r := range walkRoutes(t, srv.buildRouter()) {
		if strings.HasPrefix(r.path, "/runtime/") {
			found++
			if _, ok := routeScopeClasses[r.key]; !ok {
				t.Errorf("%s is not classified", r.key)
			}
		}
	}
	if found != 3 {
		t.Fatalf("walked %d /runtime routes, want 3", found)
	}
	body := serve(t, srv, "GET", "/runtime/claim", nil).Body.String()
	if !strings.Contains(body, "s-perso-a") {
		t.Fatalf("positive control: the claim is served: %s", body)
	}
	for _, m := range []string{probeSubjectTitle, "zqx perso reason"} {
		if strings.Contains(body, m) {
			t.Errorf("GET /runtime/claim leaks %q: %s", m, body)
		}
	}
}
