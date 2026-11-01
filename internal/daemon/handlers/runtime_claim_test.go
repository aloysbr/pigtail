package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/runtimeclaim"
	"github.com/aloysbr/pigtail/pkg/pigtail"
	"github.com/go-chi/chi/v5"
)

// detailResponder is testResponder plus the error details (the 409 carries them).
type detailResponder struct{ testResponder }

func (detailResponder) Error(w http.ResponseWriter, _ *http.Request, status int, code pigtail.ErrorCode, msg string, details map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": msg, "details": details})
}

func TestRuntimeClaimRoutes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	alive := true
	svc := &Services{RuntimeClaimDir: dir, Alive: func(string, int, string) bool { return alive }}
	r := chi.NewRouter()
	RegisterRuntimeClaimRoutes(r, svc, detailResponder{})
	call := func(method, path string, body any) (int, map[string]any, string) {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
		var v map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
		return rec.Code, v, rec.Body.String()
	}
	reqA := pigtail.RuntimeClaimRequest{Session: "sess-A", Label: "secret label", LabelScope: "perso", PID: 4242, ConfigDir: "/cfg",
		Branch: "feat/a", SHA: strings.Repeat("a", 40), PR: 218, Reason: "secret reason"}
	if code, _, body := call("POST", "/runtime/claim", reqA); code != 200 {
		t.Fatalf("POST A = %d %s", code, body)
	}
	code, v, body := call("GET", "/runtime/claim", nil)
	if code != 200 || v["present"] != true || v["session"] != "sess-A" || v["branch"] != "feat/a" || v["sha"] != reqA.SHA || v["liveness"] != "alive" || v["dir"] != dir {
		t.Fatalf("GET = %d %s", code, body)
	}
	if strings.Contains(body, "secret label") || strings.Contains(body, "secret reason") || strings.Contains(body, "label") || strings.Contains(body, "reason") {
		t.Fatalf("GET leaks label or reason: %s", body)
	}
	reqB := reqA
	reqB.Session = "sess-B"
	code, v, body = call("POST", "/runtime/claim", reqB)
	det, _ := v["details"].(map[string]any)
	if code != 409 || v["code"] != string(pigtail.ErrConflict) || det["session"] != "sess-A" || det["id"] == nil || det["liveness"] != "alive" {
		t.Fatalf("POST B = %d %s", code, body)
	}
	if strings.Contains(body, "secret label") || strings.Contains(body, "feat/a") || strings.Contains(body, reqA.SHA) {
		t.Fatalf("409 leaks label, branch or sha: %s", body)
	}
	if code, _, body := call("POST", "/runtime/release", map[string]any{"session": "sess-B"}); code != 409 {
		t.Fatalf("release B = %d %s", code, body)
	}
	alive = false
	code, v, body = call("POST", "/runtime/release", map[string]any{"session": "sess-B", "force": true})
	if code != 200 || v["released"] == nil || !strings.HasPrefix(v["released"].(string), "rtc_") {
		t.Fatalf("release B --force over a stale holder = %d %s", code, body)
	}
	if code, v, body := call("GET", "/runtime/claim", nil); code != 200 || v["present"] != false {
		t.Fatalf("GET after release = %d %s", code, body)
	}
	if code, v, body := call("POST", "/runtime/release", map[string]any{"session": "sess-B"}); code != 200 || v["released"] != nil {
		t.Fatalf("release with nothing held = %d %s", code, body)
	}
	for name, bad := range map[string]pigtail.RuntimeClaimRequest{
		"no reason":   {Session: "sess-A"},
		"long reason": {Session: "sess-A", Reason: strings.Repeat("x", 501)},
		"bad session": {Session: "../x", Reason: "r"},
	} {
		if code, v, body := call("POST", "/runtime/claim", bad); code != 400 || v["code"] != string(pigtail.ErrInvalidRequest) {
			t.Errorf("%s = %d %s", name, code, body)
		}
	}
}

func runtimeRouter(dir string) chi.Router {
	svc := &Services{RuntimeClaimDir: dir, Alive: func(string, int, string) bool { return true }}
	r := chi.NewRouter()
	RegisterRuntimeClaimRoutes(r, svc, detailResponder{})
	return r
}

func TestRuntimeClaimViewExpiresAt(t *testing.T) {
	r := runtimeRouter(t.TempDir())
	body, _ := json.Marshal(pigtail.RuntimeClaimRequest{Session: "sess-A", PID: 4242, ConfigDir: "/cfg", Reason: "r"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/runtime/claim", bytes.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/runtime/claim", nil))
	var v pigtail.RuntimeClaimView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || v.ExpiresAt.IsZero() {
		t.Fatalf("GET = %d %s (%v)", rec.Code, rec.Body, err)
	}
	if d := v.ExpiresAt.Sub(v.Since.Add(4 * time.Hour)); d < -time.Second || d > time.Second {
		t.Fatalf("expires_at %v, since %v", v.ExpiresAt, v.Since)
	}
}

func TestRuntimeClaimGetLiftsExpired(t *testing.T) {
	dir := t.TempDir()
	r := runtimeRouter(dir)
	st := runtimeclaim.State{ID: "rtc_0123456789abcdef", Session: "sess-A", Reason: "r", Since: time.Now().Add(-5 * time.Hour)}
	raw, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(dir, runtimeclaim.FileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	get := func() map[string]any {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", "/runtime/claim", nil))
		var v map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
		if rec.Code != 200 {
			t.Fatalf("GET = %d %s", rec.Code, rec.Body)
		}
		return v
	}
	if v := get(); v["present"] == true {
		t.Fatalf("GET = %v, want present false", v)
	}
	if _, err := os.Stat(filepath.Join(dir, runtimeclaim.FileName)); !os.IsNotExist(err) {
		t.Fatalf("lease file still there: %v", err)
	}
	if exp, ok := runtimeclaim.ReadExpiry(dir); !ok || exp.Lease.ID != st.ID {
		t.Fatalf("ReadExpiry = %+v %v", exp, ok)
	}
	if err := os.WriteFile(filepath.Join(dir, runtimeclaim.FileName), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v := get(); v["present"] != true || v["unreadable"] == nil {
		t.Fatalf("GET unreadable = %v", v)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, runtimeclaim.FileName)); string(b) != "{nope" {
		t.Fatalf("unreadable file changed: %q", b)
	}
}
