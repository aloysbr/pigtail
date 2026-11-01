package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func newHooksRunDaemon(t *testing.T, write func(w http.ResponseWriter)) *stubDaemon {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/workspaces/ws_bad/hooks/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		write(w)
	})
	s := &stubDaemon{srv: httptest.NewServer(mux)}
	t.Cleanup(s.srv.Close)
	return s
}

func runHooksCLI(t *testing.T, s *stubDaemon, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	var err error
	withStubDaemon(t, s, func() {
		cmd := hooksCmd()
		silenceUsageOnRunE(cmd) // comme runSpawnCLI : sans lui cobra écrit l'usage sur SetOut quand RunE échoue
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		err = cmd.Execute()
	})
	return out.String(), err
}

// TestHooksRunReportsTheRepair (criterion 4, CLI side).
func TestHooksRunReportsTheRepair(t *testing.T) {
	s := newHooksRunDaemon(t, func(w http.ResponseWriter) {
		writeData(w, http.StatusOK, pigtail.Workspace{ID: "ws_bad", Hooks: &pigtail.HooksResult{Status: pigtail.HooksOK, Log: "/s/hooks.log"}})
	})
	out, err := runHooksCLI(t, s, "run", "ws_bad")
	if err != nil || !strings.Contains(out, "ok") || !strings.Contains(out, "/s/hooks.log") {
		t.Fatalf("out %q err %v, want ok and the log", out, err)
	}
}

func TestHooksRunReportsAFailure(t *testing.T) {
	s := newHooksRunDaemon(t, func(w http.ResponseWriter) {
		writeData(w, http.StatusOK, pigtail.Workspace{ID: "ws_bad", Hooks: &pigtail.HooksResult{
			Status: pigtail.HooksFailed, Hook: "husky", Error: "hook husky failed: exit status 3", Log: "/s/hooks.log"}})
	})
	_, err := runHooksCLI(t, s, "run", "ws_bad")
	if err == nil || !strings.Contains(err.Error(), "husky") || !strings.Contains(err.Error(), "/s/hooks.log") {
		t.Fatalf("err = %v, want an error naming husky and the log", err)
	}
}

// TestHooksRunWhileRunning: the daemon answers 409 CONFLICT; the CLI says the
// hooks are already running and prints nothing on stdout.
func TestHooksRunWhileRunning(t *testing.T) {
	s := newHooksRunDaemon(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"code":"CONFLICT","message":"post-create hooks already running — log: /s/hooks.log"}}`)
	})
	out, err := runHooksCLI(t, s, "run", "ws_bad")
	var api *pigtail.APIError
	if err == nil || !strings.Contains(err.Error(), "already running") || !errors.As(err, &api) || api.Code != pigtail.ErrConflict {
		t.Fatalf("err = %v, want the 409 CONFLICT saying already running", err)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want nothing", out)
	}
}
