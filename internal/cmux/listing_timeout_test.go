package cmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// slowCLI installs a cmux stand-in that answers the listings after
// listingDelay and holds every other call (a write) for writeDelay. Each
// test sets the bounds it asserts after it (fakeCLI widens the write bound).
func slowCLI(t *testing.T, listingDelay, writeDelay string) {
	t.Helper()
	fakeCLI(t, "case \"$1\" in\n"+
		"  list-windows) sleep "+listingDelay+"; echo '[]' ;;\n"+
		"  sessions) sleep "+listingDelay+"; echo '[]' ;;\n"+
		"  *) sleep "+writeDelay+" ;;\n"+
		"esac\n")
}

// killedByBound reports whether err is a call cut by its bound: the process
// killed, or the context expired before it could start (a loaded machine).
func killedByBound(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "signal: killed") || errors.Is(err, context.DeadlineExceeded))
}

// D1·B: under load a listing takes longer than the write bound; it must
// still answer, while a write keeps the short bound (incident 2026-10-04:
// 51 of 55 pauses were listings killed at 3 s). Which bound each call takes
// is proven in memory by TestEachCallTakesItsBound; this end-to-end twin
// keeps a real process with wide margins on both sides (0.5 s against 60 s,
// 30 s against 200 ms).
func TestListingOutlivesTheWriteBound(t *testing.T) {
	slowCLI(t, "0.5", "30")
	defer SetCallTimeoutForTest(200 * time.Millisecond)()
	defer SetListingTimeoutForTest(60 * time.Second)()
	sb := SidebarCLI{}
	if _, err := sb.ListWindows(); err != nil {
		t.Fatalf("listing cut by the write bound: %v", err)
	}
	err := sb.SetStatus("C0FFEE00-0000-4000-8000-000000000001", StatusEntry{Key: "pigtail-attn", Value: "x"})
	if !killedByBound(err) {
		t.Fatalf("a write must be killed at the 200 ms write bound, got %v", err)
	}
}

// The loop's registry snapshot takes the listing bound too; the hook-path
// SessionsJSON keeps the short one.
func TestSessionsListingTakesTheListingBound(t *testing.T) {
	slowCLI(t, "0.5", "30")
	defer SetCallTimeoutForTest(200 * time.Millisecond)()
	defer SetListingTimeoutForTest(60 * time.Second)()
	if _, err := SessionsJSONListing(); err != nil {
		t.Fatalf("snapshot cut by the write bound: %v", err)
	}
	if _, err := SessionsJSON(); !killedByBound(err) {
		t.Fatalf("the hook-path SessionsJSON lost its short bound: %v", err)
	}
}

// A cmux killed at its bound may leave a child holding stdout open; the call
// must not wait for that child (cmd.WaitDelay). The bound (2 s) outlasts a
// shell's start on a loaded machine so the grandchild exists when the kill
// lands (its pid file is the positive control); without WaitDelay the call
// waits the grandchild's 20 s, with it the bound plus one second.
func TestTimeoutKillsGrandchildHoldingStdout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	fakeCLI(t, "sleep 20 &\necho $! > '"+pidFile+"'\nexec sleep 20")
	defer SetCallTimeoutForTest(2 * time.Second)()
	start := time.Now()
	_, err := SessionsJSON()
	el := time.Since(start)
	b, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("the stand-in never forked its grandchild within the 2 s bound: %v", rerr)
	}
	if pid, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil {
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	}
	if el >= 10*time.Second {
		t.Fatalf("SessionsJSON waited %s for a grandchild holding stdout (err %v)", el, err)
	}
	if !killedByBound(err) {
		t.Fatalf("SessionsJSON past its bound = %v, want killed", err)
	}
}

// A cmux that exits 0 while a child still holds stdout open past WaitDelay
// makes Wait return exec.ErrWaitDelay: the call succeeded, its output is
// complete, and it must not read as a failure.
func TestSuccessWithGrandchildHoldingStdoutIsSuccess(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	fakeCLI(t, "sleep 20 &\necho $! > '"+pidFile+"'\necho '[]'\nexit 0")
	defer SetCallTimeoutForTest(30 * time.Second)()
	out, err := SessionsJSON()
	if b, rerr := os.ReadFile(pidFile); rerr != nil {
		t.Fatalf("the stand-in never forked its grandchild: %v", rerr)
	} else if pid, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil {
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	}
	if err != nil {
		t.Fatalf("a cmux that exited 0 read as a failure: %v", err)
	}
	if strings.TrimSpace(string(out)) != "[]" {
		t.Fatalf("output %q, want the cmux answer", out)
	}
}

// Each call takes its bound, proven in memory: the seam records the bound it
// is handed, no process and no clock involved.
func TestEachCallTakesItsBound(t *testing.T) {
	var got []time.Duration
	old := execTimeout
	execTimeout = func(d time.Duration, _ []string, _ ...string) ([]byte, error) {
		got = append(got, d)
		return []byte("[]"), nil
	}
	t.Cleanup(func() { execTimeout = old })
	defer SetCallTimeoutForTest(7 * time.Second)()
	defer SetListingTimeoutForTest(70 * time.Second)()

	sb := SidebarCLI{}
	const ws = "C0FFEE00-0000-4000-8000-000000000001"
	cases := []struct {
		name string
		call func()
		want time.Duration
	}{
		{"SessionsJSON", func() { _, _ = SessionsJSON() }, 7 * time.Second},
		{"WorkspaceList", func() { _, _ = WorkspaceList() }, 7 * time.Second},
		{"WorkspaceKnown", func() { _, _ = WorkspaceKnown(ws) }, 7 * time.Second},
		{"SidebarCLI.SetStatus", func() { _ = sb.SetStatus(ws, StatusEntry{Key: "pigtail-attn", Value: "x"}) }, 7 * time.Second},
		{"SidebarCLI.ClearStatus", func() { _ = sb.ClearStatus(ws, "pigtail-attn") }, 7 * time.Second},
		{"SessionsJSONListing", func() { _, _ = SessionsJSONListing() }, 70 * time.Second},
		{"SidebarCLI.ListWindows", func() { _, _ = sb.ListWindows() }, 70 * time.Second},
	}
	for _, c := range cases {
		got = nil
		c.call()
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s took bounds %v, want [%s]", c.name, got, c.want)
		}
	}
}
