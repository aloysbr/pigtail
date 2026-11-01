package daemon

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// healthServer is a Server whose scope table failed to load, over a store
// that holds a session of an undeclared scope (zeta).
func healthServer(t *testing.T, loadErr error) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertPeer(&pigtail.PeerSession{SessionID: "z", Scope: "zeta"}); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{SocketPath: testSocketPath(t), ScopeTableErr: loadErr}, st, events.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// healthScopesAs is the "scopes" object of GET /health for a caller.
func healthScopesAs(t *testing.T, srv *Server, header string) (map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/health", nil)
	if header != "" {
		req.Header.Set(pigtail.HeaderScope, header)
	}
	rec := httptest.NewRecorder()
	srv.handleHealth(rec, req)
	if rec.Code != 200 {
		t.Fatalf("/health as %q = %d", header, rec.Code)
	}
	var body struct {
		Data struct {
			Scopes map[string]any `json:"scopes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Data.Scopes, rec.Body.String()
}

func TestHealthScopesMaster(t *testing.T) {
	scopetest.Two(t)
	srv := healthServer(t, errors.New("scopes.yaml: two sees_all scopes"))
	sc, raw := healthScopesAs(t, srv, "perso")
	if sc["source"] != "scopetest" || sc["master"] != "perso" || sc["default"] != "globex" {
		t.Fatalf("scopes = %s", raw)
	}
	if e, _ := sc["error"].(string); e != "scopes.yaml: two sees_all scopes" {
		t.Fatalf("error = %v", sc["error"])
	}
	if b, _ := json.Marshal(sc["undeclared"]); string(b) != `["zeta"]` {
		t.Fatalf("undeclared = %s", b)
	}
	if b, _ := json.Marshal(sc["names"]); string(b) != `["globex","perso"]` {
		t.Fatalf("names = %s", b)
	}
}

// D19: a non-master caller reads its own scope only in /health — no master,
// no default, no undeclared list, and a generic error. An unreadable header
// is the default scope's view (/health never answers 400).
func TestHealthScopesNonMaster(t *testing.T) {
	scopetest.Two(t)
	srv := healthServer(t, errors.New("scopes.yaml: two sees_all scopes"))
	for _, h := range []string{"globex", "", "nope"} {
		sc, raw := healthScopesAs(t, srv, h)
		if b, _ := json.Marshal(sc["names"]); string(b) != `["globex"]` {
			t.Errorf("as %q: names = %s", h, b)
		}
		for _, k := range []string{"master", "undeclared"} {
			if _, ok := sc[k]; ok {
				t.Errorf("as %q: key %s shown: %s", h, k, raw)
			}
		}
		if e, _ := sc["error"].(string); e == "" || strings.Contains(e, "two sees_all") {
			t.Errorf("as %q: error = %q, want the generic sentence", h, e)
		}
	}
	scopetest.Three(t)
	srv = healthServer(t, nil)
	sc, raw := healthScopesAs(t, srv, "acme")
	if b, _ := json.Marshal(sc["names"]); string(b) != `["acme"]` {
		t.Errorf("acme: names = %s", b)
	}
	if _, ok := sc["default"]; ok {
		t.Errorf("acme: default shown: %s", raw)
	}
	for _, w := range []string{"perso", "globex", "zeta"} {
		if strings.Contains(raw, w) {
			t.Errorf("acme: /health names %q: %s", w, raw)
		}
	}
}
