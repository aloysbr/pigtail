package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/waittype"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func cmuxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cmux",
		Short: "cmux: sidebar pills and titles, and the post-relaunch check",
	}
	cmd.AddCommand(cmuxSidebarCmd(), cmuxRetitleCmd(), cmuxCheckCmd(), cmuxPilotCmd(), cmuxFoldersCmd())
	return cmd
}

func cmuxSidebarCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "sidebar [status|on|off]",
		Annotations: scopedRead(),
		Short:       "Show, or switch on/off until the daemon restarts, the cmux sidebar loop",
		Long: `Show the cmux sidebar loop's state, or switch it on or off.

The loop shows "waiting for you" / "dead" pills on cmux workspaces, and
renames the workspaces bound to a subject (title = the subject's short title,
description = theme · phase · #rank). A name given by hand always wins.

on/off last until pigtaild restarts; at start, pigtaild's --cmux-sidebar flag
decides. off clears pigtail's pills and gives back the titles and descriptions
it posed.`,
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"status", "on", "off"},
		RunE: func(_ *cobra.Command, args []string) error {
			action := "status"
			if len(args) == 1 {
				action = args[0]
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var st *pigtail.CmuxSidebarState
			switch action {
			case "status":
				st, err = c.CmuxSidebar(cx)
			case "on", "off":
				st, err = c.SetCmuxSidebar(cx, action == "on")
			default:
				return fmt.Errorf("unknown action %q: want status, on or off", action)
			}
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(st)
			}
			printCmuxSidebar(os.Stdout, st)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func printCmuxSidebar(w io.Writer, st *pigtail.CmuxSidebarState) {
	state := "off"
	switch {
	case st.DisabledByPanic:
		state = "off (switched off after repeated panics)"
	case st.Enabled && st.CmuxAvailable:
		state = "on (cmux reachable)"
	case st.Enabled:
		state = "on (cmux not reachable yet)"
	}
	fmt.Fprintf(w, "sidebar: %s · %d error(s) in the last hour", state, st.ErrorsLastHour)
	if st.LastReconcileAt != nil {
		fmt.Fprintf(w, " · reconciled %s", st.LastReconcileAt.Local().Format("15:04:05"))
	}
	fmt.Fprintln(w)
	folders := "off"
	if st.Folders {
		folders = "on"
	}
	if st.FoldersNote != "" {
		folders += " — " + st.FoldersNote
	}
	fmt.Fprintf(w, "folders: %s\n", folders)
	// D3 of sub_x000000000000019, per window (D32 of sub_x000000000000007):
	// one line per window that waits, named by its first 8 characters when
	// the folders arrange several.
	for _, fw := range st.FoldersWait {
		label := "folders wait"
		if fw.Window != "" && len(st.FoldersWindows) > 1 {
			label += " " + shortWindow(fw.Window)
		}
		fmt.Fprintf(w, "%s: %s since %s (%s)\n", label, fw.Reason, fw.Since.Local().Format("15:04:05"),
			time.Since(fw.Since).Round(time.Second))
	}
	switch len(st.FoldersWindows) {
	case 0:
	case 1:
		fmt.Fprintf(w, "folders window: %s\n", st.FoldersWindows[0])
	default:
		fmt.Fprintf(w, "folders windows: %s\n", strings.Join(st.FoldersWindows, ", "))
	}
	if len(st.Windows) > 0 {
		fmt.Fprintf(w, "windows: %v\n", st.Windows)
	}
	for _, m := range st.Warnings {
		fmt.Fprintf(w, "warning: %s\n", m)
	}
	if len(st.Workspaces) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKSPACE\tATTENTION\tSUBJECT\tTITLE")
	for _, x := range st.Workspaces {
		att := x.Attention
		if x.Attention == "waiting" && x.WaitType != "" {
			att = waittype.Type(x.WaitType).Label()
			if x.WaitReason != "" && x.WaitingCount <= 1 {
				att += " · " + x.WaitReason
			}
		}
		if x.WaitingCount > 1 {
			att = fmt.Sprintf("%s ×%d", att, x.WaitingCount)
		}
		if att == "" {
			att = "-"
		}
		subject, title := "-", "-"
		if x.Subject != nil {
			subject, title = x.Subject.ID, x.Subject.ShortTitle
		}
		if x.DisplayName != "" {
			title = x.DisplayName
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", x.CmuxWorkspaceID, att, subject, title)
	}
	_ = tw.Flush()
}

func cmuxRetitleCmd() *cobra.Command {
	var (
		workspace  string
		crossScope bool
	)
	cmd := &cobra.Command{
		Use:         "retitle",
		Annotations: scopedWrite(),
		Short:       "Give a bound workspace's description back to pigtail after a manual edit",
		Long: `A bound workspace's title is always its subject's short title: pigtail puts it
back after a manual rename (to rename, change the short title: pigtail subject
set <id> short_title=…). Its description, once edited by hand in cmux, is left
alone; retitle hands it back to pigtail: the sidebar loop rewrites it on its
next pass, and gives the description you had back on detach.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			wsID, err := resolveCmuxWorkspaceArg(workspace)
			if err != nil {
				return err
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			b, err := c.RetitleCmuxWorkspace(cx, wsID)
			if err != nil {
				return err
			}
			fmt.Printf("✓ cmux workspace %s: description back to pigtail (%s)\n", b.CmuxWorkspaceID, b.SubjectID)
			return nil
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "cmux workspace UUID or workspace:N ref (default: $CMUX_WORKSPACE_ID)")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

// shortWindow is a window id's first 8 characters, as the status names it.
func shortWindow(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
