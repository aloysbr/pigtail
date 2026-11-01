package main

import (
	"strings"
	"testing"
	"time"
)

// TestResolveIdleDuration covers the pflag optional-value quirk this
// function exists to paper over: --idle never greedily consumes a following
// bare token as its own value, so the natural `--idle 1m` syntax (used
// throughout this command's docs and manual verification) leaves "1m" as a
// stray positional instead. Found via manual E2E testing of Task 7 (the
// first version errored "mutually exclusive with a positional target" on
// exactly this invocation) — this test locks the fix in.
//
// Reinterpreting a leftover positional as the duration is only valid for a
// genuinely bare --idle (idle == idleGraceNoOptDefVal). A code-review finding
// on the first version of this function caught it deciding purely on
// len(args), so `--idle=1m <target>` silently discarded the explicit "1m"
// and reinterpreted <target> as a *different* grace whenever <target>
// happened to parse as a duration (e.g. a stray token, a script bug) — no
// error, just the wrong sweep width run in silence. The
// "explicit value + residual" cases below pin the fix: once --idle already
// carries a value, any leftover positional is a hard mutual-exclusion error,
// whether or not it happens to look like a duration.
func TestResolveIdleDuration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		idle    string
		args    []string
		want    string
		wantErr bool
	}{
		{"bare --idle (NoOptDefVal, no leftover args)", "0s", nil, "0s", false},
		{"--idle=1m (equals form, no leftover args)", "1m", nil, "1m", false},
		{"--idle 1m (space form: duration lands as a stray positional)", "0s", []string{"1m"}, "1m", false},
		{"bare --idle plus more than one leftover positional is refused", "0s", []string{"1m", "extra"}, "", true},
		{"explicit --idle=1m plus a residual non-duration target is refused", "1m", []string{"myworktree"}, "", true},
		{"explicit --idle=1m plus a residual target shaped like a duration is STILL refused (the silent-wrong-grace bug)", "1m", []string{"5m"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveIdleDuration(tc.idle, tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("resolveIdleDuration(%q, %v) err = %v, wantErr %v", tc.idle, tc.args, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("resolveIdleDuration(%q, %v) = %q, want %q", tc.idle, tc.args, got, tc.want)
			}
		})
	}
}

// TestStopIdleFlagHasNoOptDefVal: --idle must be usable bare (no duration),
// which requires pflag's NoOptDefVal to be set to something time.ParseDuration
// accepts as "no explicit grace" (see resolveIdleDuration/stopIdleServers).
func TestStopIdleFlagHasNoOptDefVal(t *testing.T) {
	t.Parallel()
	f := stopServerCmd().Flags().Lookup("idle")
	if f == nil {
		t.Fatal("stop command has no --idle flag")
	}
	if f.NoOptDefVal == "" {
		t.Fatal("--idle has no NoOptDefVal: bare `pigtail stop --idle` would fail with \"flag needs an argument\"")
	}
	if _, err := time.ParseDuration(f.NoOptDefVal); err != nil {
		t.Fatalf("--idle NoOptDefVal %q does not parse as a duration: %v", f.NoOptDefVal, err)
	}
}

// TestStopIdleMutuallyExclusiveWithAllAndEverywhere: cobra must refuse the
// combination before ever reaching RunE (so no daemon call is attempted).
func TestStopIdleMutuallyExclusiveWithAllAndEverywhere(t *testing.T) {
	t.Parallel()
	for _, other := range []string{"--all", "--everywhere"} {
		t.Run(other, func(t *testing.T) {
			cmd := stopServerCmd()
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetArgs([]string{"--idle", "1m", other})
			if err := cmd.Execute(); err == nil {
				t.Fatalf("--idle combined with %s must be refused", other)
			}
		})
	}
}

// TestStopIdleMutuallyExclusiveWithServer: --idle silently overriding
// --server (instead of refusing) would mean --server is ignored without a
// word — the same class of silent-wrong-behavior bug this review round
// fixed for a positional target. --idle now shares the same exclusivity
// group as --all/--everywhere/--server, so this must be refused before
// RunE, same as the --all/--everywhere cases above.
func TestStopIdleMutuallyExclusiveWithServer(t *testing.T) {
	t.Parallel()
	cmd := stopServerCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"--idle=1m", "--server", "storybook"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("--idle combined with --server must be refused")
	}
}

// TestStopIdleRejectsResidualTargetEvenWhenDurationShaped is an end-to-end
// (cmd.Execute) regression test for the exact review finding: an explicit
// --idle=1m plus a genuine positional target must be a hard mutual-exclusion
// error, even when that target happens to parse as a duration (e.g. "5m") —
// never a silent re-grace that runs the sweep at "5m" instead of the "1m"
// the caller asked for. Args are parsed all the way through cobra (not just
// resolveIdleDuration in isolation) to prove the wiring, not just the helper.
func TestStopIdleRejectsResidualTargetEvenWhenDurationShaped(t *testing.T) {
	t.Parallel()
	cmd := stopServerCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"--idle=1m", "5m"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("--idle=1m plus a residual duration-shaped target must be refused, not silently re-graced")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected a mutual-exclusion error, got: %v", err)
	}
}

func TestRequireForceForEverywhere(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		isTTY   bool
		force   bool
		wantErr bool
	}{
		{"interactive without force is allowed (prompt handles it)", true, false, false},
		{"interactive with force is allowed", true, true, false},
		{"non-interactive without force is refused", false, false, true},
		{"non-interactive with force is allowed", false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireForceForEverywhere(tc.isTTY, tc.force)
			if (err != nil) != tc.wantErr {
				t.Fatalf("requireForceForEverywhere(%v, %v) err = %v, wantErr %v", tc.isTTY, tc.force, err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "--force") {
				t.Fatalf("the error must tell the caller about --force, got: %v", err)
			}
		})
	}
}
