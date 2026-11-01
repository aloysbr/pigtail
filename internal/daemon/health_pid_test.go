package daemon

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// healthBody is the "data" object of GET /health on a server built with cfg.
func healthBody(t *testing.T, cfg Config, header string) map[string]any {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if cfg.SocketPath == "" {
		cfg.SocketPath = testSocketPath(t)
	}
	if cfg.TokenPath == "" {
		cfg.TokenPath = filepath.Join(t.TempDir(), "token")
	}
	srv, err := New(cfg, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/health", nil)
	if header != "" {
		req.Header.Set(pigtail.HeaderScope, header)
	}
	rec := httptest.NewRecorder()
	srv.handleHealth(rec, req)
	if rec.Code != 200 {
		t.Fatalf("/health = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Data
}

// /health names the daemon's pid and HTTP address: `pigtail doctor`, setup
// and the first-launch checks read them (plan alpha-oss, P1b).
func TestHealthCarriesPidAndHTTPAddr(t *testing.T) {
	t.Cleanup(scope.Use(scope.Builtin()))
	d := healthBody(t, Config{HTTPAddr: "127.0.0.1:7899"}, "")
	if pid, _ := d["pid"].(float64); int(pid) != os.Getpid() {
		t.Fatalf("pid = %v, want %d", d["pid"], os.Getpid())
	}
	if d["http_addr"] != "127.0.0.1:7899" {
		t.Fatalf("http_addr = %v, want 127.0.0.1:7899", d["http_addr"])
	}
	if d := healthBody(t, Config{}, ""); d["http_addr"] != "" {
		t.Fatalf("HTTP disabled: http_addr = %v, want \"\"", d["http_addr"])
	}
}

// D19 holds with the new fields: a non-master caller of a two-scope table
// reads no master.
func TestHealthNonMasterStillHidesMaster(t *testing.T) {
	scopetest.Three(t)
	sc, _ := healthBody(t, Config{}, "globex")["scopes"].(map[string]any)
	if _, ok := sc["master"]; ok {
		t.Fatalf("non-master caller reads master: %v", sc)
	}
	if _, ok := sc["default"]; ok {
		t.Fatalf("non-master caller reads default: %v", sc)
	}
}

// The neutral builtin: one scope, so a caller that names none is the master
// and reads it.
func TestHealthBuiltinSingleScopeNoHeader(t *testing.T) {
	t.Cleanup(scope.Use(scope.Builtin()))
	sc, _ := healthBody(t, Config{}, "")["scopes"].(map[string]any)
	if sc["master"] != "personal" || sc["default"] != "personal" || sc["source"] != "builtin" {
		t.Fatalf("scopes = %v, want master = default = personal, source builtin", sc)
	}
}
