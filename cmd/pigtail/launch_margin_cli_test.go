package main

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The CLI side of the daemon's launch margin guard (sub_x000000000000011):
// dispatch and run print the decision as a stderr warning, and a refusal
// (409 LAUNCH_OVER_MARGIN) exits 3 with the daemon's message.

var marginSwitch = &pigtail.LaunchMargin{Decision: "switched", Requested: "mp", Final: "jp",
	Warning: "launch margin: cell mp is over the margin — launching on jp instead"}

// launchDaemon serves /dispatch and /workspaces/{id}/run: a 201 carrying
// margin, or the 409 refusal when refuse.
func launchDaemon(t *testing.T, refuse bool) *stubDaemon {
	t.Helper()
	s, _ := realSubjectDaemonWith(t, func(r chi.Router, _ *store.Store) {
		answer := func(w http.ResponseWriter, req *http.Request, body any) {
			if refuse {
				envelopeResponder{}.Error(w, req, http.StatusConflict, pigtail.ErrLaunchOverMargin,
					"every account that can run cell mp is over the margin (5h or 7d at or above the account's ceiling); first release at Wed 15:04 — pass --over-margin to launch anyway", nil)
				return
			}
			envelopeResponder{}.JSON(w, req, http.StatusCreated, body)
		}
		r.Post("/dispatch", func(w http.ResponseWriter, req *http.Request) {
			answer(w, req, pigtail.DispatchResponse{WorkspaceID: "ws_1", AgentSessionID: "ag_1", Margin: marginSwitch})
		})
		r.Post("/workspaces/{id}/run", func(w http.ResponseWriter, req *http.Request) {
			answer(w, req, pigtail.RunResponse{WorkspaceID: "ws_1", AgentSessionID: "ag_2", Margin: marginSwitch})
		})
	})
	return s
}

func runLaunchCLI(t *testing.T, s *stubDaemon, mk func() *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	var o, e bytes.Buffer
	var err error
	withStubDaemon(t, s, func() {
		cmd := mk()
		silenceUsageOnRunE(cmd)
		cmd.SetOut(&o)
		cmd.SetErr(&e)
		cmd.SetArgs(args)
		err = cmd.Execute()
	})
	return o.String(), e.String(), err
}

func TestDispatchCLIPrintsMarginWarning(t *testing.T) {
	out, stderr, err := runLaunchCLI(t, launchDaemon(t, false), dispatchCmd, "--project", "prj_x", "do it")
	if err != nil || !strings.Contains(out, "✓ dispatched · workspace ws_1") {
		t.Fatalf("dispatch = %v out %q", err, out)
	}
	if !strings.Contains(stderr, "warning: launch margin: cell mp is over the margin — launching on jp instead") {
		t.Fatalf("stderr = %q", stderr)
	}
	_, _, err = runLaunchCLI(t, launchDaemon(t, true), dispatchCmd, "--project", "prj_x", "do it")
	if exitCode(err) != 3 || !strings.Contains(err.Error(), "first release at") {
		t.Fatalf("refusal = exit %d %v, want 3 and the daemon's message", exitCode(err), err)
	}
}

func TestRunCLIPrintsMarginWarning(t *testing.T) {
	out, stderr, err := runLaunchCLI(t, launchDaemon(t, false), runCmd, "ws_1", "do it")
	if err != nil || !strings.Contains(out, "✓ agent ag_2 started") {
		t.Fatalf("run = %v out %q", err, out)
	}
	if !strings.Contains(stderr, "warning: launch margin: cell mp is over the margin") {
		t.Fatalf("stderr = %q", stderr)
	}
	_, _, err = runLaunchCLI(t, launchDaemon(t, true), runCmd, "ws_1", "do it")
	if exitCode(err) != 3 {
		t.Fatalf("refusal = exit %d %v, want 3", exitCode(err), err)
	}
}
