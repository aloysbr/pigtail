package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
)

func deepServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg.SocketPath = testSocketPath(t)
	cfg.TokenPath = filepath.Join(t.TempDir(), "token")
	srv, err := New(cfg, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func deepGet(t *testing.T, srv *Server) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.handleHealth(rec, httptest.NewRequest("GET", "/health?deep=1", nil))
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", rec.Body, err)
	}
	return rec.Code, body.Data
}

func TestHealthDeepOK(t *testing.T) {
	code, d := deepGet(t, deepServer(t, Config{}))
	if code != 200 || d["ok"] != true {
		t.Fatalf("deep = %d %v", code, d)
	}
}

func TestHealthDeepStoreHeld503(t *testing.T) {
	srv := deepServer(t, Config{DeepHealthTimeout: 100 * time.Millisecond})
	srv.ping = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() } // a held connection
	start := time.Now()
	code, d := deepGet(t, srv)
	if code != 503 || time.Since(start) > time.Second {
		t.Fatalf("deep = %d after %s", code, time.Since(start))
	}
	f, _ := d["failing"].([]any)
	if d["ok"] != false || len(f) != 1 || !strings.HasPrefix(f[0].(string), "store:") {
		t.Fatalf("body = %v", d)
	}
}

func TestHealthDeepStaleLoop503(t *testing.T) {
	srv := deepServer(t, Config{})
	srv.roleStale = func() (string, bool) { return "loop roles: last tick 4m0s ago", true }
	code, d := deepGet(t, srv)
	f, _ := d["failing"].([]any)
	if code != 503 || len(f) != 1 || !strings.HasPrefix(f[0].(string), "loop roles: last tick") {
		t.Fatalf("deep = %d %v", code, d)
	}
}

func TestHealthDeepRoleWatchOff(t *testing.T) {
	srv := deepServer(t, Config{RoleWatchDisabled: true})
	if srv.roleStale != nil {
		t.Fatal("roleStale set with the watch off")
	}
	if code, _ := deepGet(t, srv); code != 200 {
		t.Fatalf("deep = %d", code)
	}
}

func TestHealthDeepStoreErrorIsFailing(t *testing.T) {
	srv := deepServer(t, Config{})
	srv.ping = func(context.Context) error { return errors.New("disk I/O") }
	if code, d := deepGet(t, srv); code != 503 || !strings.Contains(d["failing"].([]any)[0].(string), "disk I/O") {
		t.Fatalf("deep = %d %v", code, d)
	}
}

// Regression guard: plain /health keeps exactly its five keys.
func TestHealthPlainUnchanged(t *testing.T) {
	d := healthBody(t, Config{}, "")
	want := map[string]bool{"ok": true, "daemon_version": true, "pid": true, "http_addr": true, "scopes": true}
	if len(d) != len(want) {
		t.Fatalf("keys = %v", d)
	}
	for k := range d {
		if !want[k] {
			t.Fatalf("unexpected key %q", k)
		}
	}
	if int(d["pid"].(float64)) != os.Getpid() {
		t.Fatalf("pid = %v", d["pid"])
	}
}
