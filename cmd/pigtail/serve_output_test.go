package main

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// fakeStaticServers answers POST /static-servers with the requested server on
// port 7123, in the envelope.
func fakeStaticServers(r chi.Router, _ *store.Store) {
	r.Post("/static-servers", func(w http.ResponseWriter, req *http.Request) {
		var in pigtail.CreateStaticServerRequest
		body, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(body, &in)
		writeData(w, http.StatusCreated, pigtail.StaticServer{ID: "ss_x", Name: in.Name, Dir: in.Dir, Port: 7123})
	})
}

func TestServeAddRegistersServerOutput(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, st := realSubjectDaemonWith(t, fakeStaticServers)
	bindWS(t, st, "sub_p")
	dir := t.TempDir()
	out, errOut, err := runCobraIO(t, s, serveCmd, "add", "docs", dir)
	if err != nil || errOut != "" {
		t.Fatalf("serve add = %v, stderr %q", err, errOut)
	}
	rows := outputRows(t, st, "sub_p")
	if len(rows) != 1 || rows[0].Kind != "server" || rows[0].URL != "http://127.0.0.1:7123" || rows[0].Path != dir || rows[0].Title != "docs" {
		t.Fatalf("rows = %+v", rows)
	}
	want := regexp.MustCompile(`^✓ added docs  port=7123  .+\n  ↳ output ` + rows[0].ID + ` → docs\n$`)
	if !want.MatchString(out) {
		t.Fatalf("stdout = %q", out)
	}
	out, _, err = runCobraIO(t, s, serveCmd, "add", "docs", dir)
	if err != nil || !strings.HasPrefix(out, "✓ added docs") || strings.Contains(out, "↳") {
		t.Fatalf("replay stdout = %q %v", out, err)
	}
}

func TestServeAddExplicitSubject(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, st := realSubjectDaemonWith(t, fakeStaticServers)
	if _, _, err := runCobraIO(t, s, serveCmd, "add", "docs", t.TempDir(), "--subject", "sub_p"); err != nil {
		t.Fatal(err)
	}
	if rows := outputRows(t, st, "sub_p"); len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestServeAddUnboundNoOutput(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, st := realSubjectDaemonWith(t, fakeStaticServers)
	_, errOut, err := runCobraIO(t, s, serveCmd, "add", "docs", t.TempDir())
	if err != nil || errOut != "" {
		t.Fatalf("= %v, stderr %q", err, errOut)
	}
	for _, id := range []string{"sub_p", "sub_t"} {
		if rows := outputRows(t, st, id); len(rows) != 0 {
			t.Fatalf("%s rows = %+v", id, rows)
		}
	}
}

// Verify gap 1 for serve add: a pane UUID whose lookup answers 404.
func TestServeAddPaneLookup404(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, st := realSubjectDaemonWith(t, fakeStaticServers)
	t.Setenv(cmux.EnvWorkspaceID, attachWS)
	_, errOut, err := runCobraIO(t, s, serveCmd, "add", "docs", t.TempDir())
	if err != nil || errOut != "" {
		t.Fatalf("= %v, stderr %q", err, errOut)
	}
	if !slices.Contains(pathsOf(s), "GET /cmux-workspaces/"+attachWS) {
		t.Fatalf("lookup never reached: %v", pathsOf(s))
	}
	if rows := outputRows(t, st, "sub_p"); len(rows) != 0 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestServeAddCrossScopeFlag(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	cmd := serveCmd()
	add, _, _ := cmd.Find([]string{"add"})
	if add == nil || add.Flags().Lookup("cross-scope") == nil {
		t.Fatal("serve add has no --cross-scope")
	}
	s, st := realSubjectDaemonWith(t, fakeStaticServers)
	_, errOut, err := runCobraIO(t, s, serveCmd, "add", "docs", t.TempDir(), "--subject", "sub_t")
	if err != nil || !strings.HasPrefix(errOut, "warn: output not registered:") || !strings.Contains(errOut, string(pigtail.ErrScopeMismatch)) {
		t.Fatalf("without --cross-scope = %v, stderr %q", err, errOut)
	}
	if rows := outputRows(t, st, "sub_t"); len(rows) != 0 {
		t.Fatalf("refused write landed: %+v", rows)
	}
	if _, errOut, err := runCobraIO(t, s, serveCmd, "add", "docs", t.TempDir(), "--subject", "sub_t", "--cross-scope"); err != nil || errOut != "" {
		t.Fatalf("with --cross-scope = %v, stderr %q", err, errOut)
	}
	if rows := outputRows(t, st, "sub_t"); len(rows) != 1 {
		t.Fatalf("opted-in write did not land: %+v", rows)
	}
}

func TestServeAddOutputsRootNotRegistered(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, st := realSubjectDaemonWith(t, fakeStaticServers)
	bindWS(t, st, "sub_p")
	root := filepath.Join(t.TempDir(), "_outputs")
	out, errOut, err := runCobraIO(t, s, serveCmd, "add", "report-perso", root, "--port", "7123")
	if err != nil || !strings.Contains(out, "report-perso") || errOut != "" {
		t.Fatalf("= %q %v, stderr %q", out, err, errOut)
	}
	if rows := outputRows(t, st, "sub_p"); len(rows) != 0 {
		t.Fatalf("outputs root registered: %+v", rows)
	}
}

func TestServeAddResolutionError(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, st := realSubjectDaemonWith(t, func(r chi.Router, st *store.Store) {
		fakeStaticServers(r, st)
		r.Get("/cmux-workspaces/{wsid}", func(w http.ResponseWriter, req *http.Request) {
			envelopeResponder{}.Error(w, req, http.StatusInternalServerError, pigtail.ErrInternal, "boom", nil)
		})
	})
	t.Setenv(cmux.EnvWorkspaceID, attachWS)
	out, errOut, err := runCobraIO(t, s, serveCmd, "add", "docs", t.TempDir())
	if err != nil || !strings.HasPrefix(out, "✓ added docs") || !strings.HasPrefix(errOut, "warn: output not registered:") {
		t.Fatalf("= %q %v, stderr %q", out, err, errOut)
	}
	if !slices.Contains(pathsOf(s), "GET /cmux-workspaces/"+attachWS) {
		t.Fatalf("faulty route never reached: %v", pathsOf(s))
	}
	if rows := outputRows(t, st, "sub_p"); len(rows) != 0 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestServeAddJSONStaysJSON(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	outputEnv(t)
	s, _ := realSubjectDaemonWith(t, fakeStaticServers)
	out, errOut, err := runCobraIO(t, s, serveCmd, "add", "docs", t.TempDir(), "--json", "--subject", "sub_zz")
	if err != nil || !strings.HasPrefix(errOut, "warn: output not registered:") {
		t.Fatalf("= %v, stderr %q", err, errOut)
	}
	dec := json.NewDecoder(strings.NewReader(out))
	var ss pigtail.StaticServer
	if err := dec.Decode(&ss); err != nil || ss.Name != "docs" || dec.More() {
		t.Fatalf("stdout = %q %v", out, err)
	}
}
