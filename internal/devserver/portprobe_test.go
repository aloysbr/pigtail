package devserver

import (
	"os/exec"
	"testing"
)

// exitErr runs a trivial shell that exits with code, and returns the resulting
// *exec.ExitError. Building one by hand is not possible — ProcessState has no
// exported constructor — so the real thing is produced instead.
func exitErr(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+itoa(code)).Run()
	if err == nil {
		t.Fatalf("sh -c 'exit %d' unexpectedly succeeded", code)
	}
	return err
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestPortProbeResultSeparatesNoMatchFromFailure is the regression test for
// "IsPortBusy swallows lsof's errors".
//
// The whole point is the first two cases: exit 1 means "nothing is listening",
// while any other failure means the question was never answered. Collapsing
// both into (false, nil, nil) is what let a machine that could not run lsof
// look like a machine with no dev servers on it.
func TestPortProbeResultSeparatesNoMatchFromFailure(t *testing.T) {
	t.Run("exit 1 is a free port, not an error", func(t *testing.T) {
		busy, pids, err := portProbeResult(3000, nil, exitErr(t, 1))
		if err != nil {
			t.Fatalf("exit 1 means no match; it must not surface as an error: %v", err)
		}
		if busy || pids != nil {
			t.Fatalf("busy=%v pids=%v, want free", busy, pids)
		}
	})

	t.Run("any other exit status is a failed probe", func(t *testing.T) {
		_, _, err := portProbeResult(3000, []byte("lsof: some fatal thing"), exitErr(t, 3))
		if err == nil {
			t.Fatal("exit 3 must not be reported as a free port")
		}
	})

	t.Run("lsof missing entirely is a failed probe", func(t *testing.T) {
		runErr := exec.Command("/nonexistent/lsof").Run()
		if runErr == nil {
			t.Skip("unexpectedly ran")
		}
		if _, _, err := portProbeResult(3000, nil, runErr); err == nil {
			t.Fatal("a probe that could not run must not be reported as a free port")
		}
	})

	t.Run("listeners are parsed", func(t *testing.T) {
		busy, pids, err := portProbeResult(3000, []byte("1234\n5678\n"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !busy || len(pids) != 2 || pids[0] != 1234 || pids[1] != 5678 {
			t.Fatalf("busy=%v pids=%v, want [1234 5678]", busy, pids)
		}
	})

	t.Run("success with empty output is a free port", func(t *testing.T) {
		busy, _, err := portProbeResult(3000, []byte("\n"), nil)
		if err != nil || busy {
			t.Fatalf("busy=%v err=%v, want free", busy, err)
		}
	})
}

// TestIsPortBusyErrorsWhenLsofIsUnreachable exercises the real-world trigger:
// the daemon's plist puts a cmux-provided directory first on PATH, so a cmux
// update or a trimmed environment can take lsof out of reach. Before the fix
// that read as "every port is free".
func TestIsPortBusyErrorsWhenLsofIsUnreachable(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // an empty directory: no lsof anywhere
	busy, pids, err := IsPortBusy(3000)
	if err == nil {
		t.Fatalf("want an error when lsof cannot be found; got busy=%v pids=%v", busy, pids)
	}
	if busy {
		t.Error("a failed probe must never read as busy either")
	}
}
