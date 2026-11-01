package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/cmux"
)

// underTempDir reports whether p lies inside the test machine's temp dir or /tmp
// (symlinks resolved: on macOS $TMPDIR sits under /var → /private/var).
func underTempDir(p string) bool {
	if p == "" {
		return false
	}
	resolve := func(s string) string {
		if r, err := filepath.EvalSymlinks(s); err == nil {
			return r
		}
		return filepath.Clean(s)
	}
	abs := resolve(p)
	// testutil.MkdirTemp* makes its dirs under /tmp, not $TMPDIR.
	for _, tmp := range []string{resolve(os.TempDir()), resolve("/tmp")} {
		if abs == tmp || strings.HasPrefix(abs, tmp+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// TestPackageIsHermetic: no test of this package runs the real cmux nor
// writes the real state (TestMain sets the scene). A test that ran the real
// cmux cleared the badge of the developer's own workspace and waited on the
// live app under load; one that took the usage refresh lock in the real
// ~/.local/state/pigtail lost it to the running daemon.
func TestPackageIsHermetic(t *testing.T) {
	t.Parallel()
	cli := os.Getenv(cmux.EnvBundledCLI)
	if !underTempDir(cli) {
		t.Errorf("%s = %q, want a stand-in under %s", cmux.EnvBundledCLI, cli, os.TempDir())
	} else if fi, err := os.Stat(cli); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("%s = %q is not an executable file (%v)", cmux.EnvBundledCLI, cli, err)
	}
	for _, k := range []string{cmux.EnvWorkspaceID, cmux.EnvSurfaceID, cmux.EnvSocketPath} {
		if v := os.Getenv(k); v != "" {
			t.Errorf("%s = %q inherited from the developer's session, want empty", k, v)
		}
	}
	if st := os.Getenv("XDG_STATE_HOME"); !underTempDir(st) {
		t.Errorf("XDG_STATE_HOME = %q, want a dir under %s", st, os.TempDir())
	}
}

// TestHermeticEnvKeepsGoCaches: HOME moves to a temp dir, and `go` derives its
// module and build caches from HOME: tests that run `go build` or `go list`
// then re-downloaded the whole module cache (read-only, 0444/0555) into the
// temp dir and left it behind. The real
// caches are pinned before HOME moves.
func TestHermeticEnvKeepsGoCaches(t *testing.T) {
	t.Parallel()
	hermetic := os.Getenv(hermeticMarker)
	if hermetic == "" {
		t.Fatal("not under the hermetic TestMain")
	}
	out, err := exec.Command("go", "env", "GOMODCACHE", "GOCACHE").Output()
	if err != nil {
		t.Fatalf("go env: %v", err)
	}
	lines := strings.Fields(string(out))
	if len(lines) != 2 {
		t.Fatalf("go env gave %q, want two paths", out)
	}
	for _, p := range lines {
		if p == "" || underDir(p, hermetic) {
			t.Errorf("go cache %q is inside the hermetic dir %s", p, hermetic)
		}
	}
}

func underDir(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}
