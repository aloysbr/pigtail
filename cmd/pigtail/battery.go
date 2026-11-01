package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func batteryCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "battery",
		Short: "Inspect and toggle power-aware (battery) dev-server eviction",
		RunE: func(c *cobra.Command, _ []string) error {
			return runBatteryStatus(asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show power source, mode state, and per-server eviction countdown",
		RunE:  func(_ *cobra.Command, _ []string) error { return runBatteryStatus(asJSON) },
	}
	onCmd := &cobra.Command{
		Use:   "on",
		Short: "Resume battery mode for the running daemon session",
		RunE:  func(_ *cobra.Command, _ []string) error { return runBatterySet(true, asJSON) },
	}
	offCmd := &cobra.Command{
		Use:   "off",
		Short: "Suspend battery mode for the running daemon session",
		RunE:  func(_ *cobra.Command, _ []string) error { return runBatterySet(false, asJSON) },
	}
	cmd.AddCommand(statusCmd, onCmd, offCmd)
	return cmd
}

func runBatteryStatus(asJSON bool) error {
	c := newClient()
	cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.Battery(cx)
	if err != nil {
		return err
	}
	return emitBattery(resp, asJSON)
}

func runBatterySet(enabled, asJSON bool) error {
	c := newClient()
	cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.SetBattery(cx, enabled)
	if err != nil {
		return err
	}
	return emitBattery(resp, asJSON)
}

func emitBattery(resp pigtail.BatteryResponse, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(resp)
	}
	printBattery(os.Stdout, resp)
	return nil
}

func printBattery(w io.Writer, resp pigtail.BatteryResponse) {
	src := "mains"
	if resp.OnBattery {
		src = "battery"
	}
	mode := "off"
	if resp.Enabled {
		mode = "on"
	}
	fmt.Fprintf(w, "power=%s · battery mode=%s · grace=%dm\n",
		src, mode, resp.GraceSeconds/60)
	if len(resp.Servers) == 0 {
		fmt.Fprintln(w, "no running dev servers")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVER\tPORT\tIDLE\tEVICT-IN\tSPARED")
	for _, s := range resp.Servers {
		idle := (time.Duration(s.IdleSeconds) * time.Second).Round(time.Second)
		evictIn := (time.Duration(s.EvictInSeconds) * time.Second).Round(time.Second)
		spared := ""
		if s.Spared {
			spared = "spared"
		}
		fmt.Fprintf(tw, "%s:%s\t%d\t%s\t%s\t%s\n", s.WorkspaceID, s.Server, s.Port, idle, evictIn, spared)
	}
	tw.Flush()
}
