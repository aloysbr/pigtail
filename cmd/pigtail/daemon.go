package main

import "github.com/spf13/cobra"

// `pigtail daemon …` groups the commands about pigtaild itself.
func daemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Commands about the pigtaild process itself",
	}
	cmd.AddCommand(daemonWatchdogCmd())
	return cmd
}
