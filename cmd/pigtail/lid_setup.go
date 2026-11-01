package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/lid"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// Seams of `pigtail lid setup`, replaced in tests: no test ever runs sudo,
// osascript or pmset, or reaches a daemon. The CLI runs no sudo probe of its
// own (D7): pigtaild probes under its manager lock (POST /lid/probe).
var (
	lidRootExec    = runAsRoot
	lidStdinTTY    = stdinIsTTY
	lidDaemonProbe = func() (pigtail.LidProbeResponse, error) {
		cx, cancel := context.WithTimeout(context.Background(), pigtail.LidCallTimeout)
		defer cancel()
		return newClient().LidProbe(cx)
	}
	lidUser = func() (string, error) {
		u, err := user.Current()
		if err != nil {
			return "", err
		}
		return u.Username, nil
	}
	lidReadPM    = lid.ExecRunner{}.ReadPM // --remove pre-check only
	lidDaemonOff = func() error {
		cx, cancel := context.WithTimeout(context.Background(), pigtail.LidCallTimeout)
		defer cancel()
		_, err := newClient().SetLid(cx, false, 0)
		return lidError(err)
	}
)

func lidSetupCmd() *cobra.Command {
	var remove bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install the sudoers rule that lets pigtaild run pmset -a disablesleep 0|1 (once)",
		Long: "Install " + lid.SudoersPath + ": the current user may run exactly\n" +
			"`/usr/bin/pmset -a disablesleep 0` and `/usr/bin/pmset -a disablesleep 1` as root\n" +
			"without a password, nothing else. Asks for your admin password once (sudo in a\n" +
			"terminal, the macOS admin dialog otherwise); never run it from an agent.\n" +
			"--remove restores normal sleep first, then removes the rule.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error { return runLidSetup(os.Stdout, remove) },
	}
	cmd.Flags().BoolVar(&remove, "remove", false, "remove the sudoers rule (restores normal sleep first)")
	return cmd
}

func runLidSetup(w io.Writer, remove bool) error {
	if runtime.GOOS != "darwin" {
		return errors.New("pigtail lid setup: macOS only")
	}
	if remove {
		return runLidRemove(w)
	}
	ok, err := ruleCheck(w)
	if err != nil {
		return fmt.Errorf("cannot check the sudo rule (%w)", err)
	}
	if ok {
		fmt.Fprintf(w, "already set up: %s\n", lid.SudoersPath)
		return nil
	}
	name, err := lidUser()
	if err != nil {
		return fmt.Errorf("cannot read the current user: %w", err)
	}
	script, err := lid.InstallScript(name)
	if err != nil {
		return fmt.Errorf("cannot set up for user %q: %w", name, err)
	}
	tty := lidStdinTTY()
	if err := lidRootExec(script, tty); err != nil {
		return fmt.Errorf("setup failed: %w", err)
	}
	ok, err = ruleCheck(w)
	if err != nil {
		// The file passed both visudo checks: keep it, a rerun verifies it.
		return fmt.Errorf("rule installed but could not be verified (%w) — rerun pigtail lid setup", err)
	}
	if ok {
		fmt.Fprintf(w, "sleep control set up: %s (pmset -a disablesleep 0|1 only, no password)\n", lid.SudoersPath)
		return nil
	}
	// Installed but inoperative (/etc/sudoers without #includedir): never
	// leave it behind.
	if !tty {
		fmt.Fprintln(w, "the rule is not in effect — removing it (a second admin dialog)")
	}
	const msg = "rule installed but sudo -n still asks for a password — removed " + lid.SudoersPath +
		"; check that /etc/sudoers has the line #includedir /private/etc/sudoers.d (sudo visudo), then rerun pigtail lid setup"
	if err := lidRootExec(lid.RemoveScript(), tty); err != nil {
		return fmt.Errorf("%s … and removing it failed (%w): run sudo /bin/rm -f %s", msg, err, lid.SudoersPath)
	}
	return errors.New(msg)
}

func runLidRemove(w io.Writer) error {
	const refuse = "refusing to remove the rule: the timer could no longer restore sleep"
	out, err := lidReadPM()
	if err != nil {
		return fmt.Errorf("cannot read the sleep state (pmset -g: %w) — %s", err, refuse)
	}
	if lid.ParseSleepDisabled(out) {
		if err := lidDaemonOff(); err != nil {
			return fmt.Errorf("the Mac is still kept awake and turning it off failed (%w) — %s", err, refuse)
		}
	}
	if err := lidRootExec(lid.RemoveScript(), lidStdinTTY()); err != nil {
		return fmt.Errorf("removal failed: %w", err)
	}
	ok, err := ruleCheck(w)
	if err != nil {
		return fmt.Errorf("rule removed but the removal could not be verified (%w) — rerun pigtail lid setup --remove", err)
	}
	if ok {
		return errors.New("rule still active after removal — another sudoers entry allows pmset without a password (sudo visudo)")
	}
	fmt.Fprintln(w, "sleep control rule removed")
	return nil
}

// ruleCheck asks pigtaild for the D7 probe and turns each failure into its
// own message; a warning is printed on w, never swallowed.
func ruleCheck(w io.Writer) (bool, error) {
	p, err := lidDaemonProbe()
	if err != nil {
		var apiErr *pigtail.APIError
		var httpErr *pigtail.HTTPStatusError
		switch {
		case errors.As(err, &apiErr):
			return false, fmt.Errorf("pigtaild: %s", apiErr.Message)
		case errors.Is(err, context.DeadlineExceeded):
			return false, fmt.Errorf("pigtaild did not answer within %d s — check pigtail lid status, then rerun pigtail lid setup",
				int(pigtail.LidCallTimeout.Seconds()))
		case errors.As(err, &httpErr):
			return false, fmt.Errorf("pigtaild answered HTTP %d — restart it (it may predate pigtail lid), then rerun pigtail lid setup", httpErr.Status)
		default:
			return false, fmt.Errorf("pigtaild is not reachable (%w) — the sudo rule is checked by the daemon itself; start pigtaild, then rerun pigtail lid setup", err)
		}
	}
	if p.Warning != "" {
		fmt.Fprintf(w, "warning: %s\n", p.Warning)
	}
	return p.RuleOK, nil
}

// runAsRoot runs script as root: sudo in a terminal, the macOS admin dialog
// (osascript) otherwise. The only place the CLI asks for the admin password.
func runAsRoot(script string, tty bool) error {
	var cmd *exec.Cmd
	if tty {
		cmd = exec.Command("/usr/bin/sudo", "/bin/sh", "-c", script)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	} else {
		as, err := lid.AppleScript(script)
		if err != nil {
			return err
		}
		cmd = exec.Command("/usr/bin/osascript", "-e", as)
		cmd.Stderr = os.Stderr
	}
	return cmd.Run()
}

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
