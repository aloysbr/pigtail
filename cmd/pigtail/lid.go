package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/lid"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// lidCmd is `pigtail lid`: keep the Mac awake with the lid closed, with a
// timer and a battery guard run by pigtaild. Machine-wide, no subject: it
// uses newClient() like `battery`, and declares no scope access.
func lidCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "lid",
		Aliases: []string{"travel"},
		Short:   "Keep the Mac awake with the lid closed (timer + battery and heat guards)",
		Long: "Keep the Mac awake with the lid closed: `pigtail lid on [hours]` disables sleep\n" +
			"until a deadline (default 8 h, at most 24 h), restored earlier on battery at\n" +
			"<= 25% (lid open or closed), or when the Mac runs hot with the lid closed\n" +
			"(thermal state serious for 10 min, or critical). With the lid open, heat\n" +
			"never turns it off.\n" +
			"`pigtail lid` alone shows the status. Needs `pigtail lid setup` once.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error { return runLidStatus(asJSON) },
	}
	cmd.PersistentFlags().BoolVar(&asJSON, "json", false, "emit JSON")

	onCmd := &cobra.Command{
		Use:   "on [hours]",
		Short: "Disable sleep for hours (1-24, default 8); re-arms the timer when already on",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			hours, err := parseLidHours(args)
			if err != nil {
				return err
			}
			return runLidSet(true, hours, asJSON)
		},
	}
	offCmd := &cobra.Command{
		Use:   "off",
		Short: "Restore normal sleep",
		Args:  cobra.NoArgs,
		RunE:  func(_ *cobra.Command, _ []string) error { return runLidSet(false, 0, asJSON) },
	}
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show the real sleep state and pigtail's timer",
		Args:  cobra.NoArgs,
		RunE:  func(_ *cobra.Command, _ []string) error { return runLidStatus(asJSON) },
	}
	cmd.AddCommand(onCmd, offCmd, statusCmd, lidSetupCmd())
	return cmd
}

// parseLidHours reads the optional hours argument: none = lid.DefaultHours,
// else an integer from 1 to lid.MaxHours.
func parseLidHours(args []string) (int, error) {
	if len(args) == 0 {
		return lid.DefaultHours, nil
	}
	h, err := strconv.Atoi(args[0])
	if err != nil || h < 1 || h > lid.MaxHours {
		return 0, lid.ErrHours
	}
	return h, nil
}

func runLidStatus(asJSON bool) error {
	cx, cancel := context.WithTimeout(context.Background(), pigtail.LidCallTimeout)
	defer cancel()
	resp, err := newClient().Lid(cx)
	if err != nil {
		return lidError(err)
	}
	return emitLid(resp, asJSON)
}

func runLidSet(on bool, hours int, asJSON bool) error {
	cx, cancel := context.WithTimeout(context.Background(), pigtail.LidCallTimeout)
	defer cancel()
	resp, err := newClient().SetLid(cx, on, hours)
	if err != nil {
		return lidError(err)
	}
	return emitLid(resp, asJSON)
}

func emitLid(resp pigtail.LidResponse, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(resp)
	}
	printLid(os.Stdout, resp)
	return nil
}

func printLid(w io.Writer, r pigtail.LidResponse) {
	switch {
	case !r.SleepDisabled:
		fmt.Fprintln(w, "off — normal sleep")
	case r.Deadline != nil:
		fmt.Fprintf(w, "on until %s — lid can close (auto-off at the deadline, on battery at <= %d%%, or when the Mac runs hot with the lid closed)\n",
			r.Deadline.Local().Format("15:04"), r.BatteryFloor)
	default:
		fmt.Fprintln(w, "on (no timer) — sleep is disabled but pigtail did not arm it; run pigtail lid off to restore sleep")
	}
	if r.ThermalState != "" {
		lidPart := ""
		if r.LidClosed != nil {
			lidPart = ", lid open"
			if *r.LidClosed {
				lidPart = ", lid closed"
			}
		}
		fmt.Fprintf(w, "thermal: %s%s\n", r.ThermalState, lidPart)
	}
	if r.GuardError != "" {
		fmt.Fprintf(w, "guard: %s\n", r.GuardError)
	}
	if r.LastOff != nil {
		fmt.Fprintf(w, "last off: %s (%s)\n", r.LastOff.At.Local().Format("15:04"), r.LastOff.Reason)
	}
	if r.LastError != "" {
		fmt.Fprintf(w, "error: %s\n", r.LastError)
	}
}

// lidError turns the daemon's lid answers into actionable messages; any other
// error passes through unchanged.
func lidError(err error) error {
	var apiErr *pigtail.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case pigtail.ErrLidNotSetUp:
			return errors.New("sleep control is not set up — run `pigtail lid setup` once (asks for your admin password)")
		case pigtail.ErrLidUnavailable:
			return errors.New("pigtail lid works on macOS only")
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("pigtaild did not answer within %d s — the change may still apply; check pigtail lid status",
			int(pigtail.LidCallTimeout.Seconds()))
	}
	return err
}
