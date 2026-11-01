package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/claudeprofile"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// logRequest is what the stub daemon saw of the last subject-log call.
type logRequest struct {
	method, query, scope, cross string
}

// withCells points the CLI at a cell table (work dir, perso dir) and runs the
// session as the given Claude config dir.
func withCells(t *testing.T, sessionDir func(work, perso string) string) {
	t.Helper()
	xdg := t.TempDir()
	workDir, persoDir := filepath.Join(xdg, "work"), filepath.Join(xdg, "perso")
	if err := os.MkdirAll(filepath.Join(xdg, "pigtail"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "cells:\n  t: { config_dir: " + workDir + ", scope: globex }\n  p: { config_dir: " + persoDir + ", scope: perso }\n"
	if err := os.WriteFile(filepath.Join(xdg, "pigtail", claudeprofile.Filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("CLAUDE_CONFIG_DIR", sessionDir(workDir, persoDir))
	t.Setenv(envScope, "")
	prev := flagScope
	t.Cleanup(func() { flagScope = prev })
}

func runSubjectLog(t *testing.T, args ...string) (*logRequest, error) {
	t.Helper()
	testutil.SerialOnly(t) // swaps os.Stdout
	var got *logRequest
	mux := http.NewServeMux()
	mux.HandleFunc("/subjects/sub_1/log", func(w http.ResponseWriter, r *http.Request) {
		got = &logRequest{r.Method, r.URL.RawQuery, r.Header.Get(pigtail.HeaderScope), r.Header.Get(pigtail.HeaderCrossScope)}
		if r.Method == http.MethodPost {
			writeData(w, http.StatusCreated, pigtail.SubjectLogEntry{ID: "log_1", SubjectID: "sub_1", Kind: "note", Text: "x"})
			return
		}
		writeData(w, http.StatusOK, []pigtail.SubjectLogEntry{})
	})
	s := &stubDaemon{srv: httptest.NewServer(mux)}
	t.Cleanup(s.srv.Close)
	var err error
	withStubDaemon(t, s, func() {
		// Keep the command's own printing off the test output.
		stdout := os.Stdout
		devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		os.Stdout = devnull
		defer func() { os.Stdout = stdout; _ = devnull.Close() }()
		cmd := subjectCmd()
		cmd.SetArgs(append([]string{"log"}, args...))
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err = cmd.Execute()
	})
	return got, err
}

func TestSubjectLogReadsWithoutText(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	got, err := runSubjectLog(t, "sub_1", "--since", "7d", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.method != http.MethodGet || !strings.HasPrefix(got.query, "since=") {
		t.Fatalf("read request = %+v", got)
	}
	if got.scope != "perso" {
		t.Fatalf("scope header = %q, want perso (from the cell)", got.scope)
	}
}

func TestSubjectLogAppendsWithText(t *testing.T) {
	withCells(t, func(_, perso string) string { return perso })
	got, err := runSubjectLog(t, "sub_1", "--cross-scope", "shipped", "it")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.method != http.MethodPost || got.cross != pigtail.CrossScopeWrite {
		t.Fatalf("append request = %+v", got)
	}
	if _, err := runSubjectLog(t, "sub_1", "--since", "7d", "some", "text"); err == nil || !strings.Contains(err.Error(), "--since") {
		t.Fatalf("--since with text: %v, want an explicit refusal", err)
	}
}

func TestSubjectScopeFromWorkCellCannotWiden(t *testing.T) {
	withCells(t, func(work, _ string) string { return work })
	flagScope = "perso"
	got, err := runSubjectLog(t, "sub_1")
	if err == nil || !strings.Contains(err.Error(), "globex cell") {
		t.Fatalf("work cell --scope perso: %v, want a refusal", err)
	}
	if got != nil {
		t.Fatalf("the refused call still reached the daemon: %+v", got)
	}
	flagScope = ""
	got, err = runSubjectLog(t, "sub_1")
	if err != nil || got.scope != "globex" {
		t.Fatalf("work cell read: %+v, %v", got, err)
	}
}

func TestSubjectScopeBareDirIsWork(t *testing.T) {
	withCells(t, func(_, _ string) string { return "/somewhere/.claude" })
	got, err := runSubjectLog(t, "sub_1")
	if err != nil || got.scope != "globex" {
		t.Fatalf("bare dir: %+v, %v", got, err)
	}
	t.Setenv(envScope, "perso")
	got, err = runSubjectLog(t, "sub_1")
	if err != nil || got.scope != "perso" {
		t.Fatalf("bare dir with %s=perso: %+v, %v", envScope, got, err)
	}
}
