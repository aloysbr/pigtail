package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// foldersRoot is `pigtail cmux folders …` mounted under a bare root.
func foldersRoot() *cobra.Command {
	root := &cobra.Command{Use: "cmux", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(cmuxFoldersCmd())
	return root
}

// foldersDaemon serves POST /cmux/folders/toggle with the given status and
// body, in the daemon's {"data":…} envelope.
func foldersDaemon(t *testing.T, status int, data any, msg string) (*stubDaemon, *[]string) {
	t.Helper()
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status >= 400 {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "CONFLICT", "message": msg}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return &stubDaemon{srv: srv}, &hits
}

func runFolders(t *testing.T, s *stubDaemon, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errb bytes.Buffer
	withStubDaemon(t, s, func() {
		cmd := foldersRoot()
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(&errb)
		err = cmd.Execute()
	})
	return out.String(), errb.String(), err
}

func TestCmuxFoldersToggleCLI(t *testing.T) {
	for _, tc := range []struct {
		data map[string]any
		want string
	}{
		{map[string]any{"collapsed": true, "folders": 4}, "dossiers repliés (4)\n"},
		{map[string]any{"collapsed": false, "folders": 5}, "dossiers dépliés (5)\n"},
	} {
		s, hits := foldersDaemon(t, 200, tc.data, "")
		out, _, err := runFolders(t, s, "folders", "toggle")
		if err != nil || out != tc.want {
			t.Fatalf("out = %q, err = %v, want %q", out, err, tc.want)
		}
		if len(*hits) != 1 || (*hits)[0] != "POST /cmux/folders/toggle" {
			t.Fatalf("hits = %q", *hits)
		}
	}
}

func TestCmuxFoldersToggleQuiet(t *testing.T) {
	s, _ := foldersDaemon(t, 200, map[string]any{"collapsed": true, "folders": 2}, "")
	out, errOut, err := runFolders(t, s, "folders", "toggle", "--quiet")
	if err != nil || out != "" || errOut != "" {
		t.Fatalf("--quiet printed %q / %q, err = %v", out, errOut, err)
	}
}

func TestCmuxFoldersToggleIdleFails(t *testing.T) {
	s, _ := foldersDaemon(t, http.StatusConflict, nil, "folders idle")
	out, _, err := runFolders(t, s, "folders", "toggle")
	if err == nil || !strings.Contains(err.Error(), "folders idle") || out != "" {
		t.Fatalf("out = %q, err = %v, want a failure naming « folders idle »", out, err)
	}
}

// The toggle speaks for the caller's scope, like `pigtail cmux sidebar on`
// (Task 8): it sends X-Pigtail-Scope, outside a cell the default scope.
func TestCmuxFoldersToggleSendsCallerScope(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no cell table: not in a cell
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv(envScope, "")
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get(pigtail.HeaderScope))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"collapsed": true, "folders": 1}})
	}))
	t.Cleanup(srv.Close)
	if _, _, err := runFolders(t, &stubDaemon{srv: srv}, "folders", "toggle"); err != nil {
		t.Fatal(err)
	}
	if want := string(scope.Default()); len(got) != 1 || got[0] != want {
		t.Fatalf("X-Pigtail-Scope = %q, want [%q] (newScopedClient, as `cmux sidebar on`)", got, want)
	}
	c, _, err := cmuxCmd().Find([]string{"folders", "toggle"})
	if err != nil || c.Annotations[scopeAccessKey] != "read" {
		t.Fatalf("toggle annotations = %v (err %v), want scopedRead()", c.Annotations, err)
	}
}

// The real `pigtail cmux` tree carries the toggle (the tests above mount it
// under a bare root, so they would not see it missing).
func TestCmuxCmdRegistersFoldersToggle(t *testing.T) {
	t.Parallel()
	c, _, err := cmuxCmd().Find([]string{"folders", "toggle"})
	if err != nil || c.Name() != "toggle" {
		t.Fatalf("pigtail cmux folders toggle not registered: %v (found %q)", err, c.Name())
	}
}
