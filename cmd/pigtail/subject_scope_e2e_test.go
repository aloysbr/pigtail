package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// realSubjectDaemon serves the real subject routes (behind envelopeResponder,
// see admit_test.go) over an in-memory store
// seeded with one work subject (sub_t) and one perso subject (sub_p).
func realSubjectDaemon(t *testing.T) (*stubDaemon, *store.Store) {
	t.Helper()
	return realSubjectDaemonWith(t, nil)
}

// realSubjectDaemonWith is realSubjectDaemon with extra routes registered on
// the same router (nil = none), and every request recorded in s.paths as
// "METHOD /path".
func realSubjectDaemonWith(t *testing.T, extra func(r chi.Router, st *store.Store)) (*stubDaemon, *store.Store) {
	t.Helper()
	return realSubjectDaemonSvc(t, nil, extra)
}

// realSubjectDaemonSvc is realSubjectDaemonWith with a hook on the Services
// the routes are built from (nil = none), e.g. to give them a space table.
func realSubjectDaemonSvc(t *testing.T, tune func(*handlers.Services), extra func(r chi.Router, st *store.Store)) (*stubDaemon, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.UnixMilli(1).UTC()
	for _, th := range []*pigtail.Theme{
		{ID: "thm_t", Name: "T", Kind: "project", VaultLink: "Globex/Projects/t", CreatedAt: now},
		{ID: "thm_p", Name: "P", Kind: "ongoing", VaultLink: "Perso/Areas/p", CreatedAt: now},
	} {
		if err := st.CreateTheme(th); err != nil {
			t.Fatal(err)
		}
	}
	for _, sub := range []*pigtail.Subject{
		{ID: "sub_t", ThemeID: "thm_t", Title: "work one", Slug: "t", Status: pigtail.StatusInbox, Rank: 2, CreatedAt: now, UpdatedAt: now},
		{ID: "sub_p", ThemeID: "thm_p", Title: "perso one", Slug: "p", Status: pigtail.StatusInbox, Rank: 1, CreatedAt: now, UpdatedAt: now},
	} {
		if err := st.CreateSubject(sub); err != nil {
			t.Fatal(err)
		}
	}
	s := &stubDaemon{}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			s.pathsMu.Lock()
			s.paths = append(s.paths, req.Method+" "+req.URL.Path)
			s.pathsMu.Unlock()
			next.ServeHTTP(w, req)
		})
	})
	svc := &handlers.Services{Store: st}
	if tune != nil {
		tune(svc)
	}
	handlers.RegisterSubjectRoutes(r, svc, envelopeResponder{})
	if extra != nil {
		extra(r, st)
	}
	s.srv = httptest.NewServer(r)
	t.Cleanup(s.srv.Close)
	return s, st
}

// subjectCalls returns the recorded requests under /subjects, in order. The
// bindings read and the attach live there too (/subjects/{id}/cmux-workspaces,
// pkg/pigtail/cmux_binding_client.go), as do the phases.
func subjectCalls(s *stubDaemon) []string {
	s.pathsMu.Lock()
	defer s.pathsMu.Unlock()
	var out []string
	for _, p := range s.paths {
		if _, path, _ := strings.Cut(p, " "); strings.HasPrefix(path, "/subjects") {
			out = append(out, p)
		}
	}
	return out
}

// runTheme runs `pigtail theme <args>` against s, returning its stdout.
func runTheme(t *testing.T, s *stubDaemon, args ...string) (string, error) {
	t.Helper()
	return runCobra(t, s, themeCmd, args...)
}

// runSubject runs `pigtail subject <args>` against s, returning its stdout.
func runSubject(t *testing.T, s *stubDaemon, args ...string) (string, error) {
	t.Helper()
	return runCobra(t, s, subjectCmd, args...)
}

// runCobra runs a root-less command built by mk against s, capturing stdout.
func runCobra(t *testing.T, s *stubDaemon, mk func() *cobra.Command, args ...string) (string, error) {
	t.Helper()
	testutil.SerialOnly(t) // swaps os.Stdout
	var out string
	var err error
	withStubDaemon(t, s, func() {
		stdout := os.Stdout
		rd, wr, perr := os.Pipe()
		if perr != nil {
			t.Fatal(perr)
		}
		os.Stdout = wr
		cmd := mk()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err = cmd.Execute()
		_ = wr.Close()
		os.Stdout = stdout
		b, _ := io.ReadAll(rd)
		out = string(b)
	})
	return out, err
}

// TestPersoCellScopeWorkIsAViewNotAnIdentity: from a perso cell, --scope work
// narrows what is listed but never lets a write onto a work subject skip the
// --cross-scope opt-in.
func TestPersoCellScopeWorkIsAViewNotAnIdentity(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)
	flagScope = "globex"

	_, err := runSubject(t, s, "set", "sub_t", "rank=7")
	var apiErr *pigtail.APIError
	if err == nil || !errors.As(err, &apiErr) || apiErr.Code != pigtail.ErrScopeMismatch {
		t.Fatalf("perso cell --scope work set on a work subject: %v, want SCOPE_MISMATCH", err)
	}
	if got, _ := st.GetSubject("sub_t"); got.Rank != 2 {
		t.Fatalf("refused write still landed: rank=%d", got.Rank)
	}
	if _, err := runSubject(t, s, "set", "sub_t", "rank=7", "--cross-scope"); err != nil {
		t.Fatalf("with --cross-scope: %v", err)
	}
	if got, _ := st.GetSubject("sub_t"); got.Rank != 7 {
		t.Fatalf("opted-in write did not land: rank=%d", got.Rank)
	}

	// The view restriction still applies.
	out, err := runSubject(t, s, "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "work one") || strings.Contains(out, "perso one") {
		t.Fatalf("--scope work list = %q, want work only", out)
	}
	out, err = runSubject(t, s, "next")
	if err != nil || !strings.Contains(out, "sub_t") {
		t.Fatalf("--scope work next = %q, %v; want sub_t", out, err)
	}
	flagScope = ""
	out, _ = runSubject(t, s, "list")
	if !strings.Contains(out, "work one") || !strings.Contains(out, "perso one") {
		t.Fatalf("perso cell list = %q, want both", out)
	}
}
