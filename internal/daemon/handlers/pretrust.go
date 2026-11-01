package handlers

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/aloysbr/pigtail/internal/claudeconfig"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// dispatchPreTrust is the pre-trust call, a seam for tests.
var dispatchPreTrust = claudeconfig.PreTrustWorkspace

// preTrustAgent pre-accepts folder trust before the daemon launches Claude in
// ws, when the profile names the cell's config dir. Without one the daemon
// does not know which cell will read trust: nothing is written, nothing logged.
// A leading "~/" is expanded like claudeprofile does for cells; any other
// relative value reaches PreTrust, which answers not_eligible.
func preTrustAgent(env map[string]string, ws *pigtail.Workspace, logf func(string, ...any)) claudeconfig.Result {
	dir := env["CLAUDE_CONFIG_DIR"]
	if dir == "" {
		return claudeconfig.Result{}
	}
	if env["CLAUDE_CODE_CUSTOM_OAUTH_URL"] != "" { // the agent's env, not only the daemon's (PreTrust checks os.Getenv)
		r := claudeconfig.Result{Outcome: claudeconfig.NotEligible, Reason: "CLAUDE_CODE_CUSTOM_OAUTH_URL is set in the agent env: Claude uses another config file name"}
		logf("workspace %s: folder trust not pre-accepted (%s): %s", ws.ID, r.Outcome, r.Reason)
		return r
	}
	if strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, dir[2:])
		}
	}
	r := dispatchPreTrust(dir, ws.Path)
	switch r.Outcome {
	case claudeconfig.AlreadyTrusted:
	case claudeconfig.Written:
		logf("workspace %s: folder trust pre-accepted for %s in %s (inherited from %s)", ws.ID, r.Key, dir, r.Ancestor)
	default:
		logf("workspace %s: folder trust not pre-accepted (%s): %s", ws.ID, r.Outcome, r.Reason)
	}
	return r
}
