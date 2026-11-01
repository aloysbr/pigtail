package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aloysbr/pigtail/internal/setup"
	"github.com/spf13/cobra"
)

// `pigtail setup --sdk-host … --sdk-node … --sdk-launch`, run as a command: the
// three flags reach the plist (cobra flag → Changed → runSetup's arguments →
// installLaunchdPlistOpts), the SDK stays off unless asked, a re-run without
// the options keeps them, and `=false` / an empty value switches them off.
func TestSetupCmd_sdkFlags(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "pigtail")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	prev := runSetupFn
	t.Cleanup(func() { runSetupFn = prev })
	runSetupFn = func(_ *cobra.Command, o setupOptions) error {
		return installLaunchdPlistOpts(configDir, o)
	}
	run := func(args ...string) string {
		t.Helper()
		root := &cobra.Command{Use: "pigtail", SilenceUsage: true}
		root.AddCommand(setupCmd(root))
		root.SetArgs(append([]string{"setup"}, args...))
		if err := root.Execute(); err != nil {
			t.Fatalf("setup %v: %v", args, err)
		}
		b, err := os.ReadFile(setup.PlistPath())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	has := func(plist, flag string) bool { return strings.Contains(plist, "<string>"+flag+"</string>") }

	// Nothing asked: no SDK flag at all (opt-in, off by default).
	if got := run(); has(got, "--sdk-host") || has(got, "--sdk-node") || has(got, "--sdk-launch") {
		t.Fatalf("a plain setup wrote an SDK flag:\n%s", got)
	}
	// Host and node alone turn the routes on, never the launch.
	got := run("--sdk-host", "/h/sdkhost", "--sdk-node", "/n/node")
	if !strings.Contains(got, "<string>--sdk-host</string>\n    <string>/h/sdkhost</string>") ||
		!strings.Contains(got, "<string>--sdk-node</string>\n    <string>/n/node</string>") {
		t.Fatalf("setup --sdk-host --sdk-node: not in the plist:\n%s", got)
	}
	if has(got, "--sdk-launch") {
		t.Fatalf("--sdk-launch written without being asked:\n%s", got)
	}
	// The launch is its own opt-in.
	if got = run("--sdk-launch"); !has(got, "--sdk-launch") || !strings.Contains(got, "/h/sdkhost") {
		t.Fatalf("setup --sdk-launch: not written, or the host path lost:\n%s", got)
	}
	// A re-run without the options keeps all three.
	if got = run(); !has(got, "--sdk-launch") || !strings.Contains(got, "/h/sdkhost") || !strings.Contains(got, "/n/node") {
		t.Fatalf("a plain re-run dropped the SDK flags:\n%s", got)
	}
	// And they come off on request.
	if got = run("--sdk-launch=false"); has(got, "--sdk-launch") || !strings.Contains(got, "/h/sdkhost") {
		t.Fatalf("--sdk-launch=false: launch still on, or the host lost:\n%s", got)
	}
	if got = run("--sdk-host", "", "--sdk-node", ""); strings.Contains(got, "sdk-host") || strings.Contains(got, "sdk-node") {
		t.Fatalf("empty --sdk-host/--sdk-node did not remove them:\n%s", got)
	}
}

// pigtaild refuses at start a plist whose final SDK flags are inconsistent
// (cmd/pigtaild sdkConfig): setup validates the merged result and refuses,
// before writing anything.
func TestSetupCmd_sdkFlagsRefusedWhenPigtaildWould(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "pigtail")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	prev := runSetupFn
	t.Cleanup(func() { runSetupFn = prev })
	runSetupFn = func(_ *cobra.Command, o setupOptions) error {
		return installLaunchdPlistOpts(configDir, o)
	}
	run := func(args ...string) error {
		root := &cobra.Command{Use: "pigtail", SilenceUsage: true, SilenceErrors: true}
		root.AddCommand(setupCmd(root))
		root.SetArgs(append([]string{"setup"}, args...))
		return root.Execute()
	}
	plist := func() string {
		b, err := os.ReadFile(setup.PlistPath())
		if err != nil {
			return ""
		}
		return string(b)
	}
	for name, args := range map[string][]string{
		"launch alone":      {"--sdk-launch"},
		"host without node": {"--sdk-host", "/h/sdkhost"},
		"node without host": {"--sdk-node", "/n/node"},
	} {
		if err := run(args...); err == nil || !strings.Contains(err.Error(), "sdk") {
			t.Errorf("%s: want a clear refusal, got %v", name, err)
		}
		if got := plist(); strings.Contains(got, "sdk-") {
			t.Errorf("%s: refused but the plist was written:\n%s", name, got)
		}
	}
	// A valid state, then a change that would break it is refused and leaves it intact.
	if err := run("--sdk-host", "/h/sdkhost", "--sdk-node", "/n/node", "--sdk-launch"); err != nil {
		t.Fatal(err)
	}
	before := plist()
	if err := run("--sdk-host", "", "--sdk-node", ""); err == nil {
		t.Fatal("removing host and node while the launch stays on must be refused")
	}
	if plist() != before {
		t.Fatal("the plist changed on a refused setup")
	}
}
