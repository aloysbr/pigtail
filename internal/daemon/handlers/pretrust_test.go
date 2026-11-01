package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/agent"
	"github.com/aloysbr/pigtail/internal/claudeconfig"
	"github.com/aloysbr/pigtail/internal/testutil"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func stubDispatchPreTrust(t *testing.T, res claudeconfig.Result, calls *[]string) {
	t.Helper()
	testutil.SerialOnly(t) // swaps the package's dispatchPreTrust
	saved := dispatchPreTrust
	t.Cleanup(func() { dispatchPreTrust = saved })
	dispatchPreTrust = func(dir, wt string) claudeconfig.Result {
		*calls = append(*calls, "pretrust:"+dir+"|"+wt)
		return res
	}
}

func TestPreTrustAgentLogsEveryOutcomeButAlreadyTrusted(t *testing.T) {
	ws := &pigtail.Workspace{ID: "w", Path: "/d/repo-wt/x"}
	home, _ := os.UserHomeDir()
	cases := []struct {
		res     claudeconfig.Result
		wantLog string // "" = no log line
	}{
		{claudeconfig.Result{Outcome: claudeconfig.Written, Key: "/d/repo", Ancestor: "/d"}, "workspace w: folder trust pre-accepted for /d/repo in /c (inherited from /d)"},
		{claudeconfig.Result{Outcome: claudeconfig.Failed, Reason: "lock held"}, "workspace w: folder trust not pre-accepted (failed): lock held"},
		{claudeconfig.Result{Outcome: claudeconfig.NoTrustedAncestor, Reason: "no directory …"}, "workspace w: folder trust not pre-accepted (no_trusted_ancestor): no directory …"},
		{claudeconfig.Result{Outcome: claudeconfig.NotEligible, Reason: `path "c" is not absolute`}, `workspace w: folder trust not pre-accepted (not_eligible): path "c" is not absolute`},
		{claudeconfig.Result{Outcome: claudeconfig.AlreadyTrusted, Key: "/d/repo"}, ""},
	}
	for _, tc := range cases {
		var calls, logs []string
		stubDispatchPreTrust(t, tc.res, &calls)
		logf := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
		preTrustAgent(map[string]string{"CLAUDE_CONFIG_DIR": "/c"}, ws, logf)
		if len(calls) != 1 || calls[0] != "pretrust:/c|/d/repo-wt/x" {
			t.Fatalf("%s: calls %v", tc.res.Outcome, calls)
		}
		if (tc.wantLog == "" && len(logs) != 0) || (tc.wantLog != "" && (len(logs) != 1 || logs[0] != tc.wantLog)) {
			t.Fatalf("%s: logs %q, want %q", tc.res.Outcome, logs, tc.wantLog)
		}
	}
	// No profile config dir: no call, no log.
	var calls, logs []string
	stubDispatchPreTrust(t, claudeconfig.Result{Outcome: claudeconfig.Written}, &calls)
	if r := preTrustAgent(nil, ws, func(f string, a ...any) { logs = append(logs, f) }); r.Outcome != "" || len(calls)+len(logs) != 0 {
		t.Fatalf("no config dir: %+v %v %v", r, calls, logs)
	}
	// CLAUDE_CODE_CUSTOM_OAUTH_URL in the agent env: no call, one not_eligible log line.
	calls, logs = nil, nil
	stubDispatchPreTrust(t, claudeconfig.Result{Outcome: claudeconfig.Written}, &calls)
	r := preTrustAgent(map[string]string{"CLAUDE_CONFIG_DIR": "/c", "CLAUDE_CODE_CUSTOM_OAUTH_URL": "https://x.invalid"}, ws,
		func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	if r.Outcome != claudeconfig.NotEligible || len(calls) != 0 || len(logs) != 1 || !strings.Contains(logs[0], "CLAUDE_CODE_CUSTOM_OAUTH_URL") {
		t.Fatalf("custom OAuth in agent env: %+v calls=%v logs=%v", r, calls, logs)
	}
	// "~/" is expanded before the call.
	calls = nil
	stubDispatchPreTrust(t, claudeconfig.Result{Outcome: claudeconfig.AlreadyTrusted}, &calls)
	preTrustAgent(map[string]string{"CLAUDE_CONFIG_DIR": "~/.claude-x"}, ws, func(string, ...any) {})
	if want := "pretrust:" + filepath.Join(home, ".claude-x") + "|/d/repo-wt/x"; len(calls) != 1 || calls[0] != want {
		t.Fatalf("calls %v, want %s", calls, want)
	}
}

// orderAgent records Spawn and stops spawnAgentInto there (an error return
// writes the response and leaves before the store is touched).
type orderAgent struct {
	agent.Agent
	calls *[]string
}

func (a orderAgent) Kind() string { return "fake" }
func (a orderAgent) Spawn(context.Context, *pigtail.Workspace, agent.SpawnIntent) (*pigtail.AgentSession, error) {
	*a.calls = append(*a.calls, "spawn")
	return nil, errors.New("stop here")
}

func TestSpawnAgentIntoPreTrustsBeforeSpawn(t *testing.T) {
	root := t.TempDir()
	yml := "version: 1\nname: t\nprofiles:\n  p:\n    config_dir: /c\n"
	if err := os.WriteFile(filepath.Join(root, pigtail.ProjectConfigFilename), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls, logs []string
	stubDispatchPreTrust(t, claudeconfig.Result{Outcome: claudeconfig.Written, Key: "/d/repo", Ancestor: "/d"}, &calls)
	svc := &Services{Agent: orderAgent{calls: &calls}, Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }}
	proj := &pigtail.Project{ID: "p1", RootPath: root, DefaultProfileID: "p"}
	ws := &pigtail.Workspace{ID: "w1", Path: "/d/wt"}
	rec := httptest.NewRecorder()
	if _, ok := spawnAgentInto(rec, httptest.NewRequest("POST", "/dispatch", nil), svc, testResponder{}, proj, ws, spawnParams{Intent: "x"}); ok {
		t.Fatal("the fake Spawn fails: want ok=false")
	}
	if want := []string{"pretrust:/c|/d/wt", "spawn"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls %v, want %v (pre-trust before Spawn)", calls, want)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "folder trust pre-accepted for /d/repo") {
		t.Fatalf("not logged through svc.logf: %v", logs)
	}
}
