package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/ports"
	"github.com/aloysbr/pigtail/internal/staticsrv"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// newStaticTestRouter wires the static-server routes against an in-memory
// store, a real allocator + manager, and a small port range.
func newStaticTestRouter(t *testing.T) (*chi.Mux, *Services) {
	t.Helper()
	_, st := newTestRouter(t) // reuse the in-memory store setup
	mgr := staticsrv.New()
	t.Cleanup(mgr.StopAll)
	svc := &Services{
		Store:           st,
		Ports:           ports.New(st),
		StaticMgr:       mgr,
		StaticPortRange: pigtail.PortRange{18080, 18099},
	}
	r := chi.NewRouter()
	RegisterStaticServerRoutes(r, svc, testResponder{})
	return r, svc
}

func TestStaticServerCRUDLifecycle(t *testing.T) {
	t.Parallel()
	r, _ := newStaticTestRouter(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>ok</h1>"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Create — port auto-allocated from the range.
	rec := doJSON(t, r, "POST", "/static-servers", CreateStaticServerRequest{Name: "docs", Dir: dir})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var created pigtail.StaticServer
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ID == "" || created.ID[:3] != "ss_" {
		t.Fatalf("id not minted: %q", created.ID)
	}
	if created.Port < 18080 || created.Port > 18099 {
		t.Fatalf("port %d outside range", created.Port)
	}
	if !filepath.IsAbs(created.Dir) {
		t.Fatalf("dir not absolute: %q", created.Dir)
	}

	// Duplicate name → 409.
	rec = doJSON(t, r, "POST", "/static-servers", CreateStaticServerRequest{Name: "docs", Dir: dir})
	if rec.Code != http.StatusConflict {
		t.Fatalf("dup status = %d, want 409", rec.Code)
	}

	// Missing dir → 400.
	rec = doJSON(t, r, "POST", "/static-servers", CreateStaticServerRequest{Name: "bad", Dir: filepath.Join(dir, "nope")})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing-dir status = %d, want 400", rec.Code)
	}

	// List — not running yet.
	rec = doJSON(t, r, "GET", "/static-servers", nil)
	var list []StaticServerView
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 || list[0].Running {
		t.Fatalf("unexpected list: %+v", list)
	}

	// Start.
	rec = doJSON(t, r, "POST", "/static-servers/docs/start", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d body=%s", rec.Code, rec.Body.String())
	}

	// List — now running.
	rec = doJSON(t, r, "GET", "/static-servers", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || !list[0].Running || list[0].RunningPort != created.Port {
		t.Fatalf("expected running view: %+v", list)
	}

	// Double-start → 409.
	rec = doJSON(t, r, "POST", "/static-servers/docs/start", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("double-start status = %d, want 409", rec.Code)
	}

	// Stop.
	rec = doJSON(t, r, "POST", "/static-servers/docs/stop", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("stop status = %d", rec.Code)
	}

	// Delete (record gone afterwards).
	rec = doJSON(t, r, "DELETE", "/static-servers/docs", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rec.Code)
	}
	rec = doJSON(t, r, "POST", "/static-servers/docs/start", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("start-after-delete status = %d, want 404", rec.Code)
	}
}

func TestStaticServerExplicitPortCollision(t *testing.T) {
	t.Parallel()
	r, _ := newStaticTestRouter(t)
	dir := t.TempDir()

	rec := doJSON(t, r, "POST", "/static-servers", CreateStaticServerRequest{Name: "a", Dir: dir, Port: 18090})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create a status = %d body=%s", rec.Code, rec.Body.String())
	}
	// Same explicit port now in UsedPorts → 409.
	rec = doJSON(t, r, "POST", "/static-servers", CreateStaticServerRequest{Name: "b", Dir: dir, Port: 18090})
	if rec.Code != http.StatusConflict {
		t.Fatalf("collision status = %d, want 409", rec.Code)
	}
}

func TestStaticServerStopAll(t *testing.T) {
	t.Parallel()
	r, _ := newStaticTestRouter(t)
	dir := t.TempDir()
	doJSON(t, r, "POST", "/static-servers", CreateStaticServerRequest{Name: "x", Dir: dir})
	doJSON(t, r, "POST", "/static-servers/x/start", nil)
	rec := doJSON(t, r, "POST", "/static-servers/stop-all", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("stop-all status = %d", rec.Code)
	}
	rec = doJSON(t, r, "GET", "/static-servers", nil)
	var list []StaticServerView
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Running {
		t.Fatalf("expected stopped after stop-all: %+v", list)
	}
}
