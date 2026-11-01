package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/maintenance"
	"github.com/go-chi/chi/v5"
)

func maintRouter(t *testing.T, dir string) *chi.Mux {
	t.Helper()
	svc := &Services{Maintenance: maintenance.NewWatch(dir)}
	r := chi.NewRouter()
	r.Get("/maintenance", GetMaintenance(svc, testResponder{}))
	return r
}

func getView(t *testing.T, r http.Handler) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/maintenance", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /maintenance: %d %s", rec.Code, rec.Body)
	}
	// testResponder writes the payload bare (the daemon's responder wraps it
	// in {"data": …}).
	var v map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	return v
}

func TestGetMaintenanceSaysWhatTheDaemonReads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := maintRouter(t, dir)
	if v := getView(t, r); v["present"] != false || v["dir"] != dir {
		t.Fatalf("absent: %v", v)
	}
	_ = maintenance.Save(dir, maintenance.State{ID: "mnt_a", On: true, Freeze: true, Reason: "secret reason", Exempt: "sid-lead", Since: time.Now()})
	v := getView(t, r)
	if v["present"] != true || v["id"] != "mnt_a" || v["freeze"] != true {
		t.Fatalf("on: %v", v)
	}
	raw, _ := json.Marshal(v)
	if s := string(raw); containsAny(s, "secret reason", "sid-lead") {
		t.Fatalf("leaks reason or exempt session: %s", s)
	}
	_ = os.WriteFile(filepath.Join(dir, maintenance.FileName), []byte("{"), 0o600)
	if v := getView(t, r); v["unreadable"] == nil || v["unreadable"] == "" {
		t.Fatalf("corrupt: %v", v)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}
