package testutil

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/fsutil"
)

// TempPrefix is the name prefix of every directory MkdirTemp makes: the one
// the daemon's tmp sweeper recognises (fsutil.TestTempPrefix).
const TempPrefix = fsutil.TestTempPrefix

// tmpRoot is short on purpose: a Unix socket path is capped near 104 bytes on
// darwin, and t.TempDir() under /var/folders overshoots it.
const tmpRoot = "/tmp"

// MkdirTemp makes /tmp/pt-<name><digits> and removes it at the end of the
// test, read-only files and directories included (fsutil.RemoveTree). A
// failed removal is logged, never swallowed. name is lowercase letters,
// digits and dashes ("admit-"): the tmp sweeper matches the result by name.
//
// Use it instead of os.MkdirTemp (TestNoRawMkdirTempInTests): a raw one is
// neither cleaned when it holds a read-only tree nor recognised by the sweeper.
// t.TempDir() stays fine when the path length does not matter.
func MkdirTemp(t testing.TB, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp(tmpRoot, TempPrefix+name)
	if err != nil {
		t.Fatalf("testutil.MkdirTemp: %v", err)
	}
	t.Cleanup(func() { removeLogged(t.Logf, dir, fsutil.RemoveTree) })
	return dir
}

// MkdirTempMain is MkdirTemp for a TestMain, which has no t: the failure to
// clean goes to stderr. Call cleanup explicitly before os.Exit, a defer would
// not run.
func MkdirTempMain(name string) (dir string, cleanup func()) {
	dir, err := os.MkdirTemp(tmpRoot, TempPrefix+name)
	if err != nil {
		panic(fmt.Sprintf("testutil.MkdirTempMain: %v", err))
	}
	return dir, func() {
		removeLogged(func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}, dir, fsutil.RemoveTree)
	}
}

// sharedMarker is the env var that carries the dir MkdirTempMainShared made
// for name, from a test binary to the copies of itself it re-executes.
func sharedMarker(name string) string {
	return "PIGTAIL_TESTMAIN_DIR_" + strings.ToUpper(strings.NewReplacer("-", "_").Replace(name))
}

// MkdirTempMainShared is MkdirTempMain for a package whose tests re-execute
// their own binary as a helper process (exec.Command(os.Args[0], "-test.run=…")).
// The helper runs TestMain again and is killed by its test, so a dir it made
// would never be cleaned. The first call exports the dir under a marker env
// var; a later call in a child that inherited it returns the same dir with a
// no-op cleanup and creates nothing. The parent owns the dir and removes it.
func MkdirTempMainShared(name string) (dir string, cleanup func()) {
	key := sharedMarker(name)
	if dir = os.Getenv(key); dir != "" {
		return dir, func() {}
	}
	dir, cleanup = MkdirTempMain(name)
	if err := os.Setenv(key, dir); err != nil {
		panic(fmt.Sprintf("testutil.MkdirTempMainShared: %v", err))
	}
	return dir, cleanup
}

// removeLogged removes dir with remove and reports a failure through logf.
func removeLogged(logf func(string, ...any), dir string, remove func(string) error) {
	if err := remove(dir); err != nil {
		logf("testutil: could not remove temp dir %s: %v", dir, err)
	}
}
