package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

func serveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Manage named static file servers (serve a directory over HTTP)",
	}
	cmd.AddCommand(serveAddCmd(), serveLsCmd(), serveRmCmd(), serveOpenCmd())
	return cmd
}

func serveAddCmd() *cobra.Command {
	var port int
	var subject string
	var asJSON, crossScope bool
	cmd := &cobra.Command{
		Use:         "add <name> <dir>",
		Annotations: scopedWrite(),
		Short:       "Register a static server for a directory (--port to pin, else auto-allocated)",
		Long: `Register a static server for a directory (--port to pin, else auto-allocated).

The server is also registered as an output (kind server) of the subject given
by --subject, else of the one bound to this pane's cmux workspace; with
neither, nothing is registered. A served directory named _outputs (an org's
outputs root) is no subject's output and is never registered. A failed
registration prints one "warn: output not registered: …" line on stderr and
leaves the exit code and stdout unchanged.`,
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			// Resolve dir to an absolute path client-side so a relative dir
			// entered by the user resolves against the cwd, not the daemon's.
			dir, err := filepath.Abs(args[1])
			if err != nil {
				return fmt.Errorf("resolve dir: %w", err)
			}
			c := newClient()
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			ss, err := c.CreateStaticServer(cx, name, dir, port)
			if err != nil {
				return err
			}
			// An org's outputs root (the shared report server) is no
			// subject's output: nothing is registered, no warning.
			var res *pigtail.SubjectOutputAddResult
			if filepath.Base(ss.Dir) != "_outputs" {
				sc, err := newScopedClient()
				if err == nil {
					if crossScope {
						sc = sc.WithCrossScopeWrite()
					}
					res, err = registerServerOutput(cx, sc, ss, subject)
				}
				if err != nil {
					fmt.Fprintf(os.Stderr, "warn: output not registered: %v\n", err)
				}
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(ss)
			}
			fmt.Printf("✓ added %s  port=%d  %s\n", ss.Name, ss.Port, ss.Dir)
			if res != nil && res.Created {
				fmt.Printf("  ↳ output %s → %s\n", res.Output.ID, res.Output.Title)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "fixed port (default: auto-allocate)")
	cmd.Flags().StringVar(&subject, "subject", "", "register the server as an output of this subject (default: the pane's bound subject)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}

// registerServerOutput registers ss as a kind=server output of subject (else
// the pane's bound subject). nil, nil: no subject, nothing registered. An
// error is the caller's warning, never its failure.
func registerServerOutput(ctx context.Context, c *pigtail.Client, ss *pigtail.StaticServer, subject string) (*pigtail.SubjectOutputAddResult, error) {
	id := strings.TrimSpace(subject)
	if id == "" {
		var ok bool
		var err error
		id, _, ok, err = resolveBoundSubject(ctx, c)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, nil
		}
	}
	return c.AddSubjectOutput(ctx, pigtail.SubjectOutput{
		SubjectID: id, Kind: "server", Path: ss.Dir,
		URL:       fmt.Sprintf("http://127.0.0.1:%d", ss.Port),
		Title:     pigtail.TruncateTitle(ss.Name),
		SessionID: os.Getenv("CLAUDE_CODE_SESSION_ID"),
	})
}

func serveLsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List static servers and their running state",
		RunE: func(_ *cobra.Command, _ []string) error {
			c := newClient()
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			servers, err := c.ListStaticServers(cx)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(servers)
			}
			printStaticServers(os.Stdout, servers)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

func printStaticServers(w *os.File, servers []pigtail.StaticServerView) {
	if len(servers) == 0 {
		fmt.Fprintln(w, "no static servers")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPORT\tRUNNING\tDIR")
	for _, s := range servers {
		running := "○ stopped"
		if s.Running {
			running = "● running"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", s.Name, s.Port, running, s.Dir)
	}
	tw.Flush()
}

func serveRmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a static server (stops it first if running)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c := newClient()
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := c.DeleteStaticServer(cx, args[0]); err != nil {
				return err
			}
			fmt.Printf("✓ removed %s\n", args[0])
			return nil
		},
	}
	return cmd
}

func serveOpenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "open <name>",
		Short: "Open a static server's root directory listing in your browser",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			c := newClient()
			cx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			servers, err := c.ListStaticServers(cx)
			if err != nil {
				return err
			}
			var found *pigtail.StaticServerView
			for i := range servers {
				if servers[i].Name == name {
					found = &servers[i]
					break
				}
			}
			if found == nil {
				return fmt.Errorf("no such static server: %s", name)
			}
			if !found.Running {
				fmt.Printf("(not running — start it with: pigtail start %s)\n", name)
			}
			url := fmt.Sprintf("http://127.0.0.1:%d/", found.Port)
			fmt.Println(url)
			if err := openBrowser(url); err != nil {
				// Non-fatal: the URL is already printed for manual opening.
				fmt.Printf("(could not auto-open a browser: %v)\n", err)
			}
			return nil
		},
	}
	return cmd
}
