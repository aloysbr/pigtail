package daemon

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aloysbr/pigtail/internal/claudeusage"
	"github.com/aloysbr/pigtail/internal/daemon/events"
	"github.com/aloysbr/pigtail/internal/daemon/store"
	"github.com/aloysbr/pigtail/internal/daemon/usagehistory"
	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/aloysbr/pigtail/internal/spawnreg"
)

// within: is p inside dir (or dir itself)?
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// usageFilesUnder lists the usage history and refresh-state files under root.
func usageFilesUnder(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if n := d.Name(); n == usagehistory.FileName || n == claudeusage.BackgroundStateName {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// The package's TestMain isolates every default seam: no Server built by a
// test, no sweep pass and no history append reaches ~/.local/state/pigtail or
// ~/.config/pigtail. Driven on the sweep rig's fake seams (never the default
// ones, which would read setup.ConfigDir() and launch a real claude -p).
func TestServerNeverTouchesRealUsageState(t *testing.T) {
	tmp := os.TempDir()
	// testutil.MkdirTempMain makes the TestMain dirs under /tmp, not $TMPDIR.
	underTmp := func(p string) bool { return within(p, tmp) || within(p, "/tmp") }
	xdgState := os.Getenv("XDG_STATE_HOME")
	if xdgState == "" || !underTmp(xdgState) {
		t.Fatalf("XDG_STATE_HOME = %q, want a dir under %s or /tmp (TestMain)", xdgState, tmp)
	}
	if cd := setup.ConfigDir(); !underTmp(cd) {
		t.Fatalf("setup.ConfigDir() = %q, want under %s or /tmp (TestMain)", cd, tmp)
	}
	sp := spawnreg.DefaultPath()
	if !within(sp, xdgState) {
		t.Fatalf("spawnreg.DefaultPath() = %q, want under %s", sp, xdgState)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && within(sp, filepath.Join(home, ".local", "state")) {
		t.Fatalf("spawnreg.DefaultPath() = %q is the real state dir", sp)
	}
	cwd := t.TempDir()
	t.Chdir(cwd)

	pass := func(t *testing.T, srv *Server) {
		t.Helper()
		if err := srv.usageHistory.Append([]claudeusage.Sample{{At: sweepNow, AccountUUID: uuidSweepM, Label: "m"}}); err != nil {
			t.Fatal(err)
		}
		r := newSweepRig(t)
		r.pass.stateDir, r.pass.history = srv.cfg.UsageStateDir, srv.usageHistory
		r.pass.run(context.Background())
	}
	newServer := func(t *testing.T, stateDir string) *Server {
		t.Helper()
		st, err := store.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		sock := testSocketPath(t)
		srv, err := New(Config{SocketPath: sock, UsageStateDir: stateDir}, st, events.New(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return srv
	}

	// Empty UsageStateDir: no history, nothing written anywhere we can see.
	srv := newServer(t, "")
	if srv.usageHistory != nil {
		t.Fatal("a history opened without a state dir")
	}
	pass(t, srv)
	for _, root := range []string{xdgState, setup.ConfigDir(), cwd, filepath.Dir(srv.cfg.SocketPath)} {
		if got := usageFilesUnder(t, root); len(got) != 0 {
			t.Fatalf("empty UsageStateDir wrote %v", got)
		}
	}

	// Set: written there, and only there (positive control: the walk finds it).
	dir := t.TempDir()
	srv = newServer(t, dir)
	if srv.usageHistory == nil {
		t.Fatal("no history opened with a state dir")
	}
	pass(t, srv)
	got := usageFilesUnder(t, dir)
	if len(got) != 2 {
		t.Fatalf("state dir holds %v, want the history and the refresh state", got)
	}
	if n := len(srv.usageHistory.Since(time.Time{})); n < 2 {
		t.Fatalf("history holds %d samples, want the append and the pass", n)
	}
	for _, root := range []string{xdgState, setup.ConfigDir(), cwd, filepath.Dir(srv.cfg.SocketPath)} {
		if got := usageFilesUnder(t, root); len(got) != 0 {
			t.Fatalf("set UsageStateDir also wrote %v", got)
		}
	}
}
