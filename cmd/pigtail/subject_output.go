package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/aloysbr/pigtail/internal/cmux"
	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// The subject outputs registry (docs/wire-protocol.md § Subject outputs):
// the artifacts produced for a subject, registered when produced.

var errNoSubject = errors.New("no subject: pass <subject_id> or bind the workspace")

// resolveBoundSubject returns the subject bound to this pane's cmux workspace.
// ok=false, err=nil: $CMUX_WORKSPACE_ID empty, workspace unbound or bound to a
// subject this scope cannot read (a 404 lookup). err: any other failure.
func resolveBoundSubject(ctx context.Context, c *pigtail.Client) (id, shortTitle string, ok bool, err error) {
	if strings.TrimSpace(os.Getenv(cmux.EnvWorkspaceID)) == "" {
		return "", "", false, nil
	}
	wsID, err := resolveCmuxWorkspaceArg("")
	if err != nil {
		return "", "", false, err
	}
	l, err := c.CmuxWorkspace(ctx, wsID)
	if err != nil {
		if isNotFound(err) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	return l.Subject.ID, l.Subject.ShortTitle, true, nil
}

// isNoSuchOutput: the purge's 404 on an unknown key, distinct from the
// 404 no such subject of an unknown or invisible subject.
func isNoSuchOutput(err error) bool {
	var ae *pigtail.APIError
	return isNotFound(err) && errors.As(err, &ae) && ae.Message == "no such output"
}

// outputSubject returns the subject a command acts on: args[0], else the
// workspace's bound subject; ok=false when neither.
func outputSubject(ctx context.Context, c *pigtail.Client, args []string) (string, bool, error) {
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		return strings.TrimSpace(args[0]), true, nil
	}
	id, _, ok, err := resolveBoundSubject(ctx, c)
	return id, ok, err
}

var outputSchemeRE = regexp.MustCompile(`^[a-z][a-z0-9+.-]*:`)

func subjectOutputCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "output",
		Short: "Registry of the artifacts produced for a subject (captures, mockups, reports, servers, PRs, files)",
		Long: `The registry of the artifacts produced for a subject, registered when produced.

The subject is <subject_id>, else the one bound to this pane's cmux workspace
($CMUX_WORKSPACE_ID, as "pigtail subject which" resolves it). With neither,
the command exits 2 with "no subject: pass <subject_id> or bind the workspace";
"add --quiet-unbound" turns that case alone into a silent exit 0 (hooks). A
daemon that cannot be reached, a refusal, or a lookup error other than "not
bound" stays an error (exit 1), even with --quiet-unbound.

Kinds: capture html pdf mockup report server pr clip link file. Kind "capture"
is a screenshot (an output): it has nothing to do with "pigtail capture" or the
captures table (backlog ideas).`,
	}
	cmd.AddCommand(subjectOutputAddCmd(), subjectOutputLsCmd(), subjectOutputPurgeCmd(), subjectOutputBackfillCmd())
	return cmd
}

func subjectOutputAddCmd() *cobra.Command {
	var kind, path, url, title, step, meta, session, ts string
	var quietUnbound, asJSON, crossScope bool
	cmd := &cobra.Command{
		Use:         "add [<subject_id>] --kind K (--path P | --url U)",
		Annotations: scopedWrite(),
		Short:       "Register an output (idempotent: the same path, else url, is a no-op)",
		Long: `Register an output on a subject. The key is --path (made absolute), else
--url; re-adding a key changes nothing (the first writer wins) and exits 0.

The subject is <subject_id>, else the one bound to this pane's cmux workspace.
With neither: exit 2, stderr "no subject: pass <subject_id> or bind the
workspace"; --quiet-unbound makes that case alone a silent exit 0.

--title defaults to the basename of the path, else the url without its
scheme; it is cut to 60 characters. --session defaults to
$CLAUDE_CODE_SESSION_ID. --ts is RFC 3339 (default: now).

Kind "capture" is a screenshot (an output), unrelated to "pigtail capture" or
the captures table (backlog ideas).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			o := pigtail.SubjectOutput{Kind: kind, URL: strings.TrimSpace(url), Step: step}
			if strings.TrimSpace(path) == "" && o.URL == "" {
				return errors.New("pass --path or --url")
			}
			if p := strings.TrimSpace(path); p != "" {
				abs, err := filepath.Abs(p)
				if err != nil {
					return fmt.Errorf("--path %q: %w", p, err)
				}
				o.Path = abs
			}
			if title != "" {
				o.Title = pigtail.TruncateTitle(strings.TrimSpace(title))
			} else {
				o.Title = pigtail.DefaultOutputTitle(o.Path, o.URL)
			}
			if meta != "" {
				if !json.Valid([]byte(meta)) {
					return errors.New("meta: invalid JSON")
				}
				o.Meta = json.RawMessage(meta)
			}
			o.SessionID = session
			if o.SessionID == "" {
				o.SessionID = os.Getenv("CLAUDE_CODE_SESSION_ID")
			}
			if v := strings.TrimSpace(ts); v != "" {
				t, err := time.Parse(time.RFC3339, v)
				if err != nil {
					return fmt.Errorf("--ts %q: want RFC 3339 (2026-10-21T10:00:00Z)", v)
				}
				o.TS = t.UnixMilli()
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			cx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			id, ok, err := outputSubject(cx, c, args)
			if err != nil {
				return err
			}
			if !ok {
				if quietUnbound {
					return nil
				}
				return withExitCode(2, errNoSubject)
			}
			o.SubjectID = id
			res, err := c.AddSubjectOutput(cx, o)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(res)
			}
			if res.Created {
				fmt.Printf("✓ output %s (%s) → %s\n", res.Output.ID, res.Output.Kind, res.Output.Title)
			} else {
				fmt.Printf("= output %s already registered\n", res.Output.ID)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&kind, "kind", "", "capture html pdf mockup report server pr clip link file (required)")
	f.StringVar(&path, "path", "", "file or directory (made absolute); the key when set")
	f.StringVar(&url, "url", "", "url with a scheme (https:, clip:, …); the key when --path is not set")
	f.StringVar(&title, "title", "", "one line, cut to 60 characters (default: basename of the path, else the url)")
	f.StringVar(&step, "step", "", "workflow step (verify, mockup, …)")
	f.StringVar(&meta, "meta", "", "JSON object, at most 4096 bytes")
	f.StringVar(&session, "session", "", "producing Claude session (default: $CLAUDE_CODE_SESSION_ID)")
	f.StringVar(&ts, "ts", "", "production time, RFC 3339 (default: now)")
	f.BoolVar(&quietUnbound, "quiet-unbound", false, "no subject given nor bound: exit 0 silently (hooks)")
	f.BoolVar(&asJSON, "json", false, `print {"output":{…},"created":bool}`)
	addCrossScopeFlag(cmd, &crossScope)
	_ = cmd.MarkFlagRequired("kind")
	return cmd
}

func subjectOutputLsCmd() *cobra.Command {
	var q pigtail.SubjectOutputQuery
	var asJSON bool
	cmd := &cobra.Command{
		Use:         "ls [<subject_id>]",
		Annotations: scopedRead(),
		Short:       "List a subject's outputs, oldest first (purged ones included)",
		Args:        cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			cx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			id, ok, err := outputSubject(cx, c, args)
			if err != nil {
				return err
			}
			if !ok {
				return withExitCode(2, errNoSubject)
			}
			out, err := c.SubjectOutputs(cx, id, q)
			if err != nil {
				return err
			}
			if asJSON {
				if out == nil {
					out = []pigtail.SubjectOutput{}
				}
				return json.NewEncoder(os.Stdout).Encode(out)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "SEQ\tKIND\tTITLE\tKEY\tSESSION\tTS\tEXISTS")
			for _, o := range out {
				sess := o.SessionID
				if len(sess) > 8 {
					sess = sess[:8]
				}
				if sess == "" {
					sess = "-"
				}
				exists := "yes"
				if o.Exists != nil && !*o.Exists {
					exists = "no"
				}
				if o.PurgedAt != nil {
					exists = "purged"
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", o.Seq, o.Kind, o.Title, o.Key, sess,
					time.UnixMilli(o.TS).Local().Format("2006-01-02 15:04:05"), exists)
			}
			return tw.Flush()
		},
	}
	f := cmd.Flags()
	f.Int64Var(&q.SinceSeq, "since", 0, "only rows after this seq (exclusive)")
	f.StringVar(&q.Kind, "kind", "", "only this kind")
	f.StringVar(&q.SessionID, "session", "", "only this producing session")
	f.IntVar(&q.Limit, "limit", 0, "at most N rows, 1..1000 (default 200)")
	f.BoolVar(&asJSON, "json", false, "print a JSON array (times in epoch ms)")
	return cmd
}

func subjectOutputPurgeCmd() *cobra.Command {
	var key string
	var crossScope bool
	cmd := &cobra.Command{
		Use:         "purge <subject_id> --key <path|url>",
		Annotations: scopedWrite(),
		Short:       "Mark an output purged (the row stays, its first purge date kept)",
		Args:        cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			k := strings.TrimSpace(key)
			if k == "" {
				return errors.New("--key is required")
			}
			if !outputSchemeRE.MatchString(k) {
				abs, err := filepath.Abs(k)
				if err != nil {
					return fmt.Errorf("--key %q: %w", k, err)
				}
				k = abs
			}
			c, err := newScopedClient()
			if err != nil {
				return err
			}
			if crossScope {
				c = c.WithCrossScopeWrite()
			}
			cx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			o, err := c.PurgeSubjectOutput(cx, args[0], k)
			if err != nil {
				if isNoSuchOutput(err) {
					return errors.New("no such output")
				}
				return err
			}
			fmt.Printf("✓ purged %s\n", o.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&key, "key", "", "the output's key: its path (made absolute), else its url (required)")
	addCrossScopeFlag(cmd, &crossScope)
	return cmd
}
