package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

// webCmd prints a single-use login link for the web front. It asks the code
// on the Unix socket only and never opens a browser tab: the user opens the link
// himself.
func webCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "web",
		Short: "Print a single-use link that signs your browser in to Pigtail",
		Long: "Print a single-use link (valid 2 minutes) that signs the browser in to Pigtail on the web,\n" +
			"the backlog of every scope served by pigtaild: read it, reorder it, park a subject, capture\n" +
			"an idea, launch a session. Opening it sets a long-lived\n" +
			"cookie; no browser is launched. The code is asked on the Unix socket only.",
		Args:        cobra.NoArgs,
		Annotations: scopedRead(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flagHTTP != "" {
				return errors.New("pigtail web asks its login code on the Unix socket only: drop --http")
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			res, err := c.WebLoginCode(context.Background())
			if err != nil {
				return err
			}
			if res.HTTPAddr == "" || res.Code == "" {
				return errors.New("the daemon answered no HTTP address or code")
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "http://%s/web/login?code=%s\n", res.HTTPAddr, url.QueryEscape(res.Code))
			return err
		},
	}
}
