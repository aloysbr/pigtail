package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func purgeCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Inspect and set the always-on idle dev-server purge threshold",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runPurgeStatus(asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of text")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show the current idle-purge threshold",
		RunE:  func(_ *cobra.Command, _ []string) error { return runPurgeStatus(asJSON) },
	}
	offCmd := &cobra.Command{
		Use:   "off",
		Short: "Disable the idle purge",
		RunE:  func(_ *cobra.Command, _ []string) error { return runPurgeSet(0, asJSON) },
	}
	setCmd := &cobra.Command{
		Use:   "set <duration>",
		Short: "Set the idle-purge threshold (e.g. 5m, 10m, 30m, 1h)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			d, err := time.ParseDuration(args[0])
			if err != nil {
				return fmt.Errorf("invalid duration %q: %w", args[0], err)
			}
			if d < 0 {
				return fmt.Errorf("duration must be >= 0")
			}
			return runPurgeSet(int(d.Seconds()), asJSON)
		},
	}
	cmd.AddCommand(statusCmd, offCmd, setCmd)
	return cmd
}

func runPurgeStatus(asJSON bool) error {
	c := newClient()
	cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.Purge(cx)
	if err != nil {
		return err
	}
	return emitPurge(resp, asJSON)
}

func runPurgeSet(graceSeconds int, asJSON bool) error {
	c := newClient()
	cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.SetPurge(cx, graceSeconds)
	if err != nil {
		return err
	}
	return emitPurge(resp, asJSON)
}

func emitPurge(resp pigtail.PurgeResponse, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(resp)
	}
	printPurge(os.Stdout, resp)
	return nil
}

func printPurge(w io.Writer, resp pigtail.PurgeResponse) {
	if resp.GraceSeconds <= 0 {
		fmt.Fprintln(w, "idle purge = off")
		return
	}
	fmt.Fprintf(w, "idle purge = %dm\n", resp.GraceSeconds/60)
}
