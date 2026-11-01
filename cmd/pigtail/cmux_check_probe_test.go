package main

import (
	"strings"
	"testing"
)

// sub_x000000000000024: the human's check probes the input; an unattended
// pass (pigtaild's) does not — the daemon's own loop probes.
func TestCmuxCheckProbeOffWhenUnattended(t *testing.T) {
	o, _, _ := runCheckCmd(t)
	if !o.Probe {
		t.Fatal("a check run by hand must probe the input")
	}
	o, _, _ = runCheckCmd(t, "--unattended")
	if o.Probe {
		t.Fatal("an unattended check must not probe (pigtaild's loop does)")
	}
}

// G2 (M1): the help names the probe's one promise, verbatim.
func TestCmuxCheckHelpNamesQueryOnlyProbe(t *testing.T) {
	t.Parallel()
	long := cmuxCheckCmd().Long
	if !strings.Contains(long, "The input probe writes only a terminal status query (ESC [ 5 n) to the session's own tty: never a keystroke, never text.") {
		t.Fatal("pigtail cmux check --help must say the probe writes only a terminal status query")
	}
	if strings.Contains(long, "focus events") {
		t.Fatal("pigtail cmux check --help still says focus events")
	}
}

// D4 of sub_x000000000000024: --fix now stops a dead-input claude by SIGTERM;
// the help says so word for word (plan ## Goût, G2).
func TestCmuxCheckHelpNamesResumeSIGTERM(t *testing.T) {
	t.Parallel()
	long := strings.Join(strings.Fields(cmuxCheckCmd().Long), " ")
	const want = "A session whose input is dead (keystrokes never reach its pty) is resumed in a new tab only when --surface names it: its claude is stopped by SIGTERM (never a forced kill), the new tab opens without focus, and the dead tab closes once the new claude is seen; if it does not stop, nothing opens and the line says to stop it first."
	if !strings.Contains(long, want) {
		t.Fatalf("help lacks, verbatim:\n%s", want)
	}
}
