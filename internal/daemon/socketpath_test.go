package daemon

import (
	"net"
	"path/filepath"
	"testing"

	"github.com/aloysbr/pigtail/internal/testutil"
)

// sunPathMax is the smallest sun_path capacity across the platforms pigtail
// runs on: 104 bytes on darwin, 108 on linux. Checking against the smaller of
// the two means a Linux CI run rejects a path that would only break on macOS
// — which is precisely what stayed invisible until 2026-07-28.
const sunPathMax = 104

// testSocketPath returns a unix socket path short enough to bind on every
// supported platform, and registers its directory for cleanup.
//
// t.TempDir() is not usable for a socket: it returns
// $TMPDIR/<TestName><random>/<NNN>, and on macOS $TMPDIR is already a 49-byte
// per-user path. Any test whose name runs past ~27 characters overflows
// sun_path, and net.Listen fails with a bare "invalid argument" that names
// neither the limit nor the cause. Linux keeps $TMPDIR at /tmp, so the same
// code passes there and CI never sees it.
func testSocketPath(t *testing.T) string {
	t.Helper()

	dir := testutil.MkdirTemp(t, "pgt-")

	p := filepath.Join(dir, "d.sock")
	if len(p) > sunPathMax {
		t.Fatalf("socket path is %d bytes, over the %d-byte sun_path limit: %q\n"+
			"$TMPDIR is too long for a unix socket; set TMPDIR to a shorter directory", len(p), sunPathMax, p)
	}
	return p
}

// TestSocketPathBindsUnderALongTestNameThatWouldOverflowTheTempDirPrefix is
// deliberately named at length: the bug this guards against is triggered by
// the test name itself, which t.TempDir() embeds in the directory it returns.
func TestSocketPathBindsUnderALongTestNameThatWouldOverflowTheTempDirPrefix(t *testing.T) {
	p := testSocketPath(t)

	if len(p) > sunPathMax {
		t.Fatalf("socket path is %d bytes, over the %d-byte sun_path limit: %q", len(p), sunPathMax, p)
	}

	ln, err := net.Listen("unix", p)
	if err != nil {
		t.Fatalf("listen on %q (%d bytes): %v", p, len(p), err)
	}
	t.Cleanup(func() { _ = ln.Close() })
}
