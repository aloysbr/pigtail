package main

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aloysbr/pigtail/internal/peers"
	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

const ac1ID = "acac0000-0000-4000-8000-000000000002"

var resolvePeers = []pigtail.PeerSession{
	{SessionID: "7a7a0000-0000-4000-8000-000000000011", Name: "ts1", CmuxTitle: "review front", Scope: "globex"},
	{SessionID: "7a7a0000-0000-4000-8000-000000000012", Name: "ts2", CmuxTitle: "review back", Scope: "globex"},
	{SessionID: ac1ID, Name: "ac1", CmuxTitle: "review acme-ops", Scope: "acme"},
}

// peersDaemon serves GET /peers the way the daemon filters it: the master's
// header gets every session, any other header only the sessions of that
// scope. It records the header of the last call.
func peersDaemon(t *testing.T) func() string {
	t.Helper()
	dir := testutil.MkdirTemp(t, "pmr-")
	sock := filepath.Join(dir, "p.sock")
	var mu sync.Mutex
	var header string
	mux := http.NewServeMux()
	mux.HandleFunc("/peers", func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get(pigtail.HeaderScope)
		mu.Lock()
		header = h
		mu.Unlock()
		out := []pigtail.PeerSession{}
		for _, p := range resolvePeers {
			if h == string(scope.Master()) || p.Scope == h {
				out = append(out, p)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": out})
	})
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	pointClientAt(t, sock)
	return func() string { mu.Lock(); defer mu.Unlock(); return header }
}

// D21: --exempt resolves with the caller's own view; a full session id is
// always accepted, its label read only when the caller sees that session.
func TestMaintenanceResolveUsesTheCallerView(t *testing.T) {
	t.Run("globex cell, ambiguous address", func(t *testing.T) {
		scopetest.Three(t)
		withCellScopes(t, map[string]string{"s": "globex", "m": "perso", "a": "acme"}, "s")
		header := peersDaemon(t)
		_, _, _, err := resolveMaintenancePeer("review")
		if err == nil || !strings.Contains(err.Error(), "ts1") || !strings.Contains(err.Error(), "ts2") {
			t.Fatalf("err = %v, want an ambiguity naming ts1 and ts2", err)
		}
		if strings.Contains(err.Error(), "acme") || strings.Contains(err.Error(), "acac") {
			t.Fatalf("the ambiguity names the acme session: %v", err)
		}
		if h := header(); h != "globex" {
			t.Fatalf("header = %q, want globex", h)
		}
	})
	t.Run("globex cell, full id of an acme session", func(t *testing.T) {
		scopetest.Three(t)
		withCellScopes(t, map[string]string{"s": "globex", "m": "perso", "a": "acme"}, "s")
		peersDaemon(t)
		id, label, sc, err := resolveMaintenancePeer(ac1ID)
		if err != nil || id != ac1ID || label != ac1ID || sc != "" {
			t.Fatalf("= %q, %q, %q, %v; want the id as its own label, no scope", id, label, sc, err)
		}
	})
	t.Run("renamed master sees everything", func(t *testing.T) {
		scopetest.RenamedMaster(t)
		withCellScopes(t, map[string]string{"s": "globex", "m": "root", "a": "acme"}, "m")
		header := peersDaemon(t)
		_, _, _, err := resolveMaintenancePeer("review")
		if err == nil || !strings.Contains(err.Error(), "ts1") || !strings.Contains(err.Error(), "ac1") {
			t.Fatalf("err = %v, want an ambiguity over the three sessions", err)
		}
		id, label, sc, err := resolveMaintenancePeer(ac1ID)
		if err != nil || id != ac1ID || label != peers.Label(resolvePeers[2]) || sc != "acme" {
			t.Fatalf("= %q, %q, %q, %v; want ac1 with its label and scope", id, label, sc, err)
		}
		if h := header(); h != "root" {
			t.Fatalf("header = %q, want root", h)
		}
	})
	t.Run("outside any cell", func(t *testing.T) {
		scopetest.Three(t)
		withCellScopes(t, map[string]string{"s": "globex", "m": "perso", "a": "acme"}, "")
		header := peersDaemon(t)
		_, _, _, err := resolveMaintenancePeer("review")
		if err == nil || strings.Contains(err.Error(), "ac1") || !strings.Contains(err.Error(), "ts1") {
			t.Fatalf("err = %v, want an ambiguity over ts1/ts2 only", err)
		}
		if h := header(); h != "globex" {
			t.Fatalf("header = %q, want globex (default scope)", h)
		}
		t.Setenv(envScope, "all")
		_, _, _, err = resolveMaintenancePeer("review")
		if err == nil || !strings.Contains(err.Error(), "ac1") {
			t.Fatalf("PIGTAIL_SCOPE=all: err = %v, want the three sessions", err)
		}
		if h := header(); h != "perso" {
			t.Fatalf("header = %q, want perso (master)", h)
		}
	})
}
