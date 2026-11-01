package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const depChainSubject = `{"id":"sub_1","theme_id":"thm_1","title":"t","slug":"t","status":"inbox","source":"local","blocked_by":["sub_2"],"blocked_by_all":["sub_2","sub_3"],"blocks":["sub_4"],"blocks_all":["sub_4","sub_5"],"created_at":"2026-10-22T00:00:00Z","updated_at":"2026-10-22T00:00:00Z"}`

func depChainStub(t *testing.T, subject string) *stubDaemon {
	t.Helper()
	withCells(t, func(_, perso string) string { return perso })
	raw := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":`+body+`,"meta":{}}`)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/subjects/sub_1", func(w http.ResponseWriter, _ *http.Request) { raw(w, subject) })
	mux.HandleFunc("/subjects/sub_1/phases", func(w http.ResponseWriter, _ *http.Request) { raw(w, `[]`) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &stubDaemon{srv: srv}
}

// show --json lifts blocked_by_all, blocks and blocks_all to the top level,
// never under subject; an older daemon without them gives no key, never null.
func TestSubjectShowLiftsDepChains(t *testing.T) {
	keys := []string{"blocked_by_all", "blocks", "blocks_all"}
	out, err := runSubject(t, depChainStub(t, depChainSubject), "show", "sub_1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &top); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	var sub map[string]json.RawMessage
	_ = json.Unmarshal(top["subject"], &sub)
	for k, want := range map[string]string{"blocked_by_all": `["sub_2","sub_3"]`, "blocks": `["sub_4"]`, "blocks_all": `["sub_4","sub_5"]`} {
		if string(top[k]) != want {
			t.Errorf("top-level %s = %s, want %s", k, top[k], want)
		}
		if _, dup := sub[k]; dup {
			t.Errorf("%s also under subject", k)
		}
	}
	old := `{"id":"sub_1","theme_id":"thm_1","title":"t","slug":"t","status":"inbox","source":"local","created_at":"2026-10-22T00:00:00Z","updated_at":"2026-10-22T00:00:00Z"}`
	out, err = runSubject(t, depChainStub(t, old), "show", "sub_1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	top = nil
	_ = json.Unmarshal([]byte(out), &top)
	for _, k := range keys {
		if _, ok := top[k]; ok {
			t.Errorf("older daemon: key %s present: %s", k, out)
		}
	}
}

func TestSubjectShowTextDepChains(t *testing.T) {
	out, err := runSubject(t, depChainStub(t, depChainSubject), "show", "sub_1")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"  blocked by (chain): sub_2, sub_3\n", "  blocks: sub_4\n", "  blocks (chain): sub_4, sub_5\n"} {
		if !strings.Contains(out, line) {
			t.Errorf("missing %q in %q", line, out)
		}
	}
	empty := strings.NewReplacer(`["sub_2","sub_3"]`, `[]`, `["sub_4","sub_5"]`, `[]`, `["sub_4"]`, `[]`).Replace(depChainSubject)
	out, err = runSubject(t, depChainStub(t, empty), "show", "sub_1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "blocked by (chain)") || strings.Contains(out, "blocks") {
		t.Errorf("empty chains printed: %q", out)
	}
}

// subject dep returns an error carrying the daemon's refusal text.
func TestSubjectDepReturnsRefusal(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	mux := http.NewServeMux()
	mux.HandleFunc("/subjects/sub_1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"INVALID_REQUEST","message":"dependency cycle: sub_1 → sub_2 → sub_1"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"id":"sub_1","theme_id":"thm_1","title":"t","slug":"t","status":"inbox","source":"local","created_at":"2026-10-22T00:00:00Z","updated_at":"2026-10-22T00:00:00Z"},"meta":{}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, err := runSubject(t, &stubDaemon{srv: srv}, "dep", "sub_1", "--on", "sub_2")
	if err == nil || !strings.Contains(err.Error(), "dependency cycle: sub_1 → sub_2 → sub_1") {
		t.Fatalf("dep = %v, want the daemon's refusal text", err)
	}
}
