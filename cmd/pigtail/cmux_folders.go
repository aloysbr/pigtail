package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// cmuxFoldersCmd is `pigtail cmux folders`: the user's own gestures on the cmux
// folders pigtail keeps (D12). The arrangement itself runs in pigtaild.
func cmuxFoldersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "folders",
		Short: "The cmux folders pigtail keeps: fold or unfold them all",
	}
	cmd.AddCommand(cmuxFoldersToggleCmd())
	return cmd
}

func cmuxFoldersToggleCmd() *cobra.Command {
	var quiet bool
	cmd := &cobra.Command{
		Use:         "toggle",
		Annotations: scopedRead(), // like `cmux sidebar on`: not a subject-layer write
		Short:       "Fold every cmux folder, or unfold them all when none is open",
		Long: `Fold every cmux folder pigtail keeps; when none is open, unfold them all.
Only pigtail's own folders move: a group you made yourself, a pinned group and
a folder whose anchor holds a session are left as they are. It works while
cmux has the focus, and the arrangement never undoes it.

It needs the sidebar loop and the folder arrangement on (pigtaild
--cmux-sidebar --cmux-folders) and at least one allowed cmux window; each window
is toggled on its own. Otherwise it fails with "folders idle" and writes nothing. --quiet prints nothing on success (for
a key binding).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			res, err := c.ToggleCmuxFolders(cx)
			if err != nil {
				return err
			}
			if quiet {
				return nil
			}
			if res.Collapsed {
				fmt.Fprintf(cmd.OutOrStdout(), "dossiers repliés (%d)\n", res.Folders)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "dossiers dépliés (%d)\n", res.Folders)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&quiet, "quiet", false, "print nothing on success")
	return cmd
}
