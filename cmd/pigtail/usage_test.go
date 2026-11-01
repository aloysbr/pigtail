package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestUsageDumpOnlyForMisuse pins the two properties that have to hold
// together, driven through the real command tree so cobra's own ordering
// (parse flags → validate args → validate flag groups → RunE) is what is
// under test, not a stand-in:
//
//  1. A *runtime* failure — daemon unreachable, workspace missing, port busy
//     — prints the error and nothing else. Those errors carry operational
//     instructions ("stop the daemon, restore, start it again"); a usage dump
//     underneath scrolls them off the screen, which is the bug.
//
//  2. Genuine *misuse* — unknown flag, unparseable flag value, wrong arg
//     count, mutually exclusive flags — still prints usage, because the user
//     needs to be shown how to invoke the command.
//
// The two are one test on purpose: every cheap fix for (1) that has been
// tried breaks (2), and a test that only covered (1) would pass for
// `root.SilenceUsage = true`, which silences flag-parse and arg-count errors
// as well.
func TestUsageDumpOnlyForMisuse(t *testing.T) {
	deadSocket := filepath.Join(t.TempDir(), "nonexistent.sock")

	cases := []struct {
		name      string
		args      []string
		wantUsage bool
	}{
		{
			name:      "runtime error: daemon unreachable",
			args:      []string{"--socket", deadSocket, "list"},
			wantUsage: false,
		},
		{
			name:      "misuse: unknown flag",
			args:      []string{"list", "--nope"},
			wantUsage: true,
		},
		{
			name:      "misuse: unparseable flag value",
			args:      []string{"clean", "--timeout", "not-a-duration", "ws_x"},
			wantUsage: true,
		},
		{
			name:      "misuse: wrong arg count",
			args:      []string{"attach"},
			wantUsage: true,
		},
		{
			name:      "misuse: mutually exclusive flags",
			args:      []string{"stop", "--idle=1m", "--all"},
			wantUsage: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newRootCmd()
			var out bytes.Buffer
			// Cobra writes the error to ErrOrStderr and the usage block to
			// OutOrStdout; one buffer keeps the assertion about what the user
			// sees, not about which stream carried it.
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(tc.args)

			if err := root.Execute(); err == nil {
				t.Fatalf("expected %v to fail, it succeeded; output:\n%s", tc.args, out.String())
			}

			gotUsage := strings.Contains(out.String(), "Usage:")
			if gotUsage != tc.wantUsage {
				verb := "printed"
				if tc.wantUsage {
					verb = "did not print"
				}
				t.Fatalf("%v %s a usage block; wantUsage=%v. Output:\n%s",
					tc.args, verb, tc.wantUsage, out.String())
			}
			// Whatever happens, the error itself must still reach the user.
			if !strings.Contains(out.String(), "Error:") {
				t.Fatalf("%v printed no error line at all. Output:\n%s", tc.args, out.String())
			}
		})
	}
}

// TestSilenceUsageOnRunE covers the wrapper in isolation: it must set
// SilenceUsage on the command cobra is actually running (cobra passes the
// command itself as the first RunE argument), it must do so *before* the
// wrapped RunE gets a chance to fail, it must leave the wrapped error
// untouched, and it must reach nested subcommands.
func TestSilenceUsageOnRunE(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")

	var sawSilencedDuringRun bool
	leaf := &cobra.Command{
		Use: "leaf",
		RunE: func(c *cobra.Command, _ []string) error {
			sawSilencedDuringRun = c.SilenceUsage
			return boom
		},
	}
	mid := &cobra.Command{Use: "mid"}
	mid.AddCommand(leaf)
	root := &cobra.Command{Use: "root"}
	root.AddCommand(mid)

	silenceUsageOnRunE(root)

	if leaf.SilenceUsage {
		t.Fatal("SilenceUsage must not be set up front — that would silence usage for flag-parse and arg-count errors too")
	}
	if err := leaf.RunE(leaf, nil); !errors.Is(err, boom) {
		t.Fatalf("the wrapper must return the wrapped error verbatim, got %v", err)
	}
	if !sawSilencedDuringRun {
		t.Fatal("SilenceUsage must already be set when the wrapped RunE runs, not after it returns")
	}
}

// TestSilenceUsageOnRunELeavesRunOnlyCommandsAlone: commands that use Run
// (not RunE) cannot fail, so there is nothing to silence and the wrapper must
// not invent a RunE for them — that would change which branch cobra takes.
func TestSilenceUsageOnRunELeavesRunOnlyCommandsAlone(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{Use: "runonly", Run: func(*cobra.Command, []string) {}}
	silenceUsageOnRunE(cmd)
	if cmd.RunE != nil {
		t.Fatal("wrapper added a RunE to a Run-only command")
	}
}
