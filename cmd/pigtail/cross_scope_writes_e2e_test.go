package main

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// runCLI runs one top-level command (handoffCmd, themeCmd…) with args against
// s, discarding its output.
func runCLI(t *testing.T, s *stubDaemon, root func() *cobra.Command, args ...string) error {
	t.Helper()
	testutil.SerialOnly(t) // swaps os.Stdout
	var err error
	withStubDaemon(t, s, func() {
		stdout := os.Stdout
		devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		os.Stdout = devnull
		defer func() { os.Stdout = stdout; _ = devnull.Close() }()
		cmd := root()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err = cmd.Execute()
	})
	return err
}

func wantScopeMismatch(t *testing.T, what string, err error) {
	t.Helper()
	var apiErr *pigtail.APIError
	if err == nil || !errors.As(err, &apiErr) || apiErr.Code != pigtail.ErrScopeMismatch {
		t.Fatalf("%s: %v, want SCOPE_MISMATCH", what, err)
	}
}

func wantNotFound(t *testing.T, what string, err error) {
	t.Helper()
	var apiErr *pigtail.APIError
	if err == nil || !errors.As(err, &apiErr) || apiErr.Code != pigtail.ErrNotFound {
		t.Fatalf("%s: %v, want NOT_FOUND", what, err)
	}
}

func handoffCount(t *testing.T, st *store.Store, id string) int {
	t.Helper()
	logs, err := st.ListLog(id, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range logs {
		if e.Kind == pigtail.LogHandoff {
			n++
		}
	}
	return n
}

// TestPersoCellHandoffWriteNeedsCrossScope: a perso cell writing a handoff on a
// work subject is refused without --cross-scope and lands with it.
func TestPersoCellHandoffWriteNeedsCrossScope(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)

	wantScopeMismatch(t, "perso handoff write on work", runCLI(t, s, handoffCmd, "write", "sub_t", "state", "x"))
	if n := handoffCount(t, st, "sub_t"); n != 0 {
		t.Fatalf("refused handoff still landed: %d", n)
	}
	if err := runCLI(t, s, handoffCmd, "write", "sub_t", "state", "x", "--cross-scope"); err != nil {
		t.Fatalf("with --cross-scope: %v", err)
	}
	if n := handoffCount(t, st, "sub_t"); n != 1 {
		t.Fatalf("opted-in handoff did not land: %d", n)
	}
	// Its own scope needs no opt-in.
	if err := runCLI(t, s, handoffCmd, "write", "sub_p", "y"); err != nil {
		t.Fatalf("perso handoff on perso: %v", err)
	}
}

// TestWorkCellHandoffWriteNeverReachesPerso: the opt-in never widens a work
// caller — a perso subject stays unknown to it.
func TestWorkCellHandoffWriteNeverReachesPerso(t *testing.T) {
	withCells(t, func(work, _ string) string { return work })
	s, st := realSubjectDaemon(t)

	if err := runCLI(t, s, handoffCmd, "write", "sub_t", "x"); err != nil {
		t.Fatalf("work handoff on work: %v", err)
	}
	wantNotFound(t, "work handoff on perso", runCLI(t, s, handoffCmd, "write", "sub_p", "x", "--cross-scope"))
	if n := handoffCount(t, st, "sub_p"); n != 0 {
		t.Fatalf("work caller wrote a perso handoff: %d", n)
	}
}

// TestPersoCellThemeWritesNeedCrossScope covers theme create/set/delete on a
// work theme from a perso cell.
func TestPersoCellThemeWritesNeedCrossScope(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	s, st := realSubjectDaemon(t)

	create := []string{"create", "--name", "N", "--kind", "project", "--vault-link", "Globex/Projects/n"}
	wantScopeMismatch(t, "perso theme create Globex/", runCLI(t, s, themeCmd, create...))
	if err := runCLI(t, s, themeCmd, append(create, "--cross-scope")...); err != nil {
		t.Fatalf("theme create with --cross-scope: %v", err)
	}
	themes, err := st.ListThemes("globex")
	if err != nil {
		t.Fatal(err)
	}
	var created string
	for _, th := range themes {
		if th.VaultLink == "Globex/Projects/n" {
			created = th.ID
		}
	}
	if created == "" {
		t.Fatalf("opted-in theme create did not land: %+v", themes)
	}

	wantScopeMismatch(t, "perso theme set on work", runCLI(t, s, themeCmd, "set", "thm_t", "name=R"))
	if err := runCLI(t, s, themeCmd, "set", "thm_t", "name=R", "--cross-scope"); err != nil {
		t.Fatalf("theme set with --cross-scope: %v", err)
	}
	if th, _ := st.GetTheme("thm_t"); th.Name != "R" {
		t.Fatalf("opted-in theme set did not land: %q", th.Name)
	}

	wantScopeMismatch(t, "perso theme delete on work", runCLI(t, s, themeCmd, "delete", created))
	if err := runCLI(t, s, themeCmd, "delete", created, "--cross-scope"); err != nil {
		t.Fatalf("theme delete with --cross-scope: %v", err)
	}
	if _, err := st.GetTheme(created); err == nil {
		t.Fatal("opted-in theme delete did not land")
	}

	// Perso/ needs no opt-in.
	if err := runCLI(t, s, themeCmd, "create", "--name", "Q", "--kind", "area", "--vault-link", "Perso/Areas/q"); err != nil {
		t.Fatalf("perso theme create Perso/: %v", err)
	}
}

// TestWorkCellThemeWritesNeverReachPerso: a work cell cannot create, edit or
// delete a perso theme, --cross-scope or not.
func TestWorkCellThemeWritesNeverReachPerso(t *testing.T) {
	withCells(t, func(work, _ string) string { return work })
	s, st := realSubjectDaemon(t)

	// to a work cell another scope's root is no declared root — 400.
	var apiErr *pigtail.APIError
	if err := runCLI(t, s, themeCmd, "create", "--name", "N", "--kind", "area", "--vault-link", "Perso/Areas/n", "--cross-scope"); err == nil ||
		!errors.As(err, &apiErr) || apiErr.Code != pigtail.ErrInvalidRequest || strings.Contains(err.Error(), "perso") {
		t.Fatalf("work theme create Perso/: %v, want INVALID_REQUEST naming no other scope", err)
	}
	wantNotFound(t, "work theme set on perso", runCLI(t, s, themeCmd, "set", "thm_p", "name=R", "--cross-scope"))
	wantNotFound(t, "work theme delete on perso", runCLI(t, s, themeCmd, "delete", "thm_p", "--cross-scope"))
	if th, _ := st.GetTheme("thm_p"); th == nil || th.Name != "P" {
		t.Fatalf("work caller touched a perso theme: %+v", th)
	}
	if err := runCLI(t, s, themeCmd, "create", "--name", "T2", "--kind", "project", "--vault-link", "Globex/Projects/t2"); err != nil {
		t.Fatalf("work theme create Globex/: %v", err)
	}
}
