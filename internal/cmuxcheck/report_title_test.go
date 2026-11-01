package cmuxcheck

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/scope"
	"github.com/aloysbr/pigtail/internal/scope/scopetest"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// D23 of the repo (D18 spirit): a workspace's title belongs to the scope of
// the subject it is bound to — cmuxsync renames a bound workspace with its
// subject's short title (compute.go), which cmux saves as the workspace's
// customTitle. A pane split in that workspace may run on another scope's
// account: its row is readable by that account's cell, but the workspace
// title is proven of no scope, so a non-master reader never sees it — text
// and --json alike. The pane's own title (its session's) stays; the master
// reads both. Same for a panel rebuilt from the peers snapshot, whose
// CmuxTitle is the workspace's title too.
func TestRedactedHidesWorkspaceTitle(t *testing.T) {
	scopetest.Three(t)
	master, other := scope.Master(), otherScope(t)
	const wsTitle, paneTitle = "leak-workspace-subject-title", "pane-own-title"
	dir := t.TempDir()
	file := filepath.Join(dir, PreviousFile)
	body := `{"windows":[{"tabManager":{"workspaces":[{"workspaceId":"W1","customTitle":"` + wsTitle + `","panels":[
	 {"id":"S-T","title":"` + paneTitle + `","terminal":{"resumeBinding":{"kind":"claude","autoResume":false,
	   "checkpointId":"sid-t","cwd":"/w","command":"x","environment":{"CLAUDE_CONFIG_DIR":"/cfg/other"},"launchCommand":{"arguments":[]}}}}]}]}}]}`
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ph, err := LoadPhoto(file)
	if err != nil {
		t.Fatal(err)
	}
	peer := pigtail.PeerSession{SessionID: "sid-p", CmuxSurfaceID: "S-P", CmuxWorkspaceID: "W1", CmuxTitle: wsTitle,
		ConfigDir: "/cfg/other", UpdatedAt: tNow}
	l := cleanLaunch()
	l.Start = tNow // a recent relaunch: only the photo before is classified
	rebuilt, _ := ChooseBefore(l, Photo{}, os.ErrNotExist, []pigtail.PeerSession{peer})
	w := newWorld()
	rows := Classify(ph, Photo{}, nil, l, Options{}, w.deps())
	rows = append(rows, Classify(rebuilt, Photo{}, nil, l, Options{}, w.deps())...)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v; want the file's pane and the peer's", rows)
	}
	scopeOf := func(cfg string) (string, bool) {
		if cfg == "/cfg/other" {
			return string(other), true
		}
		return "", false
	}
	print := func(r Report) string {
		var buf bytes.Buffer
		r.Write(&buf)
		b, _ := json.Marshal(r)
		return buf.String() + string(b)
	}
	rep := Report{Launch: l, Rows: rows}
	if out := print(rep.Redacted(other, scopeOf)); strings.Contains(out, wsTitle) || !strings.Contains(out, paneTitle) {
		t.Fatalf("non-master cell of the pane's scope: output shows the workspace title (or lost the pane's own):\n%s", out)
	}
	if out := print(rep.Redacted(master, scopeOf)); !strings.Contains(out, wsTitle) {
		t.Fatalf("master: workspace title gone:\n%s", out)
	}
}

// otherScope: a declared scope that is not the master (scopetest.Three).
func otherScope(t *testing.T) scope.Scope {
	t.Helper()
	for _, s := range scope.Active().Names() {
		if !scope.SeesAll(s) {
			return s
		}
	}
	t.Fatal("no non-master scope declared")
	return ""
}
